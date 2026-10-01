package policy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/interpreter"
)

// EvalOptions configure one evaluation.
type EvalOptions struct {
	Explain  bool          // build the trace
	Mode     Mode          // enforce (default) or shadow; shadow never returns Reservations
	Deadline time.Duration // a backstop, default 50 ms; the CEL cost limit is the real bound
}

// DefaultDeadline is EvalOptions.Deadline when it is zero.
const DefaultDeadline = 50 * time.Millisecond

// Evaluate decides one action under one policy (S11 §3.3, docs/POLICY_LANGUAGE.md §6). It is pure:
// no I/O, no clock and no randomness reach the decision, so the same input always gives the same
// decision (Elapsed, which is not serialised, is the only exception). It returns no Go error:
// every failure is a deny, and an error never widens access.
func Evaluate(ctx context.Context, c *Compiled, in Input, o EvalOptions) Decision {
	start := time.Now()
	p, err := Prepare(in)
	var d Decision
	if err != nil {
		d = invalidInput(err)
	} else {
		d = evaluatePrepared(ctx, c, p, o)
	}
	d.Elapsed = time.Since(start)
	return d
}

// EvaluatePair evaluates the enforced policy and, when shadow is not nil, the shadow policy over
// the same prepared input. The shadow decision is independent and never returns Reservations.
func EvaluatePair(ctx context.Context, enforce, shadow *Compiled, in Input, o EvalOptions) (Decision, *Decision) {
	start := time.Now()
	p, err := Prepare(in)
	if err != nil {
		d := invalidInput(err)
		d.Elapsed = time.Since(start)
		if shadow == nil {
			return d, nil
		}
		s := d
		return d, &s
	}
	o.Mode = ModeEnforce
	d := evaluatePrepared(ctx, enforce, p, o)
	d.Elapsed = time.Since(start)
	if shadow == nil {
		return d, nil
	}
	s0 := time.Now()
	o.Mode = ModeShadow
	s := evaluatePrepared(ctx, shadow, p, o)
	s.Elapsed = time.Since(s0)
	return d, &s
}

func invalidInput(err error) Decision {
	reason := fmt.Sprintf(msgInvalidInput, err.Error())
	return Decision{Outcome: Deny, Code: CodePolicyDenied, Source: SourceBuiltin, RuleID: RuleInvalidInput,
		Reason: reason, Message: reason, Facts: DerivedFacts{SQL: noSQL()}}
}

func unavailable(why string) Decision {
	d := Decision{Outcome: Deny, Code: CodePolicyUnavailable, Source: SourceBuiltin, RuleID: RuleUnavailable,
		Message: msgUnavailable, Reason: msgUnavailable}
	if why != "" {
		d.Errors = []string{why}
	}
	return d
}

// evaluator carries one evaluation's state.
type evaluator struct {
	ctx     context.Context
	c       *Compiled
	p       *Prepared
	o       EvalOptions
	explain bool
	d       Decision
	res     []Reservation
	cost    []CostTarget
	cur     string // the rule or budget being evaluated, for the time-limit error
}

// errDeadline marks a deadline or an evaluator failure inside CEL: the decision is already set to
// policy_unavailable.
var errDeadline = errors.New("deadline")

func evaluatePrepared(ctx context.Context, c *Compiled, p *Prepared, o EvalOptions) (d Decision) {
	defer func() {
		if r := recover(); r != nil {
			d = unavailable("evaluator error")
			d.Facts = p.Facts()
		}
	}()
	if c == nil {
		d = unavailable("no policy")
		d.Facts = p.Facts()
		return d
	}
	if o.Deadline <= 0 {
		o.Deadline = DefaultDeadline
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, o.Deadline)
	defer cancel()
	e := &evaluator{ctx: ctx, c: c, p: p, o: o, explain: o.Explain}
	e.d.Facts = p.Facts()
	e.d.SQLGuard = p.sqlv
	if e.stop() || e.agentBudgets() || e.policyBudgets() || e.rules() {
		return e.finish()
	}
	e.decideDefault()
	return e.finish()
}

func (e *evaluator) finish() Decision {
	if e.ctx.Err() != nil && e.d.Outcome != Deny {
		// A later deny might have matched: never let a deadline widen access.
		d := unavailable("deadline")
		d.Facts, d.SQLGuard, d.Trace = e.d.Facts, e.d.SQLGuard, e.d.Trace
		return d
	}
	if e.d.Outcome != Deny && e.o.Mode != ModeShadow {
		e.d.Reservations = e.res
		e.d.CostBudgets = e.cost
	}
	return e.d
}

