package gate

import "testing"

func TestStateTable(t *testing.T) {
	if n := len(States()); n != 15 {
		t.Fatalf("%d states, want 15", n)
	}
	n := 0
	for _, from := range States() {
		for _, to := range States() {
			if CanTransition(from, to) {
				n++
				if from == to {
					t.Errorf("self edge %s", from)
				}
			}
		}
	}
	if n != 22 {
		t.Fatalf("%d edges, want 22", n)
	}
	for from, tos := range edges {
		if !from.Valid() {
			t.Errorf("unknown from %q", from)
		}
		for _, to := range tos {
			if !to.Valid() {
				t.Errorf("unknown to %q", to)
			}
		}
	}
	if State("nope").Valid() || CanTransition("nope", Sealed) {
		t.Error("unknown state accepted")
	}
}

func TestTerminalAndReachable(t *testing.T) {
	for _, s := range States() {
		if s.Terminal() != (s == Sealed) {
			t.Errorf("%s.Terminal() = %v", s, s.Terminal())
		}
		if s.Terminal() != (len(s.Next()) == 0) {
			t.Errorf("%s: terminal iff no way out", s)
		}
	}
	// Every state is reachable from Initial, and Sealed is reachable from every state.
	seen := map[State]bool{Initial: true}
	queue := []State{Initial}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for _, n := range s.Next() {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	if len(seen) != 15 {
		t.Fatalf("reachable %d of 15", len(seen))
	}
	for _, s := range States() {
		if !reaches(s, Sealed, map[State]bool{}) {
			t.Errorf("%s never reaches sealed", s)
		}
	}
}

func reaches(from, to State, seen map[State]bool) bool {
	if from == to {
		return true
	}
	seen[from] = true
	for _, n := range from.Next() {
		if !seen[n] && reaches(n, to, seen) {
			return true
		}
	}
	return false
}

// The honest-unknown rule: no shortcut from executing to a verdict.
func TestNoExecutingShortcut(t *testing.T) {
	for _, to := range []State{Unverifiable, Verified, Mismatch, Sealed, Witnessing} {
		if CanTransition(Executing, to) {
			t.Errorf("executing → %s must not exist", to)
		}
	}
	if !CanTransition(Executing, Executed) || !CanTransition(Executed, Witnessing) || !CanTransition(Witnessing, Unverifiable) {
		t.Error("the honest-unknown path is missing")
	}
}
