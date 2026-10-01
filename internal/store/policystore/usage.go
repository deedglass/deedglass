package policystore

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
)

// Usage is the Postgres policy.UsageStore.
type Usage struct{ db *store.DB }

// NewUsage returns the store on db.
func NewUsage(db *store.DB) *Usage { return &Usage{db: db} }

var _ policy.UsageStore = (*Usage)(nil)

const (
	// snapshotSQL reads every key in one statement; ordinality keeps the request's order and the
	// LEFT JOIN gives 0 for a counter that has no row yet.
	snapshotSQL = `SELECT k.i, COALESCE(u.value, 0)
	  FROM ROWS FROM (unnest($2::text[], $3::text[], $4::text[], $5::timestamptz[])) WITH ORDINALITY AS k (scope, counter, window_kind, window_start, i)
	  LEFT JOIN deedglass.usage_counters AS u
	    ON u.org_id = $1 AND u.scope = k.scope AND u.counter = k.counter
	   AND u.window_kind = k.window_kind AND u.window_start = k.window_start
	 ORDER BY k.i`

	upsertSQL = `INSERT INTO deedglass.usage_counters AS u (org_id, scope, counter, window_kind, window_start, value)
	  VALUES ($1, $2, $3, $4, $5, $6)
	  ON CONFLICT (org_id, scope, counter, window_kind, window_start)
	  DO UPDATE SET value = u.value + EXCLUDED.value, updated_at = pg_catalog.now()
	  RETURNING u.value`

	// condUpsertSQL returns no row when the increment would pass $7. A fresh row takes the INSERT
	// path, which has no WHERE, so Reserve checks amount <= limit before it sends anything.
	condUpsertSQL = `INSERT INTO deedglass.usage_counters AS u (org_id, scope, counter, window_kind, window_start, value)
	  VALUES ($1, $2, $3, $4, $5, $6)
	  ON CONFLICT (org_id, scope, counter, window_kind, window_start)
	  DO UPDATE SET value = u.value + EXCLUDED.value, updated_at = pg_catalog.now()
	  WHERE u.value + EXCLUDED.value <= $7
	  RETURNING u.value`

	valueSQL = `SELECT COALESCE((SELECT value FROM deedglass.usage_counters
	  WHERE org_id = $1 AND scope = $2 AND counter = $3 AND window_kind = $4 AND window_start = $5), 0)`

	releaseSQL = `UPDATE deedglass.usage_counters
	  SET value = GREATEST(value - $6, 0), updated_at = pg_catalog.now()
	  WHERE org_id = $1 AND scope = $2 AND counter = $3 AND window_kind = $4 AND window_start = $5`

	pruneSQL = `DELETE FROM deedglass.usage_counters WHERE window_kind = $1 AND window_start < $2`
)

func parseOrg(org string) (ids.OrgID, error) {
	id, err := ids.Parse[ids.OrgKind](org)
	if err != nil {
		return ids.OrgID{}, fmt.Errorf("policystore: %w", err)
	}
	return id, nil
}

// Snapshot implements policy.UsageStore: one SELECT in a read-only org transaction.
func (u *Usage) Snapshot(ctx context.Context, in policy.SnapshotRequest) (policy.UsageSnapshot, error) {
	org, err := parseOrg(in.Org)
	if err != nil {
		return policy.UsageSnapshot{}, err
	}
	ks, cur, err := policy.SnapshotKeys(in)
	if err != nil {
		return policy.UsageSnapshot{}, err
	}
	scopes, counters, windows := make([]string, len(ks)), make([]string, len(ks)), make([]string, len(ks))
	starts := make([]time.Time, len(ks))
	for i, k := range ks {
		scopes[i], counters[i], windows[i], starts[i] = k.Scope, k.Counter, k.Window, k.WindowStart.UTC()
	}
	vals := make([]int64, len(ks))
	err = u.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, snapshotSQL, org, scopes, counters, windows, starts)
		if err != nil {
			return err
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var i, v int64
			if err := rows.Scan(&i, &v); err != nil {
				return err
			}
			if i < 1 || int(i) > len(vals) {
				return fmt.Errorf("policystore: snapshot row %d out of range", i)
			}
			vals[i-1] = v
			n++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if n != len(ks) {
			return fmt.Errorf("policystore: snapshot read %d of %d counters", n, len(ks))
		}
		return nil
	})
	if err != nil {
		return policy.UsageSnapshot{}, err
	}
	return policy.FillSnapshot(ks, cur, vals), nil
}

