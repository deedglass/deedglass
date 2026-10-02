package gate

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
)

// DefaultApprovalTTL is how long an approval waits when the rule doesn't say (S13 §4 step 9).
const DefaultApprovalTTL = 30 * time.Minute

// StubApprovals is S13's Approvals, kept as the dependency-free default (gate tests, and a
// pipeline built without internal/approve): it records a pending approval from the rule's
// approval spec and nothing else, with no context snapshot. serve uses approve.Service (S22),
// which adds the snapshot, eligibility, links, notifications and four-eyes. A person's decision
// with the stub is a direct row change (tests).
type StubApprovals struct {
	TTL time.Duration    // when the rule sets no expires_in; DefaultApprovalTTL when zero
	Now func() time.Time // nil: time.Now
}

var _ Approvals = StubApprovals{}

// Prepare implements Approvals: the stub keeps no snapshot.
func (s StubApprovals) Prepare(_ context.Context, _ ApprovalInput, d policy.Decision) (PreparedApproval, error) {
	return PreparedApproval{RuleID: d.RuleID}, nil
}

// Request implements Approvals: one pending approvals row, expiring per the rule.
func (s StubApprovals) Request(ctx context.Context, tx pgx.Tx, in ApprovalInput, d policy.Decision, _ PreparedApproval) (ids.ApprovalID, time.Time, error) {
	sub := in.Subject
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = DefaultApprovalTTL
	}
	spec := json.RawMessage(`{}`)
	required := 1
	if a := d.Approval; a != nil {
		if a.ExpiresIn > 0 {
			ttl = time.Duration(a.ExpiresIn) * time.Second
		}
		if a.Required > 0 {
			required = a.Required
		}
		if b, err := json.Marshal(a); err == nil {
			spec = b
		}
	}
	id := ids.New[ids.ApprovalKind]()
	exp := now().Add(ttl).UTC().Truncate(time.Millisecond)
	var rule, reason *string
	if d.RuleID != "" {
		r := d.RuleID
		rule = &r
	}
	if d.Reason != "" {
		r := d.Reason
		reason = &r
	}
	err := store.InsertApproval(ctx, tx, sub.Org, store.NewApproval{ID: id, Action: sub.ID, RuleID: rule, Reason: reason,
		Required: required, Spec: spec, ExpiresAt: exp})
	return id, exp, err
}
