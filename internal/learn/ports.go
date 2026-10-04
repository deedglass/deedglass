package learn

import (
	"context"
	"encoding/json"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// Window is a half-open range of decision time, [From, To). Insights buckets everything by
// decision time (actions.evaluated_at = policy_input.time, S27 §3.2), never by seal time.
type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Cursor is a keyset position in (evaluated_at, id) order.
type Cursor struct {
	At time.Time
	ID string
}

// RecordedDecision is the decision the Gate actually made, as the action row records it.
type RecordedDecision struct {
	Decision        string // allow | deny | require_approval
	Code            string // decision_code (policy_denied, budget_exceeded, tool_unknown, …)
	Source          string // decision_source: rule, default, budget, stop, builtin
	RuleID          string // decision_rule_id
	PolicyVersionID string // the enforced version that decided ("" for a builtin)
}

// ApprovalOutcome is what people did with a require_approval action.
type ApprovalOutcome string

// Approval outcomes (S22's statuses).
const (
	ApprovalApproved  ApprovalOutcome = "approved"
	ApprovalDenied    ApprovalOutcome = "denied"
	ApprovalExpired   ApprovalOutcome = "expired"
	ApprovalCancelled ApprovalOutcome = "cancelled"
	ApprovalPending   ApprovalOutcome = "pending"
)

// InputRow is one recorded action as replay reads it.
type InputRow struct {
	ActionID  string
	DecidedAt time.Time       // evaluated_at
	Tool      string          //
	Input     json.RawMessage // policy_input; nil when it was never stored or retention pruned it
	Pruned    bool            // the payload was pruned (payload_pruned_at is set)
	Recorded  RecordedDecision
	Approval  ApprovalOutcome // the joined approval's status; "" when the action had none
	SQLGuard  string          // the sqlguard version recorded with the decision ("" when no SQL was classified)
}

// InputSource reads recorded decisions of one org, in (evaluated_at, id) order. Implementations
// run each call in a short read-only org transaction under row-level security
// (store/learnstore.Inputs).
type InputSource interface {
	Count(ctx context.Context, org ids.OrgID, w Window) (int64, error)
	// Scan returns up to limit rows strictly after `after` (the zero Cursor starts at w.From), and
	// the cursor of the last row. Fewer than limit rows means the window is done.
	Scan(ctx context.Context, org ids.OrgID, w Window, after Cursor, limit int) ([]InputRow, Cursor, error)
}

// Actor is who asked: a person, an org admin key, or the system (S28's suggestions).
type Actor struct {
	Type string `json:"type"` // user | api_key | system
	ID   string `json:"id"`   // usr_…, key_…, or "deedglass"
}