// Reserve implements policy.UsageStore: every row in one batch, in one transaction, all or nothing.
func (u *Usage) Reserve(ctx context.Context, org string, rs []policy.Reservation) error {
	o, err := parseOrg(org)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if err := policy.CheckReservation(r); err != nil {
			return err
		}
	}
	if len(rs) == 0 {
		return nil
	}
	// An amount larger than its own limit can never fit: refuse without writing.
	for _, r := range rs {
		if r.Limit > 0 && r.Amount > r.Limit {
			var used int64
			if err := u.db.OrgReadTx(ctx, o, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, valueSQL, o, r.Scope, r.Counter, r.Window, r.WindowStart.UTC()).Scan(&used)
			}); err != nil {
				return err
			}
			return &policy.ExceededError{BudgetID: r.Budget, Used: used, Limit: r.Limit, ResetsAt: r.ResetsAt}
		}
	}
	return u.db.OrgTx(ctx, o, func(ctx context.Context, tx pgx.Tx) error {
		return upsert(ctx, tx, o, rs)
	})
}

// upsert sends rs as one batch. It returns *policy.ExceededError for the first conditional row that
// came back empty; the caller's transaction then rolls back. Rows go in key order, so two
// transactions that touch the same counters lock them in the same order and never deadlock.
func upsert(ctx context.Context, tx pgx.Tx, org ids.OrgID, rs []policy.Reservation) error {
	rs = slices.Clone(rs)
	slices.SortStableFunc(rs, func(a, b policy.Reservation) int {
		return cmp.Or(cmp.Compare(a.Scope, b.Scope), cmp.Compare(a.Counter, b.Counter),
			cmp.Compare(a.Window, b.Window), a.WindowStart.Compare(b.WindowStart))
	})
	b := &pgx.Batch{}
	for _, r := range rs {
		if r.Limit > 0 {
			b.Queue(condUpsertSQL, org, r.Scope, r.Counter, r.Window, r.WindowStart.UTC(), r.Amount, r.Limit)
		} else {
			b.Queue(upsertSQL, org, r.Scope, r.Counter, r.Window, r.WindowStart.UTC(), r.Amount)
		}
	}
	br := tx.SendBatch(ctx, b)
	failed := -1
	applied := make([]bool, len(rs))
	for i := range rs {
		var v int64
		err := br.QueryRow().Scan(&v)
		switch {
		case err == nil:
			applied[i] = true
		case errors.Is(err, pgx.ErrNoRows) && rs[i].Limit > 0:
			if failed < 0 {
				failed = i
			}
		default:
			_ = br.Close()
			return err
		}
	}
	if err := br.Close(); err != nil {
		return err
	}
	if failed < 0 {
		return nil
	}
	// The rows after the refused one ran too (the batch was already sent); take back what they
	// added to the same counter, so Used is the value the refused row saw.
	r := rs[failed]
	var used int64
	if err := tx.QueryRow(ctx, valueSQL, org, r.Scope, r.Counter, r.Window, r.WindowStart.UTC()).Scan(&used); err != nil {
		return err
	}
	for i := failed + 1; i < len(rs); i++ {
		q := rs[i]
		if applied[i] && q.Scope == r.Scope && q.Counter == r.Counter && q.Window == r.Window && q.WindowStart.Equal(r.WindowStart) {
			used -= q.Amount
		}
	}
	return &policy.ExceededError{BudgetID: r.Budget, Used: max(used, 0), Limit: r.Limit, ResetsAt: r.ResetsAt}
}

// Release implements policy.UsageStore: it floors at 0 and never creates a row.
func (u *Usage) Release(ctx context.Context, org string, rs []policy.Reservation) error {
	o, err := parseOrg(org)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if err := policy.CheckReservation(r); err != nil {
			return err
		}
	}
	if len(rs) == 0 {
		return nil
	}
	return u.db.OrgTx(ctx, o, func(ctx context.Context, tx pgx.Tx) error {
		b := &pgx.Batch{}
		for _, r := range rs {
			b.Queue(releaseSQL, o, r.Scope, r.Counter, r.Window, r.WindowStart.UTC(), r.Amount)
		}
		return tx.SendBatch(ctx, b).Close()
	})
}

// RecordCost implements policy.UsageStore: unconditional increments of every target whose currency
// matches the cost's.
func (u *Usage) RecordCost(ctx context.Context, org string, targets []policy.CostTarget, cost policy.Money) error {
	o, err := parseOrg(org)
	if err != nil {
		return err
	}
	rs, err := policy.CostReservations(targets, cost)
	if err != nil || len(rs) == 0 {
		return err
	}
	return u.db.OrgTx(ctx, o, func(ctx context.Context, tx pgx.Tx) error {
		return upsert(ctx, tx, o, rs)
	})
}

// Prune implements policy.UsageStore. It is the one cross-org path here: a maintenance job.
func (u *Usage) Prune(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := u.db.SystemTx(ctx, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		n = 0
		for _, c := range []struct {
			window string
			keep   time.Duration
		}{{policy.WindowMinute, policy.KeepMinute}, {policy.WindowHour, policy.KeepHour}, {policy.WindowDay, policy.KeepDay}} {
			tag, err := tx.Exec(ctx, pruneSQL, c.window, now.Add(-c.keep).UTC())
			if err != nil {
				return err
			}
			n += tag.RowsAffected()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
