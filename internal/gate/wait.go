package gate

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
)

// KeyWindow is how long an Idempotency-Key names one action (contract §8).
const KeyWindow = 24 * time.Hour

// pollEvery is the fallback re-read of a wait, in case a notification is lost.
const pollEvery = 250 * time.Millisecond

// replay answers a request whose Idempotency-Key this agent already used (S13 §3.7). It returns
// (action, nil, nil) for a replay, (nil, nil, error) for a conflict, and (nil, k, nil) when k's
// 24-hour window has closed (the new request takes the key over). (nil, nil, nil) means the key
// is free.
func (p *Pipeline) replay(ctx context.Context, pr principal, n *Normalized, req ActionRequest, wait time.Duration) (*Action, *store.KeyedAction, error) {
	var k *store.KeyedAction
	err := p.o.DB.OrgReadTx(ctx, pr.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		k, err = store.FindActionByKey(ctx, tx, pr.OrgID, pr.AgentID, req.IdempotencyKey)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if p.now().Sub(k.CreatedAt) >= KeyWindow {
		return nil, k, nil
	}
	if k.Fingerprint != n.Fingerprint {
		return nil, nil, httpx.E(httpx.CodeIdempotencyConflict, httpx.Msg(MsgIdempotencyConflict), httpx.Param("Idempotency-Key"))
	}
	// The same request: answer with the original, after waiting (up to wait) for it to reach an
	// answer. The tool runs once, whatever the client retries.
	v, err := p.waitView(ctx, pr.OrgID, k.ID, wait, func(v *ledger.Action) bool { return v.Status != ledger.StatusProcessing })
	if err != nil {
		return nil, nil, err
	}
	a := &Action{Action: *v, replayed: true}
	if v.Status == ledger.StatusDenied {
		a.refusal = p.refusalFromView(ctx, pr.OrgID, k.ID, v)
	}
	return a, nil, nil
}

// refusalFromView rebuilds a denial's error for a replay: the same code and message; for a budget,
// Retry-After counts down to the same reset.
func (p *Pipeline) refusalFromView(ctx context.Context, org ids.OrgID, id ids.ActionID, v *ledger.Action) *httpx.Error {
	d := policy.Decision{Outcome: policy.Deny, Code: policy.CodePolicyDenied}
	if v.Decision != nil {
		if v.Decision.Code != nil {
			d.Code = policy.Code(*v.Decision.Code)
		}
		if v.Decision.Message != nil {
			d.Message = *v.Decision.Message
		}
	}
	if d.Code == policy.CodeBudgetExceeded {
		_ = p.o.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			ga, err := store.GetGateAction(ctx, tx, org, id, false)
			if err != nil {
				return err
			}
			var dd decisionDetail
			if json.Unmarshal(ga.DecisionDetail, &dd) == nil && dd.Budget != nil {
				h := *dd.Budget
				h.RetryAfterS = int64(math.Ceil(h.ResetsAt.Sub(p.now()).Seconds()))
				d.Budget = &h
			}
			return nil
		})
	}
	return refusalFor(d, v)
}

// waitView re-reads an action until done says so, or wait passes.
func (p *Pipeline) waitView(ctx context.Context, org ids.OrgID, id ids.ActionID, wait time.Duration, done func(*ledger.Action) bool) (*ledger.Action, error) {
	ch, unsub := p.broker.Subscribe(id.String())
	defer unsub()
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		v, err := p.o.Ledger.ReadActionView(context.WithoutCancel(ctx), org, id.String())
		if err != nil {
			return nil, err
		}
		if done(v) {
			return v, nil
		}
		select {
		case <-ch:
		case <-tick.C:
		case <-ctx.Done():
			return p.o.Ledger.ReadActionView(context.WithoutCancel(ctx), org, id.String())
		}
	}
}

// Wait is GET /v1/actions/{id}?wait= (ledger.Waiter): it returns when the action is Settled
// (sealed, or waiting for a person), when its state changes, when wait passes, or, for an agent
// key, at once when the agent or its org is stopped (with the stop error). An action that is
// already pending when the wait starts waits for its next change: that is how a client waits for
// a person's decision. ownAgent limits an agent key to its own agent's actions.
func (p *Pipeline) Wait(ctx context.Context, org ids.OrgID, id, ownAgent string, wait time.Duration) error {
	aid, err := ids.Parse[ids.ActionKind](id)
	if err != nil {
		return ledger.ErrNotFound
	}
	st0, agent, err := p.readState(ctx, org, aid)
	if errors.Is(err, store.ErrNotFound) || err == nil && ownAgent != "" && agent != ownAgent {
		return ledger.ErrNotFound
	}
	if err != nil {
		return err
	}
	if State(st0) == Sealed {
		return nil
	}
	if ownAgent != "" {
		if !p.openWait(ownAgent) {
			return httpx.E(httpx.CodeRateLimited, httpx.Msg(MsgWaitTooMany), httpx.WithHeader("Retry-After", "1"))
		}
		defer p.closeWait(ownAgent)
	}
	ch, unsub := p.broker.Subscribe(id)
	defer unsub()
	wctx, cancel := context.WithTimeout(ctx, min(wait, MaxWait))
	defer cancel()
	var agentID ids.AgentID
	if ownAgent != "" && p.o.Guard != nil {
		if agentID, err = ids.Parse[ids.AgentKind](ownAgent); err == nil {
			var stop context.CancelFunc
			wctx, stop = p.o.Guard.Watch(wctx, org, agentID)
			defer stop()
		}
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	pending := State(st0) == PendingApproval
	for {
		select {
		case <-ch:
		case <-tick.C:
		case <-wctx.Done():
			if c := context.Cause(wctx); errors.Is(c, identity.ErrAgentStopped) || errors.Is(c, identity.ErrOrgStopped) {
				if err := p.o.Guard.CheckActive(context.WithoutCancel(ctx), org, agentID, true); err != nil {
					return err
				}
			}
			return nil
		}
		st, _, err := p.readState(ctx, org, aid)
		if err != nil {
			return err
		}
		if st != st0 || !pending && State(st).Settled() {
			return nil
		}
	}
}

func (p *Pipeline) readState(ctx context.Context, org ids.OrgID, id ids.ActionID) (state, agent string, err error) {
	err = p.o.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		state, agent, _, err = store.ActionState(ctx, tx, org, id)
		return err
	})
	return state, agent, err
}

func (p *Pipeline) openWait(agent string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waits[agent] >= p.cfg.MaxWaitsPerAgent {
		return false
	}
	p.waits[agent]++
	return true
}

func (p *Pipeline) closeWait(agent string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waits[agent]--; p.waits[agent] <= 0 {
		delete(p.waits, agent)
	}
}

// Get returns one action, after waiting like GET /v1/actions/{id}?wait= (for S14's
// deedglass.check_approval and the SDKs' polling).
func (p *Pipeline) Get(ctx context.Context, pr principal, id string, wait time.Duration) (*Action, error) {
	own := ""
	if pr.Kind == httpx.KindAgentKey {
		own = pr.AgentID.String()
	}
	if wait > 0 {
		if err := p.Wait(ctx, pr.OrgID, id, own, wait); err != nil {
			if errors.Is(err, ledger.ErrNotFound) {
				return nil, notFound(id)
			}
			return nil, err
		}
	}
	v, err := p.o.Ledger.ReadActionView(ctx, pr.OrgID, id)
	if errors.Is(err, ledger.ErrNotFound) || err == nil && own != "" && v.Agent.ID != own {
		return nil, notFound(id)
	}
	if err != nil {
		return nil, err
	}
	return &Action{Action: *v}, nil
}
