package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// Approvals in Slack (S29, migration 0026): routing a Slack request to its org, the replay table,
// the approval cards posted, and the reads the card is built from.

// SlackRoute is deedglass.slack_route: the org and connector a Slack app (team id, app id) routes
// to, or ok false. It runs before the org is known, so it only picks whose signing secret the
// caller checks; the signature decides.
func (p PreAuth) SlackRoute(ctx context.Context, team, app string) (org ids.OrgID, connectorID string, ok bool, err error) {
	var o, c string
	err = p.db.pool.QueryRow(ctx, `SELECT org_id, connector_id FROM deedglass.slack_route($1, $2)`, team, app).Scan(&o, &c)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.OrgID{}, "", false, nil
	}
	if err != nil {
		return ids.OrgID{}, "", false, err
	}
	org, err = ids.Parse[ids.OrgKind](o)
	if err != nil {
		return ids.OrgID{}, "", false, err
	}
	return org, c, true, nil
}

// PruneInboundReplay deletes up to limit expired replay digests (deedglass.maint_prune_inbound_replay)
// and returns how many it removed.
func (p PreAuth) PruneInboundReplay(ctx context.Context, limit int) (int64, error) {
	var n int64
	err := p.db.pool.QueryRow(ctx, `SELECT deedglass.maint_prune_inbound_replay($1)`, limit).Scan(&n)
	return n, err
}

