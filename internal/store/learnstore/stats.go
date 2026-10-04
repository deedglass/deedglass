package learnstore

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
)

// Stats (S27 §3.6). A grouping is read as three statements that run side by side, each over
// actions_org_evaluated_idx with a custom plan: the counts (one hash aggregate by key and policy
// version, the approval joined), the percentiles (latency and approval wait, with the totals row as
// a second grouping set) and the costs (by key and currency, and by currency). The totals' counts
// are the groups' sums. Rule keys: the deciding rule id, or "@default", "@stop", "@budget",
// "@builtin" for decisions no rule made.

// groupKey is the grouping expression of each `by`: a fixed table, never input.
var groupKey = map[string]string{
	learn.ByRule:  `COALESCE(NULLIF(a.decision_rule_id, ''), '@' || COALESCE(a.decision_source, 'none'))`,
	learn.ByTool:  `a.tool`,
	learn.ByAgent: `a.agent_id`,
	learn.ByNone:  `''`,
}

// statsFrom is the shared FROM and WHERE of every stats read: $1 org, $2 from, $3 to, $4 agent or
// "", $5 a LIKE pattern for the tool or "".
const statsFrom = ` FROM deedglass.actions AS a
  LEFT JOIN deedglass.approvals AS ap ON ap.org_id = a.org_id AND ap.id = a.approval_id
  WHERE a.org_id = $1 AND a.evaluated_at >= $2 AND a.evaluated_at < $3 AND a.decision IS NOT NULL
    AND ($4 = '' OR a.agent_id = $4) AND ($5 = '' OR a.tool LIKE $5 ESCAPE '\')`

// statsFilter is statsFrom for reads that don't need the approval.
const statsFilter = ` FROM deedglass.actions AS a
  WHERE a.org_id = $1 AND a.evaluated_at >= $2 AND a.evaluated_at < $3 AND a.decision IS NOT NULL
    AND ($4 = '' OR a.agent_id = $4) AND ($5 = '' OR a.tool LIKE $5 ESCAPE '\')`

// statsSettings pins every stats read: a custom plan (the generic plan of the optional filters
// scans badly at 100k rows) and room to sort in memory.
const statsSettings = `SELECT pg_catalog.set_config('plan_cache_mode', 'force_custom_plan', true), pg_catalog.set_config('work_mem', '32MB', true)`

// statsParallel bounds how many stats statements one Store runs at once (the pool is shared with
// the Gate).
const statsParallel = 4

type statsStmts struct{ counts, pct, cost string }

