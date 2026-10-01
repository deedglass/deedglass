package policy

// Step is one line of an explain trace (S11 §3.4). The trace is built only with
// EvalOptions.Explain; the hot path allocates nothing for it. Rules after the deciding one are
// left out, and one final step says how many.
type Step struct {
	Step   string `json:"step"`          // stop | budget | rule | default
	ID     string `json:"id,omitempty"`  // rule or budget id
	Line   int    `json:"line,omitzero"` // the rule's line in the policy file
	Result string `json:"result"`        // pass | match | no_match | error | exceeded | reserved | not_evaluated
	Detail string `json:"detail"`        // "when → true", "0 of 60 calls this minute"
	Cost   uint64 `json:"cost,omitzero"` // CEL actual cost of the condition
}

// Trace results.
const (
	ResultPass         = "pass"
	ResultMatch        = "match"
	ResultNoMatch      = "no_match"
	ResultError        = "error"
	ResultExceeded     = "exceeded"
	ResultReserved     = "reserved"
	ResultNotEvaluated = "not_evaluated"
)

// Steps.
const (
	StepStop    = "stop"
	StepBudget  = "budget"
	StepRule    = "rule"
	StepDefault = "default"
)

func (e *evaluator) step(s Step) {
	if e.explain {
		e.d.Trace = append(e.d.Trace, s)
	}
}
