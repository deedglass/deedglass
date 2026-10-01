package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
)

// errSeal marks a failure of the seal step itself (a retired seal key, a draft the deed builder
// refuses). The outcome is then recorded without the deed, and the unsealed sweeper seals it once
// the cause is fixed: a result that came back is never lost to a sealing problem.
var errSeal = errors.New("gate: sealing failed")

// seal seals the action in tx; a chain conflict stays itself (OrgTxRetry retries it).
func (p *Pipeline) seal(ctx context.Context, tx pgx.Tx, id ids.ActionID) error {
	_, err := p.sealEnv(ctx, tx, id)
	return err
}

func (p *Pipeline) sealEnv(ctx context.Context, tx pgx.Tx, id ids.ActionID) (*deed.Envelope, error) {
	env, err := p.o.Ledger.SealAction(ctx, tx, id.String())
	if err != nil {
		if errors.Is(err, deed.ErrChainConflict) || errors.Is(err, ledger.ErrStateConflict) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", errSeal, err)
	}
	return env, nil
}

// sealView seals the action and updates view, read just before, to match: the response is read
// before the seal so the org's chain lock is held only for the deed and the commit.
func (p *Pipeline) sealView(ctx context.Context, tx pgx.Tx, id ids.ActionID, view *ledger.Action) error {
	env, err := p.sealEnv(ctx, tx, id)
	if err != nil {
		return err
	}
	d := env.ID
	view.State, view.Deed, view.NextAction = string(Sealed), &d, nil
	return nil
}

// complete is Tx B (S13 §3.4): executing → executed | failed, witnessing → the verdict (L0, inline),
// the cost, and the deed, in one transaction. If only the seal fails, the outcome is recorded
// without it.
func (p *Pipeline) complete(ctx context.Context, s Subject, e Execution, costTargets []policy.CostTarget, reservations []policy.Reservation,
	actor, requestID string) (*ledger.Action, error) {
	var view *ledger.Action
	run := func(seal bool) error {
		return p.o.Ledger.OrgTxRetry(ctx, s.Org, func(ctx context.Context, tx pgx.Tx) error {
			v, err := p.recordExecution(ctx, tx, s, e, costTargets, reservations, actor, requestID, seal)
			view = v
			return err
		})
	}
	err := run(true)
	if errors.Is(err, errSeal) {
		p.log.Error("gate: the outcome is recorded, but its deed couldn't be sealed; the sweeper retries", "org_id", s.Org.String(),
			"agent_id", s.Agent.String(), "action_id", s.ID.String(), "err", err)
		err = run(false)
	}
	return view, err
}

