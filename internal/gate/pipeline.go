package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
)

// Config is the gate.* section of deedglass.yaml (S13 §3.10), with defaults for zero values.
type Config struct {
	WaitTimeout       time.Duration // POST /v1/actions default wait (max 25 s)
	ExecuteTimeout    time.Duration // a gateway connector call
	EdgeResultTimeout time.Duration // proxy: how long the edge has to report
	MaxArgsBytes      int
	MaxResultBytes    int
	WorkerConcurrency int
	WorkerMaxAttempts int
	ApprovalTTL       time.Duration // when the rule sets no expires_in
	MaxWaitsPerAgent  int           // open GET …?wait= per agent
}

// MaxWait is the longest any request waits (POST wait, GET ?wait=).
const MaxWait = 25 * time.Second

// sweepGrace is added to the execution deadline before crash recovery may claim an action, so
// the instance running it records its own timeout first.
const sweepGrace = 5 * time.Second

func (c Config) withDefaults() Config {
	if c.WaitTimeout <= 0 {
		c.WaitTimeout = 10 * time.Second
	}
	if c.WaitTimeout > MaxWait {
		c.WaitTimeout = MaxWait
	}
	if c.ExecuteTimeout <= 0 {
		c.ExecuteTimeout = 30 * time.Second
	}
	if c.EdgeResultTimeout <= 0 {
		c.EdgeResultTimeout = 10 * time.Minute
	}
	if c.MaxArgsBytes <= 0 || c.MaxArgsBytes > MaxArgsBytes {
		c.MaxArgsBytes = MaxArgsBytes
	}
	if c.MaxResultBytes <= 0 {
		c.MaxResultBytes = 256 << 10
	}
	if c.WorkerConcurrency <= 0 {
		c.WorkerConcurrency = 4
	}
	if c.WorkerMaxAttempts <= 0 {
		c.WorkerMaxAttempts = 8
	}
	if c.ApprovalTTL <= 0 {
		c.ApprovalTTL = DefaultApprovalTTL
	}
	if c.MaxWaitsPerAgent <= 0 {
		c.MaxWaitsPerAgent = 20
	}
	return c
}

// Options are the Pipeline's dependencies. DB, Ledger, Policies, Usage and Resolver are required.
type Options struct {
	DB        *store.DB
	Ledger    *ledger.Ledger // with a seal key
	Policies  Policies
	Usage     Usage
	Intents   identity.IntentChecker // nil: headers are ignored (tests)
	Guard     identity.Guard         // nil: long polls don't end on Stop (tests)
	Resolver  Resolver
	Witness   Witnesser // nil: L0
	Approvals Approvals // nil: StubApprovals
	Broker    *Broker   // nil: a private one
	Metrics   *Metrics  // nil: a private one
	Config    Config
	Logger    *slog.Logger
	Clock     func() time.Time
}

// Pipeline is the Gate: every agent action, from every surface, enters through Submit and leaves
// as a sealed deed (S13).
type Pipeline struct {
	o      Options
	cfg    Config
	log    *slog.Logger
	now    func() time.Time
	broker *Broker
	m      *Metrics

	inflight sync.WaitGroup // gateway executions running detached from their requests
	mu       sync.Mutex
	waits    map[string]int // open long polls per agent

	hooks hooks
}

// hooks are test seams: they let a test observe the moments the invariants talk about.
type hooks struct {
	beforeExecute func(connector.Call) // invariant 5: no transaction open here
	afterAdmit    func(ids.ActionID)   // crash tests: "killed between Tx A and Tx B"
}

