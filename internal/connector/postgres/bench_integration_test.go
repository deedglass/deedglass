//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/ids"
)

// TestPostgresBudget is S17 §3.11's numbers (make bench-postgres): the connector's extra latency
// over raw pgx for a one-row UPDATE on localhost (target p50 ≤ 1.5 ms extra), and 100 writes/s for
// 30 s (target 0 errors, p99 < 50 ms). Raw JSON goes to DEEDGLASS_PG_BENCH_OUT.
func TestPostgresBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("set DEEDGLASS_IT_BENCH=1 (make bench-postgres)")
	}
	d := db(t)
	ctx := context.Background()
	c := New(nil)
	defer c.Close()
	in := inst(t, c, benchConfig(), d.AgentDSN, "", devEnv())
	tools, _ := c.Tools(ctx, in)
	var exec connector.ToolSpec
	for _, s := range tools {
		if s.Name == ToolExecute {
			exec = s
		}
	}
	callOnce := func() (time.Duration, error) {
		args := `{"sql":"UPDATE orders SET total_cents = total_cents + 1 WHERE id = $1","params":[5]}`
		start := time.Now()
		_, err := c.Execute(ctx, in, connector.Call{ActionID: ids.New[ids.ActionKind](), OrgID: in.OrgID, AgentID: ids.New[ids.AgentKind](), Tool: exec,
			Qualified: "postgres.execute", Args: json.RawMessage(args), Deadline: time.Now().Add(30 * time.Second)})
		return time.Since(start), err
	}
	// raw pgx: the same statement in its own transaction, extended protocol, one connection
	raw, err := pgx.Connect(ctx, d.AgentDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close(ctx)
	rawOnce := func() (time.Duration, error) {
		start := time.Now()
		tx, err := raw.Begin(ctx)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, "UPDATE orders SET total_cents = total_cents + 1 WHERE id = $1", pgx.QueryExecModeDescribeExec, 6); err != nil {
			_ = tx.Rollback(ctx)
			return 0, err
		}
		err = tx.Commit(ctx)
		return time.Since(start), err
	}
	const n = 2000
	for range 100 { // warm both paths
		_, _ = callOnce()
		_, _ = rawOnce()
	}
	measure := func(f func() (time.Duration, error)) []time.Duration {
		out := make([]time.Duration, 0, n)
		for range n {
			d, err := f()
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, d)
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	q := func(s []time.Duration, p float64) float64 {
		return float64(s[int(float64(len(s)-1)*p)].Microseconds()) / 1000
	}
	rawD := measure(rawOnce)
	conD := measure(callOnce)
	res := map[string]any{"n": n, "raw_p50_ms": q(rawD, .5), "raw_p99_ms": q(rawD, .99), "connector_p50_ms": q(conD, .5), "connector_p99_ms": q(conD, .99),
		"extra_p50_ms": q(conD, .5) - q(rawD, .5), "target_extra_p50_ms": 1.5}
	t.Logf("PERF %s", mustJSON(res))
	// 100 writes/s for 30 s, spread over 4 workers, each on its own row.
	dur := 30 * time.Second
	if v := os.Getenv("DEEDGLASS_PG_LOAD_SECONDS"); v != "" {
		var s int
		fmt.Sscan(v, &s)
		dur = time.Duration(s) * time.Second
	}
	var mu sync.Mutex
	var lat []time.Duration
	var errs int
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.Now().Add(dur)
	var wg sync.WaitGroup
	i := 0
	for time.Now().Before(deadline) {
		<-tick.C
		i++
		row := 20 + i%4
		wg.Add(1)
		go func() {
			defer wg.Done()
			args := fmt.Sprintf(`{"sql":"UPDATE orders SET total_cents = total_cents + 1 WHERE id = $1","params":[%d]}`, row)
			start := time.Now()
			_, err := c.Execute(ctx, in, connector.Call{ActionID: ids.New[ids.ActionKind](), OrgID: in.OrgID, AgentID: ids.New[ids.AgentKind](), Tool: exec,
				Qualified: "postgres.execute", Args: json.RawMessage(args), Deadline: time.Now().Add(30 * time.Second)})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs++
				return
			}
			lat = append(lat, time.Since(start))
		}()
	}
	wg.Wait()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	load := map[string]any{"writes": i, "seconds": dur.Seconds(), "errors": errs, "p50_ms": q(lat, .5), "p99_ms": q(lat, .99), "target_p99_ms": 50}
	t.Logf("PERF %s", mustJSON(load))
	if out := os.Getenv("DEEDGLASS_PG_BENCH_OUT"); out != "" {
		b, _ := json.MarshalIndent(map[string]any{"date": time.Now().UTC().Format(time.RFC3339), "overhead": res, "load": load,
			"note": "localhost, one throwaway cluster shared with nothing else; laptop numbers, not a capacity claim"}, "", "  ")
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if errs > 0 {
		t.Errorf("%d errors under load", errs)
	}
	if q(lat, .99) >= 50 {
		t.Errorf("load p99 %.2f ms ≥ 50 ms", q(lat, .99))
	}
	if extra := q(conD, .5) - q(rawD, .5); extra > 1.5 {
		t.Errorf("extra p50 %.3f ms > 1.5 ms", extra)
	}
}

func benchConfig() string { return withCfg(func(m map[string]any) { m["max_connections"] = 8 }) }

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
