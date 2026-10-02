//go:build attack

package harness

import (
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

// Case is one attack. It has no expected value: those live in the meta.
type Case struct {
	ID  string
	Run func(h *H) Observed
	// Cap bounds this case instead of CaseCap (S22's L05 waits out a real one-minute expiry).
	Cap time.Duration
}

// Observed is what a case saw: the outcome word, a detail head, the actions it created and the
// evidence (status codes, error codes, rule ids, deed ids, fingerprints, hits).
type Observed struct {
	Outcome   string
	Detail    string
	ActionIDs []string
	Evidence  map[string]any
	// Forbidden names upstream listeners that must have seen no traffic in this case.
	Forbidden []string
	// TargetMayChange is set by cases whose outcome allows the target to change (none in S18).
	TargetMayChange bool
	// Line is the short text after the outcome on the terminal line ("budget_exceeded · 60 executed").
	Line string
}

// errWaiting is how a case says it waits on a later session.
type errWaiting struct{ session string }

// Runner runs cases against one booted environment and collects the results.
type Runner struct {
	Env  *Env
	Meta *Meta

	mu      sync.Mutex
	results []Result
}

// RunCase runs one case as a subtest: it looks the id up in the meta, skips it as waiting when a
// needed session's feature isn't there, runs it with a 30 s cap, checks the invariants and
// records the result. A broken invariant fails the case even if the headline matched.
func (r *Runner) RunCase(t *testing.T, c Case) {
	t.Helper()
	mc := r.Meta.Case(c.ID)
	if mc == nil {
		t.Errorf("case %s isn't declared in attack_meta.json; declare it (and pin it under an ADR) before writing its code", c.ID)
		return
	}
	t.Run(c.ID, func(t *testing.T) {
		start := time.Now()
		res := Result{ID: c.ID, Title: mc.Title, Expected: mc.Expected}
		h := r.Env.newH(t, c.ID)
		obs, waiting := runCapped(t, h, c)
		res.MS = time.Since(start).Milliseconds()
		switch {
		case waiting != "":
			res.Status, res.Waiting, res.Actual = StatusWaiting, waiting, "not run"
			res.Detail = "waiting on " + waiting
			r.record(res)
			t.Skipf("waiting on %s", waiting)
			return
		}
		res.Actual, res.Detail, res.Line, res.Evidence = obs.Outcome, obs.Detail, obs.Line, obs.Evidence
		problems := h.checkInvariants(obs)
		problems = append(problems, h.problems...)
		res.Problems = problems
		if obs.Outcome == mc.Expected && DetailMatches(mc.ExpectedDetail, obs.Detail) && len(problems) == 0 {
			res.Status = StatusMatched
		} else {
			res.Status = StatusDiffers
		}
		r.record(res)
		if res.Status != StatusMatched {
			t.Errorf("%s: expected %s (%s), got %s (%s); problems: %v; evidence: %v",
				c.ID, mc.Expected, mc.ExpectedDetail, obs.Outcome, obs.Detail, problems, obs.Evidence)
		}
	})
}

func runCapped(t *testing.T, h *H, c Case) (obs Observed, waiting string) {
	done := make(chan struct{})
	var panicked any
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				if w, ok := p.(errWaiting); ok {
					waiting = w.session
					return
				}
				panicked = p
			}
		}()
		obs = c.Run(h)
	}()
	limit := CaseCap
	if c.Cap > 0 {
		limit = c.Cap
	}
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s ran longer than %s", c.ID, limit)
	}
	if panicked != nil {
		h.problems = append(h.problems, fmt.Sprintf("the case stopped: %v", panicked))
		obs.Outcome, obs.Detail = "ERROR", fmt.Sprint(panicked)
	}
	return obs, waiting
}

// CaseCap bounds one case.
const CaseCap = 30 * time.Second

func (r *Runner) record(res Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, res)
}

// Results returns the recorded results in meta order.
func (r *Runner) Results() []Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	order := map[string]int{}
	for i, c := range r.Meta.Cases {
		order[c.ID] = i
	}
	out := append([]Result(nil), r.results...)
	sort.Slice(out, func(i, j int) bool { return order[out[i].ID] < order[out[j].ID] })
	return out
}