// deny sets a deny decision and drops reservations.
func (e *evaluator) deny(code Code, source, id string, line int, reason, message string) {
	e.d.Outcome, e.d.Code, e.d.Source, e.d.RuleID, e.d.RuleLine = Deny, code, source, id, line
	e.d.Reason, e.d.Message = reason, message
	e.res, e.cost = nil, nil
}

// 1. Stop: defence in depth (the identity layer already refuses these), and honest replays.
func (e *evaluator) stop() bool {
	in := &e.p.in
	name := clipName(in.Agent.Name)
	switch {
	case in.Org.Stopped:
		e.deny(CodeOrgStopped, SourceStop, "", 0, "", msgOrgStopped)
		e.step(Step{Step: StepStop, Result: ResultExceeded, Detail: "org stopped"})
		return true
	case in.Agent.Status == StatusStopped:
		e.deny(CodeAgentStopped, SourceStop, "", 0, "", fmt.Sprintf(msgAgentStopped, name))
		e.step(Step{Step: StepStop, Result: ResultExceeded, Detail: "agent stopped"})
		return true
	case in.Agent.Status == StatusSuspended:
		e.deny(CodeAgentSuspended, SourceStop, "", 0, "", fmt.Sprintf(msgAgentSuspended, name))
		e.step(Step{Step: StepStop, Result: ResultExceeded, Detail: "agent suspended"})
		return true
	}
	e.step(Step{Step: StepStop, Result: ResultPass, Detail: "agent active · org running"})
	return false
}

func (e *evaluator) windowStart(w string) time.Time { return WindowStart(w, e.p.in.Time, e.p.loc) }

func (e *evaluator) hit(id, per, window string, limit, used int64, unit string) bool {
	return e.hitProjected(id, per, window, limit, used, 0, unit)
}

// hitProjected denies with budget_exceeded. projected > 0 is a measured budget's total with this
// action included; the message and the trace then name it.
func (e *evaluator) hitProjected(id, per, window string, limit, used, projected int64, unit string) bool {
	end := WindowEnd(window, e.p.in.Time, e.p.loc)
	h := &BudgetHit{ID: id, Per: per, Window: window, Limit: limit, Used: used, Projected: projected, Unit: unit, ResetsAt: end,
		RetryAfterS: int64(math.Ceil(end.Sub(e.p.in.Time).Seconds()))}
	if h.RetryAfterS < 1 {
		h.RetryAfterS = 1
	}
	msg := budgetMessage(h, e.p.in.Agent.Name, e.p.in.Org.Timezone, e.p.in.Time)
	e.deny(CodeBudgetExceeded, SourceBudget, id, 0, "", msg)
	e.d.Budget = h
	detail := usedDetail(used, limit, unit, window)
	if projected > 0 {
		detail = fmt.Sprintf("%s + %s = %s of %s %s", formatMoney(used, unit), formatMoney(projected-used, unit),
			formatMoney(projected, unit), formatMoney(limit, unit), windowPhrase(window))
	}
	e.step(Step{Step: StepBudget, ID: id, Result: ResultExceeded, Detail: detail})
	return true
}

func usedDetail(used, limit int64, unit, window string) string {
	if unit == "calls" {
		return fmt.Sprintf("%s of %s calls %s", thousands(used), thousands(limit), windowPhrase(window))
	}
	return fmt.Sprintf("%s of %s %s", formatMoney(used, unit), formatMoney(limit, unit), windowPhrase(window))
}

