package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
)

// DeedTx adapts one org-scoped transaction (OrgTx) to deed.ChainStore, so the Sealer writes a deed
// in the same transaction as the state change it records (S04 §3.6):
//
//	db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
//	    // … UPDATE actions, INSERT action_events …
//	    _, err := sealer.Seal(ctx, store.DeedTx(tx), draft)
//	    return err
//	})
//
// On deed.ErrChainConflict the caller retries once in a new transaction.
func DeedTx(tx pgx.Tx) deed.ChainStore { return &deedTx{tx: tx} }

type deedTx struct {
	tx      pgx.Tx
	pending string // an org whose chain lock LockChain asked for and the next round trip takes
}

const lockChainSQL = `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('deedglass/deed-chain/' || $1::text, 0))`

// LockChain takes the org's transaction-scoped advisory lock; COMMIT or ROLLBACK releases it. The
// lock statement rides at the front of ReadState's batch (S13: one round trip less while the lock
// is held); Postgres runs a batch in order, so the head is still read under the lock. Anything
// else that runs first takes the lock on its own.
func (d *deedTx) LockChain(_ context.Context, org string) error {
	d.pending = org
	return nil
}

func (d *deedTx) lockNow(ctx context.Context) error {
	if d.pending == "" {
		return nil
	}
	_, err := d.tx.Exec(ctx, lockChainSQL, d.pending)
	d.pending = ""
	return err
}

