package gate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/internal/witness/l2"
)

// Verdicts, levels and methods (contract §6), for presenting a verdict. A verdict itself only ever
// comes from witness.Decide (S19); the Gate records what it returns.
const (
	LevelL0             = string(witness.L0)
	VerdictVerified     = string(witness.Verified)
	VerdictMismatch     = string(witness.Mismatch)
	VerdictUnverifiable = string(witness.Unverifiable)

	// MethodReported: the tool's own response, through a connector the Gate called (gateway).
	MethodReported = witness.MethodReported
	// MethodEdgeReported: the edge's report of a tool it ran (proxy). Not independent of the edge.
	MethodEdgeReported = witness.MethodEdgeReported
)

// Causes of an unknown outcome (execution.error_code and the outcome_unknown reason's evidence).
const (
	ReasonTimeout        = "timeout"         // the tool didn't answer within gate.execute_timeout
	ReasonOutcomeUnknown = "outcome_unknown" // a lost response, a panic, an untyped error, a crash
	ReasonEdgeLost       = "edge_lost"       // proxy: the edge never reported within gate.edge_result_timeout
	ReasonWitnessGaveUp  = "witness_gave_up" // a witness job failed max_attempts times
	// ReasonClientCancelled: the MCP client went away (or its agent was stopped) while the upstream
	// call ran, so Deedglass cancelled it and can't know whether it applied (S15).
	ReasonClientCancelled = "client_cancelled"
)

// Witness job kinds (migration 0007). S13's kind "witness" is read as l1_readback.
const (
	JobL1ReadBack = "l1_readback"
	jobWitness    = "witness"
)

// WitnessConfig is the witness.* section of deedglass.yaml (S19 §3.10).
type WitnessConfig struct {
	InlineBudget    time.Duration // the longest a read-back may hold the request path (100 ms to 5 s; 1.5 s)
	ReadbackTimeout time.Duration // the worker's limit per attempt (10 s)
	MaxDetailBytes  int           // witness_detail's JCS cap (64 KiB)
}

func (c WitnessConfig) withDefaults() WitnessConfig {
	if c.InlineBudget <= 0 {
		c.InlineBudget = 1500 * time.Millisecond
	}
	c.InlineBudget = min(max(c.InlineBudget, 100*time.Millisecond), 5*time.Second)
	if c.ReadbackTimeout <= 0 {
		c.ReadbackTimeout = 10 * time.Second
	}
	if c.MaxDetailBytes <= 0 {
		c.MaxDetailBytes = witness.DefaultMaxDetailBytes
	}
	return c
}

// witnessCase is what the Gate knows about one execution when it asks the Witness.
type witnessCase struct {
	sub      witness.Subject
	ex       Executor       // the connector that ran it (gateway); nil in proxy mode
	call     connector.Call // the call as it ran (the Postgres row read-back reads its statement)
	res      connector.Result
	readBack bool             // L1 is planned: B1, a read-back with no transaction open, then B2
	l2       *l2Plan          // S21: the connector is witnessed at L2 (shared by copies: Tx B sets wait)
	extra    []witness.Reason // reasons decided before any check ran (S21: decoder_halted at plan time)
	expect   *l2.Footprint    // S21: actions.witness_expect (S23's undo)
}

// requiredLevel is the level the decision's verify: asks for: it applies to anything that may
// write (write, destructive, unknown); a read always requires L0 (S19 §3.7).
func requiredLevel(verify string, side string) witness.Level {
	l, ok := witness.ParseLevel(verify)
	if !ok || connector.SideEffect(side) == connector.SideEffectRead {
		return witness.L0
	}
	return l
}

// readBackSpec converts the connector's stored read-back to the Witness's (the same JSON shape).
func readBackSpec(r *connector.ReadBackSpec) *witness.ReadBackSpec {
	if r == nil {
		return nil
	}
	return &witness.ReadBackSpec{Tool: r.Tool, Args: r.Args, Expect: r.Expect, Consistency: r.Consistency, Attempts: r.Attempts,
		IntervalMS: r.IntervalMS, OnNotFound: r.OnNotFound}
}

