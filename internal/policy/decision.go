package policy

import (
	"fmt"
	"strings"
	"time"
)

// Code is the error code a decision maps to (contract §8). "" means allow.
type Code string

// Decision codes. CodePolicyUnavailable is new in S11 (CONTRACT CHANGE, additive).
const (
	CodeNone              Code = ""
	CodePolicyDenied      Code = "policy_denied"
	CodeApprovalRequired  Code = "approval_required"
	CodeBudgetExceeded    Code = "budget_exceeded"
	CodeAgentStopped      Code = "agent_stopped"
	CodeAgentSuspended    Code = "agent_suspended"
	CodeOrgStopped        Code = "org_stopped"
	CodePolicyUnavailable Code = "policy_unavailable"
)

// Sources of a decision.
const (
	SourceStop    = "stop"
	SourceBudget  = "budget"
	SourceRule    = "rule"
	SourceDefault = "default"
	SourceBuiltin = "builtin"
)

// Builtin rule ids.
const (
	RuleInvalidInput = "invalid-input"
	RuleUnavailable  = "unavailable"
)

// Mode is how a policy is active (S12): enforce decides; shadow is recorded alongside.
type Mode string

const (
	ModeEnforce Mode = "enforce"
	ModeShadow  Mode = "shadow"
)

// DerivedFacts are the facts evaluation computed from the input: the SQL classification and the
// time parts. They are recomputed on replay, never stored as input (S11 §3.1).
type DerivedFacts struct {
	SQL  SQLFacts  `json:"sql"`
	Time TimeFacts `json:"time"`
}

// Decision is what Evaluate returns. Evaluate never returns a Go error: every failure is a deny.
// The caller (S12's registry, S13's gate) adds the policy's id, name, version and mode.
type Decision struct {
	Outcome      Outcome       `json:"decision"`
	Code         Code          `json:"code,omitempty"`
	Source       string        `json:"source"`
	RuleID       string        `json:"rule_id,omitempty"`
	RuleLine     int           `json:"rule_line,omitempty"`
	Reason       string        `json:"reason,omitempty"`
	Message      string        `json:"message"`
	Approval     *ApprovalSpec `json:"approval,omitempty"`
	Verify       Level         `json:"verify,omitempty"`
	Notify       []string      `json:"notify,omitempty"`
	NotifyNone   bool          `json:"notify_none,omitempty"` // S40: `notify: []`, the approval sends nothing
	Budget       *BudgetHit    `json:"budget,omitempty"`
	Reservations []Reservation `json:"reservations,omitempty"`
	CostBudgets  []CostTarget  `json:"cost_budgets,omitempty"`
	Facts        DerivedFacts  `json:"facts"`
	Errors       []string      `json:"errors,omitempty"`
	Trace        []Step        `json:"trace,omitempty"`
	SQLGuard     string        `json:"sqlguard_version,omitempty"`
	Elapsed      time.Duration `json:"-"`
}

// Messages the agent sees (S11 §5). Each is exact and ends with a next step. Stop messages are
// S08's catalog strings (internal/httpx/errors.go); a cmd/deedglass test keeps them equal.
const (
	msgDenyRule        = `Blocked by policy %q: %s`
	msgApproval        = `Waiting for approval under policy %q: %s`
	msgUnavailable     = "Deedglass could not evaluate the policy for this action, so it did not run. An admin can check Console › Policies."
	msgOrgStopped      = "All agents in this org are stopped. An owner can start them again in Console › Settings."
	msgAgentStopped    = `Agent %q is stopped. Deedglass refuses its calls until an admin starts it in Console › Agents.`
	msgAgentSuspended  = `Agent %q is suspended. An admin can resume it in Console › Agents.`
	msgRuleErrorSuffix = " (Deedglass could not evaluate this rule: %s.)"
	msgInvalidInput    = "Deedglass could not read this action's input (%s), so it did not run. Check the arguments the agent sent."
	msgCostLimit       = "Deedglass stopped evaluating this rule at its cost limit of %s, so the action did not run. Send smaller arguments, or ask an admin to simplify the rule in Console › Policies."
	errTimeLimit       = "%s: stopped at the evaluation time limit of %s"
	msgMeasureError    = "Deedglass could not measure this action for budget %q (%s), so it did not run. An admin can check the budget's measure in Console › Policies."
	msgBudget          = `Budget %q reached: %s of %s %s. %s`
	msgAgentBudget     = `Budget %q for agent %q reached: %s of %s %s. %s`
	msgBudgetProjected = `Budget %q reached: this action would bring %s total%s to %s of %s. %s`
	msgRetryIn         = "Try again in %s."
	msgResetsMidnight  = "It resets at midnight %s (in %s)."
)