// ReadState sends the chain lock (when pending), the head, clock and key reads as one batch (one
// round trip).
func (d *deedTx) ReadState(ctx context.Context, org, sealKeyID string) (deed.ChainState, error) {
	var st deed.ChainState
	b := &pgx.Batch{}
	locked := false
	if d.pending != "" {
		if d.pending != org {
			if err := d.lockNow(ctx); err != nil {
				return st, err
			}
		} else {
			b.Queue(lockChainSQL, d.pending)
			d.pending, locked = "", true
		}
	}
	b.Queue(`SELECT seq, body_hash, issued_at FROM deedglass.deeds WHERE org_id = $1 ORDER BY seq DESC LIMIT 1`, org)
	b.Queue(`SELECT pg_catalog.clock_timestamp()`)
	// Every seal key (a handful of rows): the Sealer's own, its extra keys and the key-set rule.
	b.Queue(`SELECT id, alg, status, activated_at, retired_at, revoked_at FROM deedglass.seal_keys ORDER BY activated_at, id`)
	br := d.tx.SendBatch(ctx, b)
	defer br.Close()
	if locked {
		if _, err := br.Exec(); err != nil {
			return st, err
		}
	}
	err := br.QueryRow().Scan(&st.Head.Seq, &st.Head.BodyHash, &st.Head.IssuedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return st, err
	}
	if err := br.QueryRow().Scan(&st.Now); err != nil {
		return st, err
	}
	rows, err := br.Query()
	if err != nil {
		return st, err
	}
	for rows.Next() {
		k := deed.KeyStatus{Found: true}
		if err := rows.Scan(&k.ID, &k.Alg, &k.Status, &k.ActivatedAt, &k.RetiredAt, &k.RevokedAt); err != nil {
			rows.Close()
			return st, err
		}
		st.Keys = append(st.Keys, k)
		if k.ID == sealKeyID {
			st.Key = k
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	return st, br.Close()
}

// Insert writes the deeds row. Unique violations (seq, prev_hash) and the chain trigger's
// DG002/DG003 mean the head moved underneath the Sealer: deed.ErrChainConflict.
func (d *deedTx) Insert(ctx context.Context, s deed.Stored) error {
	if err := d.lockNow(ctx); err != nil {
		return err
	}
	b := &s.Envelope.Body
	var actionID *string
	if b.Action != nil {
		actionID = &b.Action.ID
	}
	_, err := d.tx.Exec(ctx, `INSERT INTO deedglass.deeds (org_id, seq, id, kind, action_id, amends, issued_at,
	    body, body_jcs, body_hash, prev_hash, seal, seal_key_id, envelope)
	  VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12, $13, $14)`,
		b.Org, b.Seq, b.DeedID, string(b.Kind), actionID, b.Amends, s.IssuedAt,
		string(s.BodyJCS), s.BodyJCS, s.Envelope.BodyHash, b.PrevHash, s.Envelope.Seal, s.Envelope.SealKeyID, s.EnvelopeJCS)
	switch SQLState(err) {
	case "":
		return err
	case CodeUniqueViolation, CodeNoPredecessor, CodeChainBroken:
		return fmt.Errorf("%w: %w", deed.ErrChainConflict, err)
	}
	return err
}

// --- seal keys (deployment-wide; no org, no row-level security) ---

// SealKeyRow is one row of deedglass.seal_keys.
type SealKeyRow struct {
	ID          string
	Alg         string // Ed25519 | ML-DSA-65 (ADR-109)
	PublicKey   []byte
	Thumbprint  string
	Status      string
	ActivatedAt time.Time
	RetiredAt   *time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
}

// Record converts the row for deed.NewKeySet.
func (r SealKeyRow) Record() deed.KeyRecord {
	return deed.KeyRecord{ID: r.ID, Alg: r.Alg, PublicKey: r.PublicKey, Status: r.Status, ActivatedAt: r.ActivatedAt, RetiredAt: r.RetiredAt, RevokedAt: r.RevokedAt}
}

// ErrSealKeyConflict: the public key is already registered under another id.
var ErrSealKeyConflict = errors.New("store: this public key is registered under another seal key id")

const sealKeyCols = `id, alg, public_key, thumbprint, status, activated_at, retired_at, revoked_at, created_at`

func scanSealKey(row pgx.Row) (SealKeyRow, error) {
	var r SealKeyRow
	err := row.Scan(&r.ID, &r.Alg, &r.PublicKey, &r.Thumbprint, &r.Status, &r.ActivatedAt, &r.RetiredAt, &r.RevokedAt, &r.CreatedAt)
	return r, err
}

// activatedNow is the database clock truncated to the millisecond: a deed's issued_at is truncated
// the same way, so a key is valid for deeds sealed in its first millisecond.
const activatedNow = `pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp())`

// SealKey reads one seal key; ErrNotFound when it isn't registered.
func (db *DB) SealKey(ctx context.Context, id string) (SealKeyRow, error) {
	r, err := scanSealKey(db.pool.QueryRow(ctx, `SELECT `+sealKeyCols+` FROM deedglass.seal_keys WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// SealKeys lists every seal key ever registered, oldest activation first (keys.json order).
func (db *DB) SealKeys(ctx context.Context) ([]SealKeyRow, error) {
	rows, err := db.pool.Query(ctx, `SELECT `+sealKeyCols+` FROM deedglass.seal_keys ORDER BY activated_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SealKeyRow, error) { return scanSealKey(r) })
}

// RegisterSealKey inserts the key as active (activated now) unless its id is already registered,
// and returns the row either way. The caller checks the returned status and public key.
func (db *DB) RegisterSealKey(ctx context.Context, id string, pub []byte) (SealKeyRow, bool, error) {
	return db.RegisterSealKeyAlg(ctx, id, deed.AlgEd25519, pub)
}

// RegisterSealKeyAlg is RegisterSealKey for a key of any algorithm the table accepts (Ed25519,
// ML-DSA-65; migration 0033, ADR-109).
func (db *DB) RegisterSealKeyAlg(ctx context.Context, id, alg string, pub []byte) (SealKeyRow, bool, error) {
	var created bool
	var row SealKeyRow
	err := pgx.BeginTxFunc(ctx, db.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('deedglass/seal-keys', 0))`); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO deedglass.seal_keys (id, alg, public_key, thumbprint, activated_at)
		    VALUES ($1, $2, $3, $4, `+activatedNow+`) ON CONFLICT (id) DO NOTHING`, id, alg, pub, deed.Digest(pub))
		if SQLState(err) == CodeUniqueViolation {
			return ErrSealKeyConflict
		}
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
		row, err = scanSealKey(tx.QueryRow(ctx, `SELECT `+sealKeyCols+` FROM deedglass.seal_keys WHERE id = $1`, id))
		return err
	})
	return row, created, err
}

