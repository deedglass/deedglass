package slack

import (
	"context"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// Store is what approvals in Slack read and write. slackstore.DB is the Postgres one (every call
// is one short org transaction, or the one cross-org routing function); unit tests use a map.
type Store interface {
	// SlackRoute is the routing lookup (store.PreAuth): which org and connector a Slack app routes to.
	SlackRoute(ctx context.Context, team, app string) (org ids.OrgID, connectorID string, ok bool, err error)
	// Connector reads one of org's connectors.
	Connector(ctx context.Context, org ids.OrgID, id string) (store.Connector, error)
	// RecordInbound records a verified signature's digest; fresh is false for a replay.
	RecordInbound(ctx context.Context, org ids.OrgID, digest string, expires time.Time) (fresh bool, err error)
	// MemberByEmail is the active member with this email, or store.ErrNotFound.
	MemberByEmail(ctx context.Context, org ids.OrgID, email string) (ids.UserID, error)
	// ApprovalCard reads an approval for its card, or store.ErrNotFound.
	ApprovalCard(ctx context.Context, org ids.OrgID, approvalID string) (*store.ApprovalCardRow, error)
	// CardState reads the approval, its posted cards, and the connector to post or edit through
	// (has false when the org has no Slack connector with approvals on and no card was posted).
	CardState(ctx context.Context, org ids.OrgID, approvalID string) (CardState, error)
	// InsertMessage records a posted card.
	InsertMessage(ctx context.Context, m store.NotificationMessage) error
	// MarkShown records that a card shows a state of rank version (never lowering it).
	MarkShown(ctx context.Context, org ids.OrgID, id int64, version int) error
	// MarkFailed marks an approval's cards delivery_failed.
	MarkFailed(ctx context.Context, org ids.OrgID, approvalID, lastError string) error
	// ApprovalOfAction is the approval that paused an action, or store.ErrNotFound.
	ApprovalOfAction(ctx context.Context, org ids.OrgID, action string) (string, error)
	// Enqueue adds a notify_update job for the action (once per action).
	Enqueue(ctx context.Context, org ids.OrgID, action string, runAfter time.Time, maxAttempts int) error
	// EnqueueIfCard is Enqueue only when the action's approval has a card.
	EnqueueIfCard(ctx context.Context, org ids.OrgID, action string, runAfter time.Time, maxAttempts int) error
}

// CardState is one read of an approval and its cards.
type CardState struct {
	Row  *store.ApprovalCardRow
	Conn store.Connector
	Msgs []store.NotificationMessage
	Has  bool
}