// newWitnessCase fills the Witness's plain-data subject for one execution.
func (p *Pipeline) newWitnessCase(s Subject, e Execution, ex Executor, call connector.Call, required witness.Level, side, defaultSchema string) witnessCase {
	out := e.Result.Output
	n := len(out)
	if n > p.cfg.MaxResultBytes {
		out = nil
	}
	conn, _, _ := connector.Split(s.Tool)
	ws := witness.Subject{ActionID: s.ID.String(), Org: s.Org.String(), Agent: s.Agent.String(), Tool: s.Tool,
		ConnectorType: s.ConnectorType, Mode: string(s.Mode), SideEffect: side, Args: call.Args, Required: required,
		Result: witness.ResultView{Status: string(e.Status), Cause: e.Reason, Output: out, OutputBytes: n, Meta: e.Result.Meta,
			Evidence: e.Result.Evidence}}
	if s.Mode == ModeGateway {
		t := call.Tool
		ws.Spec = witness.ToolWitnessSpec{Connector: conn, Tool: t.Name, OutputSchema: t.OutputSchema, ReadBack: readBackSpec(t.ReadBack),
			Postcondition: t.Postcondition, PostgresRow: s.ConnectorType == "postgres" && t.Name == "execute", DefaultSchema: defaultSchema}
		if b, ok := ex.(interface{ Instance() *connector.Instance }); ok && b.Instance() != nil {
			ws.Spec.ConfigHash = b.Instance().ConfigHash
		}
		if nr, ok := ex.(connector.NativeReadBacking); ok && nr.CanNativeReadBack(t) {
			ws.Spec.Native, ws.Spec.NativeL1 = true, t.L1
		}
	}
	if e.Status == ExecUnknown && ws.Result.Cause == "" {
		ws.Result.Cause = ReasonOutcomeUnknown
	}
	rb, _ := ex.(connector.ReadBacking)
	canRead := rb != nil && rb.CanReadBack()
	if ws.Spec.Native {
		canRead = true // the NativeReadBacking check above
	}
	return witnessCase{sub: ws, ex: ex, call: call, res: e.Result, readBack: witness.PlanFor(ws).L1 && canRead,
		l2: planL2(s, e, ex, call, side)}
}

// decide runs the Witness with no read-back (L0, and the readback_skipped note where one applies).
func (p *Pipeline) decide(wc witnessCase, extra ...witness.Reason) (witness.Outcome, error) {
	sub := wc.sub
	if witness.PlanFor(sub).L1 {
		// No read-back runs here: drop it from the plan, so L0 (and extra) decide.
		sub.Spec.ReadBack, sub.Spec.PostgresRow = nil, false
	}
	out, _, err := witness.Run(context.Background(), witness.Input{Subject: sub, Extra: append(wc.reasons(), extra...),
		Final: true, Now: p.now(), MaxDetailBytes: p.wcfg.MaxDetailBytes})
	return out, err
}

// observe runs the planned read-back within ctx's budget (no transaction is open: S19 invariant 3).
// deferred means the budget ran out before an answer; the worker finishes it.
func (p *Pipeline) observe(ctx context.Context, wc witnessCase, final bool) (witness.Outcome, bool, error) {
	if p.hooks.beforeReadBack != nil {
		p.hooks.beforeReadBack(wc.sub.ActionID)
	}
	obs := p.o.Observer
	if obs == nil {
		obs = &observer{p: p, wc: wc}
	}
	return witness.Run(ctx, witness.Input{Subject: wc.sub, Observer: obs, Final: final, Extra: wc.reasons(), Now: p.now(),
		MaxDetailBytes: p.wcfg.MaxDetailBytes})
}

// observer is the Witness's Observer over the connector's ReadBacker: a REST read tool through the
// registry (S16's whole call path), or the Postgres row read-back of the statement that ran.
type observer struct {
	p  *Pipeline
	wc witnessCase
}