// recordExecution writes an execution onto an action in executing (its row is the CAS), then the
// verdict and the usage, and seals when seal is true. It is the one place an execution becomes
// columns, for the gateway, the edge report and crash recovery alike. It returns the action as it
// stands at the end.
func (p *Pipeline) recordExecution(ctx context.Context, tx pgx.Tx, s Subject, e Execution, costTargets []policy.CostTarget,
	reservations []policy.Reservation, actor, requestID string, seal bool) (*ledger.Action, error) {
	stored, digest, err := resultColumns(e.Result.Output, p.cfg.MaxResultBytes)
	if err != nil {
		return nil, err
	}
	if digest == nil && e.ResultDigest != "" {
		d := e.ResultDigest
		digest = &d
	}
	status := string(e.Status)
	started, finished := truncateMS(e.StartedAt), truncateMS(e.FinishedAt)
	lat := latencyMS(e)
	patch := ledger.Patch{ExecutionStatus: &status, Result: stored, ResultDigest: digest, ErrorCode: nonEmpty(e.ErrorCode),
		ErrorMessage: nonEmpty(e.ErrorMsg), LatencyMS: &lat, StartedAt: &started, FinishedAt: &finished}
	if c := e.Result.Cost; c != nil && c.Amount >= 0 && reCurrency(c.Currency) {
		amt, cur := c.Amount, c.Currency
		patch.CostAmount, patch.CostCurrency = &amt, &cur
	}
	ev := ledger.EventData{Actor: actor, RequestID: requestID, ErrorCode: nonEmpty(e.ErrorCode)}
	id := s.ID.String()
	finish := func() (*ledger.Action, error) {
		view, err := ledger.ReadAction(ctx, tx, s.Org, id)
		if err != nil || !seal {
			return view, err
		}
		return view, p.sealView(ctx, tx, s.ID, view)
	}
	if e.Status == ExecFailed {
		if err := p.o.Ledger.Transition(ctx, tx, id, Executing, Failed, ev, patch); err != nil {
			return nil, err
		}
		if !e.Sent { // it never reached the upstream: the reservation goes back
			if err := p.o.Usage.ReleaseTx(ctx, tx, s.Org.String(), reservations); err != nil {
				return nil, err
			}
		}
		return finish()
	}
	if patch.CostAmount != nil && *patch.CostAmount > 0 {
		if err := p.o.Usage.RecordCostTx(ctx, tx, s.Org.String(), costTargets, policy.Money{Amount: *patch.CostAmount, Currency: *patch.CostCurrency}); err != nil {
			return nil, err
		}
	}
	plain := ledger.EventData{Actor: actor, RequestID: requestID}
	steps := []ledger.Step{{To: Executed, Event: ev, Patch: &patch}, {To: Witnessing, Event: plain}}
	w, deferred := p.o.Witness.Inline(ctx, s, e)
	if !deferred {
		to, wev, wpatch, err := verdictStep(w, plain)
		if err != nil {
			return nil, err
		}
		steps = append(steps, ledger.Step{To: to, Event: wev, Patch: wpatch})
	}
	if err := p.o.Ledger.Advance(ctx, tx, id, Executing, steps...); err != nil {
		return nil, err
	}
	if deferred {
		if err := store.EnqueueJob(ctx, tx, s.Org, s.ID, "witness", p.now(), p.cfg.WorkerMaxAttempts); err != nil {
			return nil, err
		}
		return ledger.ReadAction(ctx, tx, s.Org, id)
	}
	return finish()
}

// verdictStep is the edge witnessing → verified | mismatch | unverifiable with the witness columns.
func verdictStep(w WitnessOutcome, ev ledger.EventData) (State, ledger.EventData, *ledger.Patch, error) {
	reasons := w.Reasons
	if reasons == nil {
		reasons = []string{}
	}
	detail, err := json.Marshal(struct {
		Reasons []string `json:"reasons"`
	}{reasons})
	if err != nil {
		return "", ev, nil, err
	}
	dd, err := deed.DigestJSON(detail)
	if err != nil {
		return "", ev, nil, err
	}
	to := Unverifiable
	switch w.Verdict {
	case VerdictVerified:
		to = Verified
	case VerdictMismatch:
		to = Mismatch
	}
	level, verdict, method := w.Level, w.Verdict, w.Method
	ev.Level, ev.Verdict, ev.DetailDigest = &level, &verdict, &dd
	if len(reasons) > 0 {
		ev.Reason = &reasons[0]
	}
	return to, ev, &ledger.Patch{WitnessLevel: &level, WitnessVerdict: &verdict, WitnessMethod: &method, WitnessDetail: detail, WitnessDetailDigest: &dd}, nil
}

// recordVerdict moves witnessing → verified | mismatch | unverifiable with the witness columns.
func (p *Pipeline) recordVerdict(ctx context.Context, tx pgx.Tx, id ids.ActionID, w WitnessOutcome, actor, requestID string) error {
	to, ev, patch, err := verdictStep(w, ledger.EventData{Actor: actor, RequestID: requestID})
	if err != nil {
		return err
	}
	return p.o.Ledger.Transition(ctx, tx, id.String(), Witnessing, to, ev, *patch)
}

func reCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if c[i] < 'a' || c[i] > 'z' {
			return false
		}
	}
	return true
}

// storedUsage reads the reservations and cost targets an admitted action carries.
func storedUsage(a *store.GateAction) ([]policy.Reservation, []policy.CostTarget) {
	var rs []policy.Reservation
	if len(a.Reservations) > 0 {
		_ = json.Unmarshal(a.Reservations, &rs)
	}
	var d decisionDetail
	if len(a.DecisionDetail) > 0 {
		_ = json.Unmarshal(a.DecisionDetail, &d)
	}
	return rs, d.CostBudgets
}

