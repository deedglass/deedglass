package gate

import "github.com/deedglass/deedglass/internal/gate/lifecycle"

// The state machine (contract §5). S02 created this file, S06 added Sealable and S13 added Settled
// and the persisted transitions. Since S13 the edge table itself lives in the leaf package
// internal/gate/lifecycle, so internal/ledger (which persists transitions) and this package (which
// drives them) can both use it without an import cycle; every name below is an alias of that
// package's, so gate.State and lifecycle.State are the same type. Extend lifecycle, never
// duplicate it here.

// State is an action's lifecycle state.
type State = lifecycle.State

const (
	Received        = lifecycle.Received
	Authenticated   = lifecycle.Authenticated
	Evaluated       = lifecycle.Evaluated
	Denied          = lifecycle.Denied
	PendingApproval = lifecycle.PendingApproval
	Executing       = lifecycle.Executing
	Executed        = lifecycle.Executed
	Failed          = lifecycle.Failed
	Witnessing      = lifecycle.Witnessing
	Verified        = lifecycle.Verified
	Mismatch        = lifecycle.Mismatch
	Unverifiable    = lifecycle.Unverifiable
	Sealed          = lifecycle.Sealed
	Expired         = lifecycle.Expired
	Cancelled       = lifecycle.Cancelled

	// Initial is the state every action is created in; the database refuses any other.
	Initial = lifecycle.Initial
)

// States returns the 15 states in contract order.
func States() []State { return lifecycle.States() }

// CanTransition reports whether from → to is an edge of contract §5.
func CanTransition(from, to State) bool { return lifecycle.CanTransition(from, to) }

// Errors of a persisted transition.
var (
	ErrIllegalTransition = lifecycle.ErrIllegalTransition
	ErrStateConflict     = lifecycle.ErrStateConflict
)
