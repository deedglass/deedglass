package notify

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/mailer"
)

// MailQueue is S07's mail queue (*mailer.Queue): jobs run off the request path, outside any
// transaction, and a full queue drops (and logs) rather than blocks.
type MailQueue interface {
	Submit(kind, userID, requestID string, j mailer.Job) bool
}

// Email sends each approver their own approval link (S22 §5.2). The idempotency key names the
// approval and the token, so a retried send is never a second email, and a new link is a new one.
type Email struct {
	Queue MailQueue
	Now   func() time.Time // nil: time.Now
	// SkipReserved leaves out addresses at reserved names (ReservedAddress): set when a real
	// provider sends (Resend), so demo and test members never cost a bounce. The dev mail log
	// prints them all.
	SkipReserved bool
}

var _ Notifier = (*Email)(nil)

// Name implements Notifier.
func (e *Email) Name() string { return "email" }

// ApprovalRequested queues one email per recipient. A message that can't be rendered is skipped
// (its error names no address, link or code).
func (e *Email) ApprovalRequested(_ context.Context, n ApprovalNotice) error {
	if e.Queue == nil {
		return errors.New("notify: the email notifier has no mail queue")
	}
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	loc, err := time.LoadLocation(n.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	var cs []mailer.ApprovalCountersign
	for _, c := range n.CountersignedBy {
		cs = append(cs, mailer.ApprovalCountersign{Name: c.Name, At: c.At})
	}
	var first error
	for _, r := range n.Recipients {
		if e.SkipReserved && ReservedAddress(r.Email) {
			continue // RFC 2606 / 6761 names never receive mail (demo and test members)
		}
		msg, err := mailer.ApprovalMessage(mailer.ApprovalData{AgentName: n.AgentName, OrgName: n.OrgName, Tool: n.Tool, RuleID: n.RuleID,
			Reason: n.Reason, Summary: n.Summary, Estimated: n.Estimated, Detail: n.Detail, Required: n.Required, CountersignedBy: cs,
			ExpiresAt: n.ExpiresAt, Location: loc, ApprovalID: n.ApprovalID, Email: r.Email, Link: r.Link, ReviewOnly: n.ReviewOnly,
			Self: r.Self}, now())
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		msg.IdempotencyKey = "approval/" + n.ApprovalID + "/" + r.LinkID
		msg.UserID, msg.RequestID = r.UserID.String(), n.RequestID
		e.Queue.Submit("approval", msg.UserID, msg.RequestID, func(ctx context.Context, m mailer.Mailer) error {
			return m.Send(ctx, msg)
		})
	}
	return first
}

// ApprovalResolved implements Notifier: email sends nothing when an approval ends.
func (e *Email) ApprovalResolved(context.Context, ApprovalNotice) error { return nil }

// ReservedAddress reports an address at a name reserved for documentation and testing (RFC 2606,
// RFC 6761): example.com/.net/.org and the .test, .example, .invalid and .localhost domains. The
// demo's own members live there; sending to them would only bounce and hurt the sender's standing.
func ReservedAddress(email string) bool {
	i := strings.LastIndexByte(email, '@')
	if i < 0 {
		return true
	}
	d := strings.TrimSuffix(strings.ToLower(email[i+1:]), ".")
	switch d {
	case "example.com", "example.net", "example.org", "localhost":
		return true
	}
	for _, tld := range []string{".test", ".example", ".invalid", ".localhost"} {
		if strings.HasSuffix(d, tld) {
			return true
		}
	}
	return strings.HasSuffix(d, ".example.com") || strings.HasSuffix(d, ".example.net") || strings.HasSuffix(d, ".example.org")
}
