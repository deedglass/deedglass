package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// MinPruneAge is the youngest cutoff Prune accepts outside dev.
const MinPruneAge = 24 * time.Hour

// PruneOptions tunes Prune.
type PruneOptions struct {
	DryRun        bool     // count only; nothing changes
	RetentionDays int      // recorded in the deed; 0 when the cutoff came from --older-than alone
	Actor         deed.Ref // default {system, deedglass}; the CLI passes {system, operator}
}

// PruneResult is what a prune removed (or, in a dry run, would remove).
type PruneResult struct {
	Object      string  `json:"object"` // "ledger_prune"
	Org         string  `json:"org"`
	DryRun      bool    `json:"dry_run"`
	Cutoff      Time    `json:"cutoff"`
	Actions     int64   `json:"actions"`
	Bytes       int64   `json:"bytes"`
	FirstAction *string `json:"first_action"`
	LastAction  *string `json:"last_action"`
	Deed        *string `json:"deed"` // the ledger.pruned deed; null in a dry run or when nothing was removed
	Seq         *int64  `json:"seq"`
}

// RetentionDays returns org's payload retention: orgs.retention_days, or DefaultRetentionDays.
func (l *Ledger) RetentionDays(ctx context.Context, org ids.OrgID) (int, error) {
	var days *int
	err := l.readTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT retention_days FROM deedglass.orgs WHERE id = $1`, org).Scan(&days)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: org %s", ErrNotFound, org)
	}
	if err != nil || days == nil {
		return DefaultRetentionDays, err
	}
	return *days, nil
}

// payloadBytes is what a pruned action gives up, as Postgres stores it.
const payloadBytes = `COALESCE(pg_catalog.pg_column_size(request), 0) + COALESCE(pg_catalog.pg_column_size(result), 0)
  + COALESCE(pg_catalog.pg_column_size(witness_detail), 0)`

// Prune removes the payloads (request, result, witness_detail) of org's sealed actions created
// before cutoff, and seals one ledger.pruned admin deed with the counts and range. It never
// touches deeds, events, admin events, approvals, digests, ids or any action that is not sealed,
// so every deed still verifies. One prune per org runs at a time (a session advisory lock); it
// works in batches of 1,000, each its own short transaction, so an interrupted run is safe and the
// next run continues and seals its own deed.
func (l *Ledger) Prune(ctx context.Context, org ids.OrgID, cutoff time.Time, o PruneOptions) (*PruneResult, error) {
	if !l.dev && l.now().Sub(cutoff) < MinPruneAge {
		return nil, paramErr("older_than", "Prune only payloads at least 24 hours old (DEEDGLASS_ENV=dev allows less).")
	}
	cutoff = cutoff.UTC()
	res := &PruneResult{Object: "ledger_prune", Org: org.String(), DryRun: o.DryRun, Cutoff: Time(cutoff)}
	if o.DryRun {
		err := l.readTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT pg_catalog.count(*), COALESCE(pg_catalog.sum(`+payloadBytes+`), 0)::bigint
			    FROM deedglass.actions WHERE org_id = $1 AND state = 'sealed' AND created_at < $2 AND payload_pruned_at IS NULL`,
				org, cutoff).Scan(&res.Actions, &res.Bytes)
		})
		return res, err
	}
	if l.sealer == nil {
		return nil, ErrNoSigner
	}
	err := l.db.WithLock(ctx, "deedglass/prune/"+org.String(), func(ctx context.Context) error {
		for {
			var (
				n, bytes    int64
				first, last *string
			)
			err := l.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `WITH b AS (
				    SELECT id, created_at, `+payloadBytes+` AS bytes FROM deedglass.actions
				    WHERE org_id = $1 AND state = 'sealed' AND created_at < $2 AND payload_pruned_at IS NULL
				    ORDER BY created_at, id LIMIT $3 FOR UPDATE SKIP LOCKED),
				  u AS (
				    UPDATE deedglass.actions AS a SET request = NULL, result = NULL, witness_detail = NULL,
				      payload_pruned_at = pg_catalog.clock_timestamp()
				    FROM b WHERE a.org_id = $1 AND a.id = b.id
				    RETURNING a.id, b.created_at, b.bytes)
				  SELECT pg_catalog.count(*), COALESCE(pg_catalog.sum(bytes), 0)::bigint,
				    (pg_catalog.array_agg(id ORDER BY created_at, id))[1],
				    (pg_catalog.array_agg(id ORDER BY created_at DESC, id DESC))[1]
				  FROM u`, org, cutoff, PruneBatch).Scan(&n, &bytes, &first, &last)
			})
			if err != nil {
				return err
			}
			if n == 0 {
				return nil
			}
			res.Actions += n
			res.Bytes += bytes
			if res.FirstAction == nil {
				res.FirstAction = first
			}
			res.LastAction = last
		}
	})
	if errors.Is(err, store.ErrLocked) {
		return nil, ErrPruneRunning
	}
	if err != nil {
		return nil, err
	}
	if res.Actions == 0 {
		return res, nil
	}
	actor := o.Actor
	if actor.Type == "" {
		actor = deed.Ref{Type: "system", ID: "deedglass"}
	}
	data := map[string]any{"cutoff": deed.FormatTime(cutoff), "retention_days": nil, "actions": res.Actions,
		"first_action": res.FirstAction, "last_action": res.LastAction, "bytes": res.Bytes}
	if o.RetentionDays > 0 {
		data["retention_days"] = o.RetentionDays
	}
	err = l.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		env, err := l.RecordAdmin(ctx, tx, AdminEvent{Action: AdminLedgerPruned, Actor: actor,
			Target: &deed.Ref{Type: "org", ID: org.String()}, Data: data})
		if err != nil {
			return err
		}
		seq := env.Body.Seq
		res.Deed, res.Seq = &env.ID, &seq
		return nil
	})
	if err != nil {
		return nil, err
	}
	l.log.InfoContext(ctx, "ledger pruned", "org_id", org.String(), "actions", res.Actions, "bytes", res.Bytes, "deed", *res.Deed)
	return res, nil
}

// OrgTxRetry runs fn in an org transaction and runs it once more, in a new transaction, when the
// Sealer reports deed.ErrChainConflict (another writer took the next seq first).
func (l *Ledger) OrgTxRetry(ctx context.Context, org ids.OrgID, fn store.TxFunc) error {
	err := l.db.OrgTx(ctx, org, fn)
	if errors.Is(err, deed.ErrChainConflict) {
		err = l.db.OrgTx(ctx, org, fn)
	}
	return err
}
