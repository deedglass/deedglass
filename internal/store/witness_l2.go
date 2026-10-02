package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// L2's storage (S21, migration 0020). Every statement is a constant and runs in an org
// transaction with row-level security on.

// ActionJob reads an action's job of one kind (without locking it). ErrNotFound when there is none.
func ActionJob(ctx context.Context, tx pgx.Tx, org ids.OrgID, action ids.ActionID, kind string) (JobRow, error) {
	var j JobRow
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT id, attempts, max_attempts, locked_by, payload, last_error FROM deedglass.witness_jobs
	    WHERE org_id = $1 AND action_id = $2 AND kind = $3`, org, action, kind).Scan(&j.ID, &j.Attempts, &j.MaxAttempts, &j.LockedBy, &payload, &j.LastError)
	j.Payload = payload
	return j, notFound(err)
}

// DeferJob moves a job's run_after without counting an attempt and ends its lease (an L2 wait
// whose read-back is still open).
func DeferJob(ctx context.Context, tx pgx.Tx, org ids.OrgID, id int64, runAfter time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.witness_jobs SET run_after = $3, locked_by = NULL, locked_until = NULL
	    WHERE org_id = $1 AND id = $2`, org, id, runAfter)
	return err
}

// PendingL2 lists the actions of one connector still waiting for L2: an open l2_match job on an
// action in witnessing whose connector is connectorID.
func PendingL2(ctx context.Context, tx pgx.Tx, org ids.OrgID, connectorID string) ([]ids.ActionID, error) {
	rows, err := tx.Query(ctx, `SELECT j.action_id FROM deedglass.witness_jobs AS j
	    JOIN deedglass.actions AS a ON a.org_id = j.org_id AND a.id = j.action_id
	    JOIN deedglass.connectors AS c ON c.org_id = a.org_id AND c.name = a.connector
	   WHERE j.org_id = $1 AND j.kind = 'l2_match' AND a.state = 'witnessing' AND c.id = $2 AND c.deleted_at IS NULL
	   ORDER BY j.id LIMIT 10000`, org, connectorID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ids.ActionID, error) {
		var id ids.ActionID
		err := r.Scan(&id)
		return id, err
	})
}

// WitnessState reads an action's stored witness_detail, its amendment count and its witness
// section (level and verdict, "" while there is none).
func WitnessState(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.ActionID) (detail json.RawMessage, amendments int, level, verdict string, err error) {
	var d []byte
	var l, v *string
	err = tx.QueryRow(ctx, `SELECT witness_detail, witness_amendments, witness_level, witness_verdict FROM deedglass.actions
	    WHERE org_id = $1 AND id = $2`, org, id).Scan(&d, &amendments, &l, &v)
	if err != nil {
		return nil, 0, "", "", notFound(err)
	}
	if l != nil {
		level = *l
	}
	if v != nil {
		verdict = *v
	}
	return d, amendments, level, verdict, nil
}

// SetPartialWitness stores the L0 and L1 detail of an action still waiting for L2 (no claim, no
// verdict columns). ok is false when the action isn't witnessing any more.
func SetPartialWitness(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.ActionID, detail json.RawMessage) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.actions SET witness_detail = $3::jsonb
	    WHERE org_id = $1 AND id = $2 AND state = 'witnessing' AND witness_verdict IS NULL`, org, id, string(detail))
	return tag.RowsAffected() == 1, err
}

// IncrementAmendments counts one more amendment deed on an action (the CHECK caps it at 3).
func IncrementAmendments(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.ActionID) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.actions SET witness_amendments = witness_amendments + 1 WHERE org_id = $1 AND id = $2`, org, id)
	return err
}

// SetWitnessExpect stores an action's explicit footprint (S23's undo writes it before it runs).
func SetWitnessExpect(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.ActionID, expect json.RawMessage) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.actions SET witness_expect = $3::jsonb WHERE org_id = $1 AND id = $2`, org, id, string(expect))
	return err
}