func statsStmt(by string) (statsStmts, error) {
	key, ok := groupKey[by]
	if !ok {
		return statsStmts{}, fmt.Errorf("learnstore: unknown grouping %q", by)
	}
	sets := `GROUP BY GROUPING SETS ((s.k), ())`
	costSets := `GROUP BY GROUPING SETS ((s.k, s.cur), (s.cur))`
	if by == learn.ByNone {
		sets, costSets = `GROUP BY s.k`, `GROUP BY s.k, s.cur`
	}
	return statsStmts{
		counts: `SELECT ` + key + ` AS k, a.policy_version_id,
    pg_catalog.count(*),
    pg_catalog.count(*) FILTER (WHERE a.decision = 'allow'),
    pg_catalog.count(*) FILTER (WHERE a.decision = 'deny'),
    pg_catalog.count(*) FILTER (WHERE a.decision = 'require_approval'),
    pg_catalog.count(*) FILTER (WHERE a.decision_source = 'stop'),
    pg_catalog.count(*) FILTER (WHERE a.decision = 'deny' AND a.decision_source IS DISTINCT FROM 'stop'),
    pg_catalog.count(*) FILTER (WHERE a.decision = 'require_approval' AND ap.status = 'approved'),
    pg_catalog.count(*) FILTER (WHERE a.decision = 'require_approval' AND ap.status = 'denied'),
    pg_catalog.count(*) FILTER (WHERE a.decision = 'require_approval' AND ap.status = 'expired'),
    pg_catalog.count(*) FILTER (WHERE a.decision = 'require_approval' AND ap.status = 'cancelled'),
    pg_catalog.count(*) FILTER (WHERE a.decision = 'require_approval' AND (ap.status = 'pending' OR ap.status IS NULL)),
    pg_catalog.count(*) FILTER (WHERE a.witness_verdict = 'VERIFIED'),
    pg_catalog.count(*) FILTER (WHERE a.witness_verdict = 'MISMATCH'),
    pg_catalog.count(*) FILTER (WHERE a.witness_verdict = 'UNVERIFIABLE'),
    pg_catalog.count(*) FILTER (WHERE a.witness_verdict IS NULL),
    pg_catalog.count(*) FILTER (WHERE a.witness_level = 'L0' AND a.witness_verdict IS NOT NULL),
    pg_catalog.count(*) FILTER (WHERE a.witness_level = 'L1' AND a.witness_verdict IS NOT NULL),
    pg_catalog.count(*) FILTER (WHERE a.witness_level = 'L2' AND a.witness_verdict IS NOT NULL),
    pg_catalog.count(*) FILTER (WHERE a.execution_status IS NOT NULL)` + statsFrom + ` GROUP BY 1, 2`,
		pct: `SELECT s.k, GROUPING(s.k) = 1,
    pg_catalog.percentile_cont(0.5) WITHIN GROUP (ORDER BY s.wait) FILTER (WHERE s.wait IS NOT NULL),
    pg_catalog.percentile_cont(0.95) WITHIN GROUP (ORDER BY s.wait) FILTER (WHERE s.wait IS NOT NULL),
    pg_catalog.percentile_cont(0.5) WITHIN GROUP (ORDER BY s.lat) FILTER (WHERE s.lat IS NOT NULL),
    pg_catalog.percentile_cont(0.95) WITHIN GROUP (ORDER BY s.lat) FILTER (WHERE s.lat IS NOT NULL)
  FROM (SELECT ` + key + ` AS k,
      CASE WHEN ap.status IN ('approved', 'denied') AND ap.decided_at IS NOT NULL AND a.pending_approval_at IS NOT NULL
        THEN EXTRACT(EPOCH FROM ap.decided_at - a.pending_approval_at) END AS wait,
      CASE WHEN a.execution_status IS NOT NULL THEN a.latency_ms END AS lat` + statsFrom + `
      AND ((ap.status IN ('approved', 'denied') AND ap.decided_at IS NOT NULL AND a.pending_approval_at IS NOT NULL)
        OR (a.execution_status IS NOT NULL AND a.latency_ms IS NOT NULL))) AS s ` + sets,
		cost: `SELECT s.k, GROUPING(s.k) = 1, s.cur, pg_catalog.sum(s.amt)::bigint,
    pg_catalog.percentile_cont(0.5) WITHIN GROUP (ORDER BY s.amt), pg_catalog.percentile_cont(0.95) WITHIN GROUP (ORDER BY s.amt)
  FROM (SELECT ` + key + ` AS k, a.cost_currency AS cur, a.cost_amount AS amt` + statsFilter + `
      AND a.cost_amount IS NOT NULL AND a.cost_currency IS NOT NULL) AS s ` + costSets + ` ORDER BY 1, 3`,
	}, nil
}

// statsRead runs fn in a read-only org transaction with the stats settings, at most statsParallel at a time.
func (s *Store) statsRead(ctx context.Context, org ids.OrgID, fn func(ctx context.Context, tx pgx.Tx) error) error {
	s.statsOnce.Do(func() { s.statsSem = make(chan struct{}, statsParallel) })
	select {
	case s.statsSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.statsSem }()
	return readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, statsSettings); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// together runs fns side by side and returns the first error.
