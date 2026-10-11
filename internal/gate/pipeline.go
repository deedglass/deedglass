package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
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
	"github.com/deedglass/deedglass/internal/witness"
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
	Witness           WitnessConfig // witness.* (S19)
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
	DB       *store.DB
	Ledger   *ledger.Ledger // with a seal key
	Policies Policies
	Usage    Usage
	Intents  identity.IntentChecker // nil: headers are ignored (tests)
	Guard    identity.Guard         // nil: long polls don't end on Stop (tests)
	Resolver Resolver
	// Internal resolves the internal tools a person's action may run (S23: <connector>.undo,
	// wrapped by the undo executor). Nil: the console principal has nothing to run.
	Internal Resolver
	// Observer replaces the read-back path through the connectors' ReadBacker (tests). Nil: the
	// connectors read back.
	Observer  witness.Observer
	Approvals Approvals // nil: StubApprovals
	// Nudge makes the decoder poll a connector's slot fast (S21: an action started waiting for L2).
	// Nil without a decoder; set after New with SetNudge when the decoder starts later.
	Nudge   func(connectorID string)
	Broker  *Broker  // nil: a private one
	Metrics *Metrics // nil: a private one
	Config  Config
	Logger  *slog.Logger
	// Clock is the pipeline clock (S27 §3.2): the decision time in policy_input.time, every
	// timestamp the Gate writes on actions and action_events, and approval decisions. In
	// production time.Now; deedglass dev simulate passes a virtual clock.
	Clock func() time.Time
	// Anomalies feeds anomaly.* to the policies (S27 §3.8, opt-in per org). Nil: anomaly.active is
	// always false.
	Anomalies AnomalySource
	// Simulated (S27, dev only: deedglass dev simulate) scripts the verdicts. When set, the Gate asks
	// it instead of running the Witness's checks; its outcomes come from witness.Decide with the
	// method "simulated", so a simulated deed can never pass for real evidence.
	Simulated SimulatedWitness
}

// AnomalySource is the anomaly facts a policy sees for one agent at decision time (S27 §3.8), and
// the decision trace's line about them ("anomaly facts: volume_spike"; "" when inactive).
// internal/learn's FactsCache implements it; it fails closed (detector_stale) on its own.
type AnomalySource interface {
	Facts(ctx context.Context, org ids.OrgID, agent ids.AgentID, at time.Time) (policy.AnomalyFacts, string)
}

// SimulatedWitness scripts verdicts for deedglass dev simulate (S27 §3.3). It is never wired by
// serve.
type SimulatedWitness interface {
	Verdict(ctx context.Context, s Subject, e Execution, required witness.Level) (witness.Outcome, error)
}

// Pipeline is the Gate: every agent action, from every surface, enters through Submit and leaves
// as a sealed deed (S13).
type Pipeline struct {
	o      Options
	cfg    Config
	wcfg   WitnessConfig
	log    *slog.Logger
	now    func() time.Time
	broker *Broker
	m      *Metrics

	inflight sync.WaitGroup // gateway executions running detached from their requests
	mu       sync.Mutex
	waits    map[string]int // open long polls per agent

	hooks hooks
	jobs  map[string]JobHandler // job kinds other packages own (S22's approval_notify); guarded by mu
}

// hooks are test seams: they let a test observe the moments the invariants talk about.
type hooks struct {
	beforeExecute  func(connector.Call)    // invariant 5: no transaction open here
	afterAdmit     func(ids.ActionID)      // crash tests: "killed between Tx A and Tx B"
	beforeReadBack func(action string)     // S19 invariant 3: no transaction open during a read-back
	afterB1        func(ids.ActionID) bool // crash tests: true stops after B1, as if the instance died
}

// SetNudge sets Options.Nudge once the decoder exists (serve starts it after the Gate).
func (p *Pipeline) SetNudge(f func(connectorID string)) { p.o.Nudge = f }

