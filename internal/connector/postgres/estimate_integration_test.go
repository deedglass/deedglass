//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
	"github.com/deedglass/deedglass/internal/ids"
)

func estimate(t *testing.T, ctx context.Context, c *Connector, in *connector.Instance, tool, args string) (connector.Estimate, error) {
	t.Helper()
	tools, err := c.Tools(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range tools {
		if s.Name == tool {
			return c.Estimate(ctx, in, connector.Call{ActionID: ids.New[ids.ActionKind](), OrgID: in.OrgID, AgentID: ids.New[ids.AgentKind](),
				Tool: s, Qualified: "postgres." + tool, Args: json.RawMessage(args), Deadline: time.Now().Add(30 * time.Second)})
		}
	}
	t.Fatalf("no tool %s", tool)
	return connector.Estimate{}, nil
}

// TestEstimate is S22 §3.4 on a real cluster: EXPLAIN only (nothing changes), inside a READ ONLY
// transaction as the agent role, the planner's rows, and every way an estimate is missing.
func TestEstimate(t *testing.T) {
	d := db(t)
	c := New(nil)
	defer c.Close()
	in := inst(t, c, pgtest.ConfigJSON, d.AgentDSN, "", devEnv())
	bg := context.Background()

	var seen struct {
		readOnly, user string
	}
	estimateConnHook = func(ctx context.Context, conn *pgx.Conn) {
		_ = conn.QueryRow(ctx, "SELECT pg_catalog.current_setting('transaction_read_only'), current_user").Scan(&seen.readOnly, &seen.user)
	}
	defer func() { estimateConnHook = nil }()

	admin := d.Admin(t)
	defer admin.Close(bg)
	var before string
	if err := admin.QueryRow(bg, "SELECT status FROM public.orders WHERE id = 42").Scan(&before); err != nil {
		t.Fatal(err)
	}

	e, err := estimate(t, bg, c, in, ToolExecute, `{"sql":"UPDATE orders SET status = 'shipped' WHERE id = $1","params":[42]}`)
	if err != nil || e.Method != connector.EstimateExplain || e.Rows != 1 || e.Took <= 0 {
		t.Fatalf("one row: %+v %v", e, err)
	}
	if seen.readOnly != "on" || seen.user != "deedglass_agent" {
		t.Fatalf("estimate ran with read_only=%q as %q", seen.readOnly, seen.user)
	}
	var after string
	if err := admin.QueryRow(bg, "SELECT status FROM public.orders WHERE id = 42").Scan(&after); err != nil || after != before {
		t.Fatalf("the estimate changed the row: %q → %q (%v)", before, after, err)
	}
	// Typical cost (S22 §3.12: hard cap 500 ms, typical < 10 ms), on a warm pool.
	var took []time.Duration
	for range 50 {
		e, err := estimate(t, bg, c, in, ToolExecute, `{"sql":"UPDATE orders SET status = 'shipped' WHERE id = $1","params":[7]}`)
		if err != nil || e.Method != connector.EstimateExplain {
			t.Fatalf("%+v %v", e, err)
		}
		took = append(took, e.Took)
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	t.Logf("PERF {\"benchmark\":\"TestEstimate\",\"estimates\":50,\"p50_ms\":%.2f,\"p99_ms\":%.2f,\"target_typical_ms\":10,\"cap_ms\":500}",
		float64(took[25].Microseconds())/1000, float64(took[49].Microseconds())/1000)
	if took[25] > 10*time.Millisecond {
		t.Errorf("typical estimate %v (10 ms)", took[25])
	}
	if e, err := estimate(t, bg, c, in, ToolExecute, `{"sql":"DELETE FROM refunds WHERE amount_cents > 0"}`); err != nil || e.Method != connector.EstimateExplain {
		t.Fatalf("delete: %+v %v", e, err)
	}
	if e, err := estimate(t, bg, c, in, ToolQuery, `{"sql":"SELECT id FROM wide"}`); err != nil || e.Method != connector.EstimateExplain || e.Rows < 100 {
		t.Fatalf("select: %+v %v", e, err)
	}

	// Missing estimates: refused by admission, a tool that takes no SQL, out of time, a planner error.
	for _, c2 := range []struct {
		tool, args, why string
	}{
		{ToolExecute, `{"sql":"DROP TABLE orders"}`, connector.EstimateRefused},
		{ToolExecute, `{"sql":"UPDATE orders SET status = 'x'; DELETE FROM refunds"}`, connector.EstimateRefused},
		{ToolExecute, `{"sql":"UPDATE nope SET a = 1"}`, connector.EstimateError},
	} {
		e, _ := estimate(t, bg, c, in, c2.tool, c2.args)
		if e.Method != connector.EstimateNone || e.Detail != c2.why {
			t.Errorf("%s: %+v", c2.args, e)
		}
	}
	tools, _ := c.Tools(bg, in)
	for _, s := range tools {
		if s.Name != ToolExecute && s.Name != ToolQuery {
			e, err := c.Estimate(bg, in, connector.Call{OrgID: in.OrgID, Tool: s, Args: json.RawMessage(`{}`)})
			if err != nil || e.Detail != connector.EstimateUnsupported {
				t.Errorf("%s: %+v %v", s.Name, e, err)
			}
			break
		}
	}
	gone, cancel := context.WithDeadline(bg, time.Now().Add(-time.Second))
	defer cancel()
	if e, _ := estimate(t, gone, c, in, ToolExecute, `{"sql":"UPDATE orders SET status = 'x' WHERE id = 1"}`); e.Detail != connector.EstimateTimeout {
		t.Errorf("past deadline: %+v", e)
	}
	// A slow plan: lock the table so planning waits, and the estimate gives up at the budget.
	tx, err := admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(bg)
	if _, err := tx.Exec(bg, "LOCK TABLE public.orders IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	e, _ = estimate(t, bg, c, in, ToolExecute, `{"sql":"UPDATE orders SET status = 'x' WHERE id = 1"}`)
	if e.Method != connector.EstimateNone || (e.Detail != connector.EstimateTimeout && e.Detail != connector.EstimateError) || time.Since(start) > 3*time.Second {
		t.Errorf("locked: %+v after %v", e, time.Since(start))
	}
}