// New returns a Pipeline.
func New(o Options) (*Pipeline, error) {
	if o.DB == nil || o.Ledger == nil || o.Policies == nil || o.Usage == nil || o.Resolver == nil {
		return nil, errors.New("gate: DB, Ledger, Policies, Usage and Resolver are required")
	}
	if o.Witness == nil {
		o.Witness = L0{}
	}
	cfg := o.Config.withDefaults()
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Approvals == nil {
		o.Approvals = StubApprovals{TTL: cfg.ApprovalTTL, Now: o.Clock}
	}
	if o.Broker == nil {
		o.Broker = NewBroker()
	}
	if o.Metrics == nil {
		o.Metrics = NewMetrics()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Pipeline{o: o, cfg: cfg, log: o.Logger, now: o.Clock, broker: o.Broker, m: o.Metrics, waits: map[string]int{}}, nil
}

// Metrics are the Gate's metrics (for serve's /metrics).
func (p *Pipeline) Metrics() *Metrics { return p.m }

// Broker is the wait broker (serve feeds it from deedglass_events).
func (p *Pipeline) Broker() *Broker { return p.broker }

// Config is the effective configuration.
func (p *Pipeline) Config() Config { return p.cfg }

// Close waits for detached executions to record their outcome, at most until ctx ends. Anything
// still running is recovered by the sweeper (crash recovery).
func (p *Pipeline) Close(ctx context.Context) error {
	done := make(chan struct{})
	go func() { p.inflight.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// plan is what admission hands to execution: an allowed gateway action, committed in executing.
type plan struct {
	subject      Subject
	exec         Executor
	call         connector.Call
	costTargets  []policy.CostTarget
	reservations []policy.Reservation
	actor        string
	requestID    string
	// link, when set, is the request's context: the tool call (and only the call, never the
	// recording) is cancelled with ErrRequestGone when it ends (ActionRequest.CancelOn).
	link context.Context
}

// admission is the outcome of Tx A.
type admission struct {
	view    *ledger.Action
	refusal *httpx.Error
	plan    *plan // set for an allowed gateway action
}

// errKeyRace: another request with the same Idempotency-Key was admitted between our check and
// our insert. The caller answers as a replay.
var errKeyRace = errors.New("gate: idempotency key taken by a concurrent request")

// Submit takes one action from an agent key through the pipeline: normalize, decide, record, and
// (gateway, allowed) execute, witness and seal. It returns the action and, for a refusal, the
// error to answer with (the action is attached to it and sealed). See S13 §3.4 for the
// transaction boundaries.
func (p *Pipeline) Submit(ctx context.Context, pr principal, req ActionRequest) (*Action, error) {
	t0 := p.now()
	if pr.Kind != httpx.KindAgentKey || pr.AgentID.IsZero() || pr.OrgID.IsZero() {
		return nil, httpx.ErrInsufficientScope(httpx.ScopeActionsExecute)
	}
	n, err := Normalize(req, p.cfg.MaxArgsBytes)
	if err != nil {
		return nil, err
	}
	wait := p.cfg.WaitTimeout
	if req.WaitSet {
		wait = min(max(req.Wait, 0), MaxWait)
	}
	// The tool (gateway): the in-process registry, no I/O.
	var (
		ex   Executor
		spec connector.ToolSpec
	)
	unknown := n.Unknown
	if !unknown && n.Mode == ModeGateway {
		var ctype string
		ex, spec, ctype, err = p.o.Resolver.Resolve(ctx, pr.OrgID, n.Tool)
		switch {
		case errors.Is(err, connector.ErrToolUnknown):
			unknown = true
		case errors.Is(err, connector.ErrConnectorUnavailable):
			return nil, httpx.E(httpx.CodeConnectorUnavailable, httpx.Msg(ConnectorUnavailableMessage(n.Connector)), httpx.Cause(err))
		case err != nil:
			return nil, err
		default:
			n.ConnectorType = ctype
		}
	}
	// The active policies, before any transaction: a cold load reads Postgres on its own.
	var act *policy.Active
	if !unknown {
		if act, err = p.o.Policies.Active(ctx, pr.OrgID.String()); err != nil {
			return nil, err
		}
	}
	// Idempotency: a cheap read before anything is reserved, so a replay never reserves.
	var release *store.KeyedAction
	if req.IdempotencyKey != "" {
		a, k, err := p.replay(ctx, pr, n, req, wait)
		if err != nil {
			return nil, err
		}
		if a != nil {
			return a.answer()
		}
		release = k // nil, or an action whose 24-hour window has closed
	}
	adm, err := p.admit(ctx, pr, n, req, act, spec, ex, unknown, release)
	if errors.Is(err, errKeyRace) {
		a, _, err := p.replay(ctx, pr, n, req, wait)
		switch {
		case err != nil:
			return nil, err
		case a == nil:
			return nil, httpx.E(httpx.CodeIdempotencyKeyInUse)
		}
		return a.answer()
	}
	if err != nil {
		return nil, err
	}
	if p.hooks.afterAdmit != nil {
		p.hooks.afterAdmit(ids.ActionID(mustAction(adm.view.ID)))
	}
	p.broker.Notify(adm.view.ID)
	if adm.plan == nil {
		p.m.observeOverhead(p.now().Sub(t0))
		a := &Action{Action: *adm.view, refusal: adm.refusal}
		p.m.action(n.Mode, a.Status)
		return a.answer()
	}
	// Allowed, gateway: run the tool with no transaction open. The execution is detached from
	// the request, so a client that stops waiting never leaves an action half-recorded.
	done := make(chan *ledger.Action, 1)
	var toolTime time.Duration
	p.inflight.Add(1)
	if req.CancelOn != nil {
		adm.plan.link = req.CancelOn
	}
	go func() {
		defer p.inflight.Done()
		v, d := p.run(context.WithoutCancel(ctx), adm.plan)
		toolTime = d
		done <- v
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case v := <-done:
		if v == nil { // recording failed (the sweeper finishes it): answer with what is stored
			if v, err = p.o.Ledger.ReadActionView(context.WithoutCancel(ctx), pr.OrgID, adm.view.ID); err != nil {
				return nil, err
			}
		}
		p.m.observeOverhead(p.now().Sub(t0) - toolTime)
		a := &Action{Action: *v}
		p.m.action(n.Mode, a.Status)
		return a, nil
	case <-timer.C:
	case <-ctx.Done():
	}
	v, err := p.o.Ledger.ReadActionView(context.WithoutCancel(ctx), pr.OrgID, adm.view.ID)
	if err != nil {
		v = adm.view
	}
	a := &Action{Action: *v}
	p.m.action(n.Mode, a.Status)
	return a, nil
}

func mustAction(id string) ids.ActionID {
	a, _ := ids.Parse[ids.ActionKind](id)
	return a
}

// admit is Tx A (S13 §3.4): the signed intent, the decision, the reservation, the action row and
// its transitions up to denied → sealed, pending_approval, or executing. One transaction, retried
// once on a chain conflict.
func (p *Pipeline) admit(ctx context.Context, pr principal, n *Normalized, req ActionRequest, act *policy.Active,
	spec connector.ToolSpec, ex Executor, unknown bool, release *store.KeyedAction) (*admission, error) {
	org, agent := pr.OrgID, pr.AgentID
	actor := agent.String()
	var adm *admission
	err := p.o.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		adm = &admission{}
		// 1. The agent and its org, read fresh in this transaction (the Stop state is what counts).
		subj, err := store.LoadGateSubject(ctx, tx, org, agent)
		if errors.Is(err, store.ErrNotFound) {
			return httpx.ErrAgentStopped(pr.AgentName) // an agent that doesn't exist never acts
		}
		if err != nil {
			return err
		}
		// 2. The signed intent (S09), inside this transaction: a failed insert never burns the
		// nonce. Identity failures are not actions: nothing is recorded. Mode off ignores the
		// header; optional checks one when it is sent; required needs one, and MCP clients can't
		// sign in v1.
		obo := identity.OBO{OnBehalfOf: req.OnBehalfOf, Session: req.Session}
		ir := identity.IntentResult{OBO: obo}
		mode := identity.IntentMode(subj.IntentMode)
		if p.o.Intents != nil && (mode == identity.IntentRequired || mode == identity.IntentOptional && req.Intent != "") {
			if isMCP(n.Transport) && mode == identity.IntentRequired {
				e := httpx.ErrIntent("required_mcp", "", "agent", subj.AgentName)
				httpx.Cause(&identity.IntentRefusal{Reason: "required"})(e)
				return e
			}
			if ir, err = p.o.Intents.CheckTx(ctx, tx, identity.IntentInput{Principal: &pr, Header: req.Intent, Tool: n.Tool, Args: n.Args, OBO: obo}); err != nil {
				return err
			}
		}
		now := p.now()
		in, loc, err := buildInput(org, agent, subj, n, ir, now)
		if err != nil {
			return err
		}
		if n.Mode == ModeGateway {
			withSideEffect(&in, spec)
		}
		// 3. The decision: Stop first, then an unknown tool, then the policies (S11's sequence).
		var (
			d      policy.Decision
			sd     *policy.Decision
			loaded *policy.Loaded
			shadow *policy.Loaded
		)
		switch {
		case subj.OrgStopped || subj.AgentStatus != policy.StatusActive:
			d = stopDecision(subj)
		case unknown:
			d = toolUnknownDecision(n.Tool)
		default:
			loaded, shadow = act.Enforce, act.Shadow
			enforce := loaded.Policy()
			cur := "usd"
			if in.Agent.Budgets != nil && in.Agent.Budgets.SpendPerDay != nil {
				cur = in.Agent.Budgets.SpendPerDay.Currency
			}
			in.Usage, err = p.o.Usage.SnapshotTx(ctx, tx, policy.SnapshotRequestFor(enforce, org.String(), agent.String(), loc, now, cur))
			if err != nil {
				return err
			}
			d, sd = policy.EvaluatePair(ctx, enforce, shadow.Policy(), in, policy.EvalOptions{})
			if loaded.Unavailable != nil && d.Code == policy.CodePolicyUnavailable && len(d.Errors) == 0 {
				d.Errors = []string{loaded.Unavailable.Error()}
			}
			p.watchTimeLimit(org, agent, d, sd)
		}
		// 4. Reserve (allow, require_approval), in a savepoint: a refused reservation is a
		// budget_exceeded deny, recorded like any other.
		reserved := false
		if d.Outcome != policy.Deny && len(d.Reservations) > 0 {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			err = p.o.Usage.ReserveTx(ctx, sp, org.String(), d.Reservations)
			if ex := policy.AsExceeded(err); ex != nil {
				if err := sp.Rollback(ctx); err != nil {
					return err
				}
				d = policy.ExceededDecision(ex, d.Reservations, subj.AgentName, subj.Timezone, now)
			} else if err != nil {
				_ = sp.Rollback(ctx)
				return err
			} else {
				if err := sp.Commit(ctx); err != nil {
					return err
				}
				reserved = true
			}
		}
		// 5. Record: the action in received, its key, and the transitions.
		if release != nil {
			if _, err := store.ReleaseActionKey(ctx, tx, release.ID, req.IdempotencyKey); err != nil {
				return err
			}
		}
		na := ledger.NewAction{Agent: agent, Mode: string(n.Mode), Tool: n.Tool, Request: n.Args, RequestDigest: n.Digest,
			Actor: actor, RequestID: req.RequestID, Fingerprint: n.Fingerprint, Context: n.Context, Livemode: pr.Livemode,
			ToolDigest: n.ToolDigest, OnBehalfOf: nonEmpty(ir.OBO.OnBehalfOf), Session: nonEmpty(ir.OBO.Session),
			IntentSigned: ir.Signed, IdempotencyKey: nonEmpty(req.IdempotencyKey)}
		if ir.Signed {
			na.IntentHash = &ir.IntentHash
		}
		if !pr.KeyID.IsZero() {
			k := pr.KeyID.String()
			na.APIKeyID = &k
		}
		if n.Connector != "" {
			c := n.Connector
			na.ConnectorSlug = &c
		}
		id, err := p.o.Ledger.CreateAction(ctx, tx, org, na)
		if isKeyRace(err) {
			return errKeyRace
		}
		if err != nil {
			return err
		}
		ev := ledger.EventData{Actor: actor, RequestID: req.RequestID}
		patch, err := decisionPatch(in, d, sd, loaded, shadow, reserved)
		if err != nil {
			return err
		}
		evEval := ev
		evEval.RuleID = nonEmpty(d.RuleID)
		sub := Subject{ID: id, Org: org, Agent: agent, Mode: n.Mode, Tool: n.Tool, ConnectorType: n.ConnectorType}
		// received → authenticated → evaluated → (denied | pending_approval | executing): one
		// statement, one event row per edge (ledger.Advance).
		steps := []ledger.Step{{To: Authenticated, Event: ev}, {To: Evaluated, Event: evEval, Patch: &patch}}
		switch d.Outcome {
		case policy.Deny:
			evDeny := evEval
			evDeny.Reason = nonEmpty(string(d.Code))
			steps = append(steps, ledger.Step{To: Denied, Event: evDeny})
		case policy.RequireApproval:
			apr, _, err := p.o.Approvals.Request(ctx, tx, sub, d)
			if err != nil {
				return err
			}
			a := apr.String()
			evPend := evEval
			evPend.Reason, evPend.ApprovalID = strp("approval_required"), &a
			steps = append(steps, ledger.Step{To: PendingApproval, Event: evPend, Patch: &ledger.Patch{ApprovalID: &a}})
		default:
			deadline := now.Add(p.cfg.ExecuteTimeout + sweepGrace)
			if n.Mode == ModeProxy {
				deadline = now.Add(p.cfg.EdgeResultTimeout)
			}
			steps = append(steps, ledger.Step{To: Executing, Event: evEval, Patch: &ledger.Patch{ExecutingDeadline: &deadline}})
			if n.Mode == ModeGateway {
				adm.plan = &plan{subject: sub, exec: ex, actor: actor, requestID: req.RequestID,
					costTargets: d.CostBudgets, reservations: d.Reservations,
					call: connector.Call{ActionID: id, OrgID: org, AgentID: agent, Tool: spec, Qualified: n.Tool, Args: n.Args,
						IdempotencyKey: id.String(), Deadline: now.Add(p.cfg.ExecuteTimeout)}}
			}
		}
		if err := p.o.Ledger.Advance(ctx, tx, id.String(), Received, steps...); err != nil {
			return err
		}
		if adm.plan != nil {
			// Allowed, gateway: the answer is read after the tool runs (Tx B).
			adm.view = &ledger.Action{Object: "action", ID: id.String(), State: string(Executing), Status: ledger.StatusProcessing, Mode: string(n.Mode)}
			return nil
		}
		// Read the answer before sealing: the chain lock, taken by the seal, is then held only
		// for the deed itself and the commit.
		view, err := ledger.ReadAction(ctx, tx, org, id.String())
		if err != nil {
			return err
		}
		if d.Outcome == policy.Deny {
			if err := p.sealView(ctx, tx, id, view); err != nil {
				return err
			}
			adm.refusal = refusalFor(d, view)
		}
		adm.view = view
		return nil
	})
	if err != nil {
		return nil, err
	}
	return adm, nil
}

// isKeyRace reports a unique violation of actions_idem (org, agent, idempotency_key).
func isKeyRace(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "actions_idem"
}

// watchTimeLimit counts and logs every evaluation the 50 ms time limit stopped (DL-025): it fails
// closed, so it is a visible deny, and an operator should know how often it happens.
func (p *Pipeline) watchTimeLimit(org ids.OrgID, agent ids.AgentID, d policy.Decision, sd *policy.Decision) {
	if policy.TimeLimitTripped(d) {
		p.m.timeLimitEnforce.Add(1)
		p.log.Warn("gate: policy evaluation hit the time limit; the action was denied (policy_unavailable)",
			"org_id", org.String(), "agent_id", agent.String(), "mode", "enforce", "errors", d.Errors, "elapsed_ms", d.Elapsed.Milliseconds())
	}
	if sd != nil && policy.TimeLimitTripped(*sd) {
		p.m.timeLimitShadow.Add(1)
		p.log.Warn("gate: shadow policy evaluation hit the time limit (recorded, never enforced)",
			"org_id", org.String(), "agent_id", agent.String(), "mode", "shadow", "errors", sd.Errors, "elapsed_ms", sd.Elapsed.Milliseconds())
	}
}

// buildInput is S11's policy.Input for this action (S13 §3.6 step 4): raw facts only.
func buildInput(org ids.OrgID, agent ids.AgentID, subj store.GateSubject, n *Normalized, ir identity.IntentResult, now time.Time) (policy.Input, *time.Location, error) {
	labels := map[string]string{}
	if len(subj.Labels) > 0 {
		if err := json.Unmarshal(subj.Labels, &labels); err != nil {
			return policy.Input{}, nil, fmt.Errorf("gate: agent labels: %w", err)
		}
	}
	var budgets *policy.AgentBudgets
	if len(subj.Budgets) > 0 {
		var b policy.AgentBudgets
		if err := json.Unmarshal(subj.Budgets, &b); err != nil {
			return policy.Input{}, nil, fmt.Errorf("gate: agent budgets: %w", err)
		}
		if b.CallsPerMinute != nil || b.CallsPerDay != nil || b.SpendPerDay != nil {
			budgets = &b
		}
	}
	loc, err := policy.LoadLocation(subj.Timezone)
	if err != nil {
		loc = time.UTC
	}
	side := "unknown"
	ctype := n.ConnectorType
	in := policy.Input{V: policy.InputVersion,
		Org:     policy.OrgInput{OrgFacts: policy.OrgFacts{ID: org.String(), Timezone: subj.Timezone}, Stopped: subj.OrgStopped},
		Agent:   policy.AgentInput{AgentFacts: policy.AgentFacts{ID: agent.String(), Name: subj.AgentName, Labels: labels, Status: subj.AgentStatus}, Budgets: budgets},
		User:    policy.UserFacts{Ref: ir.OBO.OnBehalfOf},
		Session: policy.SessionFacts{Ref: ir.OBO.Session},
		Tool:    policy.ToolInput{ToolFacts: policy.ToolFacts{Name: n.Tool, Connector: n.Connector, ConnectorType: ctype, SideEffect: side}},
		Args:    n.Args,
		Request: policy.RequestFacts{Mode: string(n.Mode), Bytes: n.ArgsBytes, IntentSigned: ir.Signed, Digest: n.Digest},
		Time:    now.UTC(),
	}
	return in, loc, nil
}

// withSideEffect sets the tool's declared side effect (gateway); MCP tools stay unknown.
func withSideEffect(in *policy.Input, spec connector.ToolSpec) {
	if spec.SideEffect != "" {
		in.Tool.SideEffect = string(spec.SideEffect)
	}
}

// stopDecision is the evaluator's step 1 (S11) for a stopped org, or a stopped or suspended agent,
// with S08's messages.
func stopDecision(s store.GateSubject) policy.Decision {
	orgMsg, stopped, suspended := policy.StopMessages(s.AgentName)
	d := policy.Decision{Outcome: policy.Deny, Source: policy.SourceStop}
	switch {
	case s.OrgStopped:
		d.Code, d.Message = policy.CodeOrgStopped, orgMsg
	case s.AgentStatus == policy.StatusSuspended:
		d.Code, d.Message = policy.CodeAgentSuspended, suspended
	default:
		d.Code, d.Message = policy.CodeAgentStopped, stopped
	}
	return d
}

// RuleToolUnknown is the builtin rule id of a recorded tool_unknown denial.
const RuleToolUnknown = "tool-unknown"

// toolUnknownDecision is the builtin deny of a name no connector serves (S13 §3.6 step 1).
func toolUnknownDecision(tool string) policy.Decision {
	msg := ToolUnknownMessage(tool)
	return policy.Decision{Outcome: policy.Deny, Code: policy.Code(httpx.CodeToolUnknown), Source: policy.SourceBuiltin,
		RuleID: RuleToolUnknown, Reason: msg, Message: msg}
}

// decisionDetail is actions.decision_detail: what the decision carries beyond its columns.
type decisionDetail struct {
	RuleLine    int                  `json:"rule_line,omitempty"`
	Approval    *policy.ApprovalSpec `json:"approval,omitempty"`
	Verify      policy.Level         `json:"verify,omitempty"`
	Notify      []string             `json:"notify,omitempty"`
	Budget      *policy.BudgetHit    `json:"budget,omitempty"`
	CostBudgets []policy.CostTarget  `json:"cost_budgets,omitempty"`
	Errors      []string             `json:"errors,omitempty"`
	SQLGuard    string               `json:"sqlguard_version,omitempty"`
	Facts       *policy.DerivedFacts `json:"facts,omitempty"`
	ElapsedUS   int64                `json:"elapsed_us"`
	PolicyName  string               `json:"policy_name,omitempty"`
	Version     int                  `json:"policy_version_number,omitempty"`
}

// maxDetail keeps decision_detail under its 16 KiB column limit (facts are dropped first; they
// can be recomputed from policy_input).
const maxDetail = 15 << 10

func detailJSON(d policy.Decision, l *policy.Loaded) (json.RawMessage, error) {
	dd := decisionDetail{RuleLine: d.RuleLine, Approval: d.Approval, Verify: d.Verify, Notify: d.Notify, Budget: d.Budget,
		CostBudgets: d.CostBudgets, Errors: d.Errors, SQLGuard: d.SQLGuard, ElapsedUS: d.Elapsed.Microseconds()}
	if d.Facts.SQL.Present || d.Facts.SQL.Kind != "" {
		f := d.Facts
		dd.Facts = &f
	}
	if l != nil && !l.Builtin {
		dd.PolicyName, dd.Version = l.Name, l.Version
	}
	b, err := json.Marshal(dd)
	if err == nil && len(b) > maxDetail {
		dd.Facts = nil
		b, err = json.Marshal(dd)
	}
	if err == nil && len(b) > maxDetail {
		dd.Errors, dd.Notify = nil, nil
		b, err = json.Marshal(dd)
	}
	return b, err
}

// decisionPatch is what the edge into evaluated records: the input, the decision, the version
// that made it, the shadow decision next to it (recorded, never enforced) and the reservations.
func decisionPatch(in policy.Input, d policy.Decision, sd *policy.Decision, l, sl *policy.Loaded, reserved bool) (ledger.Patch, error) {
	inJSON, err := json.Marshal(in)
	if err != nil {
		return ledger.Patch{}, err
	}
	digest, err := in.Digest()
	if err != nil {
		return ledger.Patch{}, err
	}
	detail, err := detailJSON(d, l)
	if err != nil {
		return ledger.Patch{}, err
	}
	p := ledger.Patch{PolicyInput: inJSON, PolicyInputDigest: &digest, Decision: strp(string(d.Outcome)),
		DecisionCode: nonEmpty(string(d.Code)), DecisionSource: nonEmpty(d.Source), DecisionRuleID: nonEmpty(clip(d.RuleID, 200)),
		DecisionReason: nonEmpty(clip(d.Reason, 1990)), DecisionMessage: nonEmpty(clip(d.Message, 1990)), DecisionDetail: detail,
		PolicyMode: strp(string(policy.ModeEnforce))}
	if l != nil && !l.Builtin && l.Unavailable == nil && l.VersionID != "" {
		p.PolicyID, p.PolicyVersionID, p.PolicyVersionHash = nonEmpty(l.PolicyID), nonEmpty(l.VersionID), nonEmpty(l.VersionHash)
	}
	if sd != nil && sl != nil && sl.VersionID != "" {
		p.ShadowDecision, p.ShadowRuleID, p.ShadowVersionID = strp(string(sd.Outcome)), nonEmpty(clip(sd.RuleID, 200)), nonEmpty(sl.VersionID)
		sdetail, err := json.Marshal(struct {
			Code     policy.Code       `json:"code,omitempty"`
			Source   string            `json:"source"`
			Reason   string            `json:"reason,omitempty"`
			Message  string            `json:"message"`
			RuleLine int               `json:"rule_line,omitempty"`
			Budget   *policy.BudgetHit `json:"budget,omitempty"`
			Errors   []string          `json:"errors,omitempty"`
			Policy   string            `json:"policy_name,omitempty"`
			Version  int               `json:"policy_version_number,omitempty"`
		}{sd.Code, sd.Source, clip(sd.Reason, 1000), clip(sd.Message, 1000), sd.RuleLine, sd.Budget, sd.Errors, sl.Name, sl.Version})
		if err != nil {
			return ledger.Patch{}, err
		}
		p.ShadowDetail = sdetail
	}
	if reserved && len(d.Reservations) > 0 {
		b, err := json.Marshal(d.Reservations)
		if err != nil {
			return ledger.Patch{}, err
		}
		p.Reservations = b
	}
	return p, nil
}

// refusalFor is the error a denied action answers with (S13 §3.7): S11's message verbatim, the
// sealed action attached.
func refusalFor(d policy.Decision, view *ledger.Action) *httpx.Error {
	code := httpx.Code(d.Code)
	if _, ok := httpx.Lookup(code); !ok || code == "" {
		code = httpx.CodePolicyDenied
	}
	opts := []httpx.Opt{httpx.WithAction(view)}
	if d.Message != "" {
		opts = append(opts, httpx.Msg(d.Message))
	}
	if code == httpx.CodeBudgetExceeded {
		n := int64(1)
		if d.Budget != nil && d.Budget.RetryAfterS > 0 {
			n = d.Budget.RetryAfterS
		}
		opts = append(opts, httpx.WithHeader("Retry-After", strconv.FormatInt(n, 10)))
	}
	return httpx.E(code, opts...)
}

// run executes an admitted gateway action and records its outcome (Tx B). It returns the final
// view (nil if recording failed; the sweeper finishes it) and the tool's own run time.
func (p *Pipeline) run(ctx context.Context, pl *plan) (*ledger.Action, time.Duration) {
	// The tool gets the whole gate.execute_timeout from the moment it starts; the stored
	// executing_deadline adds sweepGrace on top, so crash recovery never races a live call.
	pl.call.Deadline = p.now().Add(p.cfg.ExecuteTimeout)
	ectx := ctx
	if pl.link != nil {
		var cancel context.CancelCauseFunc
		ectx, cancel = context.WithCancelCause(ctx)
		stop := context.AfterFunc(pl.link, func() { cancel(ErrRequestGone) })
		defer func() { stop(); cancel(nil) }()
	}
	e := p.execute(ectx, pl.exec, pl.call)
	tool := e.FinishedAt.Sub(e.StartedAt)
	v, err := p.complete(ctx, pl.subject, e, pl.costTargets, pl.reservations, pl.actor, pl.requestID)
	if err != nil {
		if !errors.Is(err, ErrStateConflict) {
			p.log.Error("gate: recording an execution failed; the sweeper will finish it", "org_id", pl.subject.Org.String(),
				"agent_id", pl.subject.Agent.String(), "action_id", pl.subject.ID.String(), "err", err)
		}
		return nil, tool
	}
	p.broker.Notify(pl.subject.ID.String())
	return v, tool
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func strp(s string) *string { return &s }
