package slack

import (
	"fmt"
	"time"
)

// The ephemeral messages a person sees in Slack when Deedglass doesn't record their click (S29
// §5). One catalog; times are Slack date tokens, so each viewer sees their own time zone.
const (
	MsgUnmapped = "Deedglass couldn't match your Slack account to a person in this org. Ask an admin to invite you in Console › Settings › Members with the email you use in Slack, then try again."
	MsgSelf     = "You asked for this action, so someone else approves it."
	MsgRepeat   = "You already approved. One more person needs to."
	MsgSlow     = "Deedglass couldn't check who you are in time. Try again in a moment."
	MsgChanged  = "This request changed after the card was posted. Open it in the Console to see what is asked now."
	MsgFailed   = "Deedglass couldn't record that. Try again, or decide in the Console."
	MsgGone     = "Deedglass can't find this request any more. Open the Console to see the current approvals."
	// MsgReasonEmpty is the deny modal's field error.
	MsgReasonEmpty = "Add a short reason. It is recorded with the decision."
	// MsgReasonLong is the deny modal's field error for a reason over 280 characters.
	MsgReasonLong = "Keep the reason to 280 characters. It is recorded with the decision."
)

// MsgNotEligible is "You can see this request, but policy "<rule>" needs <who> to approve it.".
func MsgNotEligible(rule, who string) string {
	if who == "" {
		who = "someone else"
	}
	return fmt.Sprintf("You can see this request, but policy %q needs %s to approve it.", rule, who)
}

// MsgDecided is "Arjun already approved this at 10:14. Nothing else to do." (or denied).
func MsgDecided(name, verb string, at time.Time) string {
	if name == "" {
		name = "Someone"
	}
	return fmt.Sprintf("%s already %s this at %s. Nothing else to do.", Escape(name), verb, slackTime(at))
}

// MsgExpired is "This request expired at 11:14. Nothing ran.".
func MsgExpired(at time.Time) string {
	return fmt.Sprintf("This request expired at %s. Nothing ran.", slackTime(at))
}

// slackTime is a Slack date token showing the time in the viewer's zone, with a UTC fallback.
func slackTime(t time.Time) string {
	if t.IsZero() {
		return "an earlier time"
	}
	return fmt.Sprintf("<!date^%d^{time}|%s>", t.Unix(), t.UTC().Format("15:04 UTC"))
}

// approverWords names who may approve, from a rule's approvers list ("role:admin" → "an admin").
func approverWords(approvers []string) string {
	if len(approvers) != 1 {
		return "a listed approver"
	}
	switch approvers[0] {
	case "role:admin":
		return "an admin"
	case "role:owner":
		return "an owner"
	case "role:viewer", "role:member":
		return "a member of this org"
	}
	return "a listed approver"
}
