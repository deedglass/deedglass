//go:build integration

package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// budgetPolicy is a 50-rule policy whose rules all run CEL for fake.echo (S13 §6: the evaluation
// inside the pipeline must keep S11's p99 under 1 ms at 50 rules).
func budgetPolicy() string {
	var b strings.Builder
	b.WriteString(strings.SplitN(testPolicy, "rules:\n", 2)[0])
	b.WriteString("rules:\n")
	for i := range 49 {
		fmt.Fprintf(&b, "  - id: never-%02d\n    match: { tool: [fake.echo] }\n    when: has(args.n) && args.n == %d && args.n > 1000000\n    decision: deny\n    reason: Never matches.\n", i, -i)
	}
	b.WriteString("  - id: allow-fake\n    match: { tool: [\"fake.*\"] }\n    decision: allow\n    reason: Fake tools are safe to run.\n")
	return b.String()
}

// TestGateBudget is S13's overhead budget (contract §12): an allowed action with no witness work
// (fake.echo, latency 0), 8 at a time, one org, on a local Postgres 18: p50 < 5 ms and p99 < 25 ms
// for the whole pipeline (normalize, decide, record, execute, L0, seal). make bench-gate runs it
// and keeps the raw output in bench/results/.
func TestGateBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("a budget run: make bench-gate")
	}
	w := newWorld(t, worldOpts{agents: 8, maxConns: 32, policy: budgetPolicy()})
	const warm, n = 200, 4000
	conc := 8
	if c := os.Getenv("DEEDGLASS_IT_CONC"); c != "" { // experiments only; the budget is at 8
		fmt.Sscan(c, &conc)
	}
	run := func(count int) []time.Duration {
		out := make([]time.Duration, 0, count)
		var mu sync.Mutex
		var wg sync.WaitGroup
		per := count / conc
		for g := range conc {
			wg.Go(func() {
				local := make([]time.Duration, 0, per)
				for i := range per {
					t0 := time.Now()
					a, err := w.submit(g, req("fake.echo", fmt.Sprintf(`{"n":%d}`, i)))
					local = append(local, time.Since(t0))
					if err != nil || a.State != "sealed" {
						t.Errorf("%v %v", err, a)
						return
					}
				}
				mu.Lock()
				out = append(out, local...)
				mu.Unlock()
			})
		}
		wg.Wait()
		return out
	}
	run(warm)
	start := time.Now()
	lat := run(n)
	elapsed := time.Since(start)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p50, p99 := lat[len(lat)/2], lat[len(lat)*99/100]
	// The policy evaluation inside the pipeline, as the pipeline measured it (decision_detail.elapsed_us).
	var evals []int64
	_ = w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT (decision_detail->>'elapsed_us')::bigint FROM deedglass.actions ORDER BY created_at DESC LIMIT $1`, n)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v int64
			if err := rows.Scan(&v); err != nil {
				return err
			}
			evals = append(evals, v)
		}
		return rows.Err()
	})
	sort.Slice(evals, func(i, j int) bool { return evals[i] < evals[j] })
	evalP99 := evals[len(evals)*99/100]
	perf := map[string]any{"benchmark": "BenchmarkGateAllowNoWitness", "actions": len(lat), "concurrency": conc, "orgs": 1, "rules": 50,
		"p50_ms": ms(p50), "p90_ms": ms(lat[len(lat)*90/100]), "p99_ms": ms(p99), "max_ms": ms(lat[len(lat)-1]),
		"ops_per_s": float64(len(lat)) / elapsed.Seconds(), "eval_p99_us": evalP99, "target_p50_ms": 5, "target_p99_ms": 25,
		"target_eval_p99_us": 1000, "race": raceEnabled}
	b, _ := json.Marshal(perf)
	t.Logf("PERF %s", b)
	if p50 > 5*time.Millisecond || p99 > 25*time.Millisecond || evalP99 > 1000 {
		t.Errorf("over budget: p50 %s (5 ms), p99 %s (25 ms), eval p99 %d µs (1,000)", p50, p99, evalP99)
	}
}

// BenchmarkGateAllowNoWitness is the same path as a Go benchmark (go test -tags integration -bench).
func BenchmarkGateAllowNoWitness(b *testing.B) {
	w := newWorld(b, worldOpts{agents: 8, maxConns: 32, policy: budgetPolicy()})
	b.SetParallelism(1)
	b.ResetTimer()
	var i int64
	var mu sync.Mutex
	b.RunParallel(func(pb *testing.PB) {
		mu.Lock()
		g := int(i % 8)
		i++
		mu.Unlock()
		for pb.Next() {
			if _, err := w.p.Submit(bg, w.pr(g), req("fake.echo", `{"n":1}`)); err != nil {
				b.Fatal(err)
			}
		}
	})
}
