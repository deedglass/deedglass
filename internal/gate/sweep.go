package gate

import (
	"context"
	"errors"

	"github.com/deedglass/deedglass/internal/store"
)

// Sweepers (S13 §3.8), every 5 s on every instance. store.ClaimStaleActions finds the work across
// orgs; Resume does it in an org transaction, as a compare-and-set, so two instances that race on
// one action seal it exactly once.
//
//  1. Crash recovery: executing past its deadline → executed (unknown) → witnessing → unverifiable → sealed.
//  2. Unsealed: a final state idle for 30 s → sealed (a crash, or a seal key problem, between parts).
//  3. Approval expiry: pending_approval past its approval's expires_at → expired → sealed.
//  4. Idempotency release: keys of actions older than 24 h are cleared, 1,000 per pass.

// SweepResult counts one pass.
type SweepResult struct {
	CrashRecovered int
	Sealed         int
	Expired        int
	KeysReleased   int
	Errors         int
}

// sweeperOf names the sweeper a stale action belongs to, for metrics.
func sweeperOf(state string) string {
	switch State(state) {
	case Executing:
		return "crash_recovery"
	case PendingApproval:
		return "approval_expiry"
	}
	return "unsealed"
}

// Sweep runs the four sweepers once.
func (w *Worker) Sweep(ctx context.Context) SweepResult {
	return w.p.Sweep(ctx)
}

// Sweep runs the four sweepers once (exported for the worker, tests and S35's ops tooling).
func (p *Pipeline) Sweep(ctx context.Context) SweepResult {
	var r SweepResult
	stale, err := p.o.DB.ClaimStaleActions(ctx, staleBatch)
	if err != nil {
		if ctx.Err() == nil {
			p.log.Warn("gate: claiming stale actions failed", "err", err)
		}
		r.Errors++
	}
	for _, s := range stale {
		name := sweeperOf(s.State)
		a, acted, err := p.resume(ctx, s.OrgID, s.ActionID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			continue
		case err != nil:
			r.Errors++
			p.m.sweep(name, "error")
			p.log.Warn("gate: a sweeper couldn't finish an action", "sweeper", name, "org_id", s.OrgID.String(),
				"action_id", s.ActionID.String(), "err", err)
			continue
		}
		if a.State != string(Sealed) || !acted {
			p.m.sweep(name, "skipped") // still in flight, or another instance got there first
			continue
		}
		switch name {
		case "crash_recovery":
			r.CrashRecovered++
		case "approval_expiry":
			r.Expired++
		default:
			r.Sealed++
		}
		p.m.sweep(name, "done")
	}
	n, err := p.o.DB.ReleaseActionKeys(ctx, keyBatch)
	if err != nil {
		r.Errors++
		p.m.sweep("idempotency_release", "error")
	} else if n > 0 {
		r.KeysReleased = n
		p.m.add(p.m.sweeps, "idempotency_release", "done", uint64(n))
	}
	return r
}
