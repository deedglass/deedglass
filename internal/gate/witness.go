package gate

import (
	"context"
	"errors"
)

// Verdicts, levels and methods (contract §6).
const (
	LevelL0             = "L0"
	VerdictVerified     = "VERIFIED"
	VerdictMismatch     = "MISMATCH"
	VerdictUnverifiable = "UNVERIFIABLE"

	// MethodReported: the tool's own response, through a connector the Gate called (gateway).
	MethodReported = "reported"
	// MethodEdgeReported: the edge's report of a tool it ran (proxy). Not independent of the edge.
	MethodEdgeReported = "edge_reported"
)

// Witness reasons: short codes in witness_detail, the deed's detail digest and the events.
const (
	ReasonTimeout        = "timeout"         // the tool didn't answer within gate.execute_timeout
	ReasonOutcomeUnknown = "outcome_unknown" // a lost response, a panic, an untyped error, a crash
	ReasonEdgeLost       = "edge_lost"       // proxy: the edge never reported within gate.edge_result_timeout
	ReasonWitnessGaveUp  = "witness_gave_up" // a deferred witness failed max_attempts times
	// ReasonClientCancelled: the MCP client went away (or its agent was stopped) while the upstream
	// call ran, so Deedglass cancelled it and can't know whether it applied (S15).
	ReasonClientCancelled = "client_cancelled"
)

// L0 is S13's witness: the verdict is what the tool (or the edge) reported, and only a clean
// success is VERIFIED (CLAUDE.md rule 3). An unknown outcome is UNVERIFIABLE with its reason, never
// VERIFIED and never MISMATCH. S19's witness.Engine replaces it behind Witnesser.
type L0 struct{}

var _ Witnesser = L0{}

// Inline implements Witnesser. A failed execution has no verdict (failed → sealed), so the
// pipeline never asks.
func (L0) Inline(_ context.Context, s Subject, e Execution) (WitnessOutcome, bool) {
	method := MethodReported
	if s.Mode == ModeProxy {
		method = MethodEdgeReported
	}
	if e.Status == ExecSucceeded {
		return WitnessOutcome{Level: LevelL0, Verdict: VerdictVerified, Method: method, Reasons: []string{}}, false
	}
	reason := e.Reason
	if reason == "" {
		reason = ReasonOutcomeUnknown
	}
	return WitnessOutcome{Level: LevelL0, Verdict: VerdictUnverifiable, Method: method, Reasons: []string{reason}}, false
}

// ErrNoDeferredWitness: L0 never defers, so a witness job for it can only be left over.
var ErrNoDeferredWitness = errors.New("gate: the L0 witness doesn't defer")

// Deferred implements Witnesser.
func (L0) Deferred(context.Context, Subject) (WitnessOutcome, error) {
	return WitnessOutcome{}, ErrNoDeferredWitness
}

// gaveUp is the verdict when a deferred witness runs out of attempts.
func gaveUp(method string) WitnessOutcome {
	return WitnessOutcome{Level: LevelL0, Verdict: VerdictUnverifiable, Method: method, Reasons: []string{ReasonWitnessGaveUp}}
}