func subjectOf(a *store.GateAction) Subject {
	s := Subject{ID: a.ID, Org: a.OrgID, Agent: a.AgentID, Mode: Mode(a.Mode), Tool: a.Tool}
	var cx storedContext
	if len(a.Context) > 0 && json.Unmarshal(a.Context, &cx) == nil {
		s.ConnectorType = cx.ConnectorType
	}
	return s
}

// notFound is the 404 for an action the caller can't see: the same sentence whether it doesn't
// exist, belongs to another org, or (for an agent key) to another agent.
func notFound(id string) *httpx.Error {
	return httpx.E(httpx.CodeNotFound, httpx.Msgf("No action %s in this org. Check the id, or list recent actions: GET /v1/actions.", clip(id, 64)))
}

// actorOf is the event actor for a principal: the agent, the person, or system for an org admin key.
func actorOf(pr principal) string {
	switch {
	case pr.Kind == httpx.KindAgentKey && !pr.AgentID.IsZero():
		return pr.AgentID.String()
	case pr.Kind == httpx.KindUser && !pr.UserID.IsZero():
		return pr.UserID.String()
	}
	return "system"
}

// deciderOf is approvals.decided_by for a principal.
func deciderOf(pr principal) *string {
	switch {
	case pr.Kind == httpx.KindAgentKey && !pr.AgentID.IsZero():
		return strp(pr.AgentID.String())
	case pr.Kind == httpx.KindUser && !pr.UserID.IsZero():
		return strp(pr.UserID.String())
	case !pr.KeyID.IsZero():
		return strp(pr.KeyID.String())
	}
	return nil
}

// ReportResult records the edge's result of a proxy-mode action (POST /v1/actions/{id}/result):
// the same agent only, once, while the action is executing (S13 §3.7).
func (p *Pipeline) ReportResult(ctx context.Context, pr principal, id string, r EdgeResult) (*Action, error) {
	aid, err := ids.Parse[ids.ActionKind](id)
	if err != nil || pr.Kind != httpx.KindAgentKey {
		return nil, notFound(id)
	}
	e, err := p.edgeExecution(r)
	if err != nil {
		return nil, err
	}
	var view *ledger.Action
	run := func(seal bool) error {
		return p.o.Ledger.OrgTxRetry(ctx, pr.OrgID, func(ctx context.Context, tx pgx.Tx) error {
			a, err := store.GetGateAction(ctx, tx, pr.OrgID, aid, true)
			if errors.Is(err, store.ErrNotFound) || err == nil && a.AgentID != pr.AgentID {
				return notFound(id)
			}
			if err != nil {
				return err
			}
			if a.Mode != string(ModeProxy) {
				return httpx.E(httpx.CodeConflict, httpx.Msg(MsgResultNotProxy))
			}
			switch State(a.State) {
			case Executing:
			case Received, Authenticated, Evaluated, PendingApproval:
				return httpx.E(httpx.CodeConflict, httpx.Msg(MsgResultNotAuthorized))
			default:
				if a.ErrorCode != nil && *a.ErrorCode == ReasonEdgeLost {
					return httpx.E(httpx.CodeConflict, httpx.Msg(MsgResultLate))
				}
				return httpx.E(httpx.CodeConflict, httpx.Msg(MsgResultReported))
			}
			if a.ExecutingAt != nil && e.StartedAt.Before(a.ExecutingAt.Add(-time.Minute)) {
				return httpx.ErrValidation("started_at", MsgResultTimes)
			}
			rs, ct := storedUsage(a)
			view, err = p.recordExecution(ctx, tx, subjectOf(a), e, ct, rs, pr.AgentID.String(), httpx.RequestID(ctx), seal)
			return err
		})
	}
	err = run(true)
	if errors.Is(err, errSeal) {
		p.log.Error("gate: the edge's result is recorded, but its deed couldn't be sealed; the sweeper retries", "org_id", pr.OrgID.String(),
			"action_id", id, "err", err)
		err = run(false)
	}
	if err != nil {
		return nil, err
	}
	p.broker.Notify(id)
	a := &Action{Action: *view}
	p.m.action(ModeProxy, a.Status)
	return a, nil
}