// 1b. The agent's own budgets (S08): calls per minute and per day on the standard calls counters,
// spend per day on the standard spend counter (cost based).
func (e *evaluator) agentBudgets() bool {
	in := &e.p.in
	u := &in.Usage
	agent := in.Agent.ID
	var b AgentBudgets
	if in.Agent.Budgets != nil {
		b = *in.Agent.Budgets
	}
	type std struct {
		window string
		used   int64
		limit  *int64
		id     string
	}
	for _, s := range []std{
		{WindowMinute, u.CallsMinute, b.CallsPerMinute, AgentCallsPerMinute},
		{WindowHour, u.CallsHour, nil, ""},
		{WindowDay, u.CallsToday, b.CallsPerDay, AgentCallsPerDay},
	} {
		r := Reservation{Scope: agent, Counter: CounterCalls, Window: s.window, WindowStart: e.windowStart(s.window),
			Amount: 1, ResetsAt: WindowEnd(s.window, in.Time, e.p.loc)}
		if s.limit != nil {
			if s.used+1 > *s.limit {
				return e.hit(s.id, PerAgent, s.window, *s.limit, s.used, "calls")
			}
			r.Budget, r.Limit = s.id, *s.limit
			e.step(Step{Step: StepBudget, ID: s.id, Result: ResultPass, Detail: usedDetail(s.used, *s.limit, "calls", s.window)})
		}
		e.res = append(e.res, r)
	}
	dayStart := e.windowStart(WindowDay)
	e.cost = append(e.cost, CostTarget{Scope: agent, Window: WindowDay, WindowStart: dayStart})
	if sp := b.SpendPerDay; sp != nil {
		used := u.SpendToday
		if u.Currency != "" && u.Currency != sp.Currency {
			used = 0
			e.step(Step{Step: StepBudget, ID: AgentSpendPerDay, Result: ResultPass, Detail: "currency_mismatch: usage is in " + u.Currency})
		}
		if used >= sp.Amount {
			return e.hit(AgentSpendPerDay, PerAgent, WindowDay, sp.Amount, used, sp.Currency)
		}
		e.step(Step{Step: StepBudget, ID: AgentSpendPerDay, Result: ResultPass, Detail: usedDetail(used, sp.Amount, sp.Currency, WindowDay)})
	}
	return false
}

// 2. The policy's budgets, in file order. Every budget whose match applies must fit.
func (e *evaluator) policyBudgets() bool {
	in := &e.p.in
	for i := range e.c.Budgets {
		b := &e.c.Budgets[i]
		if !b.Match.Match(in.Tool.ToolFacts, in.Agent.AgentFacts) {
			e.step(Step{Step: StepBudget, ID: b.ID, Result: ResultNoMatch, Detail: "match doesn't cover this action"})
			continue
		}
		var amount int64
		costBased := false
		unit := "calls"
		limit := b.MaxCalls
		switch {
		case b.MaxSpend == nil:
			amount = 1
		case b.Measure != nil:
			unit, limit = b.MaxSpend.Currency, b.MaxSpend.Amount
			e.cur = "budget:" + b.ID
			v, cost, err := e.eval(b.Measure)
			if errors.Is(err, errDeadline) {
				return true
			}
			if err == nil {
				n, ok := v.(types.Int)
				if !ok {
					err = fmt.Errorf("the measure returned %s, not a whole number", typeName(v))
				} else if n < 0 {
					err = fmt.Errorf("the measure returned %d, which is negative", int64(n))
				} else {
					amount = int64(n)
				}
			}
			if err != nil {
				reason := fmt.Sprintf(msgMeasureError, b.ID, shortError(err))
				if isCostLimit(err) {
					reason = fmt.Sprintf(msgCostLimit, thousands(MaxCost))
				}
				e.deny(CodePolicyDenied, SourceBudget, "budget:"+b.ID, b.Pos.Line, reason,
					fmt.Sprintf(msgDenyRule, "budget:"+b.ID, reason))
				e.d.Errors = append(e.d.Errors, "budget:"+b.ID+": "+shortError(err))
				e.step(Step{Step: StepBudget, ID: b.ID, Line: b.Pos.Line, Result: ResultError, Detail: "measure: " + shortError(err), Cost: cost})
				return true
			}
		default:
			unit, limit = b.MaxSpend.Currency, b.MaxSpend.Amount
			costBased = true
		}
		used := in.Usage.Budgets[b.ID]
		per := b.Per
		if per == "" {
			per = PerAgent
		}
		if costBased && used >= limit || !costBased && used+amount > limit {
			if b.Measure != nil && amount > 0 {
				return e.hitProjected(b.ID, per, b.Window, limit, used, used+amount, unit)
			}
			return e.hit(b.ID, per, b.Window, limit, used, unit)
		}
		scope := in.Agent.ID
		if per == PerOrg {
			scope = ScopeOrg
		}
		start := e.windowStart(b.Window)
		if costBased {
			e.cost = append(e.cost, CostTarget{Budget: b.ID, Scope: scope, Window: b.Window, WindowStart: start, Currency: unit})
		} else if amount > 0 {
			e.res = append(e.res, Reservation{Budget: b.ID, Scope: scope, Counter: BudgetCounter(b.ID), Window: b.Window,
				WindowStart: start, Amount: amount, Limit: limit, ResetsAt: WindowEnd(b.Window, in.Time, e.p.loc)})
		}
		e.step(Step{Step: StepBudget, ID: b.ID, Result: ResultPass, Detail: usedDetail(used, limit, unit, b.Window)})
	}
	return false
}

