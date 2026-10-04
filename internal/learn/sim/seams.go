package sim

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/witness"
)

// The three Gate seams the simulator swaps (S27 §3.3), dev only: the tools (scripted executors
// that report the imitated connector type, latency, cost and outcome), the Witness (scripted
// verdicts with the method "simulated", through witness.Decide like every verdict), and the clock
// (virtual and monotonic). Everything else (policies, budgets, approvals, Stop, idempotency, the
// shadow pair and sealing) is the real Gate.

// MethodSimulated is the witness method of every simulated verdict: a simulated deed can never pass
// for real evidence.
const MethodSimulated = "simulated"

// Clock is the virtual clock: monotonic, set by the runner, advanced by scripted tool latencies.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// Now is the virtual time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Set moves the clock to t; it never moves backwards.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.After(c.t) {
		c.t = t.UTC()
	}
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d > 0 {
		c.t = c.t.Add(d)
	}
}

// script is the planned action the Gate is running now (the runner is sequential).
type script struct {
	mu  sync.Mutex
	cur *PlannedAction
}

func (s *script) set(a *PlannedAction) {
	s.mu.Lock()
	s.cur = a
	s.mu.Unlock()
}

func (s *script) get() *PlannedAction {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Resolver serves the scenario's tools with scripted executors (gate.Resolver).
type Resolver struct {
	clock  *Clock
	script *script
	tools  map[string]bool
}

var _ gate.Resolver = (*Resolver)(nil)

// Resolve resolves a scenario tool; any other name is tool_unknown.
func (r *Resolver) Resolve(_ context.Context, _ ids.OrgID, qualified string) (gate.Executor, connector.ToolSpec, string, error) {
	if !r.tools[qualified] {
		return nil, connector.ToolSpec{}, "", connector.ErrToolUnknown
	}
	ctype, name, _ := strings.Cut(qualified, ".")
	spec := connector.ToolSpec{Name: name, SideEffect: connector.SideEffect(toolSide(qualified)), Description: "A simulated " + qualified + " tool."}
	return &executor{r: r, ctype: ctype}, spec, ctype, nil
}

// toolSide is the side effect a tool declares (a Postgres statement may raise it per call).
func toolSide(tool string) string {
	if tool == "postgres.query" {
		return SideRead
	}
	return SideWrite
}

// executor is one scripted tool.
type executor struct {
	r     *Resolver
	ctype string
}

// EnrichArgs gives Postgres tools the default schema the SQL classifier reads, and raises a
// DELETE to destructive, like the real connector (S17).
func (x *executor) EnrichArgs(_ context.Context, _ connector.ToolSpec, args json.RawMessage) (map[string]any, error) {
	if x.ctype != "postgres" {
		return nil, nil
	}
	m := map[string]any{"default_schema": "public"}
	var a struct {
		SQL string `json:"sql"`
	}
	if json.Unmarshal(args, &a) == nil && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(a.SQL)), "DELETE") {
		m["side_effect"] = SideDestructive
	}
	return m, nil
}

// Execute returns the planned outcome and advances the virtual clock by its latency.
func (x *executor) Execute(_ context.Context, _ connector.Call) (connector.Result, error) {
	a := x.r.script.get()
	if a == nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindUpstream, Code: "upstream_error", AgentMessage: "No simulated action is running."}
	}
	o := a.Outcome
	x.r.clock.Advance(time.Duration(o.LatencyMS) * time.Millisecond)
	switch o.Exec {
	case ExecFailed:
		code := o.ErrorCode
		if code == "" {
			code = "upstream_error"
		}
		return connector.Result{}, &connector.Error{Kind: connector.KindToolError, Code: code,
			AgentMessage: "The simulated tool refused the call (" + code + "). Nothing was applied."}
	case ExecUnknown:
		return connector.Result{}, &connector.Error{Kind: connector.KindTimeout, Code: "timeout",
			AgentMessage: "The simulated tool didn't answer in time. It may have run."}
	}
	res := connector.Result{Output: json.RawMessage(`{"ok":true,"simulated":true}`), Meta: map[string]any{"simulated": true}}
	if o.CostMinor > 0 && o.Currency != "" {
		res.Cost = &connector.Money{Amount: o.CostMinor, Currency: o.Currency}
	}
	return res, nil
}

// Witness scripts the verdicts (gate.SimulatedWitness).
type Witness struct {
	script   *script
	maxBytes int
}

var _ gate.SimulatedWitness = (*Witness)(nil)

// Verdict builds the scripted reasons and decides them with witness.Decide, every level's method
// "simulated".
func (w *Witness) Verdict(_ context.Context, _ gate.Subject, e gate.Execution, required witness.Level) (witness.Outcome, error) {
	methods := map[witness.Level]string{witness.L0: MethodSimulated, witness.L1: MethodSimulated, witness.L2: MethodSimulated}
	by := map[witness.Level][]witness.Reason{}
	a := w.script.get()
	switch {
	case a == nil || e.Status == gate.ExecUnknown:
		by[witness.L0] = []witness.Reason{witness.R(witness.L0, witness.CodeOutcomeUnknown, "status", map[string]any{"cause": "timeout"})}
	default:
		level, _ := witness.ParseLevel(a.Outcome.Level)
		if level == "" {
			level = witness.L0
		}
		by[witness.L0] = []witness.Reason{witness.R(witness.L0, witness.CodeToolReportedSuccess, "status", nil)}
		switch a.Outcome.Evidence {
		case EvidenceContradicts:
			table, _ := a.Outcome.Detail["table"].(string)
			if table == "" {
				table = "public.orders"
			}
			switch level {
			case witness.L2:
				by[witness.L2] = []witness.Reason{witness.R(witness.L2, witness.CodeRowCountExceeded, "footprint",
					map[string]any{"observed": a.Outcome.Detail["observed_rows"], "table": table, "expected": a.Outcome.Detail["expected_rows"]})}
			default:
				by[witness.L1] = []witness.Reason{witness.R(witness.L1, witness.CodeValueMismatch, "row",
					map[string]any{"column": "status", "observed": "simulated", "key": "id", "table": table, "expected": "expected"})}
			}
		case EvidenceGap:
			switch level {
			case witness.L2:
				by[witness.L2] = []witness.Reason{witness.R(witness.L2, witness.CodeNoEvidence, "footprint", nil)}
			case witness.L1:
				by[witness.L1] = []witness.Reason{witness.R(witness.L1, witness.CodeReadbackTimeout, "readback",
					map[string]any{"readback": "simulated", "budget": "1.5 s"})}
			default:
				by[witness.L0] = []witness.Reason{witness.R(witness.L0, witness.CodeOutcomeUnknown, "status", map[string]any{"cause": "timeout"})}
			}
		default: // VERIFIED at the scripted level
			switch level {
			case witness.L2:
				by[witness.L2] = []witness.Reason{witness.R(witness.L2, witness.CodeFootprintMatched, "footprint",
					map[string]any{"observed": 1, "rows": "row", "table": "the simulated table"})}
			case witness.L1:
				by[witness.L1] = []witness.Reason{witness.R(witness.L1, witness.CodeReadbackMatched, "readback",
					map[string]any{"readback": "simulated", "fields": "status"})}
			}
		}
	}
	d := witness.Decide(required, by, methods)
	at := time.Time{}
	if !e.FinishedAt.IsZero() {
		at = e.FinishedAt
	}
	return witness.FinishWith(d, methods, witness.Extras{}, at, w.maxBytes)
}
