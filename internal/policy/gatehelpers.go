package policy

import (
	"errors"
	"math"
	"strings"
	"time"
)

// Helpers the Gate (S13) needs from the evaluator's catalog. They are pure, like the rest of the
// package.

// ExceededDecision is the deny for a reservation that Reserve refused after the evaluation allowed
// the action: another request took the last unit between the snapshot and the reservation. It
// names the budget with the same numbers and message the evaluator would have used. rs are the
// reservations that were refused (the decision's); agentName and tz shape the message.
func ExceededDecision(e *ExceededError, rs []Reservation, agentName, tz string, at time.Time) Decision {
	h := &BudgetHit{ID: e.BudgetID, Per: PerAgent, Window: WindowMinute, Limit: e.Limit, Used: e.Used, Unit: "calls",
		ResetsAt: e.ResetsAt}
	for _, r := range rs {
		if r.Budget != e.BudgetID {
			continue
		}
		h.Window = r.Window
		if r.Scope == "org" {
			h.Per = PerOrg
		}
		if cur, ok := strings.CutPrefix(r.Counter, "spend:"); ok {
			h.Unit = cur
		}
		break
	}
	h.RetryAfterS = int64(math.Ceil(e.ResetsAt.Sub(at).Seconds()))
	if h.RetryAfterS < 1 {
		h.RetryAfterS = 1
	}
	msg := budgetMessage(h, agentName, tz, at)
	return Decision{Outcome: Deny, Code: CodeBudgetExceeded, Source: SourceBudget, RuleID: e.BudgetID, Message: msg,
		Budget: h, Facts: DerivedFacts{SQL: noSQL()}}
}

// TimeLimitTripped reports whether d is the fail-closed deny of the 50 ms evaluation time limit
// (DL-025 watch item): policy_unavailable because the deadline passed, not because no policy was
// loaded or the evaluator failed. The Gate counts and logs every one.
func TimeLimitTripped(d Decision) bool {
	if d.Code != CodePolicyUnavailable || d.Source != SourceBuiltin {
		return false
	}
	for _, e := range d.Errors {
		if e == "deadline" || strings.Contains(e, "evaluation time limit") {
			return true
		}
	}
	return false
}

// AsExceeded returns the *ExceededError in err's chain, or nil.
func AsExceeded(err error) *ExceededError {
	var e *ExceededError
	if errors.As(err, &e) {
		return e
	}
	return nil
}