// Visibility reasons (S14): why a tool is hidden from tools/list. A person reads them in the
// wrapper's log and in POST /v1/tools/resolve.
const (
	ReasonDefaultDeny = "No rule allows this tool, and the policy default is deny."
	ReasonRuleDeny    = "A rule always denies this tool."
	ReasonNoPolicy    = "No policy is active, so every tool is denied."
)

// MessageUnavailable is the policy_unavailable message, for the error catalog's test.
const MessageUnavailable = msgUnavailable

// StopMessages returns the three Stop messages for an agent name, for the catalog cross-check.
func StopMessages(agent string) (org, stopped, suspended string) {
	return msgOrgStopped, fmt.Sprintf(msgAgentStopped, agent), fmt.Sprintf(msgAgentSuspended, agent)
}

func clipName(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

// shortError turns a CEL error into the short phrase the reason suffix carries.
func shortError(err error) string {
	s := err.Error()
	s = strings.TrimSpace(strings.TrimPrefix(s, "error: "))
	s = strings.TrimRight(s, ".")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	return s
}

// windowPhrase is "this minute", "this hour" or "today".
func windowPhrase(w string) string {
	switch w {
	case WindowMinute:
		return "this minute"
	case WindowHour:
		return "this hour"
	}
	return "today"
}

// windowTotal is "today's", "this hour's" or "this minute's".
func windowTotal(w string) string {
	switch w {
	case WindowMinute:
		return "this minute's"
	case WindowHour:
		return "this hour's"
	}
	return "today's"
}

// humanDuration renders a wait: "14 s", "12 m", "6 h 12 m". It rounds up, so it never promises
// a reset earlier than the real one.
func humanDuration(d time.Duration) string {
	s := int64((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	switch {
	case s <= 60:
		return fmt.Sprintf("%d s", s)
	case s < 3600:
		return fmt.Sprintf("%d m", (s+59)/60)
	}
	h := s / 3600
	m := (s%3600 + 59) / 60
	if m == 60 {
		h, m = h+1, 0
	}
	if m == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d m", h, m)
}

var currencySymbols = map[string]string{"usd": "$", "eur": "€", "gbp": "£", "inr": "₹", "jpy": "¥"}

// formatMoney renders minor units for a person: "$500.00", "₹1,200.00", "12.50 chf".
func formatMoney(amount int64, currency string) string {
	s := Money{Amount: amount, Currency: currency}.String()
	num, cur, _ := strings.Cut(s, " ")
	intPart, frac, hasFrac := strings.Cut(num, ".")
	neg := strings.HasPrefix(intPart, "-")
	intPart = strings.TrimPrefix(intPart, "-")
	out := thousandsStr(intPart)
	if hasFrac {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	if sym, ok := currencySymbols[cur]; ok {
		return sym + out
	}
	return out + " " + cur
}

// budgetMessage renders the budget_exceeded message with exact numbers and the reset time.
func budgetMessage(h *BudgetHit, agentName, tz string, at time.Time) string {
	var used, limit, unit string
	if h.Unit == "calls" {
		used, limit, unit = thousands(h.Used), thousands(h.Limit), "calls"
	} else {
		used, limit = formatMoney(h.Used, h.Unit), formatMoney(h.Limit, h.Unit)
	}
	scope := windowPhrase(h.Window)
	if h.Per == PerOrg {
		scope = "across all agents " + scope
	}
	what := scope
	if unit != "" {
		what = unit + " " + scope
	}
	var next string
	if h.Window == WindowDay {
		if tz == "" {
			tz = "UTC"
		}
		next = fmt.Sprintf(msgResetsMidnight, tz, humanDuration(h.ResetsAt.Sub(at)))
	} else {
		next = fmt.Sprintf(msgRetryIn, humanDuration(h.ResetsAt.Sub(at)))
	}
	if h.Projected > 0 {
		// A measured budget (DL-025): name the total this action would reach, not only the usage
		// before it, so "$490.00 of $500.00" never reads like room to spare.
		across := ""
		if h.Per == PerOrg {
			across = " across all agents"
		}
		return fmt.Sprintf(msgBudgetProjected, h.ID, windowTotal(h.Window), across,
			formatMoney(h.Projected, h.Unit), formatMoney(h.Limit, h.Unit), next)
	}
	if strings.HasPrefix(h.ID, "agent.") {
		return fmt.Sprintf(msgAgentBudget, strings.TrimPrefix(h.ID, "agent."), clipName(agentName), used, limit, what, next)
	}
	return fmt.Sprintf(msgBudget, h.ID, used, limit, what, next)
}