func together(fns ...func() error) error {
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, f := range fns {
		wg.Go(func() { errs[i] = f() })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// StatsGroups reads one grouping's aggregates and the window's totals (for by=none the one row is
// both).
func (s *Store) StatsGroups(ctx context.Context, org ids.OrgID, q learn.StatsQuery, _ string) ([]learn.StatsRow, learn.StatsRow, error) {
	st, err := statsStmt(q.By)
	if err != nil {
		return nil, learn.StatsRow{}, err
	}
	args := []any{org, q.From, q.To, q.Agent, learn.ToolGlobSQL(q.Tool)}
	type pct struct{ waitP50, waitP95, latP50, latP95 *float64 }
	type cost struct {
		key   string
		total bool
		c     learn.CostRow
	}
	var (
		out      []learn.StatsRow
		index    = map[string]int{}
		names    = map[string]string{}
		pcts     = map[string]pct{}
		totalPct pct
		costs    []cost
	)
	err = together(
		func() error {
			return s.statsRead(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
				versions := map[string]int{}
				vrows, err := tx.Query(ctx, `SELECT id, version FROM deedglass.policy_versions WHERE org_id = $1`, org)
				if err != nil {
					return err
				}
				for vrows.Next() {
					var id string
					var v int32
					if err := vrows.Scan(&id, &v); err != nil {
						vrows.Close()
						return err
					}
					versions[id] = int(v)
				}
				vrows.Close()
				if q.By == learn.ByAgent {
					arows, err := tx.Query(ctx, `SELECT id, name FROM deedglass.agents WHERE org_id = $1`, org)
					if err != nil {
						return err
					}
					for arows.Next() {
						var id, name string
						if err := arows.Scan(&id, &name); err != nil {
							arows.Close()
							return err
						}
						names[id] = name
					}
					arows.Close()
				}
				rows, err := tx.Query(ctx, st.counts, args...)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var (
						r   learn.StatsRow
						pv  *string
						key string
					)
					if err := rows.Scan(&key, &pv, &r.Actions, &r.Allow, &r.Deny, &r.RequireApproval, &r.StopDecided, &r.DenyNonStop,
						&r.Approved, &r.ApprovalDenied, &r.Expired, &r.Cancelled, &r.Pending,
						&r.Verified, &r.Mismatch, &r.Unverifiable, &r.NoVerdict, &r.L0, &r.L1, &r.L2, &r.Executed); err != nil {
						return err
					}
					r.Key = key
					i, ok := index[key]
					if !ok {
						i = len(out)
						index[key] = i
						out = append(out, learn.StatsRow{Key: key})
					}
					g := &out[i]
					v := r
					v.Key, v.Versions = "", nil
					*g = addCounts(*g, v)
					if pv != nil && q.By == learn.ByRule {
						if n, ok := versions[*pv]; ok && !slices.Contains(g.Versions, n) {
							g.Versions = append(g.Versions, n)
						}
					}
				}
				return rows.Err()
			})
		},
		func() error {
			return s.statsRead(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
				rows, err := tx.Query(ctx, st.pct, args...)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var (
						key   *string
						total bool
						p     pct
					)
					if err := rows.Scan(&key, &total, &p.waitP50, &p.waitP95, &p.latP50, &p.latP95); err != nil {
						return err
					}
					switch {
					case total:
						totalPct = p
					case key != nil:
						pcts[*key] = p
						if q.By == learn.ByNone {
							totalPct = p
						}
					}
				}
				return rows.Err()
			})
		},
		func() error {
			return s.statsRead(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
				rows, err := tx.Query(ctx, st.cost, args...)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var (
						key *string
						c   cost
					)
					if err := rows.Scan(&key, &c.total, &c.c.Currency, &c.c.Total, &c.c.P50, &c.c.P95); err != nil {
						return err
					}
					if key != nil {
						c.key = *key
					}
					costs = append(costs, c)
				}
				return rows.Err()
			})
		},
	)
	if err != nil {
		return nil, learn.StatsRow{}, err
	}
	// Order: most actions first, then key (the old single statement's ORDER BY 4 DESC, 1).
	slices.SortFunc(out, func(a, b learn.StatsRow) int {
		if a.Actions != b.Actions {
			return cmp.Compare(b.Actions, a.Actions)
		}
		return strings.Compare(a.Key, b.Key)
	})
	var total learn.StatsRow
	for i := range out {
		r := &out[i]
		slices.Sort(r.Versions)
		p := pcts[r.Key]
		r.WaitP50, r.WaitP95, r.LatencyP50, r.LatencyP95 = p.waitP50, p.waitP95, p.latP50, p.latP95
		if q.By == learn.ByAgent {
			r.AgentName = names[r.Key]
		}
		total = addCounts(total, *r)
	}
	total.WaitP50, total.WaitP95, total.LatencyP50, total.LatencyP95 = totalPct.waitP50, totalPct.waitP95, totalPct.latP50, totalPct.latP95
	byKey := map[string]int{}
	for i, r := range out {
		byKey[r.Key] = i
	}
	for _, c := range costs {
		if c.total || q.By == learn.ByNone {
			total.Costs = append(total.Costs, c.c)
		}
		if !c.total {
			if i, ok := byKey[c.key]; ok {
				out[i].Costs = append(out[i].Costs, c.c)
			}
		}
	}
	if q.By == learn.ByNone {
		if len(out) == 0 {
			return nil, total, nil
		}
		out[0] = total
		return out[:1], total, nil
	}
	return out, total, nil
}