// 3. Rules, in file order: the first whose match matches and whose when is true decides.
func (e *evaluator) rules() bool {
	in := &e.p.in
	for i := range e.c.Rules {
		r := &e.c.Rules[i]
		if !r.Match.Match(in.Tool.ToolFacts, in.Agent.AgentFacts) {
			e.step(Step{Step: StepRule, ID: r.ID, Line: r.Pos.Line, Result: ResultNoMatch, Detail: "match doesn't cover this action"})
			continue
		}
		if r.When == nil {
			e.decideRule(r, "")
			e.step(Step{Step: StepRule, ID: r.ID, Line: r.Pos.Line, Result: ResultMatch, Detail: "match covers this action"})
			e.notEvaluated(i)
			return true
		}
		e.cur = r.ID
		v, cost, err := e.eval(r.When)
		if errors.Is(err, errDeadline) {
			return true
		}
		if err == nil {
			b, ok := v.(types.Bool)
			switch {
			case !ok:
				err = fmt.Errorf("when returned %s, not true or false", typeName(v))
			case !bool(b):
				e.step(Step{Step: StepRule, ID: r.ID, Line: r.Pos.Line, Result: ResultNoMatch, Detail: "when → false", Cost: cost})
				continue
			default:
				e.decideRule(r, "")
				e.step(Step{Step: StepRule, ID: r.ID, Line: r.Pos.Line, Result: ResultMatch, Detail: "when → true", Cost: cost})
				e.notEvaluated(i)
				return true
			}
		}
		short := shortError(err)
		e.d.Errors = append(e.d.Errors, r.ID+": "+short)
		if isCostLimit(err) {
			// DL-024: over the runtime cost limit is a deny with a clear reason, whatever the
			// rule's decision. An agent can't buy its way past a rule with huge arguments.
			reason := fmt.Sprintf(msgCostLimit, thousands(MaxCost))
			e.deny(CodePolicyDenied, SourceRule, r.ID, r.Pos.Line, reason, fmt.Sprintf(msgDenyRule, r.ID, reason))
			e.step(Step{Step: StepRule, ID: r.ID, Line: r.Pos.Line, Result: ResultError, Detail: "when: cost limit reached", Cost: cost})
			e.notEvaluated(i)
			return true
		}
		if r.Decision == Allow {
			// Invariant 1: an error never widens access, so an allow rule counts as not matching.
			e.step(Step{Step: StepRule, ID: r.ID, Line: r.Pos.Line, Result: ResultError, Detail: "when error: " + short, Cost: cost})
			continue
		}
		e.decideRule(r, short)
		e.step(Step{Step: StepRule, ID: r.ID, Line: r.Pos.Line, Result: ResultError, Detail: "when error: " + short + " · decides", Cost: cost})
		e.notEvaluated(i)
		return true
	}
	return false
}

func (e *evaluator) notEvaluated(i int) {
	if n := len(e.c.Rules) - i - 1; n > 0 {
		e.step(Step{Step: StepRule, Result: ResultNotEvaluated, Detail: fmt.Sprintf("%s not evaluated", plural(n, "more rule", "more rules"))})
	}
}

func (e *evaluator) verify(v Level) Level {
	if v != "" {
		return v
	}
	if e.c.Defaults.Verify != "" {
		return e.c.Defaults.Verify
	}
	return L0
}

// decideRule applies a matching rule. errShort is non-empty when its when failed (deny and
// require_approval rules still decide, with the suffix on the reason).
func (e *evaluator) decideRule(r *Rule, errShort string) {
	reason := r.Reason
	if errShort != "" {
		reason += fmt.Sprintf(msgRuleErrorSuffix, errShort)
	}
	e.d.Source, e.d.RuleID, e.d.RuleLine, e.d.Reason = SourceRule, r.ID, r.Pos.Line, reason
	switch r.Decision {
	case Allow:
		e.d.Outcome, e.d.Code = Allow, CodeNone
		e.d.Verify = e.verify(r.Verify)
		e.d.Message = ""
	case RequireApproval:
		e.d.Outcome, e.d.Code = RequireApproval, CodeApprovalRequired
		a := e.c.Defaults.Approval
		if r.Approval != nil {
			a = *r.Approval
		}
		e.d.Approval = &a
		e.d.Verify = e.verify(r.Verify)
		e.d.Notify = r.Notify
		e.d.Message = fmt.Sprintf(msgApproval, r.ID, reason)
	default:
		e.deny(CodePolicyDenied, SourceRule, r.ID, r.Pos.Line, reason, fmt.Sprintf(msgDenyRule, r.ID, reason))
		e.d.Notify = r.Notify
	}
}

