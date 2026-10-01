package gate

// State is an action's lifecycle state (contract §5). The database trigger
// deedglass.actions_state_guard() (migration 0005) mirrors this table; an integration test checks
// all 225 pairs against it. S06 adds Sealable, S13 adds Settled and the persisted Transition:
// extend this file, never recreate it.
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