// RecordInbound inserts an inbound request's digest; fresh is false when it was already there (a
// replay).
func RecordInbound(ctx context.Context, tx pgx.Tx, org ids.OrgID, source, digest string, expires time.Time) (fresh bool, err error) {
	tag, err := tx.Exec(ctx, `INSERT INTO deedglass.inbound_replay (org_id, source, digest, expires_at) VALUES ($1, $2, $3, $4)
	    ON CONFLICT (org_id, source, digest) DO NOTHING`, org, source, digest, expires)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// NotificationMessage is one posted approval card (notification_messages).
type NotificationMessage struct {
	ID          int64
	Org         ids.OrgID
	ApprovalID  string
	ActionID    string
	ConnectorID string
	ChannelID   string
	TS          string
	State       string // posted | updated | delivery_failed
	Version     int
	LastError   *string
}

// InsertNotificationMessage records a posted card. A second card for the same approval and channel
// is not recorded (inserted false).
func InsertNotificationMessage(ctx context.Context, tx pgx.Tx, m NotificationMessage) (inserted bool, err error) {
	tag, err := tx.Exec(ctx, `INSERT INTO deedglass.notification_messages (org_id, subject_type, subject_id, action_id, channel_kind,
	    connector_id, channel_id, message_ts, state, version) VALUES ($1, 'approval', $2, $3, 'slack', $4, $5, $6, 'posted', $7)
	    ON CONFLICT (org_id, subject_id, channel_kind, channel_id) DO NOTHING`,
		m.Org, m.ApprovalID, m.ActionID, m.ConnectorID, m.ChannelID, m.TS, max(m.Version, 1))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// NotificationMessagesFor lists the cards posted for an approval.
func NotificationMessagesFor(ctx context.Context, tx pgx.Tx, org ids.OrgID, approvalID string) ([]NotificationMessage, error) {
	rows, err := tx.Query(ctx, `SELECT id, org_id, subject_id, action_id, connector_id, channel_id, message_ts, state, version, last_error
	    FROM deedglass.notification_messages WHERE org_id = $1 AND subject_type = 'approval' AND subject_id = $2 ORDER BY id`, org, approvalID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (NotificationMessage, error) {
		var m NotificationMessage
		err := r.Scan(&m.ID, &m.Org, &m.ApprovalID, &m.ActionID, &m.ConnectorID, &m.ChannelID, &m.TS, &m.State, &m.Version, &m.LastError)
		return m, err
	})
}

// HasNotificationMessage reports whether any card was posted for an action.
func HasNotificationMessage(ctx context.Context, tx pgx.Tx, org ids.OrgID, action string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deedglass.notification_messages WHERE org_id = $1 AND action_id = $2)`, org, action).Scan(&ok)
	return ok, err
}

// MarkNotificationShown records that a card now shows a state of rank version. It never lowers the
// version: a stale update changes nothing (updated false).
func MarkNotificationShown(ctx context.Context, tx pgx.Tx, org ids.OrgID, id int64, version int) (updated bool, err error) {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.notification_messages SET version = $3, state = 'updated', last_error = NULL
	    WHERE org_id = $1 AND id = $2 AND version <= $3`, org, id, version)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MarkNotificationFailed records that a card couldn't be updated (the Console shows it).
func MarkNotificationFailed(ctx context.Context, tx pgx.Tx, org ids.OrgID, id int64, lastError string) error {
	if len(lastError) > 500 {
		lastError = lastError[:500]
	}
	_, err := tx.Exec(ctx, `UPDATE deedglass.notification_messages SET state = 'delivery_failed', last_error = $3
	    WHERE org_id = $1 AND id = $2`, org, id, lastError)
	return err
}

// ApprovalCardRow is what an approval card is built from: the approval (its context snapshot is
// what the person countersigns), its votes, and its action's progress to the seal.
type ApprovalCardRow struct {
	ApprovalID     string
	ActionID       string
	Status         string // pending | approved | denied | expired | cancelled
	Context        json.RawMessage
	ContextDigest  string
	Required       int
	ExpiresAt      time.Time
	DecidedAt      *time.Time
	DecisionReason *string
	DecidedBy      *string // a user id, or a system actor
	Votes          []CardVote

	Tool            string
	AgentName       string
	ActionState     string
	ExecutionStatus *string
	WitnessLevel    *string
	WitnessVerdict  *string
	WitnessDetail   json.RawMessage
	DeedID          *string
	PolicyRequest   json.RawMessage // policy_input.request: the enrich facts (request.github, request.slack)
	Notify          []string        // decision_detail.notify
}

// CardVote is one person's vote.
type CardVote struct {
	UserID string
	Name   string
	Vote   string
	At     time.Time
}

// ApprovalCard reads an approval for its card. ErrNotFound when org has no such approval.
func ApprovalCard(ctx context.Context, tx pgx.Tx, org ids.OrgID, approvalID string) (*ApprovalCardRow, error) {
	var (
		r      ApprovalCardRow
		detail []byte
	)
	err := tx.QueryRow(ctx, `SELECT ap.id, ap.action_id, ap.status, ap.context, COALESCE(ap.context_digest, ''), ap.approvers_required,
	    ap.expires_at, ap.decided_at, ap.decision_reason, ap.decided_by,
	    COALESCE(ap.tool, a.tool), g.name, a.state, a.execution_status, a.witness_level, a.witness_verdict, a.witness_detail, a.deed_id,
	    a.policy_input -> 'request', a.decision_detail
	    FROM deedglass.approvals AS ap
	    JOIN deedglass.actions AS a ON a.org_id = ap.org_id AND a.id = ap.action_id
	    JOIN deedglass.agents AS g ON g.org_id = a.org_id AND g.id = a.agent_id
	    WHERE ap.org_id = $1 AND ap.id = $2`, org, approvalID).Scan(&r.ApprovalID, &r.ActionID, &r.Status, &r.Context, &r.ContextDigest,
		&r.Required, &r.ExpiresAt, &r.DecidedAt, &r.DecisionReason, &r.DecidedBy, &r.Tool, &r.AgentName, &r.ActionState, &r.ExecutionStatus,
		&r.WitnessLevel, &r.WitnessVerdict, &r.WitnessDetail, &r.DeedID, &r.PolicyRequest, &detail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var d struct {
		Notify []string `json:"notify"`
	}
	_ = json.Unmarshal(detail, &d)
	r.Notify = d.Notify
	rows, err := tx.Query(ctx, `SELECT v.user_id, COALESCE(NULLIF(u.display_name, ''), pg_catalog.split_part(u.email, '@', 1)), v.vote, v.created_at
	    FROM deedglass.approval_votes AS v JOIN deedglass.users AS u ON u.id = v.user_id
	    WHERE v.org_id = $1 AND v.approval_id = $2 ORDER BY v.created_at, v.user_id`, org, approvalID)
	if err != nil {
		return nil, err
	}
	r.Votes, err = pgx.CollectRows(rows, func(x pgx.CollectableRow) (CardVote, error) {
		var v CardVote
		err := x.Scan(&v.UserID, &v.Name, &v.Vote, &v.At)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ApprovalOfAction is the approval that paused an action, or ErrNotFound.
func ApprovalOfAction(ctx context.Context, tx pgx.Tx, org ids.OrgID, action string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM deedglass.approvals WHERE org_id = $1 AND action_id = $2`, org, action).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// MemberByEmail is the active member of org with this email (compared lowercase), or ErrNotFound.
// Slack approvals map a Slack user to a person this way (S29).
func MemberByEmail(ctx context.Context, tx pgx.Tx, org ids.OrgID, email string) (ids.UserID, error) {
	var id ids.UserID
	err := tx.QueryRow(ctx, `SELECT u.id FROM deedglass.memberships AS m JOIN deedglass.users AS u ON u.id = m.user_id
	    WHERE m.org_id = $1 AND m.status = 'active' AND u.status <> 'disabled' AND pg_catalog.lower(u.email) = $2
	    ORDER BY u.id LIMIT 1`, org, strings.ToLower(strings.TrimSpace(email))).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.UserID{}, ErrNotFound
	}
	return id, err
}

// SlackApprovalsConnector is the org's live Slack connector with approvals enabled (at most one,
// migration 0026), or ErrNotFound.
func SlackApprovalsConnector(ctx context.Context, tx pgx.Tx, org ids.OrgID) (Connector, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM deedglass.connectors WHERE org_id = $1 AND type = 'slack' AND deleted_at IS NULL
	    AND status = 'active' AND (config -> 'approvals' ->> 'enabled') = 'true' LIMIT 1`, org).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connector{}, ErrNotFound
	}
	if err != nil {
		return Connector{}, err
	}
	return GetConnector(ctx, tx, id, false)
}