// edgeExecution checks an edge report and turns it into an Execution.
func (p *Pipeline) edgeExecution(r EdgeResult) (Execution, error) {
	switch r.Status {
	case ExecSucceeded, ExecFailed, ExecUnknown:
	default:
		return Execution{}, httpx.ErrValidation("status", MsgResultStatus)
	}
	now := p.now()
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || r.FinishedAt.After(now.Add(time.Minute)) {
		return Execution{}, httpx.ErrValidation("finished_at", MsgResultTimes)
	}
	if r.ErrorCode != "" && !reErrorCode.MatchString(r.ErrorCode) {
		return Execution{}, httpx.ErrValidation("error_code", MsgResultErrorCode)
	}
	if len(r.Result) > p.cfg.MaxResultBytes {
		return Execution{}, httpx.ErrValidation("result", MsgResultTooLarge)
	}
	e := Execution{Status: r.Status, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, ErrorCode: r.ErrorCode,
		ErrorMsg: clipMessage(r.ErrorMessage), Sent: true}
	if len(r.Result) > 0 && string(r.Result) != "null" {
		d, err := deed.DigestJSON(r.Result)
		if err != nil {
			return Execution{}, httpx.ErrValidation("result", "result isn't valid JSON. Send the tool's result as JSON, or send result_digest alone.")
		}
		if r.ResultDigest != "" && r.ResultDigest != d {
			return Execution{}, httpx.ErrValidation("result_digest", MsgResultDigest)
		}
		e.Result = connector.Result{Output: r.Result}
	} else {
		if r.ResultDigest == "" && r.Status == ExecSucceeded {
			return Execution{}, httpx.ErrValidation("result", MsgResultMissing)
		}
		if r.ResultDigest != "" && !reDigest.MatchString(r.ResultDigest) {
			return Execution{}, httpx.ErrValidation("result_digest", MsgResultDigest)
		}
		e.ResultDigest = r.ResultDigest
	}
	if e.Status == ExecUnknown {
		e.Reason = ReasonOutcomeUnknown
	}
	return e, nil
}

// Cancel cancels a pending action (POST /v1/actions/{id}/cancel): the same agent, or a person or
// admin key of the org. Only from pending_approval: cancelled → sealed, the reservation released.
func (p *Pipeline) Cancel(ctx context.Context, pr principal, id string) (*Action, error) {
	aid, err := ids.Parse[ids.ActionKind](id)
	if err != nil {
		return nil, notFound(id)
	}
	var view *ledger.Action
	err = p.o.Ledger.OrgTxRetry(ctx, pr.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		a, err := store.GetGateAction(ctx, tx, pr.OrgID, aid, true)
		if errors.Is(err, store.ErrNotFound) || err == nil && pr.Kind == httpx.KindAgentKey && a.AgentID != pr.AgentID {
			return notFound(id)
		}
		if err != nil {
			return err
		}
		switch State(a.State) {
		case PendingApproval:
		case Sealed, Denied, Expired, Cancelled, Failed, Verified, Mismatch, Unverifiable:
			return httpx.E(httpx.CodeConflict, httpx.Msg(MsgCancelDone))
		default:
			return httpx.E(httpx.CodeConflict, httpx.Msg(MsgCancelExecuting))
		}
		if a.ApprovalID != nil {
			if err := store.DecideApproval(ctx, tx, pr.OrgID, *a.ApprovalID, "cancelled", deciderOf(pr), nil); err != nil && !errors.Is(err, store.ErrNoRow) {
				return err
			}
		}
		ev := ledger.EventData{Actor: actorOf(pr), RequestID: httpx.RequestID(ctx), Reason: strp("cancelled"), ApprovalID: a.ApprovalID}
		if err := p.o.Ledger.Transition(ctx, tx, id, PendingApproval, Cancelled, ev); err != nil {
			return err
		}
		rs, _ := storedUsage(a)
		if err := p.o.Usage.ReleaseTx(ctx, tx, pr.OrgID.String(), rs); err != nil {
			return err
		}
		if view, err = ledger.ReadAction(ctx, tx, pr.OrgID, id); err != nil {
			return err
		}
		return p.sealView(ctx, tx, aid, view)
	})
	if err != nil {
		return nil, err
	}
	p.broker.Notify(id)
	return &Action{Action: *view}, nil
}

