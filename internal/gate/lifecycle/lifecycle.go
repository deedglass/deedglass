// Package lifecycle is the action state machine of contract §5: the 15 states and the 22 edges,
// nothing else. It is a leaf (standard library only) so that internal/ledger, which persists
// transitions, and internal/gate, which drives them, can both import it without a cycle.
// internal/gate/state.go re-exports every name here, so callers keep writing gate.Executing.
//
// The database trigger deedglass.actions_state_guard() (migration 0005) mirrors the edge table; an
// integration test checks all 225 pairs against it, and docs/GATE.md renders it (TestGateDoc).
package lifecycle

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

// State is an action's lifecycle state (contract §5).
type State string

const (
	Received        State = "received"
	Authenticated   State = "authenticated"
	Evaluated       State = "evaluated"
	Denied          State = "denied"
	PendingApproval State = "pending_approval"
	Executing       State = "executing"
	Executed        State = "executed"
	Failed          State = "failed"
	Witnessing      State = "witnessing"
	Verified        State = "verified"
	Mismatch        State = "mismatch"
	Unverifiable    State = "unverifiable"
	Sealed          State = "sealed"
	Expired         State = "expired"
	Cancelled       State = "cancelled"
)

var states = []State{
	Received, Authenticated, Evaluated, Denied, PendingApproval, Executing, Executed, Failed,
	Witnessing, Verified, Mismatch, Unverifiable, Sealed, Expired, Cancelled,
}

// edges is the whole state machine: exactly 22 edges. A tool that may or may not have run goes
// executing → executed (execution.status = unknown) → witnessing → unverifiable; there is no
// executing → unverifiable edge, and crash recovery never invents one.
var edges = map[State][]State{
	Received:        {Authenticated},
	Authenticated:   {Evaluated},
	Evaluated:       {Denied, PendingApproval, Executing},
	PendingApproval: {Executing, Denied, Expired, Cancelled},
	Denied:          {Sealed},
	Expired:         {Sealed},
	Cancelled:       {Sealed},
	Executing:       {Executed, Failed},
	Executed:        {Witnessing},
	Witnessing:      {Verified, Mismatch, Unverifiable},
	Verified:        {Sealed},
	Mismatch:        {Sealed},
	Unverifiable:    {Sealed},
	Failed:          {Sealed},
}

// States returns the 15 states in contract order. The slice is a copy.
func States() []State { return append([]State(nil), states...) }

// Initial is the state every action is created in; the database refuses any other.
const Initial = Received

// Valid reports whether s is one of the 15 states.
func (s State) Valid() bool {
	for _, x := range states {
		if x == s {
			return true
		}
	}
	return false
}

// CanTransition reports whether from → to is an edge. A state never transitions to itself; the
// database treats an UPDATE that keeps the same state as a no-op, not as a transition.
func CanTransition(from, to State) bool {
	for _, x := range edges[from] {
		if x == to {
			return true
		}
	}
	return false
}

// Next returns the states reachable from s in one step, in edge order. The slice is a copy.
func (s State) Next() []State { return append([]State(nil), edges[s]...) }

// Terminal reports whether s has no way out. Only Sealed is terminal.
func (s State) Terminal() bool { return s == Sealed }

// Sealable reports whether an action in s is final and may be sealed: the seven states whose one
// way out is Sealed (denied, expired, cancelled, failed, verified, mismatch, unverifiable).
// ledger.SealAction refuses any other state (S06).
func (s State) Sealable() bool { return CanTransition(s, Sealed) }

// Settled reports whether an action in s has reached a point a caller can act on: Sealed, or
// PendingApproval (waiting for a person). It is what GET /v1/actions/{id}?wait= waits for (S13).
func (s State) Settled() bool { return s == Sealed || s == PendingApproval }

// Errors of a persisted transition (S13). internal/ledger returns them; internal/gate re-exports
// them, so errors.Is works whichever package a caller names.
var (
	// ErrIllegalTransition: CanTransition(from, to) is false. Nothing was written.
	ErrIllegalTransition = errors.New("gate: illegal transition")
	// ErrStateConflict: the action wasn't in the expected state (another transaction moved it, it
	// lost a compare-and-set race, or it doesn't exist in this org). Nothing was written.
	ErrStateConflict = errors.New("gate: state changed underneath")
)

// panicOnIllegal is switched on by TestMain in the packages that drive transitions (gate,
// cmd/deedglass), so an illegal edge fails the test run at the exact call site. In production
// Check returns ErrIllegalTransition and the caller logs it at error level with the action id.
var panicOnIllegal atomic.Bool

// PanicOnIllegal turns the test-time panic on or off and returns the previous setting.
func PanicOnIllegal(on bool) bool { return panicOnIllegal.Swap(on) }

// Check returns nil when from → to is an edge, else ErrIllegalTransition (or panics, in tests that
// asked for it with PanicOnIllegal).
func Check(from, to State) error {
	if CanTransition(from, to) {
		return nil
	}
	err := fmt.Errorf("%w: %s → %s", ErrIllegalTransition, from, to)
	if panicOnIllegal.Load() {
		panic(err)
	}
	return err
}

// Mermaid renders the edge table as a mermaid stateDiagram-v2, in contract order. docs/GATE.md
// embeds it, and a test keeps the two equal.
func Mermaid() string {
	var b strings.Builder
	b.WriteString("stateDiagram-v2\n    [*] --> received\n")
	for _, from := range states {
		tos := edges[from]
		for _, to := range tos {
			fmt.Fprintf(&b, "    %s --> %s\n", from, to)
		}
	}
	b.WriteString("    sealed --> [*]\n")
	return b.String()
}

// EdgeList returns every edge as "from → to", sorted. Tests compare it with contract §5.
func EdgeList() []string {
	var out []string
	for from, tos := range edges {
		for _, to := range tos {
			out = append(out, string(from)+" → "+string(to))
		}
	}
	sort.Strings(out)
	return out
}
