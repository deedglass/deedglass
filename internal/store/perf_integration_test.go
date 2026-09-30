//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// The RLS policy's org setting is evaluated once per query (an InitPlan), not once per row.
func TestExplainShowsInitPlan(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	a := storetest.NewOrg(t, e.DB, "alpha")
	tx(t, e.DB, a.ID, func(tx pgx.Tx) {
		for i := 0; i < 60; i++ {
			if _, err := store.CreateAgent(ctx, tx, a.ID, store.NewAgent{Name: fmt.Sprintf("bot-%02d", i)}); err != nil {
				t.Fatal(err)
			}
		}
	})
	var plan []string
	tx(t, e.DB, a.ID, func(tx pgx.Tx) {
		rows, err := tx.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF, SUMMARY OFF)
		    SELECT id, org_id, name, description, status, labels, created_at FROM deedglass.agents
		    WHERE org_id = $1 ORDER BY created_at DESC, id DESC LIMIT 51`, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		plan, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
	})
	text := strings.Join(plan, "\n")
	if !strings.Contains(text, "InitPlan") {
		t.Fatalf("no InitPlan for the org setting:\n%s", text)
	}
	t.Logf("EXPLAIN ListAgents (limit 50) under OrgTx:\n%s", text)
}

type perf struct {
	Name   string  `json:"name"`
	N      int     `json:"n"`
	P50ms  float64 `json:"p50_ms"`
	P99ms  float64 `json:"p99_ms"`
	MeanMs float64 `json:"mean_ms"`
	BudP50 float64 `json:"budget_p50_ms"`
	BudP99 float64 `json:"budget_p99_ms"`
	Within bool    `json:"within_budget"`
}

func measure(t *testing.T, name string, n int, p50, p99 float64, fn func() error) perf {
	t.Helper()
	for i := 0; i < n/10; i++ { // warm up: pool, plan cache
		if err := fn(); err != nil {
			t.Fatal(err)
		}
	}
	ds := make([]time.Duration, n)
	var sum time.Duration
	for i := range ds {
		start := time.Now()
		if err := fn(); err != nil {
			t.Fatal(err)
		}
		ds[i] = time.Since(start)
		sum += ds[i]
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	msf := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	r := perf{Name: name, N: n, P50ms: msf(ds[n/2]), P99ms: msf(ds[n*99/100]), MeanMs: msf(sum / time.Duration(n)), BudP50: p50, BudP99: p99}
	r.Within = r.P50ms < p50 && r.P99ms < p99
	return r
}

// TestPerfBudget measures S02 §3.9 on the local cluster. It runs only with DEEDGLASS_IT_BENCH=1
// (make bench-store), and writes one JSON line per operation to the test log.
func TestPerfBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("set DEEDGLASS_IT_BENCH=1 (make bench-store)")
	}
	e := storetest.New(t, storetest.Options{})
	a := storetest.NewOrg(t, e.DB, "alpha")
	storetest.NewOrg(t, e.DB, "bravo") // a second org, so RLS has something to hide
	tx(t, e.DB, a.ID, func(tx pgx.Tx) {
		for i := 0; i < 60; i++ {
			if _, err := store.CreateAgent(ctx, tx, a.ID, store.NewAgent{Name: fmt.Sprintf("bot-%02d", i)}); err != nil {
				t.Fatal(err)
			}
		}
	})
	const n = 2000
	var ver string
	_ = e.Admin(t).QueryRow(ctx, "SELECT pg_catalog.current_setting('server_version')").Scan(&ver)
	results := []perf{
		measure(t, "OrgTx no-op round trip", n, 1, 3, func() error {
			return e.DB.OrgTx(ctx, a.ID, func(context.Context, pgx.Tx) error { return nil })
		}),
		measure(t, "Insert an action + its event in one OrgTx", n, 2, 5, func() error {
			return e.DB.OrgTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error {
				id, err := storetest.NewAction(ctx, tx, a.ID, a.Agent)
				if err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `INSERT INTO deedglass.action_events (id, org_id, action_id, seq, to_state, actor) VALUES ($1, $2, $3, 1, 'received', 'system')`,
					ids.New[ids.ActionEventKind](), a.ID, id)
				return err
			})
		}),
		measure(t, "Agents.List (50 rows, RLS)", n, 2, 5, func() error {
			return e.DB.OrgReadTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error {
				as, _, err := store.ListAgents(ctx, tx, a.ID, 50, nil)
				if err == nil && len(as) != 50 {
					err = fmt.Errorf("%d rows", len(as))
				}
				return err
			})
		}),
	}
	for _, r := range results {
		b, _ := json.Marshal(struct {
			perf
			Server string `json:"server"`
		}{r, "PostgreSQL " + ver})
		t.Logf("PERF %s", b)
		if !r.Within {
			t.Errorf("%s: p50 %.3f ms, p99 %.3f ms; budget %.0f / %.0f ms", r.Name, r.P50ms, r.P99ms, r.BudP50, r.BudP99)
		}
	}
}