func (o *observer) ReadBack(ctx context.Context, s witness.Subject, req witness.ReadRequest, attempt int) (witness.Observation, error) {
	call, ex := o.wc.call, o.wc.ex
	if req.Native {
		return o.native(ctx, call, ex)
	}
	if req.Tool != "" {
		rex, spec, _, err := o.p.o.Resolver.Resolve(ctx, o.wc.call.OrgID, req.Tool)
		if err != nil {
			return witness.Observation{}, &witness.ReadError{Code: connector.CodeConnectorUnavailable, Err: err}
		}
		ex = rex
		call = connector.Call{ActionID: o.wc.call.ActionID, OrgID: o.wc.call.OrgID, AgentID: o.wc.call.AgentID, Tool: spec,
			Qualified: req.Tool, Args: req.Args}
	}
	if dl, ok := ctx.Deadline(); ok {
		call.Deadline = dl
	}
	rb, ok := ex.(connector.ReadBacking)
	if !ok || !rb.CanReadBack() {
		return witness.Observation{}, &witness.SkipError{Why: "the connector can't read back"}
	}
	at := o.p.now()
	co, err := rb.ReadBack(ctx, call, o.wc.res)
	w := witness.Observation{Status: co.Status, Meta: co.Meta, At: at, Source: req.Tool, LatencyMS: co.LatencyMS}
	if req.Tool != "" {
		w.RequestDigest, _ = deed.DigestJSON(req.Args)
	}
	if len(co.Body) > 0 {
		w.ResponseDigest, _ = deed.DigestJSON(co.Body)
		w.Body = decodeNumber(co.Body)
	}
	if err == nil {
		return w, nil
	}
	var skip *connector.ErrReadBackSkipped
	switch ce := connector.AsError(err); {
	case errors.As(err, &skip):
		return w, &witness.SkipError{Why: skip.Why}
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		return w, context.DeadlineExceeded
	case ce != nil && ce.Kind == connector.KindNotFound:
		return w, witness.ErrNotFound
	case ce != nil && ce.Kind == connector.KindTimeout && ctx.Err() != nil:
		return w, context.DeadlineExceeded
	case ce != nil:
		return w, &witness.ReadError{Code: ce.Code, Err: err}
	}
	return w, &witness.ReadError{Code: connector.CodeUpstreamError, Err: err}
}

// native runs a connector's native read-back of the write itself (S29).
func (o *observer) native(ctx context.Context, call connector.Call, ex Executor) (witness.Observation, error) {
	if dl, ok := ctx.Deadline(); ok {
		call.Deadline = dl
	}
	nr, ok := ex.(connector.NativeReadBacking)
	if !ok || !nr.CanNativeReadBack(call.Tool) {
		return witness.Observation{}, &witness.SkipError{Why: "the connector can't read this tool back"}
	}
	at := o.p.now()
	co, fs, err := nr.NativeReadBack(ctx, call, o.wc.res)
	w := witness.Observation{Status: co.Status, Meta: co.Meta, At: at, Source: call.Qualified, Target: co.Target, LatencyMS: co.LatencyMS}
	for _, f := range fs {
		w.Findings = append(w.Findings, witness.Finding{Field: f.Field, Expected: f.Expected, Observed: f.Observed, Match: f.Match})
	}
	if err == nil {
		return w, nil
	}
	var skip *connector.ErrReadBackSkipped
	switch ce := connector.AsError(err); {
	case errors.As(err, &skip):
		return w, &witness.SkipError{Why: skip.Why}
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		return w, context.DeadlineExceeded
	case ce != nil && ce.Kind == connector.KindNotFound:
		return w, witness.ErrNotFound
	case ce != nil:
		return w, &witness.ReadError{Code: ce.Code, Err: err}
	}
	return w, &witness.ReadError{Code: connector.CodeUpstreamError, Err: err}
}

func decodeNumber(b []byte) any {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	return v
}

// verdictStep is the edge witnessing → verified | mismatch | unverifiable with the witness columns
// and the action.witnessed event's data.
func verdictStep(out witness.Outcome, ev ledger.EventData) (State, ledger.EventData, *ledger.Patch) {
	d := out.Decision
	to := Unverifiable
	switch d.Verdict {
	case witness.Verified:
		to = Verified
	case witness.Mismatch:
		to = Mismatch
	}
	level, verdict, method, req, dd := string(d.Level), string(d.Verdict), d.Method, string(d.Required), out.Digest
	ev.Level, ev.Verdict, ev.DetailDigest, ev.RequiredLevel = &level, &verdict, &dd, &req
	if f := out.Flags(); len(f) > 0 {
		joined := strings.Join(f, ",")
		ev.Flags = &joined
	}
	// The reason is the first violation or gap a check found; the claim-level codes
	// (level_below_required, no_checks) are already in the flags. When there is none, the first
	// claim-level code stands in.
	for _, pass := range []bool{false, true} {
		for _, r := range d.Reasons {
			claim := r.Code == witness.CodeLevelBelowRequired || r.Code == witness.CodeNoChecks
			if (r.Class == witness.Violation || r.Class == witness.Gap) && claim == pass {
				c := string(r.Code)
				ev.Reason = &c
				break
			}
		}
		if ev.Reason != nil {
			break
		}
	}
	return to, ev, &ledger.Patch{WitnessLevel: &level, WitnessVerdict: &verdict, WitnessMethod: &method,
		WitnessDetail: json.RawMessage(out.JSON), WitnessDetailDigest: &dd}
}