// 4. The default.
func (e *evaluator) decideDefault() {
	df := &e.c.Defaults
	e.d.Source, e.d.Reason = SourceDefault, df.Reason
	switch df.Decision {
	case Allow:
		e.d.Outcome, e.d.Code, e.d.Verify = Allow, CodeNone, e.verify(df.Verify)
	case RequireApproval:
		a := df.Approval
		e.d.Outcome, e.d.Code, e.d.Approval, e.d.Verify, e.d.Notify = RequireApproval, CodeApprovalRequired, &a, e.verify(df.Verify), df.Notify
		e.d.Message = fmt.Sprintf(msgApproval, e.c.Name, df.Reason)
	default:
		e.deny(CodePolicyDenied, SourceDefault, "", 0, df.Reason, fmt.Sprintf(msgDenyRule, e.c.Name, df.Reason))
		e.d.Notify = df.Notify
	}
	e.step(Step{Step: StepDefault, Result: ResultMatch, Detail: "no rule matched; defaults." + string(df.Decision)})
}

// eval runs one CEL program with the deadline and the cost limit. A deadline hit sets the
// decision to policy_unavailable and returns errDeadline.
func (e *evaluator) eval(prg cel.Program) (ref any, cost uint64, err error) {
	out, det, err := prg.ContextEval(e.ctx, &e.p.act)
	if det != nil && e.explain {
		if c := det.ActualCost(); c != nil {
			cost = *c
		}
	}
	if e.ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		why := "deadline"
		if e.cur != "" {
			why = fmt.Sprintf(errTimeLimit, e.cur, e.o.Deadline)
		}
		d := unavailable(why)
		d.Facts, d.SQLGuard, d.Trace = e.d.Facts, e.d.SQLGuard, e.d.Trace
		e.d = d
		e.res, e.cost = nil, nil
		return nil, cost, errDeadline
	}
	if err != nil && strings.HasPrefix(err.Error(), "internal error") {
		// cel-go turns a panic inside evaluation (a custom function, the interpreter) into this
		// error. It is an evaluator failure, not a rule's error: deny policy_unavailable.
		d := unavailable("evaluator error")
		d.Facts, d.SQLGuard, d.Trace = e.d.Facts, e.d.SQLGuard, e.d.Trace
		e.d = d
		e.res, e.cost = nil, nil
		return nil, cost, errDeadline
	}
	if err != nil {
		return nil, cost, err
	}
	return out, cost, nil
}

// isCostLimit reports whether err is CEL's runtime cost limit.
func isCostLimit(err error) bool {
	var c interpreter.EvalCancelledError
	if errors.As(err, &c) {
		return c.Cause == interpreter.CostLimitExceeded
	}
	return false
}

func typeName(v any) string {
	switch v.(type) {
	case types.String:
		return "a string"
	case types.Int, types.Uint, types.Double:
		return "a number"
	case types.Null:
		return "null"
	case types.Bool:
		return "true or false"
	}
	if rv, ok := v.(ref.Val); ok {
		return "a " + rv.Type().TypeName()
	}
	return fmt.Sprintf("%T", v)
}

// AlwaysDenies reports whether every call to this tool by this agent is denied by the policy's
// static structure (S14 hides such tools from tools/list). It ignores budgets and Stop, which are
// transient. A rule with a when can't be decided statically, so it answers false.
func AlwaysDenies(c *Compiled, tool ToolFacts, agent AgentFacts) bool {
	deny, _, _ := alwaysDenies(c, tool, agent)
	return deny
}

// alwaysDenies walks the rules once and returns the deciding rule (nil for the default). S14's
// AlwaysDeniesWhy builds on it.
func alwaysDenies(c *Compiled, tool ToolFacts, agent AgentFacts) (deny bool, rule *Rule, decided bool) {
	if c == nil {
		return true, nil, true
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if !r.Match.Match(tool, agent) {
			continue
		}
		if r.When != nil {
			return false, r, false
		}
		return r.Decision == Deny, r, true
	}
	return c.Defaults.Decision == Deny, nil, true
}
