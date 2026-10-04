package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// ClaimedReplay is a replay job a runner leased (S27).
type ClaimedReplay struct {
	OrgID    ids.OrgID
	ID       string
	Attempts int
}

// ClaimReplayJobs leases up to limit replay jobs to worker (claim_replay_jobs, migration 0024: at
// most one running job per org, a lost lease re-claimed). Ids only; the work runs in org
// transactions.
func (db *DB) ClaimReplayJobs(ctx context.Context, worker string, limit int, lease time.Duration) ([]ClaimedReplay, error) {
	rows, err := db.pool.Query(ctx, `SELECT org_id, id, attempts FROM deedglass.claim_replay_jobs($1, $2, $3)`, worker, limit, lease)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ClaimedReplay, error) {
		var c ClaimedReplay
		err := r.Scan(&c.OrgID, &c.ID, &c.Attempts)
		return c, err
	})
}

// LearnOrgs lists the orgs anomaly detection passes over (claim_learn_orgs, migration 0024): org
// ids with actions decided in the last `recent`, or with anomaly facts on.
func (db *DB) LearnOrgs(ctx context.Context, recent time.Duration) ([]ids.OrgID, error) {
	rows, err := db.pool.Query(ctx, `SELECT org_id FROM deedglass.claim_learn_orgs($1)`, recent)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[ids.OrgID])
}
