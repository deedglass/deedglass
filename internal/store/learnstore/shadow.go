package learnstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
)

// shadowWhere selects one shadow version's actions (actions_org_shadow_idx), optionally in a
// decision-time window.
const shadowWhere = ` WHERE a.org_id = $1 AND a.shadow_version_id = $2 AND a.shadow_decision IS NOT NULL AND a.decision IS NOT NULL
    AND ($3::timestamptz IS NULL OR a.evaluated_at >= $3) AND ($4::timestamptz IS NULL OR a.evaluated_at < $4)`

// ShadowRows aggregates the actions decided with this shadow version.
func (s *Store) ShadowRows(ctx context.Context, org ids.OrgID, shadowVersionID string, w learn.NullableWindow) ([]learn.ShadowRow, error) {
	var out []learn.ShadowRow
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT a.decision, a.shadow_decision, COALESCE(a.shadow_rule_id, ''), a.tool, a.agent_id, g.name,
		      COALESCE(a.policy_version_id, ''), COALESCE(ap.status, ''), pg_catalog.count(*)
		    FROM deedglass.actions AS a
		    JOIN deedglass.agents AS g ON g.org_id = a.org_id AND g.id = a.agent_id
		    LEFT JOIN deedglass.approvals AS ap ON ap.org_id = a.org_id AND ap.id = a.approval_id`+shadowWhere+`
		    GROUP BY 1, 2, 3, 4, 5, 6, 7, 8 ORDER BY 1, 2, 3, 4, 5, 7, 8`,
			org, shadowVersionID, tptr(w.From), tptr(w.To))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				r  learn.ShadowRow
				ap string
			)
			if err := rows.Scan(&r.Enforce, &r.Shadow, &r.ShadowRule, &r.Tool, &r.AgentID, &r.AgentName, &r.EnforceVersionID, &ap, &r.Count); err != nil {
				return err
			}
			r.Approval = learn.ApprovalOutcome(ap)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// ShadowSamples returns up to n disagreeing actions per (enforce, shadow) pair, by decision time.
func (s *Store) ShadowSamples(ctx context.Context, org ids.OrgID, shadowVersionID string, w learn.NullableWindow, n int) ([]learn.ShadowSample, error) {
	var out []learn.ShadowSample
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT d, sd, id FROM (
		      SELECT a.decision AS d, a.shadow_decision AS sd, a.id, a.evaluated_at,
		        pg_catalog.row_number() OVER (PARTITION BY a.decision, a.shadow_decision ORDER BY a.evaluated_at, a.id) AS rn
		      FROM deedglass.actions AS a`+shadowWhere+` AND a.shadow_decision IS DISTINCT FROM a.decision) AS x
		    WHERE rn <= $5 ORDER BY d, sd, evaluated_at, id`,
			org, shadowVersionID, tptr(w.From), tptr(w.To), n)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x learn.ShadowSample
			if err := rows.Scan(&x.Enforce, &x.Shadow, &x.ActionID); err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

func tptr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}