// RotateSealKey registers a new active key and retires every currently active key at
// now + overlap, in one transaction. Both stay published; deeds sealed by either keep verifying.
func (db *DB) RotateSealKey(ctx context.Context, id string, pub []byte, overlap time.Duration) (SealKeyRow, []SealKeyRow, error) {
	var (
		newRow  SealKeyRow
		retired []SealKeyRow
	)
	err := pgx.BeginTxFunc(ctx, db.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('deedglass/seal-keys', 0))`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `UPDATE deedglass.seal_keys
		    SET status = 'retired', retired_at = pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp() + $1::bigint * interval '1 millisecond')
		    WHERE status = 'active' AND id <> $2 AND alg = 'Ed25519'
		    RETURNING `+sealKeyCols, overlap.Milliseconds(), id)
		if err != nil {
			return err
		}
		if retired, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (SealKeyRow, error) { return scanSealKey(r) }); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO deedglass.seal_keys (id, alg, public_key, thumbprint, activated_at) VALUES ($1, 'Ed25519', $2, $3, `+activatedNow+`)`,
			id, pub, deed.Thumbprint(pub))
		if SQLState(err) == CodeUniqueViolation {
			return ErrSealKeyConflict
		}
		if err != nil {
			return err
		}
		newRow, err = scanSealKey(tx.QueryRow(ctx, `SELECT `+sealKeyCols+` FROM deedglass.seal_keys WHERE id = $1`, id))
		return err
	})
	return newRow, retired, err
}

// AddPQSealKey registers an ML-DSA-65 seal key beside the Ed25519 key (ADR-109, keys rotate --alg
// ML-DSA-65), in one transaction. The first one activates at now + overlap, so every instance can be
// restarted with it before the key-set rule wants its seal (an instance holding it earlier keeps
// sealing v1 until then). A later one activates now and retires the active ML-DSA-65 keys at now +
// overlap, as an Ed25519 rotation does. The Ed25519 key is never touched.
func (db *DB) AddPQSealKey(ctx context.Context, id string, pub []byte, overlap time.Duration) (SealKeyRow, []SealKeyRow, error) {
	var (
		newRow  SealKeyRow
		retired []SealKeyRow
	)
	err := pgx.BeginTxFunc(ctx, db.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('deedglass/seal-keys', 0))`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `UPDATE deedglass.seal_keys
		    SET status = 'retired', retired_at = pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp() + $1::bigint * interval '1 millisecond')
		    WHERE status = 'active' AND id <> $2 AND alg = 'ML-DSA-65'
		    RETURNING `+sealKeyCols, overlap.Milliseconds(), id)
		if err != nil {
			return err
		}
		if retired, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (SealKeyRow, error) { return scanSealKey(r) }); err != nil {
			return err
		}
		var delay int64
		if len(retired) == 0 {
			delay = overlap.Milliseconds()
		}
		_, err = tx.Exec(ctx, `INSERT INTO deedglass.seal_keys (id, alg, public_key, thumbprint, activated_at)
		    VALUES ($1, 'ML-DSA-65', $2, $3, pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp() + $4::bigint * interval '1 millisecond'))`,
			id, pub, deed.Digest(pub), delay)
		if SQLState(err) == CodeUniqueViolation {
			return ErrSealKeyConflict
		}
		if err != nil {
			return err
		}
		newRow, err = scanSealKey(tx.QueryRow(ctx, `SELECT `+sealKeyCols+` FROM deedglass.seal_keys WHERE id = $1`, id))
		return err
	})
	return newRow, retired, err
}