// addCounts adds b's counts to a (keys, versions, percentiles and costs untouched).
func addCounts(a, b learn.StatsRow) learn.StatsRow {
	a.Actions += b.Actions
	a.Allow += b.Allow
	a.Deny += b.Deny
	a.RequireApproval += b.RequireApproval
	a.StopDecided += b.StopDecided
	a.DenyNonStop += b.DenyNonStop
	a.Approved += b.Approved
	a.ApprovalDenied += b.ApprovalDenied
	a.Expired += b.Expired
	a.Cancelled += b.Cancelled
	a.Pending += b.Pending
	a.Verified += b.Verified
	a.Mismatch += b.Mismatch
	a.Unverifiable += b.Unverifiable
	a.NoVerdict += b.NoVerdict
	a.L0 += b.L0
	a.L1 += b.L1
	a.L2 += b.L2
	a.Executed += b.Executed
	return a
}

// StatsSeries counts per org-local day.
func (s *Store) StatsSeries(ctx context.Context, org ids.OrgID, q learn.StatsQuery, tz string) ([]learn.SeriesRow, error) {
	if tz == "" {
		tz = "UTC"
	}
	var out []learn.SeriesRow
	err := s.statsRead(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT pg_catalog.to_char((a.evaluated_at AT TIME ZONE $6)::date, 'YYYY-MM-DD') AS d,
		      pg_catalog.count(*), pg_catalog.count(*) FILTER (WHERE a.decision = 'deny'),
		      pg_catalog.count(*) FILTER (WHERE a.witness_verdict = 'VERIFIED'), pg_catalog.count(*) FILTER (WHERE a.witness_verdict = 'MISMATCH'),
		      pg_catalog.count(*) FILTER (WHERE a.witness_verdict = 'UNVERIFIABLE')`+statsFilter+` GROUP BY 1 ORDER BY 1`,
			org, q.From, q.To, q.Agent, learn.ToolGlobSQL(q.Tool), tz)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r learn.SeriesRow
			if err := rows.Scan(&r.Date, &r.Actions, &r.Denied, &r.Verified, &r.Mismatch, &r.Unverifiable); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// DayCounters reads the day windows of usage_counters whose day overlaps [from, to).
func (s *Store) DayCounters(ctx context.Context, org ids.OrgID, from, to time.Time, _ string) ([]learn.DayCounter, error) {
	var out []learn.DayCounter
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT scope, counter, window_start, value FROM deedglass.usage_counters
		    WHERE org_id = $1 AND window_kind = 'day' AND window_start > $2::timestamptz - interval '1 day' AND window_start < $3
		    ORDER BY scope, counter, window_start`, org, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c learn.DayCounter
			if err := rows.Scan(&c.Scope, &c.Counter, &c.Day, &c.Value); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// AgentBudgets reads the agents' own budgets (S08), system agents left out.
func (s *Store) AgentBudgets(ctx context.Context, org ids.OrgID) ([]learn.AgentBudgetRow, error) {
	var out []learn.AgentBudgetRow
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, name, budgets FROM deedglass.agents WHERE org_id = $1 AND kind <> 'system' ORDER BY name`, org)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a learn.AgentBudgetRow
			if err := rows.Scan(&a.ID, &a.Name, &a.Budgets); err != nil {
				return err
			}
			if string(a.Budgets) == "{}" {
				a.Budgets = nil
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// StatsSQL is a grouping's counts and percentiles statements and their arguments, for EXPLAIN in
// the budget test (make bench-insights), with the settings every stats read runs under.
func StatsSQL(org ids.OrgID, q learn.StatsQuery) (settings string, stmts map[string]string, args []any, err error) {
	st, err := statsStmt(q.By)
	return statsSettings, map[string]string{"counts": st.counts, "percentiles": st.pct, "costs": st.cost},
		[]any{org, q.From, q.To, q.Agent, learn.ToolGlobSQL(q.Tool)}, err
}
