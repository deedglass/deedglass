package approve

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Every approval string (S22 §5): the API's sentences that carry details, the agent-facing texts,
// the CLI's and the email's. The API's catalog copy lives in internal/httpx/errors.go; the page's
// fixed copy is in pages/approve.js and mirrors the states here. Vocabulary: approval, countersign,
// approve, deny, expired, deed, sealed. No exclamation marks, no emoji, no em dashes.

// Agent-facing texts (MCP and REST decision framing; S22 §5.5). They never name the approver:
// names are for people, not models.
const (
	AgentApprovedRan     = "Approved by a person. Ran %s."
	AgentApprovedStopped = `A person approved this, but agent %q is stopped, so nothing ran.`
	AgentDenied          = "A person denied this action. Nothing was run."
	AgentUnsatisfiable   = "No one in this org can approve this action, so it was not run."
)

// AgentDeniedWithReason is the denial with the person's reason, quoted (DL-028: the reason reaches
// the MCP client).
func AgentDeniedWithReason(reason string) string {
	r := quoteReason(reason)
	if r == "" {
		return AgentDenied
	}
	return AgentDenied + " Reason: " + r
}

// AgentStillPending is a check while nobody has decided.
func AgentStillPending(approvalID string, expires time.Time, loc *time.Location) string {
	return fmt.Sprintf("Still waiting for approval %s. It expires at %s. Nothing has run yet. Check again in a minute.", approvalID, Clock(expires, loc))
}

// AgentExpired is a check after nobody decided in time.
func AgentExpired(expires time.Time, loc *time.Location) string {
	return fmt.Sprintf("Nobody approved this before %s, so it expired. Nothing was run. Ask again if it's still needed.", Clock(expires, loc))
}

// quoteReason makes a person's reason safe to quote to a model: one line, no control or bidi
// characters, at most 280 characters, double quotes made single.
func quoteReason(reason string) string {
	r := strings.Join(strings.Fields(cleanText(reason)), " ")
	if r == "" {
		return ""
	}
	r = strings.ReplaceAll(r, `"`, "'")
	if !strings.HasSuffix(r, ".") && !strings.HasSuffix(r, "?") {
		r += "."
	}
	return `"` + r + `"`
}

// API error sentences with details (the catalog in httpx holds the fixed forms).

// MsgDecided is approval_decided with who and when.
func MsgDecided(outcome, name string, at time.Time, loc *time.Location) string {
	if name == "" {
		return fmt.Sprintf("This approval was already decided: %s at %s.", outcome, Clock(at, loc))
	}
	return fmt.Sprintf("This approval was already decided: %s by %s at %s.", outcome, name, Clock(at, loc))
}

// MsgExpired is approval_expired with the time.
func MsgExpired(at time.Time, loc *time.Location) string {
	return fmt.Sprintf("This approval expired at %s. Nothing ran. Ask the agent to try again if it's still needed.", Clock(at, loc))
}

// MsgAlreadyCountersigned is already_countersigned with how many more people are needed.
func MsgAlreadyCountersigned(more int) string {
	if more <= 1 {
		return "You already countersigned this. It needs one more person."
	}
	return fmt.Sprintf("You already countersigned this. It needs %d more people.", more)
}

// MsgNotEligible is approver_not_eligible naming the rule and its approvers.
func MsgNotEligible(rule string, approvers []string) string {
	who := strings.Join(approvers, " or ")
	if who == "" {
		who = "role:admin"
	}
	if rule == "" {
		return fmt.Sprintf("This approval needs %s to countersign. Ask one of them.", who)
	}
	return fmt.Sprintf("Rule %q needs %s to countersign. Ask one of them.", rule, who)
}

// MsgUnsatisfiable is approval_unsatisfiable with the counts.
func MsgUnsatisfiable(required, eligible int) string {
	verb := "are"
	if eligible == 1 {
		verb = "is"
	}
	return fmt.Sprintf("No one in this org can approve this: the rule needs %s and only %d %s eligible. Nothing was run. "+
		"Invite another admin, or allow self-countersign in Console › Settings › Approvals.", plural(required, "person", "people"), eligible, verb)
}

// Validation sentences (param in parentheses).
const (
	MsgReasonRequired = "A reason is required to deny. The agent sees it."                        // reason
	MsgReasonTooLong  = "The reason can be at most 280 characters. Shorten it."                   // reason
	MsgReasonChars    = "The reason can't contain control or direction-changing characters."      // reason
	MsgDigestRequired = `context_digest is required: the "sha256:…" digest of what you reviewed.` // context_digest
	MsgVoteInvalid    = `vote must be "approve" or "deny".`                                       // vote
	MsgStatusFilter   = "status must be a comma-separated list of pending, approved, denied, expired and cancelled."
	MsgApprovalID     = "That isn't an approval id. Approval ids start with apr_."
)

// Summaries (the context snapshot's summary and the email's plain sentence).

// Summary is the sentence a person reads first: "claude-desktop wants to UPDATE about 1 row in
// public.orders." or "support-bot wants to run stripe.create_refund."
func Summary(agent string, s SummaryFacts) string {
	return agent + " wants to " + ShortSummary(s) + "."
}

// SummaryFacts is what a summary is built from.
type SummaryFacts struct {
	Tool      string
	SQLKind   string   // update, insert, delete, merge, select; "" when the tool takes no SQL
	Tables    []string // schema-qualified
	Rows      int64
	Estimated bool // Rows came from the planner
}

// ShortSummary is the summary without the agent: "UPDATE about 1 row in public.orders".
func ShortSummary(s SummaryFacts) string {
	if s.SQLKind == "" {
		return "run " + s.Tool
	}
	verb := strings.ToUpper(s.SQLKind)
	if s.SQLKind == "select" {
		verb = "read"
	}
	where := ""
	if len(s.Tables) > 0 {
		prep := " in "
		if s.SQLKind == "select" {
			prep = " from "
		}
		where = prep + strings.Join(s.Tables, ", ")
	}
	if s.Estimated {
		return fmt.Sprintf("%s about %s%s", verb, plural64(s.Rows, "row", "rows"), where)
	}
	return verb + " rows" + where
}

// Clock is "10:41 IST" in loc (UTC when nil).
func Clock(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "an unknown time"
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("15:04 MST")
}

// In is "in 27 min", "in 1 h 52 min", "in 30 s", or "now" from now to t.
func In(t, now time.Time) string {
	d := t.Sub(now)
	switch {
	case d <= 0:
		return "now"
	case d < time.Minute:
		return "in " + strconv.Itoa(int(d.Seconds())) + " s"
	case d < time.Hour:
		return "in " + strconv.Itoa(int(d.Minutes())) + " min"
	default:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return "in " + strconv.Itoa(h) + " h"
		}
		return "in " + strconv.Itoa(h) + " h " + strconv.Itoa(m) + " min"
	}
}

func plural(n int, one, many string) string { return plural64(int64(n), one, many) }

func plural64(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.FormatInt(n, 10) + " " + many
}

// cleanText drops control and bidi characters (reasons, names): nothing a person types can change
// how a line renders or split a log line.
func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || isBidi(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
}

// isBidi reports the Unicode directional formatting characters (LRE…RLO, LRI…PDI, LRM, RLM, ALM).
func isBidi(r rune) bool {
	return r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069 || r == 0x200E || r == 0x200F || r == 0x061C
}

// hasForbidden reports control or bidi characters (a reason with them is refused, not cleaned:
// the person must see exactly what the agent will see).
func hasForbidden(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || isBidi(r) || r == utf8.RuneError {
			return true
		}
	}
	return false
}
