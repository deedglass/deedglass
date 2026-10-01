package lifecycle

import (
	"errors"
	"strings"
	"testing"
)

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

// TestEdgesExhaustive lists every allowed edge of contract §5 by hand and checks all 225 pairs:
// exactly these are allowed, everything else is refused.
func TestEdgesExhaustive(t *testing.T) {
	allowed := map[[2]State]bool{
		{Received, Authenticated}: true, {Authenticated, Evaluated}: true,
		{Evaluated, Denied}: true, {Evaluated, PendingApproval}: true, {Evaluated, Executing}: true,
		{PendingApproval, Executing}: true, {PendingApproval, Denied}: true, {PendingApproval, Expired}: true,
		{PendingApproval, Cancelled}: true,
		{Denied, Sealed}:             true, {Expired, Sealed}: true, {Cancelled, Sealed}: true,
		{Executing, Executed}: true, {Executing, Failed}: true, {Executed, Witnessing}: true,
		{Witnessing, Verified}: true, {Witnessing, Mismatch}: true, {Witnessing, Unverifiable}: true,
		{Verified, Sealed}: true, {Mismatch, Sealed}: true, {Unverifiable, Sealed}: true, {Failed, Sealed}: true,
	}
	if len(allowed) != 22 {
		t.Fatalf("the hand-written table has %d edges, want 22", len(allowed))
	}
	pairs := 0
	for _, from := range States() {
		for _, to := range States() {
			pairs++
			if got, want := CanTransition(from, to), allowed[[2]State{from, to}]; got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
	if pairs != 225 {
		t.Fatalf("checked %d pairs, want 225", pairs)
	}
}

func TestSealable(t *testing.T) {
	want := map[State]bool{Denied: true, Expired: true, Cancelled: true, Failed: true, Verified: true, Mismatch: true, Unverifiable: true}
	for _, s := range States() {
		if s.Sealable() != want[s] {
			t.Errorf("%s.Sealable() = %v, want %v", s, s.Sealable(), want[s])
		}
	}
	if State("nope").Sealable() {
		t.Error("an unknown state is sealable")
	}
}

func TestSettled(t *testing.T) {
	for _, s := range States() {
		if want := s == Sealed || s == PendingApproval; s.Settled() != want {
			t.Errorf("%s.Settled() = %v", s, s.Settled())
		}
	}
}

// TestRandomWalks: 10,000 seeded walks from received always end at sealed, or stop where a person
// (pending_approval) or a tool (executing) decides the next step; every step is an edge.
func TestRandomWalks(t *testing.T) {
	seed := uint64(13)
	next := func(n int) int {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		return int(seed % uint64(n))
	}
	ends := map[State]int{}
	for range 10000 {
		s := Initial
		for steps := 0; !s.Terminal(); steps++ {
			if steps > 20 {
				t.Fatal("a walk didn't end")
			}
			if (s == PendingApproval || s == Executing) && next(4) == 0 {
				break // waiting on a person or a tool
			}
			n := s.Next()
			to := n[next(len(n))]
			if err := Check(s, to); err != nil {
				t.Fatal(err)
			}
			s = to
		}
		ends[s]++
	}
	for s := range ends {
		if s != Sealed && s != PendingApproval && s != Executing {
			t.Fatalf("a walk ended at %s", s)
		}
	}
	if ends[Sealed] == 0 {
		t.Fatal("no walk reached sealed")
	}
}

func TestCheckAndPanic(t *testing.T) {
	if err := Check(Executing, Unverifiable); !errors.Is(err, ErrIllegalTransition) || !strings.Contains(err.Error(), "executing → unverifiable") {
		t.Fatal(err)
	}
	prev := PanicOnIllegal(true)
	defer PanicOnIllegal(prev)
	defer func() {
		if recover() == nil {
			t.Fatal("no panic in test mode")
		}
	}()
	_ = Check(Sealed, Received)
}

func TestRenderings(t *testing.T) {
	m := Mermaid()
	if !strings.HasPrefix(m, "stateDiagram-v2\n    [*] --> received\n") || strings.Count(m, " --> ") != 24 {
		t.Fatalf("%s", m)
	}
	if l := EdgeList(); len(l) != 22 || l[0] != "authenticated → evaluated" {
		t.Fatalf("%v", l)
	}
}
