package notify

import (
	"context"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// Recipient is one person a notice goes to. Email and Name are for the message only: they are
// never logged, never in a webhook and never in a deed.
type Recipient struct {
	UserID ids.UserID
	Email  string
	Name   string
	// Link is the person's own one-time approval link ({base}/approve#t=…), email only. LinkID is
	// the token's internal id, for the mail provider's idempotency key; it is never shown.
	Link   string
	LinkID string
	// Self: the action runs on this person's behalf, so they may deny but not countersign.
	Self bool
}

// Countersign is one earlier countersign of a four-eyes approval, for the "Priya Raman
// countersigned at 10:14 IST" line.
type Countersign struct {
	Name string
	At   time.Time
}

// ApprovalNotice is what every notifier needs to tell people about one approval (S22 §3.4).
type ApprovalNotice struct {
	OrgID      ids.OrgID
	OrgName    string
	ApprovalID string
	ActionID   string
	AgentName  string
	Tool       string
	RuleID     string
	Reason     string // the policy author's reason for the rule
	// Summary is the plain sentence without the agent ("UPDATE about 1 row in public.orders");
	// Estimated says it came from the planner, so the email adds "(planner estimate)".
	Summary   string
	Estimated bool
	// Detail is up to 300 characters of the SQL, only when the org set approvals.email_detail:
	// full; "" in the default summary mode, so no argument values leave Deedglass by email.
	Detail          string
	TimeZone        string // the org's IANA zone, for "expires 10:41 IST"
	Required        int
	Countersigned   int
	CountersignedBy []Countersign
	ExpiresAt       time.Time
	Status          string // pending | approved | denied | expired | cancelled
	ReviewOnly      bool   // approvals.email_links: review (the link opens the page; decisions happen in the Console)
	Recipients      []Recipient
	RequestID       string
}

// Notifier tells people (or a system) about approvals. Email sends the link (S22); Slack posts and
// edits a message (S29). Webhooks don't go through here: every event reaches them through the
// outbox in the same transaction as the change (internal/notify/webhook).
type Notifier interface {
	Name() string // email | slack
	ApprovalRequested(ctx context.Context, n ApprovalNotice) error
	// ApprovalResolved: the approval reached a final status. Slack edits its message (S29); email
	// sends nothing.
	ApprovalResolved(ctx context.Context, n ApprovalNotice) error
}
