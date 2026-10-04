// Package slack is approvals in Slack (S29): the approval card (a pure function from ApprovalView
// to Block Kit JSON), the notifier that posts it and edits it in place until the deed is sealed,
// and POST /v1/slack/interact, where a signed Approve or Deny click becomes S22's Decide. Every
// Slack API call that needs the bot token lives in internal/connector/slack.
package slack

import (
	"encoding/json"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
)

// State is what the card shows.
type State string

const (
	StatePending   State = "pending"   // waiting: the Approve and Deny buttons
	StatePartial   State = "partial"   // four-eyes: some countersigned, more needed (buttons stay)
	StateApproved  State = "approved"  // approved, running or witnessing
	StateSealed    State = "sealed"    // approved, ran, and the action deed is sealed (Verdict set)
	StateDenied    State = "denied"    // a person denied it (DenyReason set)
	StateExpired   State = "expired"   // nobody decided in time; nothing ran
	StateCancelled State = "cancelled" // the agent (or the Gate) cancelled it; nothing ran
)

// Rank orders states so a card update never goes back: pending < partial < approved < every
// final state. notification_messages.version stores the rank of the last state shown.
func (s State) Rank() int {
	switch s {
	case StatePending:
		return 1
	case StatePartial:
		return 2
	case StateApproved:
		return 3
	case StateSealed, StateDenied, StateExpired, StateCancelled:
		return 4
	}
	return 0
}

// Countersign is one person's approval so far.
type Countersign struct {
	Name string
	At   time.Time
}

// ApprovalView is everything the card shows, as plain data. Nothing in it is trusted to be safe
// for Slack: the card escapes every string. Args are already redacted by the caller (sensitive
// arguments digested).
type ApprovalView struct {
	ApprovalID    string // apr_…
	ActionID      string // act_…
	ContextDigest string // "sha256:…": the snapshot the card was built from; every button carries it
	Tool          string // "github.create_pr"
	Agent         string // the agent's name
	Summary       connector.Summary
	RuleID        string // the policy rule that asked for approval
	PolicyName    string // "acme-demo"
	PolicyVersion int    // 7
	Reason        string // the rule's reason, written by the org
	OnBehalfOf    string // "" when the action has none
	ExpiresAt     time.Time
	Required      int // approvals needed
	Approved      int // approvals so far
	RequestDigest string
	Args          json.RawMessage // the agent's arguments (fenced, labelled, capped)
	ConsoleURL    string          // ${DEEDGLASS_BASE_URL}/app/approvals/apr_…

	State         State
	Countersigned []Countersign // approvals so far, oldest first
	DecidedBy     string        // approved/denied: the person who made it final
	DecidedAt     time.Time
	DenyReason    string

	// Sealed only: the action deed and its verdict.
	DeedID   string // deed_…
	Level    string // L0 | L1 | L2
	Verdict  string // VERIFIED | MISMATCH | UNVERIFIABLE
	Sentence string // the witness's one line for a non-VERIFIED verdict ("GitHub shows a different title.")
	Failed   bool   // the tool failed: nothing was applied (sealed without a verdict)
}
