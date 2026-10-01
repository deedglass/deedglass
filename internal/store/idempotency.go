package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store/migrate"
)

// Idempotency keys (S03, contract §8) on deedglass.idempotency_keys, under RLS. internal/httpx
// defines the interface and the semantics; this is the Postgres implementation, and
// cmd/deedglass/serve.go adapts one to the other (store never imports httpx).

// IdemTTL is how long a key is remembered after it is first used.
const IdemTTL = 24 * time.Hour

// IdemKey identifies one stored request. (org, principal, key) is unique.
type IdemKey struct {
	OrgID     ids.OrgID
	Principal string // key_…, usr_… or agt_…
	Key       string // 1 to 255 printable ASCII characters
	Method    string
	Route     string // the route pattern
}

// IdemOutcome is what IdemBegin decided.
type IdemOutcome int

const (
	IdemStarted    IdemOutcome = iota + 1 // the caller owns the key; it must Complete or Release it
	IdemReplay                            // a stored response exists
	IdemInProgress                        // another request holds the key
	IdemMismatch                          // the key was used with a different request
)

// IdemResponse is a stored response.
type IdemResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// IdemBegin claims k for a request with fingerprint fp, in one short OrgTx:
// INSERT … ON CONFLICT DO NOTHING; on a conflict, SELECT … FOR UPDATE and decide. An expired key
// is treated as absent and reused; an in-progress key whose lock has passed is taken over.
func (db *DB) IdemBegin(ctx context.Context, k IdemKey, fp string, lockFor time.Duration) (IdemOutcome, *IdemResponse, error) {
	var (
		out  IdemOutcome
		resp *IdemResponse
	)
	lock := fmt.Sprintf("%d milliseconds", lockFor.Milliseconds())
	err := db.OrgTx(ctx, k.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		out, resp = 0, nil
		id := ids.New[ids.IdempotencyKind]()
		tag, err := tx.Exec(ctx, `INSERT INTO deedglass.idempotency_keys
		    (id, org_id, principal, key, method, route, request_hash, status, locked_until, expires_at)
		  VALUES ($1, $2, $3, $4, $5, $6, $7, 'in_progress', pg_catalog.now() + $8::interval, pg_catalog.now() + interval '24 hours')
		  ON CONFLICT (org_id, principal, key) DO NOTHING`,
			id, k.OrgID, k.Principal, k.Key, k.Method, k.Route, fp, lock)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			out = IdemStarted
			return nil
		}
		var (
			hash, status        string
			expired, lockPassed bool
			respStatus          *int16
			respHeaders         []byte
			respBody            []byte
		)
		err = tx.QueryRow(ctx, `SELECT request_hash, status, expires_at <= pg_catalog.now(), locked_until <= pg_catalog.now(),
		    response_status, response_headers, response_body
		  FROM deedglass.idempotency_keys WHERE org_id = $1 AND principal = $2 AND key = $3 FOR UPDATE`,
			k.OrgID, k.Principal, k.Key).Scan(&hash, &status, &expired, &lockPassed, &respStatus, &respHeaders, &respBody)
		if errors.Is(err, pgx.ErrNoRows) {
			// Pruned between the INSERT and the SELECT: the next attempt inserts.
			return errRetryIdem
		}
		if err != nil {
			return err
		}
		switch {
		case expired:
			_, err = tx.Exec(ctx, `UPDATE deedglass.idempotency_keys SET id = $4, method = $5, route = $6, request_hash = $7,
			    status = 'in_progress', response_status = NULL, response_headers = NULL, response_body = NULL, completed_at = NULL,
			    locked_until = pg_catalog.now() + $8::interval, expires_at = pg_catalog.now() + interval '24 hours', created_at = pg_catalog.now()
			  WHERE org_id = $1 AND principal = $2 AND key = $3`,
				k.OrgID, k.Principal, k.Key, id, k.Method, k.Route, fp, lock)
			out = IdemStarted
			return err
		case hash != fp:
			out = IdemMismatch
		case status == "complete" && respStatus != nil:
			r := &IdemResponse{Status: int(*respStatus), Body: respBody, Headers: map[string]string{}}
			if len(respHeaders) > 0 {
				if err := json.Unmarshal(respHeaders, &r.Headers); err != nil {
					return fmt.Errorf("store: stored headers: %w", err)
				}
			}
			out, resp = IdemReplay, r
		case !lockPassed:
			out = IdemInProgress
		default: // the first attempt is presumed dead: take over
			_, err = tx.Exec(ctx, `UPDATE deedglass.idempotency_keys SET locked_until = pg_catalog.now() + $4::interval
			  WHERE org_id = $1 AND principal = $2 AND key = $3`, k.OrgID, k.Principal, k.Key, lock)
			out = IdemStarted
			return err
		}
		return nil
	})
	if errors.Is(err, errRetryIdem) {
		return db.IdemBegin(ctx, k, fp, lockFor)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("store: idempotency begin: %w", err)
	}
	return out, resp, nil
}

var errRetryIdem = errors.New("store: idempotency key vanished; retry")

// IdemComplete stores the response of a started request.
func (db *DB) IdemComplete(ctx context.Context, k IdemKey, r IdemResponse) error {
	h, err := json.Marshal(r.Headers)
	if err != nil {
		return err
	}
	if r.Headers == nil {
		h = []byte("{}")
	}
	return db.OrgTx(ctx, k.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.idempotency_keys
		    SET status = 'complete', response_status = $4, response_headers = $5, response_body = $6, completed_at = pg_catalog.now()
		  WHERE org_id = $1 AND principal = $2 AND key = $3 AND status = 'in_progress'`,
			k.OrgID, k.Principal, k.Key, r.Status, h, r.Body)
		return err
	})
}

// IdemRelease forgets a started request that never completed, so a retry starts fresh.
func (db *DB) IdemRelease(ctx context.Context, k IdemKey) error {
	return db.OrgTx(ctx, k.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM deedglass.idempotency_keys
		  WHERE org_id = $1 AND principal = $2 AND key = $3 AND status = 'in_progress'`, k.OrgID, k.Principal, k.Key)
		return err
	})
}

// PruneIdempotencyKeys deletes expired keys in batches of 5,000 and returns how many. serve runs
// it every 10 minutes.
func (db *DB) PruneIdempotencyKeys(ctx context.Context) (int64, error) {
	var total int64
	for {
		var n int64
		err := db.SystemTx(ctx, ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `DELETE FROM deedglass.idempotency_keys WHERE id IN (
			    SELECT id FROM deedglass.idempotency_keys WHERE expires_at <= pg_catalog.now() LIMIT 5000)`)
			n = tag.RowsAffected()
			return err
		})
		total += n
		if err != nil {
			return total, fmt.Errorf("store: pruning idempotency keys: %w", err)
		}
		if n < 5000 {
			return total, nil
		}
	}
}

// MigrationRows compares the applied migrations with ms on one pooled connection, as the runtime
// role (it can read deedglass.schema_migrations). Health and serve's startup use it.
func (db *DB) MigrationRows(ctx context.Context, ms []migrate.Migration) ([]migrate.Row, error) {
	c, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Release()
	return migrate.Status(ctx, c.Conn(), ms)
}
