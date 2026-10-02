//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/witness/l2"
	"github.com/deedglass/deedglass/internal/witness/undo"
)

// TestUndoBudget measures S23 §3.10 on a real Postgres (make bench-undo): planning from evidence
// (1,000 rows), the preview of a 301-row undo (catalog, plan, guard rows, the checks), RunUndo for
// 301 and 1,000 rows (its whole transaction, which is when the row locks are held), and the undo
// commit until the decoder has its frame. Results go to DEEDGLASS_UNDO_BENCH_OUT as JSON.
func TestUndoBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("set DEEDGLASS_IT_BENCH=1 (make bench-undo)")
	}
	r := newUndoRig(t)
	r.exec(t, `CREATE TABLE public.ut_bench ("id" bigint PRIMARY KEY, "status" text NOT NULL, "total" numeric(12,2) NOT NULL, "updated_at" timestamptz NOT NULL)`)
	t.Cleanup(func() { _, _ = r.admin.Exec(context.Background(), "DROP TABLE IF EXISTS public.ut_bench") })
	for _, q := range []string{"ALTER TABLE public.ut_bench REPLICA IDENTITY FULL", "GRANT SELECT, INSERT, UPDATE, DELETE ON public.ut_bench TO deedglass_agent",
		"ALTER PUBLICATION deedglass_pub ADD TABLE public.ut_bench"} {
		r.exec(t, q)
	}
	r.exec(t, `INSERT INTO public.ut_bench SELECT g, 'paid', g * 1.25, '2026-09-01T00:00:00Z'::timestamptz + g * interval '1 minute' FROM generate_series(1, 1000) AS g`)
	ship := func(n int) *planned {
		a := r.act(t, fmt.Sprintf("ship %d", n), func(tx *postgres.UndoTestTx) error {
			_, _, err := tx.Exec(r.ctx, `UPDATE public.ut_bench SET status = 'shipped', updated_at = updated_at + interval '1 day' WHERE id <= $1`, int64(n))
			return err
		})
		return r.plan(t, a)
	}
	pct := func(ds []time.Duration, p float64) float64 {
		s := slices.Clone(ds)
		slices.Sort(s)
		return float64(s[int(float64(len(s)-1)*p)].Microseconds()) / 1000
	}
	out := map[string]any{"object": "bench", "name": "undo", "goos": runtime.GOOS, "goarch": runtime.GOARCH, "pg": r.ver,
		"at": time.Now().UTC().Format(time.RFC3339)}

	// 1. Plan 1,000 rows from evidence (pure: Collapse, Build, digest).
	p1000 := ship(1000)
	if p1000.plan == nil || p1000.plan.Rows != 1000 {
		t.Fatalf("the 1,000-row plan: %v", p1000.refusal())
	}
	var plans []time.Duration
	for i := 0; i < 50; i++ {
		t0 := time.Now()
		p, _ := undo.Build(undo.BuildInput{Undoes: p1000.ev.Action, ConnectorID: r.in.ID.String(), Evidence: p1000.ev, Catalog: p1000.cat})
		plans = append(plans, time.Since(t0))
		if p == nil {
			t.Fatal("no plan")
		}
	}
	out["plan_1000_ms"] = map[string]any{"p50": pct(plans, .5), "p95": pct(plans, .95), "target_ms": 20}
	// RunUndo 1,000 rows.
	t0 := time.Now()
	if _, _, err := r.undo(t, p1000.plan, runOpts); err != nil {
		t.Fatalf("undo 1,000: %v", err)
	}
	out["run_1000_ms"] = map[string]any{"value": float64(time.Since(t0).Microseconds()) / 1000, "target_ms": 1500}
	_ = r.capture(t, "")

	// 2. The 301-row hero: the preview, then RunUndo, then commit → frame.
	var previews, runs, frames []time.Duration
	for i := 0; i < 10; i++ {
		p := ship(301)
		if p.plan == nil || p.plan.Rows != 301 {
			t.Fatalf("the 301-row plan: %v", p.refusal())
		}
		for j := 0; j < 5; j++ {
			t0 := time.Now()
			relids := []uint32{}
			for _, ti := range p.ev.Tables {
				relids = append(relids, ti.RelID)
			}
			cat, err := r.c.UndoCatalog(r.ctx, r.in, relids)
			if err != nil {
				t.Fatal(err)
			}
			plan, fs := undo.Build(undo.BuildInput{Undoes: p.ev.Action, ConnectorID: r.in.ID.String(), Evidence: p.ev, Catalog: cat})
			cur, err := r.c.UndoRows(r.ctx, r.in, plan)
			if err != nil {
				t.Fatal(err)
			}
			_ = undo.Checks(undo.CheckInput{Plan: plan, Findings: fs, Catalog: cat, Current: cur, Evidence: p.ev, MaxRows: 1000})
			previews = append(previews, time.Since(t0))
		}
		t0 := time.Now()
		ua, _, err := r.undo(t, p.plan, runOpts)
		if err != nil {
			t.Fatalf("undo 301: %v", err)
		}
		committed := time.Now()
		runs = append(runs, committed.Sub(t0))
		deadline := time.Now().Add(10 * time.Second)
		for {
			fs := r.capture(t, ua.String())
			if len(fs) == 1 && fs[0].Class == l2.ClassBound {
				frames = append(frames, time.Since(committed))
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the undo's frame never arrived")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	out["preview_301_ms"] = map[string]any{"p50": pct(previews, .5), "p95": pct(previews, .95), "target_p95_ms": 150}
	out["run_301_ms"] = map[string]any{"p50": pct(runs, .5), "p95": pct(runs, .95), "target_ms": 300,
		"note": "the whole undo transaction (begin_action, catalog re-check, FOR UPDATE guards, 301 single-row steps, COMMIT): an upper bound on the lock hold"}
	out["commit_to_frame_ms"] = map[string]any{"p50": pct(frames, .5), "p95": pct(frames, .95),
		"note": "this rig's own peek loop, not the server's decoder; the server's commit → verdict is make bench-l2 (contract §12, p50 < 1 s)"}
	b, _ := json.MarshalIndent(out, "", "  ")
	t.Logf("%s", b)
	if f := os.Getenv("DEEDGLASS_UNDO_BENCH_OUT"); f != "" {
		if err := os.WriteFile(f, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
