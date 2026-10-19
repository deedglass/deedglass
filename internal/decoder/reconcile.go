package decoder

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/store"
)

// Reconcile (S20 §3.13), every decoder.reconcile_interval: every ledger row this connector's org
// wrote must have a frame once the checkpoint has passed it. A row without one is recorded as
// class missing (no commit LSN) and S21 seals or amends its action with the gap no_evidence. This
// is how actions lost to an invalidated or dropped slot are found (E03, E04).
//
// The order of the reads matters, and fixes V0 spec §8.4, which read the WAL position first:
//  1. read the ledger rows created after the watermark (minus 5 minutes of overlap);
//  2. then read q = pg_current_wal_lsn().
//
// A row visible in step 1 committed before step 1's snapshot, so its commit record is below q.
// Once the stored checkpoint is at or past q, every such row's frame, if there is one, is stored.
// Reading q first would miss a transaction that commits between the two reads.

const reconcileOverlap = 5 * time.Minute

func (r *Runner) reconcileTick(ctx context.Context) error {
	if r.target != nil && r.target.Versions != nil {
		return nil // a FHIR server's database holds no ledger rows to reconcile (ADR-108)
	}
	now := r.d.Now()
	asked := false
	select {
	case <-r.reconcile:
		asked = true
	default:
	}
	if !asked && !r.lastReconcile.IsZero() && now.Sub(r.lastReconcile) < r.cfg.ReconcileInterval {
		return nil
	}
	r.lastReconcile = now
	return r.startReconcile(ctx)
}

// ReconcileNow asks the runner to reconcile on its next round (tests, and S21 after SlotLost).
func (r *Runner) ReconcileNow() {
	select {
	case r.reconcile <- struct{}{}:
	default:
	}
	r.Nudge()
}

// pendingReconcile is a round whose ledger rows were read and whose WAL position q is known: it
// finishes once the persisted checkpoint reaches q (usually the next poll).
type pendingReconcile struct {
	q      LSN
	mine   []string
	newest time.Time
}

// startReconcile is steps 1 and 2: the ledger rows after the watermark, then q. It uses the
// runner's session: only the runner's own goroutine calls it (others call ReconcileNow).
func (r *Runner) startReconcile(ctx context.Context) error {
	row, err := r.row(ctx)
	if err != nil {
		return err
	}
	if row.ReconcileWatermark == nil {
		return nil // set when the first slot is created
	}
	since := row.ReconcileWatermark.Add(-reconcileOverlap)
	seen, err := r.sess.LedgerSince(ctx, since.UTC().Format("2006-01-02 15:04:05.999999+00"))
	if err != nil {
		return err
	}
	q, err := r.sess.CurrentWAL(ctx)
	if err != nil {
		return err
	}
	p := &pendingReconcile{q: q}
	for _, l := range seen {
		if l.OrgID == r.org.String() {
			p.mine = append(p.mine, l.ActionID)
		}
		if t, err := time.Parse("2006-01-02 15:04:05.999999-07", l.CreatedAt); err == nil && t.After(p.newest) {
			p.newest = t
		}
	}
	if len(seen) == 0 {
		return nil
	}
	r.recon = p
	return r.finishReconcile(ctx)
}

// finishReconcile is steps 3 and 4, once the checkpoint is at or past q: every row without a
// stored frame is recorded as missing, and the watermark moves to the newest row seen.
func (r *Runner) finishReconcile(ctx context.Context) error {
	p := r.recon
	if p == nil || r.lastTarget < p.q {
		return nil
	}
	r.recon = nil
	var missing []string
	err := r.d.DB.OrgTx(ctx, r.org, func(ctx context.Context, tx pgx.Tx) error {
		missing = missing[:0]
		framed, err := store.FramedActions(ctx, tx, r.id.String(), p.mine)
		if err != nil {
			return err
		}
		var rows []store.DecodedFrame
		for _, a := range p.mine {
			if framed[a] {
				continue
			}
			id := a
			rows = append(rows, store.DecodedFrame{OrgID: r.org.String(), ConnectorID: r.id.String(), SystemID: r.systemID,
				TimelineID: r.timeline, Class: string(ClassMissing), ActionID: &id, Flags: []string{"reconcile"}})
		}
		news, err := store.InsertFrames(ctx, tx, rows)
		if err != nil {
			return err
		}
		for i, ok := range news {
			if ok {
				missing = append(missing, *rows[i].ActionID)
			}
		}
		if len(missing) > 0 {
			if err := store.NotifyJobs(ctx, tx, r.org.String()); err != nil {
				return err
			}
		}
		if !p.newest.IsZero() {
			return store.SetReconcileWatermark(ctx, tx, r.id.String(), p.newest)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, a := range missing {
		r.d.Metrics.frame(r.id.String(), string(ClassMissing))
		r.counters[string(ClassMissing)]++
		if r.d.Hooks.OnMissing != nil {
			r.d.Hooks.OnMissing(ctx, r.org, r.id, a)
		}
	}
	if len(missing) > 0 {
		r.log.Warn("decoder: ledger rows with no frame (recorded as missing)", "count", len(missing))
	}
	return nil
}
