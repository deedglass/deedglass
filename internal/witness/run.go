package witness

import (
	"context"
	"strings"
	"time"
)

// Input is one witness run: the subject, the observer for read-backs (nil when none is planned),
// and reasons the caller adds (witness_gave_up when the worker ran out of attempts).
type Input struct {
	Subject        Subject
	Observer       Observer
	Final          bool // the worker: a budget that runs out is readback_timeout, not a deferral
	Extra          []Reason
	Now            time.Time
	MaxDetailBytes int
}

// Run evaluates every planned level, decides (Decide) and builds the detail (Finish). deferred is
// true when the inline budget ran out before L1 could answer; the caller then leaves the action to
// the worker and records nothing.
func Run(ctx context.Context, in Input) (out Outcome, deferred bool, err error) {
	s := in.Subject
	l0, m0 := EvaluateL0(s)
	byLevel := map[Level][]Reason{L0: l0}
	methods := map[Level]string{L0: m0}
	var rb *ReadbackDetail
	if PlanFor(s).L1 || PlanFor(s).Why != "" {
		r := EvaluateL1(ctx, s, in.Observer, in.Final)
		if r.Deferred {
			return Outcome{}, true, nil
		}
		byLevel[L1] = r.Reasons
		if r.Method != "" {
			methods[L1] = r.Method
		}
		rb = r.Readback
	}
	for _, x := range in.Extra {
		byLevel[x.Level] = append(byLevel[x.Level], x)
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	d := Decide(s.Required, byLevel, methods)
	out, err = Finish(d, methods, rb, now, in.MaxDetailBytes)
	return out, false, err
}

// GaveUp is the reason the worker adds when a witness job runs out of attempts.
func GaveUp(level Level, attempts int, lastError string) Reason {
	lastError = strings.TrimSuffix(strings.TrimSpace(lastError), ".")
	if lastError == "" {
		lastError = "see the server log"
	}
	return R(level, CodeWitnessGaveUp, "worker", map[string]any{"attempts": attempts, "last_error": lastError})
}
