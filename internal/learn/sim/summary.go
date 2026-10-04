package sim

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// Result is one run's outcome: what the CLI prints (S27 §5) and --json returns.
type Result struct {
	Org            ids.OrgID     `json:"org"`
	Slug           string        `json:"slug"`
	Name           string        `json:"name"`
	Appended       bool          `json:"appended"`
	Seed           uint64        `json:"seed"`
	Window         Window        `json:"window"`
	Planned        int           `json:"planned"`
	PlanDigest     string        `json:"plan_digest"`
	Submitted      int           `json:"actions"`
	Deeds          int64         `json:"deeds"`
	ChainIntact    bool          `json:"chain_intact"`
	ChainStatus    string        `json:"chain_status"`
	Agents         []string      `json:"agents"`
	Policy         string        `json:"policy"`
	EnforceVersion int           `json:"enforce_version"`
	ShadowVersion  int           `json:"shadow_version,omitempty"`
	ShadowFromDay  int           `json:"shadow_from_day,omitempty"`
	Pending        int           `json:"pending_decisions"` // decisions that fall after the window's end
	Counts         Counts        `json:"counts"`
	Approvals      ApprovalStats `json:"approvals"`
	Incidents      []IncidentSum `json:"incidents"`
	Elapsed        time.Duration `json:"elapsed_ns"`
	Scenario       *Scenario     `json:"-"`
}

// Counts are the org's actions by outcome, as the Gate recorded them.
type Counts struct {
	Verified     int64 `json:"verified"`
	ByLevel      []KV  `json:"verified_by_level"`
	Denied       int64 `json:"denied"`
	DeniedBy     []KV  `json:"denied_by"`
	Mismatch     int64 `json:"mismatch"`
	MismatchBy   []KV  `json:"mismatch_by"`
	Unverifiable int64 `json:"unverifiable"`
	UnverBy      []KV  `json:"unverifiable_by"`
	Failed       int64 `json:"failed"`
	FailedBy     []KV  `json:"failed_by"`
	Pending      int64 `json:"pending_approval"`
	Expired      int64 `json:"expired"`
	Methods      []KV  `json:"witness_methods"`
}

// KV is one count of a breakdown.
type KV struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// ApprovalStats summarizes the run's approvals.
type ApprovalStats struct {
	Requested     int64   `json:"requested"`
	Approved      int64   `json:"approved"`
	Denied        int64   `json:"denied"`
	Expired       int64   `json:"expired"`
	Pending       int64   `json:"pending"`
	MedianWaitMin float64 `json:"median_wait_minutes"`
}

// IncidentSum is one scenario incident as it landed.
type IncidentSum struct {
	Kind, Agent, Tool string
	Days              []int
	Count             int
	Factor            float64
}

// summarize reads the org's outcome back from the database.
func summarize(ctx context.Context, env Env, org ids.OrgID, res *Result) error {
	c := &res.Counts
	kv := func(tx pgx.Tx, q string, dst *[]KV) error {
		rows, err := tx.Query(ctx, q, org)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k KV
			if err := rows.Scan(&k.Key, &k.Count); err != nil {
				return err
			}
			*dst = append(*dst, k)
		}
		return rows.Err()
	}
	err := env.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT
		      pg_catalog.count(*) FILTER (WHERE witness_verdict = 'VERIFIED'),
		      pg_catalog.count(*) FILTER (WHERE decision = 'deny' OR denied_at IS NOT NULL AND approval_id IS NULL),
		      pg_catalog.count(*) FILTER (WHERE witness_verdict = 'MISMATCH'),
		      pg_catalog.count(*) FILTER (WHERE witness_verdict = 'UNVERIFIABLE'),
		      pg_catalog.count(*) FILTER (WHERE failed_at IS NOT NULL),
		      pg_catalog.count(*) FILTER (WHERE state = 'pending_approval'),
		      pg_catalog.count(*) FILTER (WHERE expired_at IS NOT NULL)
		    FROM deedglass.actions WHERE org_id = $1`, org).Scan(&c.Verified, &c.Denied, &c.Mismatch, &c.Unverifiable, &c.Failed, &c.Pending, &c.Expired); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FROM deedglass.deeds WHERE org_id = $1`, org).Scan(&res.Deeds); err != nil {
			return err
		}
		for _, x := range []struct {
			q   string
			dst *[]KV
		}{
			{`SELECT witness_level, pg_catalog.count(*) FROM deedglass.actions WHERE org_id = $1 AND witness_verdict = 'VERIFIED' GROUP BY 1 ORDER BY 1`, &c.ByLevel},
			{`SELECT CASE WHEN decision_code = 'budget_exceeded' THEN 'budget' ELSE COALESCE(decision_rule_id, decision_code, 'deny') END, pg_catalog.count(*)
			    FROM deedglass.actions WHERE org_id = $1 AND decision = 'deny' GROUP BY 1 ORDER BY 2 DESC, 1`, &c.DeniedBy},
			{`SELECT witness_level || ' · ' || tool, pg_catalog.count(*) FROM deedglass.actions WHERE org_id = $1 AND witness_verdict = 'MISMATCH'
			    GROUP BY 1 ORDER BY 2 DESC, 1`, &c.MismatchBy},
			{`SELECT COALESCE(e.data ->> 'reason', 'unknown'), pg_catalog.count(*) FROM deedglass.action_events AS e
			    WHERE e.org_id = $1 AND e.to_state = 'unverifiable' AND e.from_state = 'witnessing' GROUP BY 1 ORDER BY 2 DESC, 1`, &c.UnverBy},
			{`SELECT COALESCE(error_code, 'unknown'), pg_catalog.count(*) FROM deedglass.actions WHERE org_id = $1 AND failed_at IS NOT NULL
			    GROUP BY 1 ORDER BY 2 DESC, 1`, &c.FailedBy},
			{`SELECT COALESCE(witness_method, ''), pg_catalog.count(*) FROM deedglass.actions WHERE org_id = $1 AND witness_verdict IS NOT NULL
			    GROUP BY 1 ORDER BY 1`, &c.Methods},
		} {
			if err := kv(tx, x.q, x.dst); err != nil {
				return err
			}
		}
		a := &res.Approvals
		var median *float64
		err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*),
		      pg_catalog.count(*) FILTER (WHERE ap.status = 'approved'), pg_catalog.count(*) FILTER (WHERE ap.status = 'denied'),
		      pg_catalog.count(*) FILTER (WHERE ap.status = 'expired'), pg_catalog.count(*) FILTER (WHERE ap.status = 'pending'),
		      pg_catalog.percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM ap.decided_at - a.pending_approval_at))
		        FILTER (WHERE ap.status IN ('approved', 'denied'))
		    FROM deedglass.approvals AS ap JOIN deedglass.actions AS a ON a.org_id = ap.org_id AND a.id = ap.action_id
		    WHERE ap.org_id = $1`, org).Scan(&a.Requested, &a.Approved, &a.Denied, &a.Expired, &a.Pending, &median)
		if err == nil && median != nil {
			a.MedianWaitMin = *median / 60
		}
		return err
	})
	if err != nil {
		return err
	}
	if res.Scenario != nil {
		for _, inc := range res.Scenario.Incidents {
			s := IncidentSum{Kind: inc.Kind, Agent: inc.Agent, Tool: inc.Tool, Days: append([]int(nil), inc.Days...), Count: inc.Count, Factor: inc.Factor}
			sort.Ints(s.Days)
			res.Incidents = append(res.Incidents, s)
		}
	}
	return nil
}
