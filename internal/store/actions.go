package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// The Gate's reads and the few writes that aren't transitions (S13). Every statement is a
// constant; transitions, events and deeds go through internal/ledger (the one write path). All of
// these run inside the caller's org transaction, so row-level security scopes them.

// KeyedAction is the action an Idempotency-Key already names, for one agent.
type KeyedAction struct {
	ID          ids.ActionID
	Fingerprint string
	State       string
	CreatedAt   time.Time
}

// FindActionByKey returns the agent's action holding key, or ErrNotFound.
func FindActionByKey(ctx context.Context, tx pgx.Tx, org ids.OrgID, agent ids.AgentID, key string) (*KeyedAction, error) {
	var k KeyedAction
	err := tx.QueryRow(ctx, `SELECT id, request_fingerprint, state, created_at FROM deedglass.actions
	    WHERE org_id = $1 AND agent_id = $2 AND idempotency_key = $3`, org, agent, key).
		Scan(&k.ID, &k.Fingerprint, &k.State, &k.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &k, nil
}

// ReleaseActionKey ends one action's hold on its Idempotency-Key (its 24-hour window has closed),
// so a new request may use the key. It reports whether the key was still held.
func ReleaseActionKey(ctx context.Context, tx pgx.Tx, id ids.ActionID, key string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.actions SET idempotency_key = NULL WHERE id = $1 AND idempotency_key = $2`, id, key)
	return tag.RowsAffected() == 1, err
}

// GateSubject is what one evaluation reads about the calling agent and its org, in one query.
type GateSubject struct {
	AgentName   string
	AgentStatus string
	Labels      json.RawMessage
	Budgets     json.RawMessage
	IntentMode  string
	OrgName     string
	Timezone    string
	OrgStopped  bool
	AgentKind   string // agent | system (S23: the console agent; Stop all skips it)
	Lockdown    bool   // orgs.settings.lockdown (ADR-117)
	PHIMode     bool   // orgs.settings.phi_mode (HC-P09)
}

// LoadGateSubject reads the agent and its org. ErrNotFound when the agent isn't in this org.
func LoadGateSubject(ctx context.Context, tx pgx.Tx, org ids.OrgID, agent ids.AgentID) (GateSubject, error) {
	var s GateSubject
	var labels, budgets []byte
	err := tx.QueryRow(ctx, `SELECT g.name, g.status, g.labels, g.budgets, g.intent_mode, o.name, o.timezone, o.stopped_at IS NOT NULL, g.kind,
	      COALESCE((o.settings ->> 'lockdown') = 'true', false), COALESCE((o.settings ->> 'phi_mode') = 'true', false)
	    FROM deedglass.agents AS g JOIN deedglass.orgs AS o ON o.id = g.org_id
	    WHERE g.org_id = $1 AND g.id = $2`, org, agent).
		Scan(&s.AgentName, &s.AgentStatus, &labels, &budgets, &s.IntentMode, &s.OrgName, &s.Timezone, &s.OrgStopped, &s.AgentKind, &s.Lockdown, &s.PHIMode)
	if err != nil {
		return s, notFound(err)
	}
	s.Labels, s.Budgets = labels, budgets
	return s, nil
}

// AdminLockdownAcknowledged is ledger.AdminLockdownAcknowledged (store doesn't import ledger).
const AdminLockdownAcknowledged = "lockdown.acknowledged"

// LockdownHeld returns the agent's earliest action admitted in lockdown that ended UNVERIFIABLE
// (Can't check) and that no person has acknowledged (ADR-117), or "" when there is none. The hold
// covers every session of the agent: the agent names its own session ref.
func LockdownHeld(ctx context.Context, tx pgx.Tx, org ids.OrgID, agent ids.AgentID) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT a.id FROM deedglass.actions AS a
	    WHERE a.org_id = $1 AND a.agent_id = $2 AND a.witness_verdict = 'UNVERIFIABLE'
	      AND (a.decision_detail -> 'lockdown') IS NOT NULL
	      AND NOT EXISTS (SELECT 1 FROM deedglass.admin_events AS e
	        WHERE e.org_id = a.org_id AND e.action = $3 AND e.target_type = 'action' AND e.target_id = a.id::text)
	    ORDER BY a.created_at, a.id LIMIT 1`, org, agent, AdminLockdownAcknowledged).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// GateAction is an action as the Gate resumes it: everything Resume, ReportResult and Cancel need.
type GateAction struct {
	ID                ids.ActionID
	OrgID             ids.OrgID
	AgentID           ids.AgentID
	APIKeyID          *string
	Mode              string
	Tool              string
	Connector         *string
	State             string
	Request           json.RawMessage // the arguments; nil after retention pruned them
	RequestDigest     string
	Fingerprint       string
	IdempotencyKey    *string
	OnBehalfOf        *string
	Session           *string
	Context           json.RawMessage
	Livemode          bool
	IntentHash        *string
	IntentSigned      bool
	Decision          *string
	DecisionDetail    json.RawMessage
	Reservations      json.RawMessage
	ApprovalID        *string
	ApprovalStatus    *string
	ApprovalExpiresAt *time.Time
	ExecutingAt       *time.Time
	ExecutingDeadline *time.Time
	ExecutionStatus   *string
	ErrorCode         *string
	DeedID            *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	// S19: the level the decision's verify: asks for, and the stored result (≤ gate.max_result_bytes;
	// nil when it wasn't kept or was pruned) for read-back templates that name result.body.
	WitnessRequired *string
	Result          json.RawMessage
	// S20 (ADR-044): the connector's execution evidence (Postgres: xid8, marker_lsn, sql_digest,
	// command_tag), kept on the action for the Witness; nil before execution or when none.
	ExecutionEvidence map[string]string
	// S21: an explicit footprint (S23's undo), nil when none.
	WitnessExpect json.RawMessage
	// S40: the window an approved action runs in ({"not_before","not_after"}); nil when none.
	Schedule json.RawMessage
}

const gateActionSQL = `SELECT a.id, a.org_id, a.agent_id, a.api_key_id, a.mode, a.tool, a.connector, a.state, a.request,
  a.request_digest, a.request_fingerprint, a.idempotency_key, a.on_behalf_of, a.session_ref, a.context, a.livemode,
  a.intent_hash, a.intent_signed, a.decision, a.decision_detail, a.reservations, a.approval_id, ap.status, ap.expires_at,
  a.executing_at, a.executing_deadline, a.execution_status, a.error_code, a.deed_id, a.created_at, a.updated_at,
  a.witness_required, a.result, a.execution_evidence, a.witness_expect, a.schedule
FROM deedglass.actions AS a
LEFT JOIN deedglass.approvals AS ap ON ap.org_id = a.org_id AND ap.id = a.approval_id
WHERE a.org_id = $1 AND a.id = $2`

// GetGateAction reads one action; forUpdate locks its row for the rest of tx. ErrNotFound when
// the id isn't an action of this org.
func GetGateAction(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.ActionID, forUpdate bool) (*GateAction, error) {
	q := gateActionSQL
	if forUpdate {
		q += ` FOR UPDATE OF a`
	}
	var a GateAction
	var req, cx, detail, res, result, evidence, expect, schedule []byte
	err := tx.QueryRow(ctx, q, org, id).Scan(&a.ID, &a.OrgID, &a.AgentID, &a.APIKeyID, &a.Mode, &a.Tool, &a.Connector, &a.State,
		&req, &a.RequestDigest, &a.Fingerprint, &a.IdempotencyKey, &a.OnBehalfOf, &a.Session, &cx, &a.Livemode,
		&a.IntentHash, &a.IntentSigned, &a.Decision, &detail, &res, &a.ApprovalID, &a.ApprovalStatus, &a.ApprovalExpiresAt,
		&a.ExecutingAt, &a.ExecutingDeadline, &a.ExecutionStatus, &a.ErrorCode, &a.DeedID, &a.CreatedAt, &a.UpdatedAt,
		&a.WitnessRequired, &result, &evidence, &expect, &schedule)
	a.WitnessExpect, a.Schedule = expect, schedule
	if err != nil {
		return nil, notFound(err)
	}
	if len(evidence) > 0 {
		if err := json.Unmarshal(evidence, &a.ExecutionEvidence); err != nil {
			return nil, fmt.Errorf("store: action %s: execution_evidence isn't a string map: %w", id, err)
		}
	}
	a.Request, a.Context, a.DecisionDetail, a.Reservations, a.Result = req, cx, detail, res, result
	return &a, nil
}

// ActionState is the state, agent and last change of one action: the long poll's cheap re-read.
func ActionState(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.ActionID) (state, agent string, updated time.Time, err error) {
	err = tx.QueryRow(ctx, `SELECT state, agent_id, updated_at FROM deedglass.actions WHERE org_id = $1 AND id = $2`, org, id).
		Scan(&state, &agent, &updated)
	return state, agent, updated, notFound(err)
}

// ErrNoRow: an UPDATE the caller expected to change exactly one row changed none.
var ErrNoRow = errors.New("store: no row changed")

// NewApproval is the pending approval S13's stub creates (S22 replaces the stub, not the table).
type NewApproval struct {
	ID        ids.ApprovalID
	Action    ids.ActionID
	RuleID    *string
	Reason    *string
	Required  int
	Spec      json.RawMessage // the rule's approval spec (approvers, required, expires_in)
	ExpiresAt time.Time
}

// InsertApproval inserts a pending approval for an action of org.
func InsertApproval(ctx context.Context, tx pgx.Tx, org ids.OrgID, a NewApproval) error {
	spec := a.Spec
	if spec == nil {
		spec = json.RawMessage(`{}`)
	}
	required := a.Required
	if required < 1 {
		required = 1
	}
	_, err := tx.Exec(ctx, `INSERT INTO deedglass.approvals (id, org_id, action_id, status, rule_id, reason, approvers_required,
	    approver_spec, expires_at) VALUES ($1, $2, $3, 'pending', $4, $5, $6, $7::jsonb, $8)`,
		a.ID, org, a.Action, a.RuleID, a.Reason, required, string(spec), a.ExpiresAt)
	return err
}

// DecideApproval moves a pending approval to status (approved, denied, expired or cancelled),
// recording who decided (usr_…, agt_…, or nil for the expiry sweeper) and, for approved, adding
// the person to approved_by. ErrNoRow when it wasn't pending.
//
// at is the decision time from the caller's clock (S27 §3.2: the Gate's pipeline clock), never SQL now().
func DecideApproval(ctx context.Context, tx pgx.Tx, org ids.OrgID, id string, status string, by *string, reason *string, at time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.approvals SET status = $3::text, decided_at = $6::timestamptz,
	    decided_by = $4::text, decision_reason = $5::text,
	    approved_by = CASE WHEN $3::text = 'approved' AND $4::text LIKE 'usr\_%' THEN pg_catalog.array_append(approved_by, $4::text) ELSE approved_by END
	    WHERE org_id = $1 AND id = $2 AND status = 'pending'`, org, id, status, by, reason, at.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNoRow
	}
	return nil
}

// ActionByApproval returns the id and agent of the action an approval belongs to (S15: the MCP
// gateway's deedglass.check_approval is sessionless, so it finds the action from the approval).
func ActionByApproval(ctx context.Context, tx pgx.Tx, org ids.OrgID, approval string) (action, agent string, err error) {
	err = tx.QueryRow(ctx, `SELECT a.id, a.agent_id FROM deedglass.actions AS a
	    WHERE a.org_id = $1 AND a.approval_id = $2::text LIMIT 1`, org, approval).Scan(&action, &agent)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return action, agent, err
}

// ApprovalStatus is an approval's status now (S22: Cancel re-reads it after waiting for a lock).
func ApprovalStatus(ctx context.Context, tx pgx.Tx, org ids.OrgID, id string) (string, error) {
	var st string
	err := tx.QueryRow(ctx, `SELECT status FROM deedglass.approvals WHERE org_id = $1 AND id = $2`, org, id).Scan(&st)
	return st, notFound(err)
}