// recordVerdict moves witnessing → verified | mismatch | unverifiable with the witness columns.
func (p *Pipeline) recordVerdict(ctx context.Context, tx pgx.Tx, id ids.ActionID, out witness.Outcome, actor, requestID string) error {
	to, ev, patch := verdictStep(out, ledger.EventData{Actor: actor, RequestID: requestID, At: p.now()})
	return p.o.Ledger.Transition(ctx, tx, id.String(), Witnessing, to, ev, *patch)
}

// jobPayload is what a read-back needs after a restart: the connector's evidence never goes into
// the actions row, so the job carries it (≤ 16 KiB; ids, digests and small numbers).
type jobPayload struct {
	Required      string            `json:"required"`
	SideEffect    string            `json:"side_effect"`
	DefaultSchema string            `json:"default_schema,omitempty"`
	Status        string            `json:"status"`
	Cause         string            `json:"cause,omitempty"`
	OutputBytes   int               `json:"output_bytes"`
	Meta          map[string]any    `json:"meta,omitempty"`
	Evidence      map[string]string `json:"evidence,omitempty"`
	Extra         []witness.Reason  `json:"extra,omitempty"` // S21: decoder_halted at plan time
}

func payloadOf(wc witnessCase) json.RawMessage {
	pl := jobPayload{Required: string(wc.sub.Required), SideEffect: wc.sub.SideEffect, DefaultSchema: wc.sub.Spec.DefaultSchema,
		Status: wc.sub.Result.Status, Cause: wc.sub.Result.Cause, OutputBytes: wc.sub.Result.OutputBytes,
		Meta: wc.sub.Result.Meta, Evidence: wc.sub.Result.Evidence, Extra: wc.reasons()}
	b, err := json.Marshal(pl)
	if err != nil || len(b) > 12<<10 {
		pl.Meta = nil
		b, _ = json.Marshal(pl)
	}
	return b
}

// jobCase rebuilds a witness case from a stored action and its job, for the worker: the tool is
// resolved again (its config as it is now), the arguments and the result come from the row, and
// the evidence from the job.
func (p *Pipeline) jobCase(ctx context.Context, ga *store.GateAction, payload json.RawMessage) (witnessCase, error) {
	var pl jobPayload
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &pl); err != nil {
			return witnessCase{}, err
		}
	}
	sub := subjectOf(ga)
	if pl.Required == "" {
		pl.Required = derefString(ga.WitnessRequired)
	}
	if pl.Status == "" {
		pl.Status = derefString(ga.ExecutionStatus)
	}
	ev := pl.Evidence
	if len(ev) == 0 {
		ev = ga.ExecutionEvidence // ADR-044: the action keeps it too
	}
	e := Execution{Status: ExecStatus(pl.Status), Reason: pl.Cause, Result: connector.Result{Output: ga.Result, Meta: pl.Meta, Evidence: ev}}
	call := connector.Call{ActionID: ga.ID, OrgID: ga.OrgID, AgentID: ga.AgentID, Qualified: ga.Tool, Args: ga.Request,
		IntentHash: derefString(ga.IntentHash)}
	var ex Executor
	if sub.Mode == ModeGateway {
		rex, spec, ctype, err := p.resolveStored(ctx, ga.OrgID, ga.Tool)
		if err != nil {
			return witnessCase{}, err
		}
		ex, call.Tool, sub.ConnectorType = rex, spec, ctype
	}
	req, _ := witness.ParseLevel(pl.Required)
	wc := p.newWitnessCase(sub, e, ex, call, req, pl.SideEffect, pl.DefaultSchema)
	wc.sub.Result.OutputBytes = max(wc.sub.Result.OutputBytes, pl.OutputBytes)
	wc.extra = pl.Extra
	if len(ga.WitnessExpect) > 0 {
		var fp l2.Footprint
		if json.Unmarshal(ga.WitnessExpect, &fp) == nil {
			wc.expect = &fp
		}
	}
	return wc, nil
}

// storedCase is a witness case from the row alone, for an action that reached executed without a
// witness (an older server, or a crash between the two): L0 only.
func (p *Pipeline) storedCase(ga *store.GateAction) witnessCase {
	sub := subjectOf(ga)
	e := Execution{Status: ExecUnknown, Reason: ReasonOutcomeUnknown, Result: connector.Result{Output: ga.Result}}
	if ga.ExecutionStatus != nil {
		e.Status = ExecStatus(*ga.ExecutionStatus)
	}
	if ga.ErrorCode != nil && e.Status == ExecUnknown {
		e.Reason = *ga.ErrorCode
	}
	req, _ := witness.ParseLevel(derefString(ga.WitnessRequired))
	return p.newWitnessCase(sub, e, nil, connector.Call{Args: ga.Request}, req, "", "")
}
