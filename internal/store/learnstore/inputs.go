package learnstore

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
	"github.com/deedglass/deedglass/internal/store"
)

// ReadTimeout is the statement timeout of every Insights read (S27 §3.4, §3.6).
const ReadTimeout = "5s"

// readTx runs fn in a read-only org transaction with the Insights statement timeout.
func readTx(ctx context.Context, db *store.DB, org ids.OrgID, fn store.TxFunc) error {
	return db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '`+ReadTimeout+`'`); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// Inputs is learn.InputSource over deedglass.actions: the recorded decisions of one org in
// (evaluated_at, id) order, keyset-paginated over actions_org_evaluated_idx.
type Inputs struct {
	DB *store.DB
	// Scanned counts the rows returned (the two-org isolation test reads it).
	Scanned atomic.Int64
}

var _ learn.InputSource = (*Inputs)(nil)

// Count is the number of decided actions in the window.
func (s *Inputs) Count(ctx context.Context, org ids.OrgID, w learn.Window) (int64, error) {
	var n int64
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FROM deedglass.actions
		    WHERE org_id = $1 AND evaluated_at >= $2 AND evaluated_at < $3 AND decision IS NOT NULL`, org, w.From, w.To).Scan(&n)
	})
	return n, err
}

const scanCols = `SELECT a.id, a.evaluated_at, a.tool, a.policy_input, a.payload_pruned_at IS NOT NULL, a.decision,
    COALESCE(a.decision_code, ''), COALESCE(a.decision_source, ''), COALESCE(a.decision_rule_id, ''),
    COALESCE(a.policy_version_id, ''), COALESCE(ap.status, ''), COALESCE(a.decision_detail ->> 'sqlguard_version', '')
  FROM deedglass.actions AS a
  LEFT JOIN deedglass.approvals AS ap ON ap.org_id = a.org_id AND ap.id = a.approval_id
  WHERE a.org_id = $1 AND a.evaluated_at >= $2 AND a.evaluated_at < $3 AND a.decision IS NOT NULL`

// Scan returns up to limit rows after the cursor.
func (s *Inputs) Scan(ctx context.Context, org ids.OrgID, w learn.Window, after learn.Cursor, limit int) ([]learn.InputRow, learn.Cursor, error) {
	var out []learn.InputRow
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		out = out[:0]
		var (
			rows pgx.Rows
			err  error
		)
		if after.At.IsZero() {
			rows, err = tx.Query(ctx, scanCols+` ORDER BY a.evaluated_at, a.id LIMIT $4`, org, w.From, w.To, limit)
		} else {
			rows, err = tx.Query(ctx, scanCols+` AND (a.evaluated_at, a.id) > ($4::timestamptz, $5::text) ORDER BY a.evaluated_at, a.id LIMIT $6`,
				org, w.From, w.To, after.At, after.ID, limit)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				r        learn.InputRow
				in       []byte
				approval string
				at       time.Time
			)
			if err := rows.Scan(&r.ActionID, &at, &r.Tool, &in, &r.Pruned, &r.Recorded.Decision, &r.Recorded.Code, &r.Recorded.Source,
				&r.Recorded.RuleID, &r.Recorded.PolicyVersionID, &approval, &r.SQLGuard); err != nil {
				return err
			}
			r.DecidedAt = at.UTC()
			if in != nil {
				r.Input = json.RawMessage(in)
			}
			r.Approval = learn.ApprovalOutcome(approval)
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, after, err
	}
	s.Scanned.Add(int64(len(out)))
	if len(out) == 0 {
		return nil, after, nil
	}
	last := out[len(out)-1]
	return out, learn.Cursor{At: last.DecidedAt, ID: last.ActionID}, nil
}
