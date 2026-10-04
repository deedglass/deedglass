//go:build integration

package learn_test

//deedglass:allow-sql-build the budget test builds its clone INSERT from information_schema column names, never from input

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	simfiles "github.com/deedglass/deedglass/examples/sim"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
	"github.com/deedglass/deedglass/internal/store/learnstore"
)

// TestInsightsBudget measures S27's budgets on one org with 20 agents and about 100,000 decided
// actions in 7 days: the seed-7 week (real inputs, through the real Gate), cloned 16 times as admin
// onto 16 more agents (same inputs and times, new ids). It runs only with DEEDGLASS_IT_BENCH=1
// (make bench-insights) and writes PERF JSON lines to the test log.
//
//   - replay of acme-sim v3 over the week: ≥ 10,000 actions/s, 100k in < 15 s, heap growth < 256 MiB
//   - stats (cache bypassed) by rule, agent, tool and none: p99 < 300 ms; EXPLAIN for rule and agent
//   - a detect pass with the baseline cached: < 500 ms; a 7-day backfill: < 30 s
//   - a FactsCache hit: < 2 µs and no store read
func TestInsightsBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("a budget run: make bench-insights")
	}
	v := newEnv(t)
	start := time.Now()
	res := v.simulate("acme-sim")
	org := res.Org
	w := learn.Window{From: res.Window.Start, To: res.Window.End}
	admin := v.e.Admin(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(bg, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// 16 more agents (four copies of the four scenario agents), then 16 clones of the week's actions.
	exec(`CREATE FUNCTION pg_temp.rid() RETURNS text LANGUAGE sql VOLATILE AS $$
	  SELECT '0' || pg_catalog.string_agg(substr('0123456789abcdefghjkmnpqrstvwxyz', 1 + floor(random() * 32)::int, 1), '')
	  FROM pg_catalog.generate_series(1, 25) $$`)
	exec(`SET session_replication_role = replica`) // a throwaway cluster: the clones skip the state guard
	for c := 1; c <= 4; c++ {
		exec(`INSERT INTO deedglass.agents (id, org_id, name, owner_user_id, status, labels, budgets, intent_mode, created_by, created_at)
		  SELECT 'agt_' || pg_temp.rid(), org_id, name || '-b' || $2::text, owner_user_id, status, labels, budgets, intent_mode, created_by, created_at
		  FROM deedglass.agents WHERE org_id = $1 AND name NOT IN ('console', 'system', 'deedglass') AND name NOT LIKE '%-b_'`, org, fmt.Sprint(c))
	}
	rows, err := admin.Query(bg, `SELECT column_name FROM information_schema.columns
	  WHERE table_schema = 'deedglass' AND table_name = 'actions' AND is_generated = 'NEVER' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	sel := make([]string, len(cols))
	for i, c := range cols {
		switch c {
		case "id":
			sel[i] = `'act_' || pg_temp.rid()`
		case "idempotency_key":
			sel[i] = `NULL`
		case "agent_id":
			sel[i] = `COALESCE((SELECT g2.id FROM deedglass.agents AS g1 JOIN deedglass.agents AS g2
			  ON g2.org_id = g1.org_id AND g2.name = g1.name || '-b' || $2::text WHERE g1.id = a.agent_id), a.agent_id)`
		default:
			sel[i] = "a." + c
		}
	}
	clone := `INSERT INTO deedglass.actions (` + strings.Join(cols, ", ") + `) SELECT ` + strings.Join(sel, ", ") +
		` FROM deedglass.actions AS a WHERE a.org_id = $1 AND a.id = ANY($3)`
	var base []string
	if err := admin.QueryRow(bg, `SELECT pg_catalog.array_agg(id) FROM deedglass.actions WHERE org_id = $1`, org).Scan(&base); err != nil {
		t.Fatal(err)
	}
	for k := range 16 {
		exec(clone, org, fmt.Sprint(k%4+1), base)
	}
	exec(`SET session_replication_role = origin`)
	exec(`VACUUM ANALYZE`)
	agents := v.scalar(`SELECT count(*) FROM deedglass.agents WHERE org_id = $1 AND name NOT IN ('console', 'system', 'deedglass')`, org)
	n := v.scalar(`SELECT count(*) FROM deedglass.actions WHERE org_id = $1 AND decision IS NOT NULL AND evaluated_at >= $2 AND evaluated_at < $3`, org, w.From, w.To)
	t.Logf("seeded %d decided actions over %d agents in %s", n, agents, time.Since(start).Round(time.Second))
	if n < 95_000 || agents != 20 {
		t.Fatalf("seed: %d actions, %d agents", n, agents)
	}
	report := func(name string, fields map[string]any, ok bool, why string) {
		fields["name"] = name
		fields["actions"] = n
		fields["agents"] = agents
		fields["within"] = ok
		b, _ := json.Marshal(fields)
		t.Logf("PERF %s", b)
		if !ok {
			t.Errorf("%s over budget: %s", name, why)
		}
	}

	// Replay.
	v3n := v.push(org, simfiles.AcmeSimV3)
	v3 := compileFile(t, simfiles.AcmeSimV3)
	runtime.GC()
	var ms0 runtime.MemStats
	runtime.ReadMemStats(&ms0)
	var peak atomic.Uint64
	sample := func(int64, int64) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if m.HeapInuse > peak.Load() {
			peak.Store(m.HeapInuse)
		}
	}
	e := &learn.Engine{Src: v.ls.Inputs}
	t0 := time.Now()
	got, err := e.Run(bg, org, learn.Candidate{Compiled: v3, Version: v3n}, learn.Baseline{}, w, sample)
	el := time.Since(t0)
	if err != nil {
		t.Fatal(err)
	}
	growth := float64(int64(peak.Load())-int64(ms0.HeapInuse)) / (1 << 20)
	rate := float64(got.Summary.Actions) / el.Seconds()
	report("replay_v3_100k", map[string]any{"replayed": got.Summary.Actions, "seconds": round(el.Seconds()), "actions_per_s": round(rate),
		"heap_growth_mib": round(growth), "changed": got.Summary.Changed, "unknown": got.Summary.Unknown, "workers": runtime.GOMAXPROCS(0),
		"target_actions_per_s": 10000, "target_seconds_100k": 15, "target_heap_growth_mib": 256},
		rate >= 10_000 && el < 15*time.Second && growth < 256 && got.Summary.Unknown == 0, fmt.Sprintf("%.0f/s in %s, heap +%.0f MiB", rate, el, growth))

	// Stats, the cache bypassed.
	svc := v.service()
	for _, by := range []string{learn.ByRule, learn.ByAgent, learn.ByTool, learn.ByNone} {
		for _, compare := range []bool{false, true} {
			q := learn.StatsQuery{From: w.From, To: w.To, By: by, Compare: compare}
			lat := make([]time.Duration, 0, 100)
			for i := range 103 {
				t0 := time.Now()
				if _, err := svc.Stats(bg, org, q); err != nil {
					t.Fatal(err)
				}
				if i >= 3 { // warm
					lat = append(lat, time.Since(t0))
				}
			}
			slices.Sort(lat)
			p50, p99 := lat[len(lat)/2], lat[len(lat)*99/100]
			name := "stats_by_" + by
			if compare {
				name += "_compare"
			}
			report(name, map[string]any{"samples": len(lat), "p50_ms": msf(p50), "p99_ms": msf(p99), "max_ms": msf(lat[len(lat)-1]), "target_p99_ms": 300,
				"cache": "bypassed"}, p99 < 300*time.Millisecond, fmt.Sprintf("p99 %s", p99))
		}
	}
	for _, by := range []string{learn.ByRule, learn.ByAgent} {
		settings, stmts, args, err := learnstore.StatsSQL(org, learn.StatsQuery{From: w.From, To: w.To, By: by})
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range []string{"counts", "percentiles", "costs"} {
			err = v.e.DB.OrgReadTx(bg, org, func(ctx context.Context, tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, settings); err != nil {
					return err
				}
				rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+stmts[part], args...)
				if err != nil {
					return err
				}
				lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return err
				}
				t.Logf("EXPLAIN stats by=%s (%s):\n%s", by, part, strings.Join(lines, "\n"))
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	// Detect: a 7-day backfill, then live passes at the window's end (the first loads the baseline).
	d := &learn.Detector{Store: v.ls}
	t0 = time.Now()
	bf, err := d.Backfill(bg, org, w.From, w.To)
	el = time.Since(t0)
	if err != nil {
		t.Fatal(err)
	}
	report("detect_backfill_7d", map[string]any{"seconds": round(el.Seconds()), "windows": bf.Windows, "findings": bf.Findings, "inserted": bf.Inserted,
		"target_seconds": 30}, el < 30*time.Second, el.String())
	at := w.To.Add(-time.Minute)
	t0 = time.Now()
	if _, err := d.DetectOrg(bg, org, at); err != nil {
		t.Fatal(err)
	}
	cold := time.Since(t0)
	lat := make([]time.Duration, 0, 20)
	for range 20 {
		t0 := time.Now()
		if _, err := d.DetectOrg(bg, org, at); err != nil {
			t.Fatal(err)
		}
		lat = append(lat, time.Since(t0))
	}
	slices.Sort(lat)
	report("detect_pass", map[string]any{"cold_ms": msf(cold), "p50_ms": msf(lat[len(lat)/2]), "max_ms": msf(lat[len(lat)-1]), "samples": len(lat),
		"target_ms": 500, "baseline": "cached"}, lat[len(lat)-1] < 500*time.Millisecond, fmt.Sprintf("max %s", lat[len(lat)-1]))

	// FactsCache: opted in, a recent run; one load, then hits only.
	exec(`UPDATE deedglass.orgs SET settings = jsonb_set(settings, '{insights}', '{"anomaly_facts": true}') WHERE id = $1`, org)
	fc := &learn.FactsCache{Store: v.ls, Now: func() time.Time { return at.Add(time.Minute) }}
	var agentID string
	if err := admin.QueryRow(bg, `SELECT id FROM deedglass.agents WHERE org_id = $1 AND name = 'support-bot'`, org).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	agent, err := ids.Parse[ids.AgentKind](agentID)
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := fc.Facts(bg, org, agent, at); f.Active && slices.Contains(f.Kinds, learn.KindDetectorStale) {
		t.Fatalf("the facts failed closed: %+v", f)
	}
	const hits = 1_000_000
	t0 = time.Now()
	for range hits {
		fc.Facts(bg, org, agent, at)
	}
	per := time.Since(t0) / hits
	allocs := testing.AllocsPerRun(1000, func() { fc.Facts(bg, org, agent, at) })
	report("facts_cache_hit", map[string]any{"ns_per_op": per.Nanoseconds(), "allocs_per_op": allocs, "store_reads": fc.Lookups(), "calls": hits,
		"target_ns": 2000}, per < 2*time.Microsecond && fc.Lookups() == 1, fmt.Sprintf("%s per hit, %d reads", per, fc.Lookups()))
}

func round(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

func msf(d time.Duration) float64 { return round(float64(d.Microseconds()) / 1000) }