// New returns a Pipeline.
func New(o Options) (*Pipeline, error) {
	if o.DB == nil || o.Ledger == nil || o.Policies == nil || o.Usage == nil || o.Resolver == nil {
		return nil, errors.New("gate: DB, Ledger, Policies, Usage and Resolver are required")
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
	return &Pipeline{o: o, cfg: cfg, wcfg: cfg.Witness.withDefaults(), log: o.Logger, now: o.Clock, broker: o.Broker, m: o.Metrics, waits: map[string]int{}}, nil
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
	// The Witness (S19): the level the policy requires, the tool's side effect, and Postgres's
	// default schema for the row read-back.
	required      witness.Level
	side          string
	defaultSchema string
	// link, when set, is the request's context: the tool call (and only the call, never the
	// recording) is cancelled with ErrRequestGone when it ends (ActionRequest.CancelOn).
	link context.Context
	// l2 is set by run when the execution waits for L2 (S21), so Submit keeps waiting for the
	// verdict within the request's wait.
	l2 atomic.Pointer[l2Plan]
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

// needPrepare: Tx A decided require_approval and has no prepared snapshot for that rule yet. It
// rolls back; Submit prepares and runs Tx A again.
type needPrepare struct {
	in ApprovalInput
	d  policy.Decision
}

func (e *needPrepare) Error() string { return "gate: the approval snapshot must be prepared first" }

// Submit takes one action from an agent key through the pipeline: normalize, decide, record, and
// (gateway, allowed) execute, witness and seal. It returns the action and, for a refusal, the
// error to answer with (the action is attached to it and sealed). See S13 §3.4 for the
// transaction boundaries.
func (p *Pipeline) Submit(ctx context.Context, pr principal, req ActionRequest) (*Action, error) {
	t0 := p.now()
	console := pr.Kind == httpx.KindConsole
	if pr.Kind != httpx.KindAgentKey && !console || pr.AgentID.IsZero() || pr.OrgID.IsZero() {
		return nil, httpx.ErrInsufficientScope(httpx.ScopeActionsExecute)
	}
	if req.UndoesActionID != "" && !console {
		return nil, httpx.ErrInsufficientScope(httpx.ScopeActionsUndo)
	}
	n, err := Normalize(req, p.cfg.MaxArgsBytes)
	if err != nil {
		return nil, err
	}
	if err := checkSchedule(req, n, p.now()); err != nil {
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
		if console {
			// A person's action runs only the connector's internal tools (S23); an agent never
			// reaches them (Resolve refuses an internal tool as unknown).
			err = connector.ErrToolUnknown
			if p.o.Internal != nil {
				ex, spec, ctype, err = p.o.Internal.Resolve(ctx, pr.OrgID, n.Tool)
			}
		} else {
			ex, spec, ctype, err = p.o.Resolver.Resolve(ctx, pr.OrgID, n.Tool)
		}
		switch {
		case errors.Is(err, connector.ErrToolUnknown):
			unknown = true
		case errors.Is(err, connector.ErrConnectorUnavailable):
			return nil, httpx.E(httpx.CodeConnectorUnavailable, httpx.Msg(ConnectorUnavailableMessage(n.Connector)), httpx.Cause(err))
		case err != nil:
			return nil, err
		default:
			n.ConnectorType = ctype
			e := enrichAll(ctx, ex, spec, n.Args)
			n.HTTP, n.GitHub, n.Slack, n.DefaultSchema, n.RaisedSideEffect = e.HTTP, e.GitHub, e.Slack, e.DefaultSchema, e.Side
			n.Stripe, n.Email, n.Mail = e.Stripe, e.Email, e.Mail
		}
	}
	// The active policies, before any transaction: a cold load reads Postgres on its own. The
	// anomaly facts too (S27 §3.8: an in-memory cache; a miss reads Postgres on its own).
	var (
		act   *policy.Active
		anom  policy.AnomalyFacts
		trace string
	)
	if !unknown {
		if act, err = p.o.Policies.Active(ctx, pr.OrgID.String()); err != nil {
			return nil, err
		}
		if p.o.Anomalies != nil && !console {
			anom, trace = p.o.Anomalies.Facts(ctx, pr.OrgID, pr.AgentID, p.now())
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
	// Tx A. A require_approval decision first needs its snapshot prepared with no transaction
	// open (the connector's estimate, the agent's recent actions): Tx A rolls back, Prepare runs,
	// and Tx A runs again with it (S22). The second decision normally matches; if it names another
	// rule, the snapshot is prepared once more.
	var (
		adm  *admission
		prep *PreparedApproval
	)
	for attempt := 0; ; attempt++ {
		adm, err = p.admit(ctx, pr, n, req, act, anomalyIn{anom, trace}, spec, ex, unknown, release, prep)
		var np *needPrepare
		if !errors.As(err, &np) {
			break
		}
		if attempt >= 2 {
			return nil, errors.New("gate: the approval's decision kept changing while it was prepared")
		}
		pa, perr := p.o.Approvals.Prepare(ctx, np.in, np.d)
		if perr != nil {
			return nil, perr
		}
		prep = &pa
	}
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
		if v.State == string(Witnessing) && adm.plan.l2.Load() != nil {
			// S21: the verdict waits for Postgres's change log; keep waiting within the request's wait.
			left := wait - p.now().Sub(t0)
			if w, err := p.waitView(ctx, pr.OrgID, ids.ActionID(mustAction(v.ID)), left, func(x *ledger.Action) bool {
				return x.State != string(Witnessing)
			}); err == nil && w != nil {
				w.Execution = keepExecutionResult(v.Execution, w.Execution)
				v = w
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
// anomalyIn is what the AnomalySource said before Tx A: the facts and the trace line.
type anomalyIn struct {
	facts policy.AnomalyFacts
	trace string
}

func (p *Pipeline) admit(ctx context.Context, pr principal, n *Normalized, req ActionRequest, act *policy.Active, anom anomalyIn,
	spec connector.ToolSpec, ex Executor, unknown bool, release *store.KeyedAction, prep *PreparedApproval) (*admission, error) {
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
		if subj.AgentKind == "system" {
			// Stop all skips the console system agent (S23): you stop agents, then undo what they
			// wrote. An admin can still stop it on its own.
			subj.OrgStopped = false
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
		in.Anomaly = anom.facts
		if n.Mode == ModeGateway {
			withSideEffect(&in, spec, n.RaisedSideEffect)
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
		// 3a. S40: an allowed action runs now, so a schedule whose window opens later can't be
		// allowed: it is denied and sealed (schedule_needs_approval).
		if req.Schedule != nil && d.Outcome == policy.Allow && req.Schedule.NotBefore.After(now.Add(ScheduleNowWindow)) {
			d = scheduleNeedsApprovalDecision()
		}
		// 3b. A person must countersign: what they will see is prepared outside this transaction.
		var ain ApprovalInput
		if d.Outcome == policy.RequireApproval {
			ain = approvalInput(pr, n, subj, in, ir, loaded, spec, ex)
			ain.Schedule = approvalSchedule(req.Schedule, subj.Timezone)
			if prep == nil || prep.RuleID != d.RuleID {
				return &needPrepare{in: ain, d: d}
			}
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
			IntentSigned: ir.Signed, IdempotencyKey: nonEmpty(req.IdempotencyKey), UndoesActionID: nonEmpty(req.UndoesActionID), At: now,
			Schedule: scheduleJSON(req.Schedule)}
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
		if isUndoTaken(err) {
			return ErrUndoTaken
		}
		if err != nil {
			return err
		}
		ev := ledger.EventData{Actor: actor, RequestID: req.RequestID, At: now}
		patch, err := decisionPatch(in, d, sd, loaded, shadow, reserved, anom.trace)
		if err != nil {
			return err
		}
		required := requiredLevel(string(d.Verify), in.Tool.SideEffect)
		patch.WitnessRequired = strp(string(required))
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
			ain.Subject = sub
			apr, _, err := p.o.Approvals.Request(ctx, tx, ain, d, *prep)
			var unsat *UnsatisfiableError
			if errors.As(err, &unsat) {
				// Nobody can countersign: the approval is recorded denied by the system, and the
				// action is denied and sealed in this transaction (S22 §3.6).
				apr, err = unsat.Approval, nil
				a := apr.String()
				evPend := evEval
				evPend.Reason, evPend.ApprovalID = strp("approval_required"), &a
				evDeny := evEval
				evDeny.Reason, evDeny.ApprovalID = strp(string(httpx.CodeApprovalUnsatisfiable)), &a
				// The action doesn't link the approval: a deed's approval section names a person
				// for a denial, and here nobody could decide. The approval row keeps the record.
				steps = append(steps, ledger.Step{To: PendingApproval, Event: evPend}, ledger.Step{To: Denied, Event: evDeny})
				adm.refusal = httpx.E(httpx.CodeApprovalUnsatisfiable, httpx.Msg(unsat.Message))
				break
			}
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
					required: required, side: in.Tool.SideEffect, defaultSchema: n.DefaultSchema,
					call: connector.Call{ActionID: id, OrgID: org, AgentID: agent, Tool: spec, Qualified: n.Tool, Args: n.Args,
						IdempotencyKey: id.String(), Deadline: now.Add(p.cfg.ExecuteTimeout), IntentHash: ir.IntentHash}}
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
		switch {
		case d.Outcome == policy.Deny:
			if err := p.sealView(ctx, tx, id, view); err != nil {
				return err
			}
			adm.refusal = refusalFor(d, view)
		case adm.refusal != nil: // approval_unsatisfiable: denied in this transaction
			if reserved {
				if err := p.o.Usage.ReleaseTx(ctx, tx, org.String(), d.Reservations); err != nil {
					return err
				}
			}
			if err := p.sealView(ctx, tx, id, view); err != nil {
				return err
			}
			adm.refusal.Action = view
		}
		adm.view = view
		return nil
	})
	if err != nil {
		return nil, err
	}
	return adm, nil
}

// isUndoTaken reports a unique violation of actions_one_live_undo (S23).
func isUndoTaken(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "actions_one_live_undo"
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

// approvalInput is what Approvals.Prepare and Request see (S22): the request as normalized, the
// policy that decided, and (gateway) the resolved tool for the connector's estimate.
func approvalInput(pr principal, n *Normalized, subj store.GateSubject, in policy.Input, ir identity.IntentResult,
	loaded *policy.Loaded, spec connector.ToolSpec, ex Executor) ApprovalInput {
	a := ApprovalInput{
		Subject:   Subject{Org: pr.OrgID, Agent: pr.AgentID, Mode: n.Mode, Tool: n.Tool, ConnectorType: n.ConnectorType},
		AgentName: subj.AgentName, Connector: n.Connector, SideEffect: in.Tool.SideEffect, RequestDigest: n.Digest,
		Args: n.Args, OnBehalfOf: ir.OBO.OnBehalfOf, Livemode: pr.Livemode, Request: in.Request,
	}
	if loaded != nil && !loaded.Builtin {
		a.PolicyID, a.PolicyName, a.PolicyVersionID, a.PolicyVersion = loaded.PolicyID, loaded.Name, loaded.VersionID, loaded.Version
	}
	if n.Mode == ModeGateway && ex != nil {
		a.Exec = ex
		a.Call = connector.Call{OrgID: pr.OrgID, AgentID: pr.AgentID, Tool: spec, Qualified: n.Tool, Args: n.Args, IntentHash: ir.IntentHash}
	}
	return a
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
		Tool: policy.ToolInput{ToolFacts: policy.ToolFacts{Name: n.Tool, Connector: n.Connector, ConnectorType: ctype, SideEffect: side,
			Type: ctype, Op: n.ToolName}, DefaultSchema: n.DefaultSchema},
		Args: n.Args,
		Request: policy.RequestFacts{Mode: string(n.Mode), Bytes: n.ArgsBytes, IntentSigned: ir.Signed, Digest: n.Digest, HTTP: n.HTTP,
			GitHub: n.GitHub, Slack: n.Slack, Stripe: n.Stripe, Email: n.Email, Mail: n.Mail},
		Time: now.UTC(),
	}
	return in, loc, nil
}

// enrich asks a connector that enriches for the call's facts: REST's request.http (S16), and
// Postgres's default schema for the SQL classifier and a raised side effect (S17: destructive for
// a DELETE). Invalid arguments enrich nothing: the call then fails as validation_failed before
// anything is sent.
func enrich(ctx context.Context, ex Executor, spec connector.ToolSpec, args json.RawMessage) (h policy.HTTPFacts, defaultSchema, side string) {
	e := enrichAll(ctx, ex, spec, args)
	return e.HTTP, e.DefaultSchema, e.Side
}

// enriched is everything a connector's enrichment adds before the decision.
type enriched struct {
	HTTP          policy.HTTPFacts
	GitHub        policy.GitHubFacts // S29
	Slack         policy.SlackFacts  // S29
	Stripe        policy.StripeFacts // S30
	Email         policy.EmailFacts  // S30
	Mail          policy.MailFacts   // S40
	DefaultSchema string
	Side          string
}

// enrichAll asks the connector for its facts. GitHub and Slack answer {"github": {…}} and
// {"slack": {…}} as plain JSON values; a value that doesn't fit leaves the facts zero (enriched
// false), so the starter rules treat the call as unknown, never as safe.
func enrichAll(ctx context.Context, ex Executor, spec connector.ToolSpec, args json.RawMessage) enriched {
	var e enriched
	en, ok := ex.(connector.ArgsEnricher)
	if !ok {
		return e
	}
	m, err := en.EnrichArgs(ctx, spec, args)
	if err != nil || m == nil {
		return e
	}
	hm, _ := m["http"].(map[string]string)
	e.HTTP = policy.HTTPFacts{Method: hm["method"], Path: hm["path"], Host: hm["host"]}
	e.DefaultSchema, _ = m["default_schema"].(string)
	e.Side, _ = m["side_effect"].(string)
	if g, ok := m["github"]; ok {
		if b, err := json.Marshal(g); err == nil && json.Unmarshal(b, &e.GitHub) != nil {
			e.GitHub = policy.GitHubFacts{}
		}
	}
	if s, ok := m["slack"]; ok {
		if b, err := json.Marshal(s); err == nil && json.Unmarshal(b, &e.Slack) != nil {
			e.Slack = policy.SlackFacts{}
		}
	}
	if s, ok := m["stripe"]; ok {
		if b, err := json.Marshal(s); err == nil && json.Unmarshal(b, &e.Stripe) != nil {
			e.Stripe = policy.StripeFacts{}
		}
	}
	if s, ok := m["email"]; ok {
		if b, err := json.Marshal(s); err == nil && json.Unmarshal(b, &e.Email) != nil {
			e.Email = policy.EmailFacts{}
		}
	}
	if s, ok := m["mail"]; ok {
		if b, err := json.Marshal(s); err == nil && json.Unmarshal(b, &e.Mail) != nil {
			e.Mail = policy.MailFacts{}
		}
	}
	return e
}

// withSideEffect sets the tool's declared side effect (gateway), raised (never lowered) by the
// connector's per-call enrichment; MCP tools stay unknown.
func withSideEffect(in *policy.Input, spec connector.ToolSpec, raised string) {
	if spec.SideEffect != "" {
		in.Tool.SideEffect = string(spec.SideEffect)
	}
	if r := connector.SideEffect(raised); r.Rank() > connector.SideEffect(in.Tool.SideEffect).Rank() {
		in.Tool.SideEffect = string(r)
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

// checkSchedule applies S40's schedule rules before anything is recorded (400 validation_failed).
func checkSchedule(req ActionRequest, n *Normalized, now time.Time) error {
	s := req.Schedule
	if s == nil {
		return nil
	}
	switch {
	case n.Mode != ModeGateway:
		return httpx.ErrValidation("schedule", MsgScheduleProxy)
	case s.NotAfter.Sub(s.NotBefore) < ScheduleMinSpan:
		return httpx.ErrValidation("schedule", MsgScheduleOrder)
	case s.NotAfter.After(now.Add(ScheduleMaxAhead)):
		return httpx.ErrValidation("schedule", MsgScheduleTooFar)
	case !s.NotAfter.After(now):
		return httpx.ErrValidation("schedule", MsgSchedulePast)
	}
	return nil
}

// scheduleJSON is actions.schedule (≤ 256 bytes), nil without a schedule.
func scheduleJSON(s *Schedule) json.RawMessage {
	if s == nil {
		return nil
	}
	b, _ := json.Marshal(map[string]string{"not_before": s.NotBefore.UTC().Format(time.RFC3339), "not_after": s.NotAfter.UTC().Format(time.RFC3339)})
	return b
}

// ParseSchedule reads actions.schedule back; ok false when there is none.
func ParseSchedule(raw json.RawMessage) (Schedule, bool) {
	if len(raw) == 0 {
		return Schedule{}, false
	}
	var v struct {
		NotBefore time.Time `json:"not_before"`
		NotAfter  time.Time `json:"not_after"`
	}
	if json.Unmarshal(raw, &v) != nil || v.NotBefore.IsZero() || v.NotAfter.IsZero() {
		return Schedule{}, false
	}
	return Schedule{NotBefore: v.NotBefore, NotAfter: v.NotAfter}, true
}

// approvalSchedule is the schedule as the approval context shows it, in the org's time zone.
func approvalSchedule(s *Schedule, tz string) *ApprovalSchedule {
	if s == nil {
		return nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	nb, na := s.NotBefore.In(loc), s.NotAfter.In(loc)
	zone := nb.Format("MST")
	return &ApprovalSchedule{NotBefore: s.NotBefore, NotAfter: s.NotAfter,
		Summary: fmt.Sprintf("Runs %s between %s and %s %s.", nb.Format("Mon 2 Jan"), nb.Format("15:04"), na.Format("15:04"), zone)}
}

// scheduleNeedsApprovalDecision is the builtin deny of an allowed action with a later window.
func scheduleNeedsApprovalDecision() policy.Decision {
	return policy.Decision{Outcome: policy.Deny, Code: policy.Code(httpx.CodeScheduleNeedsApproval), Source: policy.SourceBuiltin,
		RuleID: RuleScheduleNeedsApproval, Reason: MsgScheduleNeedsTap, Message: MsgScheduleNeedsTap}
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
	// Anomaly is the decision trace's line about the anomaly facts the policy saw (S27 §3.8):
	// "anomaly facts: volume_spike", or detector_stale with the last run's age. Absent when inactive.
	Anomaly string `json:"anomaly,omitempty"`
}

// maxDetail keeps decision_detail under its 16 KiB column limit (facts are dropped first; they
// can be recomputed from policy_input).
const maxDetail = 15 << 10

func detailJSON(d policy.Decision, l *policy.Loaded, anomaly string) (json.RawMessage, error) {
	dd := decisionDetail{RuleLine: d.RuleLine, Approval: d.Approval, Verify: d.Verify, Notify: d.Notify, Budget: d.Budget,
		CostBudgets: d.CostBudgets, Errors: d.Errors, SQLGuard: d.SQLGuard, ElapsedUS: d.Elapsed.Microseconds(), Anomaly: clip(anomaly, 200)}
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
func decisionPatch(in policy.Input, d policy.Decision, sd *policy.Decision, l, sl *policy.Loaded, reserved bool, anomaly string) (ledger.Patch, error) {
	inJSON, err := json.Marshal(in)
	if err != nil {
		return ledger.Patch{}, err
	}
	digest, err := in.Digest()
	if err != nil {
		return ledger.Patch{}, err
	}
	detail, err := detailJSON(d, l, anomaly)
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
	wc := p.newWitnessCase(pl.subject, e, pl.exec, pl.call, pl.required, pl.side, pl.defaultSchema)
	v, err := p.complete(ctx, pl.subject, e, wc, pl.costTargets, pl.reservations, pl.actor, pl.requestID)
	if wc.l2 != nil && wc.l2.wait {
		pl.l2.Store(wc.l2)
	}
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

// keepExecutionResult keeps the tool's result from the first view when a later read leaves it out.
func keepExecutionResult(prev, next *ledger.ActionExecution) *ledger.ActionExecution {
	if next != nil && prev != nil && next.Result == nil {
		next.Result = prev.Result
	}
	return next
}
