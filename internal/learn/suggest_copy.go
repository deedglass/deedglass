package learn

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

// Suggestion copy (S28 §5): what we saw, what we'd change, what could go wrong. Calm, exact, no
// "AI" framing, and nothing that suggests Deedglass decides for anyone. The API message and the
// CLI line are the same sentence.

// Messages.
const (
	MsgRejectReason      = "A reason is required to reject a suggestion. It goes into the sealed deed."
	MsgAcceptAdvisory    = "This is advice, not a policy change, so there is nothing to accept. Dismiss it with reject and a reason."
	MsgViewerAccept      = "Only admins can accept suggestions. Ask an admin of your org, or view it in Console › Insights."
	MsgPhrasingNeedsKey  = "Claude phrasing needs DEEDGLASS_ANTHROPIC_API_KEY on the server. Deedglass keeps using its own wording until then."
	MsgAcceptReasonLong  = "The reason can be at most 280 characters, on one line."
	MsgSuggestionStatus  = "status must be open, accepting, accepted, rejected, superseded or all."
	MsgSuggestionKind    = "kind must be loosen, tighten, budget, explicit_rule or advisory."
	MsgDigestRecipients  = "digest.recipients must be owners, admins or off."
	MsgPhrasingCap       = "llm_monthly_cap_cents must be 0 to 10,000 (up to $100.00 a month)."
	MsgSuggestNoEnforced = "No policy is enforced in this org yet, so there is nothing to suggest against. Activate one: deedglass policy activate <name> --version 1 --mode enforce"
	MsgSuggestNotNow     = "Suggestions are already being generated for this org. Try again in a minute."
	MsgRejectHint        = "Deedglass won't suggest this again for 30 days unless the numbers double."
	MsgNoSuggestions     = "No suggestions yet. Deedglass suggests a change once a rule has at least 30 decisions in the last 14 days."
	MsgWordedBy          = "Worded by %s from the numbers above. Deedglass checked that every number matches the evidence."
)

// SuggestionStaleMessage is suggestion_stale's message.
func SuggestionStaleMessage(policyName string, now, base int) string {
	return fmt.Sprintf("%s v%d has been enforced since this suggestion was written for v%d. Generate suggestions again: POST /v1/suggestions/generate.", policyName, now, base)
}

// ShadowSlotTakenMessage is shadow_slot_taken's message.
func ShadowSlotTakenMessage(policyName string, version int) string {
	return fmt.Sprintf("%s v%d is already running in shadow. Promote or clear it first, or accept with replace_shadow: true to replace it.", policyName, version)
}

// SuggestionDecidedMessage is suggestion_decided's message.
func SuggestionDecidedMessage(s *Suggestion, who string) string {
	when := ""
	if s.DecidedAt != nil {
		when = " on " + s.DecidedAt.UTC().Format("2006-01-02")
	}
	if who != "" {
		who = " by " + who
	}
	switch s.Status {
	case SugAccepted:
		created := ""
		if s.ResultingVersion != nil {
			created = fmt.Sprintf(" It created %s v%d.", s.Policy.Name, s.ResultingVersion.Version)
		}
		return "This suggestion was already accepted" + when + who + "." + created
	case SugRejected:
		return "This suggestion was already rejected" + when + who + ". Generate suggestions again to see what changed."
	case SugSuperseded:
		return "This suggestion was superseded: the policy changed after it was written. Generate suggestions again: POST /v1/suggestions/generate."
	case SugAccepting:
		return "Someone is accepting this suggestion right now. Read it again in a moment to see the version it created."
	}
	return "This suggestion was already decided. Read it again to see its status."
}

// AcceptedLine is the CLI's and Console's success line.
func AcceptedLine(policyName string, version int, replaced *int) string {
	s := fmt.Sprintf("%s v%d is running in shadow", policyName, version)
	if replaced != nil {
		s += fmt.Sprintf(" (replaced v%d)", *replaced)
	}
	return s
}

// GenerateLine is "3 suggestions · 1 dropped".
func GenerateLine(created, dropped int) string {
	s := fmt.Sprintf("%d %s", created, plural(created, "suggestion", "suggestions"))
	if dropped > 0 {
		s += fmt.Sprintf(" · %d dropped", dropped)
	}
	return s
}

func fmtCount(n int64) string { return deed.FormatCount(n) }

// fmtWait writes a wait for people: 45 seconds, 21 minutes, 2 hours, 3 days.
func fmtWait(seconds float64) string {
	s := int64(math.Round(seconds))
	switch {
	case s < 90:
		return fmt.Sprintf("%d %s", s, plural(int(s), "second", "seconds"))
	case s < 90*60:
		m := int64(math.Round(float64(s) / 60))
		return fmt.Sprintf("%d %s", m, plural(int(m), "minute", "minutes"))
	case s < 36*3600:
		h := int64(math.Round(float64(s) / 3600))
		return fmt.Sprintf("%d %s", h, plural(int(h), "hour", "hours"))
	}
	d := int64(math.Round(float64(s) / 86400))
	return fmt.Sprintf("%d %s", d, plural(int(d), "day", "days"))
}

// fmtPercent writes a rate: 0.95 → 95%, 0.973 → 97.3%, 1 → 100%.
func fmtPercent(rate float64) string {
	v := math.Round(rate*1000) / 10
	s := strconv.FormatFloat(v, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0") + "%"
}

// WeekLabel is "week 41 (5–11 Oct 2026)" without the dash: "week 41 (5 to 11 Oct 2026)".
func WeekLabel(year, week int, monday time.Time) string {
	sunday := monday.AddDate(0, 0, 6)
	if monday.Month() == sunday.Month() {
		return fmt.Sprintf("week %d (%d to %d %s %d)", week, monday.Day(), sunday.Day(), monday.Format("Jan"), sunday.Year())
	}
	return fmt.Sprintf("week %d (%d %s to %d %s %d)", week, monday.Day(), monday.Format("Jan"), sunday.Day(), sunday.Format("Jan"), sunday.Year())
}