// Resume takes the next step for an action from wherever it is (S13 §3.5): an approved action
// runs (after a fresh Stop check), a denied, expired or cancelled approval ends it, an execution
// past its deadline becomes an honest unknown, and a final action is sealed. Every step is a
// compare-and-set, so the worker may call it on any action after any crash, from any instance.
// It returns the action as it stands: still pending, authorized at the edge, or sealed.
func (p *Pipeline) Resume(ctx context.Context, org ids.OrgID, id ids.ActionID) (*Action, error) {
	a, _, err := p.resume(ctx, org, id)
	return a, err
}

// resume is Resume, and whether this call moved the action (a sweeper counts only its own work;
// another instance may have won the race).
func (p *Pipeline) resume(ctx context.Context, org ids.OrgID, id ids.ActionID) (*Action, bool, error) {
	acted := false
	for range 8 {
		a, done, moved, pl, err := p.step(ctx, org, id)
		if errors.Is(err, ErrStateConflict) {
			continue // another instance moved it: look again
		}
		if err != nil {
			return nil, acted, err
		}
		acted = acted || moved
		p.broker.Notify(id.String())
		if pl != nil {
			v, _ := p.run(ctx, pl)
			if v == nil {
				continue
			}
			return &Action{Action: *v}, true, nil
		}
		if done {
			return a, acted, nil
		}
	}
	v, err := p.o.Ledger.ReadActionView(ctx, org, id.String())
	if err != nil {
		return nil, acted, err
	}
	return &Action{Action: *v}, acted, nil
}

