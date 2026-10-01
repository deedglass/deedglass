//go:build integration

package ledger_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/ledger/fake"
	"github.com/deedglass/deedglass/internal/store"
)

type perf struct {
	Name   string  `json:"name"`
	N      int     `json:"n"`
	P50ms  float64 `json:"p50_ms"`
	P99ms  float64 `json:"p99_ms"`
	MeanMs float64 `json:"mean_ms"`
	Budget float64 `json:"budget_ms"` // p99, or the total for one-shot operations
	Within bool    `json:"within_budget"`
	Extra  any     `json:"extra,omitempty"`
}

func msf(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func measure(t *testing.T, name string, n int, budgetP99 float64, fn func(i int) error) perf {
	t.Helper()
	for i := range n / 10 { // warm up
		if err := fn(i); err != nil {
			t.Fatal(err)
		}
	}
	ds := make([]time.Duration, n)
	var sum time.Duration
	for i := range ds {
		start := time.Now()
		if err := fn(i); err != nil {
			t.Fatal(err)
		}
		ds[i] = time.Since(start)
		sum += ds[i]
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	r := perf{Name: name, N: n, P50ms: msf(ds[n/2]), P99ms: msf(ds[n*99/100]), MeanMs: msf(sum / time.Duration(n)), Budget: budgetP99}
	r.Within = r.P99ms < budgetP99
	return r
}

func once(name string, budget time.Duration, d time.Duration, extra any) perf {
	return perf{Name: name, N: 1, P50ms: msf(d), P99ms: msf(d), MeanMs: msf(d), Budget: msf(budget), Within: d < budget, Extra: extra}
}

// TestLedgerBudget measures S06 §3.10 at 100,000 actions in one org on the local cluster. It runs
// only with DEEDGLASS_IT_BENCH=1 (make bench-ledger) and writes one PERF JSON line per operation,
// plus the EXPLAIN plans, to the test log.
func TestLedgerBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("set DEEDGLASS_IT_BENCH=1 (make bench-ledger)")
	}
	n := 100_000
	if s := os.Getenv("DEEDGLASS_IT_BENCH_N"); s != "" {
		fmt.Sscan(s, &n)
	}
	en := newEnv(t)
	var results []perf
	report := func(p perf) {
		results = append(results, p)
		b, _ := json.Marshal(p)
		t.Logf("PERF %s", b)
	}

	// Seeding: fake-action --n 100000 --batch 500 (budget 3 min).
	start := time.Now()
	res, err := fake.Run(ctx, en.led, en.org, en.agents, fake.Generate(42, n, nil, 3), fake.Options{Batch: 500})
	if err != nil {
		t.Fatal(err)
	}
	report(once("fake_action_batch500", 3*time.Minute, time.Since(start), map[string]any{"actions": res.Actions, "deeds": res.Deeds}))
	if _, err := en.e.Admin(t).Exec(ctx, "VACUUM ANALYZE"); err != nil {
		t.Fatal(err)
	}

	list := func(q string) func(int) error {
		v, _ := url.ParseQuery(q)
		f, p, err := ledger.ParseActionQuery(v)
		if err != nil {
			t.Fatal(err)
		}
		return func(int) error {
			l, err := en.led.ListActions(ctx, en.org, f, p)
			if err == nil && len(l.Data) == 0 {
				err = fmt.Errorf("%s: empty page", q)
			}
			return err
		}
	}
	report(measure(t, "list_actions_no_filter_limit100", 300, 50, list("limit=100")))
	report(measure(t, "list_actions_verdict_limit100", 300, 80, list("verdict=MISMATCH&limit=100")))
	report(measure(t, "list_actions_agent_limit100", 300, 80, list("agent="+en.agents[1].ID.String()+"&limit=100")))
	report(measure(t, "list_actions_q_limit100", 300, 150, list("q=refund&limit=100")))
	report(measure(t, "list_actions_q_rare_limit100", 100, 150, list("q=upstream&limit=100")))

	r := rand.New(rand.NewPCG(1, 2))
	report(measure(t, "get_action_with_events_and_deeds", 500, 20, func(int) error {
		_, err := en.led.GetAction(ctx, en.org, res.IDs[r.IntN(len(res.IDs))])
		return err
	}))

	start = time.Now()
	c, err := en.led.CheckChain(ctx, en.org, ledger.Range{}, ledger.CheckOptions{})
	if err != nil || c.Status != ledger.CheckIntact {
		t.Fatalf("%+v %v", c, err)
	}
	report(once("check_chain_whole_org", 5*time.Second, time.Since(start), map[string]any{"deeds": c.Checked}))

	// Export to io.Discard while sampling the heap: flat memory.
	var peak atomic.Uint64
	var base runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&base)
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.Load() {
					peak.Store(m.HeapAlloc)
				}
			}
		}
	}()
	start = time.Now()
	cw := &countW{}
	m, err := en.led.Export(ctx, en.org, ledger.Range{}, cw)
	took := time.Since(start)
	close(stop)
	if err != nil {
		t.Fatal(err)
	}
	growth := int64(peak.Load()) - int64(base.HeapAlloc)
	report(once("export_whole_org", 10*time.Second, took, map[string]any{"deeds": m.Range.Count, "bytes": cw.n,
		"heap_growth_mib": float64(growth) / (1 << 20), "heap_budget_mib": 64, "heap_within": growth < 64<<20}))

	// SealAction vs the bare Sealer (S04): what reading the snapshot, setting deed_id and the
	// sealed transition add.
	prep := func() string {
		var id string
		err := en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
			a, err := en.led.CreateAction(ctx, tx, en.org, ledger.NewAction{Agent: en.agents[0].ID, Mode: "gateway", Tool: "orders.get"})
			if err != nil {
				return err
			}
			id = a.String()
			for _, e := range [][2]gate.State{{gate.Received, gate.Authenticated}, {gate.Authenticated, gate.Evaluated}, {gate.Evaluated, gate.Denied}} {
				if err := en.led.Transition(ctx, tx, id, e[0], e[1], ledger.EventData{}); err != nil {
					return err
				}
			}
			_, err = tx.Exec(ctx, `UPDATE deedglass.actions SET decision = 'deny' WHERE id = $1`, id)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	timed := func(fn func(ctx context.Context, tx pgx.Tx) error) time.Duration {
		var d time.Duration
		err := en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
			s := time.Now()
			err := fn(ctx, tx)
			d = time.Since(s)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	var sealDs, bareDs []time.Duration
	sealer := deed.NewSealer(en.signer)
	for range 300 {
		id := prep()
		sealDs = append(sealDs, timed(func(ctx context.Context, tx pgx.Tx) error { _, err := en.led.SealAction(ctx, tx, id); return err }))
		bareDs = append(bareDs, timed(func(ctx context.Context, tx pgx.Tx) error {
			d, err := deed.ForAdmin(deed.AdminSnapshot{Org: en.org.String(), Event: ids.New[ids.AdminEventKind]().String(),
				Action: "ledger.pruned", Actor: deed.Ref{Type: "system", ID: "operator"}})
			if err != nil {
				return err
			}
			_, err = sealer.Seal(ctx, storeDeedTx(tx), d)
			return err
		}))
	}
	p99 := func(ds []time.Duration) time.Duration {
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)*99/100]
	}
	added := p99(sealDs) - p99(bareDs)
	report(perf{Name: "seal_action_added_p99", N: 300, P99ms: msf(added), Budget: 2, Within: added < 2*time.Millisecond,
		Extra: map[string]float64{"seal_action_p99_ms": msf(p99(sealDs)), "bare_sealer_p99_ms": msf(p99(bareDs))}})

	// EXPLAIN (ANALYZE, BUFFERS) for the three list shapes, as the runtime role inside an org tx.
	for _, shape := range []struct{ name, q string }{
		{"no_filter", "limit=100"}, {"verdict", "verdict=MISMATCH&limit=100"}, {"agent", "agent=" + en.agents[1].ID.String() + "&limit=100"},
	} {
		v, _ := url.ParseQuery(shape.q)
		f, _, _ := ledger.ParseActionQuery(v)
		sql, args := ledger.ListSQL(en.org, f, 100)
		var plan []string
		tx(t, en, en.org, func(tx pgx.Tx) {
			rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
			if err != nil {
				t.Fatal(err)
			}
			if plan, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
				t.Fatal(err)
			}
		})
		text := strings.Join(plan, "\n")
		t.Logf("EXPLAIN %s:\n%s", shape.name, text)
		if strings.Contains(text, "Sort Method") && !strings.Contains(text, "Index") {
			t.Errorf("%s sorts the org instead of reading an index:\n%s", shape.name, text)
		}
	}
	for _, p := range results {
		if !p.Within {
			t.Errorf("over budget: %s p99 %.2f ms (budget %.0f ms)", p.Name, p.P99ms, p.Budget)
		}
	}
}

type countW struct{ n int64 }

func (c *countW) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

var _ io.Writer = (*countW)(nil)

func storeDeedTx(tx pgx.Tx) deed.ChainStore { return store.DeedTx(tx) }
