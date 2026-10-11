package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// The Gate's worker and sweepers (S13). The cross-org reads are the SECURITY DEFINER functions of
// migration 0016, claim_witness_jobs, claim_stale_actions and maint_release_action_keys, named
// only here (TestPreAuthNamesStayInStore): they return ids, and every change then runs in an org
// transaction with row-level security on.

// ClaimedJob is one leased witness_jobs row.
type ClaimedJob struct {
	ID       int64
	OrgID    ids.OrgID
	ActionID ids.ActionID
	Kind     string
	Attempts int
}

// ClaimWitnessJobs leases up to limit due jobs to worker for lease (FOR UPDATE SKIP LOCKED).
func (db *DB) ClaimWitnessJobs(ctx context.Context, worker string, limit int, lease time.Duration) ([]ClaimedJob, error) {
	rows, err := db.pool.Query(ctx, `SELECT id, org_id, action_id, kind, attempts FROM deedglass.claim_witness_jobs($1, $2, $3)`,
		worker, limit, lease)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ClaimedJob, error) {
		var j ClaimedJob
		err := r.Scan(&j.ID, &j.OrgID, &j.ActionID, &j.Kind, &j.Attempts)
		return j, err
	})
}

// StaleAction is one action a sweeper should look at.
type StaleAction struct {
	OrgID    ids.OrgID
	ActionID ids.ActionID
	State    string
}

// ClaimStaleActions returns at most limit actions of each kind: executing past their deadline,
// idle in a pre-seal state for 30 s, and pending past their approval's expiry.
func (db *DB) ClaimStaleActions(ctx context.Context, limit int) ([]StaleAction, error) {
	rows, err := db.pool.Query(ctx, `SELECT org_id, action_id, state FROM deedglass.claim_stale_actions($1)`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (StaleAction, error) {
		var s StaleAction
		err := r.Scan(&s.OrgID, &s.ActionID, &s.State)
		return s, err
	})
}

// ReleaseActionKeys clears the Idempotency-Key of at most limit actions older than 24 hours and
// returns how many it cleared.
func (db *DB) ReleaseActionKeys(ctx context.Context, limit int) (int, error) {
	var n int
	err := db.pool.QueryRow(ctx, `SELECT deedglass.maint_release_action_keys($1)`, limit).Scan(&n)
	return n, err
}

// EnqueueJob adds a job for an action of org (once per action and kind) and wakes the workers when
// tx commits.
func EnqueueJob(ctx context.Context, tx pgx.Tx, org ids.OrgID, action ids.ActionID, kind string, runAfter time.Time, maxAttempts int) error {
	return EnqueueJobPayload(ctx, tx, org, action, kind, runAfter, maxAttempts, nil)
}

// EnqueueJobPayload is EnqueueJob with a payload: at most 16 KiB of JSON the job's worker needs
// (S19: the read-back's evidence), never a request or result body and never a secret.
func EnqueueJobPayload(ctx context.Context, tx pgx.Tx, org ids.OrgID, action ids.ActionID, kind string, runAfter time.Time, maxAttempts int,
	payload json.RawMessage) error {
	if maxAttempts < 1 {
		maxAttempts = 8
	}
	var p any
	if payload != nil {
		p = string(payload)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deedglass.witness_jobs (org_id, action_id, kind, run_after, max_attempts, payload)
	    VALUES ($1, $2, $3, $4, $5, $6::jsonb) ON CONFLICT (action_id, kind) DO NOTHING`, org, action, kind, runAfter, maxAttempts, p); err != nil {
		return err
	}
	return NotifyJobs(ctx, tx, org.String())
}

// DeleteActionJob removes an action's job of one kind (S19: B2 finished the read-back inline).
func DeleteActionJob(ctx context.Context, tx pgx.Tx, org ids.OrgID, action ids.ActionID, kind string) error {
	_, err := tx.Exec(ctx, `DELETE FROM deedglass.witness_jobs WHERE org_id = $1 AND action_id = $2 AND kind = $3`, org, action, kind)
	return err
}

// JobRow is a job as its worker reads it inside the org transaction.
type JobRow struct {
	ID          int64
	Attempts    int
	MaxAttempts int
	LockedBy    *string
	Payload     json.RawMessage
	LastError   *string
}

// GetJob reads one job of org, locking it. ErrNotFound when it is gone (another worker finished it).
func GetJob(ctx context.Context, tx pgx.Tx, org ids.OrgID, id int64) (JobRow, error) {
	var j JobRow
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT id, attempts, max_attempts, locked_by, payload, last_error FROM deedglass.witness_jobs
	    WHERE org_id = $1 AND id = $2 FOR UPDATE`, org, id).Scan(&j.ID, &j.Attempts, &j.MaxAttempts, &j.LockedBy, &payload, &j.LastError)
	j.Payload = payload
	return j, notFound(err)
}

// PeekJob reads one job of org without locking it (inside a read transaction). ErrNotFound when it
// is gone.
func PeekJob(ctx context.Context, tx pgx.Tx, org ids.OrgID, id int64) (JobRow, error) {
	var j JobRow
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT id, attempts, max_attempts, locked_by, payload, last_error FROM deedglass.witness_jobs
	    WHERE org_id = $1 AND id = $2`, org, id).Scan(&j.ID, &j.Attempts, &j.MaxAttempts, &j.LockedBy, &payload, &j.LastError)
	j.Payload = payload
	return j, notFound(err)
}

// CompleteJob deletes a finished job.
func CompleteJob(ctx context.Context, tx pgx.Tx, org ids.OrgID, id int64) error {
	_, err := tx.Exec(ctx, `DELETE FROM deedglass.witness_jobs WHERE org_id = $1 AND id = $2`, org, id)
	return err
}

// RetryJob schedules a failed job for runAfter with its last error (redacted by the caller, at
// most 2,000 characters) and ends its lease.
func RetryJob(ctx context.Context, tx pgx.Tx, org ids.OrgID, id int64, runAfter time.Time, lastErr string) error {
	if len(lastErr) > 2000 {
		lastErr = lastErr[:2000]
	}
	_, err := tx.Exec(ctx, `UPDATE deedglass.witness_jobs SET run_after = $3, last_error = $4, locked_by = NULL, locked_until = NULL
	    WHERE org_id = $1 AND id = $2`, org, id, runAfter, lastErr)
	return err
}

// ReleaseJobLease ends worker's lease on a job it won't finish (graceful shutdown).
func ReleaseJobLease(ctx context.Context, tx pgx.Tx, org ids.OrgID, id int64, worker string) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.witness_jobs SET locked_until = pg_catalog.now()
	    WHERE org_id = $1 AND id = $2 AND locked_by = $3`, org, id, worker)
	return err
}

// DeleteUnclaimedJob removes an action's job of one kind only while no worker holds it (S40: the
// cancel of an approved scheduled action). It reports whether a row went.
func DeleteUnclaimedJob(ctx context.Context, tx pgx.Tx, org ids.OrgID, action ids.ActionID, kind string) (bool, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM deedglass.witness_jobs WHERE org_id = $1 AND action_id = $2 AND kind = $3
	    AND (locked_until IS NULL OR locked_until < pg_catalog.now())`, org, action, kind)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
