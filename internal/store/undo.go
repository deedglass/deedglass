package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// S23: what an undo reads about the action it undoes, the one-live-undo slot, and the stored plan.
// Every statement is a constant and runs in an org transaction (RLS on).

// UndoSubject is the action an undo would put back.
type UndoSubject struct {
	ID              string
	State           string
	Mode            string
	Tool            string
	Connector       *string // the slug before the tool's first dot
	ConnectorID     *string
	WitnessLevel    *string
	WitnessVerdict  *string
	ExecutionStatus *string
	SealedAt        *time.Time
	PayloadPrunedAt *time.Time
	UndoesActionID  *string
}

// GetUndoSubject reads the action id of org. ErrNotFound when it isn't an action of org.
func GetUndoSubject(ctx context.Context, tx pgx.Tx, org ids.OrgID, id string) (UndoSubject, error) {
	var s UndoSubject
	err := tx.QueryRow(ctx, `SELECT id, state, mode, tool, connector, connector_id, witness_level, witness_verdict, execution_status,
	    sealed_at, payload_pruned_at, undoes_action_id
	  FROM deedglass.actions WHERE org_id = $1 AND id = $2`, org, id).
		Scan(&s.ID, &s.State, &s.Mode, &s.Tool, &s.Connector, &s.ConnectorID, &s.WitnessLevel, &s.WitnessVerdict, &s.ExecutionStatus,
			&s.SealedAt, &s.PayloadPrunedAt, &s.UndoesActionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

// LiveUndo is the undo that holds an action's one-live-undo slot.
type LiveUndo struct {
	ID              string
	State           string
	ExecutionStatus *string
	SealedAt        *time.Time
}

// GetLiveUndo returns the undo of action id that holds its slot (undo_released_at is null), or
// ErrNotFound.
func GetLiveUndo(ctx context.Context, tx pgx.Tx, org ids.OrgID, id string) (LiveUndo, error) {
	var u LiveUndo
	err := tx.QueryRow(ctx, `SELECT id, state, execution_status, sealed_at FROM deedglass.actions
	  WHERE org_id = $1 AND undoes_action_id = $2 AND undo_released_at IS NULL ORDER BY created_at LIMIT 1`, org, id).
		Scan(&u.ID, &u.State, &u.ExecutionStatus, &u.SealedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// HasCredential reports whether a connector has stored credentials.
func HasCredential(ctx context.Context, tx pgx.Tx, connectorID string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deedglass.credentials WHERE connector_id = $1)`, connectorID).Scan(&ok)
	return ok, err
}

// UndoPlanRow is one undo_plans row.
type UndoPlanRow struct {
	ActionID       string
	UndoesActionID string
	Plan           json.RawMessage // nil once pruned
	PlanDigest     string
	RowCount       int
	Tables         []string
	CreatedAt      time.Time
}

// InsertUndoPlan stores the plan an undo runs (written once, by the executor, before it runs).
func InsertUndoPlan(ctx context.Context, tx pgx.Tx, org ids.OrgID, r UndoPlanRow) error {
	_, err := tx.Exec(ctx, `INSERT INTO deedglass.undo_plans (org_id, action_id, undoes_action_id, plan, plan_digest, row_count, tables)
	  VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7)`, org, r.ActionID, r.UndoesActionID, string(r.Plan), r.PlanDigest, r.RowCount, r.Tables)
	return err
}

// GetUndoPlan reads the plan an undo action ran. ErrNotFound when it has none.
func GetUndoPlan(ctx context.Context, tx pgx.Tx, org ids.OrgID, action string) (UndoPlanRow, error) {
	var r UndoPlanRow
	var plan []byte
	err := tx.QueryRow(ctx, `SELECT action_id, undoes_action_id, plan, plan_digest, row_count, tables, created_at
	  FROM deedglass.undo_plans WHERE org_id = $1 AND action_id = $2`, org, action).
		Scan(&r.ActionID, &r.UndoesActionID, &plan, &r.PlanDigest, &r.RowCount, &r.Tables, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	r.Plan = plan
	return r, err
}