// step is one Resume transaction. done means there is nothing more to do now; moved means this
// transaction changed the action; pl is an approved gateway action to run (committed in executing).
func (p *Pipeline) step(ctx context.Context, org ids.OrgID, id ids.ActionID) (a *Action, done, moved bool, pl *plan, err error) {
	sealFallback := false
	for {
		err = p.o.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			a, done, pl = nil, false, nil
			ga, err := store.GetGateAction(ctx, tx, org, id, true)
			if err != nil {
				return err
			}
			before := ga.State
			seal := !sealFallback
			now := p.now()
			rs, ct := storedUsage(ga)
			sub := subjectOf(ga)
			sid := id.String()
			switch st := State(ga.State); st {
			case Sealed:
				done = true
			case PendingApproval:
				status := ""
				if ga.ApprovalStatus != nil {
					status = *ga.ApprovalStatus
				}
				if status == "pending" && ga.ApprovalExpiresAt != nil && !now.Before(*ga.ApprovalExpiresAt) {
					if err := store.DecideApproval(ctx, tx, org, *ga.ApprovalID, "expired", nil, nil); err != nil {
						return err
					}
					status = "expired"
				}
				switch status {
				case "pending":
					done = true
				case "approved":
					// A fresh Stop check, in this transaction, right before anything runs.
					subj, err := store.LoadGateSubject(ctx, tx, org, ga.AgentID)
					if err != nil {
						return err
					}
					if subj.OrgStopped || subj.AgentStatus != "active" {
						d := stopDecision(subj)
						ev := ledger.EventData{Reason: strp(string(d.Code))}
						patch := ledger.Patch{Decision: strp(string(d.Outcome)), DecisionCode: strp(string(d.Code)), DecisionSource: strp(d.Source),
							DecisionMessage: nonEmpty(d.Message)}
						if err := p.o.Ledger.Transition(ctx, tx, sid, PendingApproval, Denied, ev, patch); err != nil {
							return err
						}
						if err := p.o.Usage.ReleaseTx(ctx, tx, org.String(), rs); err != nil {
							return err
						}
						if seal {
							if err := p.seal(ctx, tx, id); err != nil {
								return err
							}
						}
						done = true
						break
					}
					deadline := now.Add(p.cfg.ExecuteTimeout + sweepGrace)
					if sub.Mode == ModeProxy {
						deadline = now.Add(p.cfg.EdgeResultTimeout)
					}
					ev := ledger.EventData{Reason: strp("approved"), ApprovalID: ga.ApprovalID}
					if err := p.o.Ledger.Transition(ctx, tx, sid, PendingApproval, Executing, ev, ledger.Patch{ExecutingDeadline: &deadline}); err != nil {
						return err
					}
					if sub.Mode == ModeProxy {
						done = true // authorized: the edge runs it now
						break
					}
					ex, spec, _, err := p.o.Resolver.Resolve(ctx, org, ga.Tool)
					if err != nil {
						return err
					}
					pl = &plan{subject: sub, exec: ex, actor: "system", costTargets: ct, reservations: rs,
						call: connector.Call{ActionID: id, OrgID: org, AgentID: ga.AgentID, Tool: spec, Qualified: ga.Tool, Args: ga.Request,
							IdempotencyKey: sid, Deadline: now.Add(p.cfg.ExecuteTimeout)}}
				case "denied", "expired", "cancelled":
					to := map[string]State{"denied": Denied, "expired": Expired, "cancelled": Cancelled}[status]
					ev := ledger.EventData{Reason: strp(status), ApprovalID: ga.ApprovalID}
					if err := p.o.Ledger.Transition(ctx, tx, sid, PendingApproval, to, ev); err != nil {
						return err
					}
					if err := p.o.Usage.ReleaseTx(ctx, tx, org.String(), rs); err != nil {
						return err
					}
					if seal {
						if err := p.seal(ctx, tx, id); err != nil {
							return err
						}
					}
					done = true
				default:
					done = true
				}
			case Executing:
				if ga.ExecutingDeadline == nil || now.Before(*ga.ExecutingDeadline) {
					done = true // still in flight (or at the edge)
					break
				}
				// Crash recovery: Deedglass can't know whether the tool ran. Never succeeded,
				// never failed: executed (unknown) → witnessing → unverifiable → sealed.
				e := Execution{Status: ExecUnknown, ErrorCode: "timeout", Reason: ReasonOutcomeUnknown, Sent: true,
					ErrorMsg: fmt.Sprintf("The tool did not finish within %s, and its result was lost. It may have run.", humanSeconds(p.cfg.ExecuteTimeout))}
				if sub.Mode == ModeProxy {
					e.ErrorCode, e.Reason = ReasonEdgeLost, ReasonEdgeLost
					e.ErrorMsg = "The edge didn't report a result before the deadline. The tool may have run."
				}
				// It started when it entered executing; Deedglass stopped waiting at the deadline.
				e.StartedAt, e.FinishedAt = *ga.ExecutingDeadline, *ga.ExecutingDeadline
				if ga.ExecutingAt != nil {
					e.StartedAt = *ga.ExecutingAt
				}
				if e.FinishedAt.Before(e.StartedAt) {
					e.FinishedAt = e.StartedAt
				}
				if _, err := p.recordExecution(ctx, tx, sub, e, ct, rs, "system", "", seal); err != nil {
					return err
				}
				done = true
			case Executed:
				if err := p.o.Ledger.Transition(ctx, tx, sid, Executed, Witnessing, ledger.EventData{}); err != nil {
					return err
				}
				e := Execution{Status: ExecUnknown, Reason: ReasonOutcomeUnknown}
				if ga.ExecutionStatus != nil {
					e.Status = ExecStatus(*ga.ExecutionStatus)
				}
				w, deferred := p.o.Witness.Inline(ctx, sub, e)
				if deferred {
					return store.EnqueueJob(ctx, tx, org, id, "witness", now, p.cfg.WorkerMaxAttempts)
				}
				if err := p.recordVerdict(ctx, tx, id, w, "system", ""); err != nil {
					return err
				}
				if seal {
					if err := p.seal(ctx, tx, id); err != nil {
						return err
					}
				}
				done = true
			case Denied, Failed, Verified, Mismatch, Unverifiable, Expired, Cancelled:
				if seal {
					if err := p.seal(ctx, tx, id); err != nil {
						return err
					}
				}
				done = true
			default:
				done = true // received … evaluated, witnessing: another transaction or a witness job owns it
			}
			v, err := ledger.ReadAction(ctx, tx, org, sid)
			if err != nil {
				return err
			}
			a, moved = &Action{Action: *v}, v.State != before
			return nil
		})
		if errors.Is(err, errSeal) && !sealFallback {
			p.log.Error("gate: an action couldn't be sealed; its outcome is recorded and the sweeper retries", "org_id", org.String(),
				"action_id", id.String(), "err", err)
			sealFallback = true
			continue
		}
		if err != nil {
			moved = false
		}
		return a, done, moved, pl, err
	}
}
