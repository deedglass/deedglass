//go:build bench

package policy

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/sqlguard"
)

// TestLatency is the p99 harness of S11 §3.7 (Go benchmarks report means only). It runs a
// realistic mix through Evaluate and sqlguard.Analyze and writes p50/p90/p99/max to
// $DEEDGLASS_BENCH_OUT (make bench-eval: bench/results/<date>-<os>-<arch>-eval.json).
func TestLatency(t *testing.T) {
	ctx := context.Background()
	ps := postgresSafe(t)
	mix := []Input{
		sqlIn("postgres.query", "SELECT id, status FROM public.orders WHERE id = 42"),
		sqlIn("postgres.query", "SELECT o.id, c.name FROM orders o JOIN customers c ON c.id = o.customer_id LIMIT 20"),
		sqlIn("postgres.execute", "/* tidy up */ DrOp TABLE public.orders"),
		sqlIn("postgres.execute", "UPDATE public.orders SET status = 'shipped' WHERE id = 42"),
		sqlIn("postgres.execute", "INSERT INTO public.support_notes (order_id, body) VALUES (42, 'Called')"),
		sqlIn("postgres.execute", "COMMIT; DROP TABLE orders"),
	}
	p50 := mustCompile(t, evalBenchPolicy(50))
	p500 := mustCompile(t, evalBenchPolicy(500))
	type result struct {
		Name  string  `json:"name"`
		N     int     `json:"n"`
		P50us float64 `json:"p50_us"`
		P90us float64 `json:"p90_us"`
		P99us float64 `json:"p99_us"`
		MaxUs float64 `json:"max_us"`
		Limit string  `json:"budget,omitempty"`
	}
	measure := func(name string, n int, limit string, fn func(i int)) result {
		for i := 0; i < n/10; i++ { // warm up
			fn(i)
		}
		runtime.GC()
		ds := make([]time.Duration, n)
		for i := 0; i < n; i++ {
			st := time.Now()
			fn(i)
			ds[i] = time.Since(st)
		}
		slices.Sort(ds)
		us := func(q float64) float64 {
			return float64(ds[min(n-1, int(q*float64(n)))].Nanoseconds()) / 1000
		}
		r := result{Name: name, N: n, P50us: us(0.50), P90us: us(0.90), P99us: us(0.99), MaxUs: float64(ds[n-1].Nanoseconds()) / 1000, Limit: limit}
		t.Logf("%-28s n=%-7d p50 %8.1f µs  p90 %8.1f µs  p99 %8.1f µs  max %9.1f µs", name, n, r.P50us, r.P90us, r.P99us, r.MaxUs)
		return r
	}
	var out []result
	out = append(out, measure("evaluate_postgres_safe_mix", 100_000, "", func(i int) { Evaluate(ctx, ps, mix[i%len(mix)], EvalOptions{}) }))
	out = append(out, measure("evaluate_postgres_safe_explain", 100_000, "", func(i int) { Evaluate(ctx, ps, mix[i%len(mix)], EvalOptions{Explain: true}) }))
	out = append(out, measure("evaluate_50_rules", 100_000, "p50 < 60 µs, p99 < 250 µs (contract < 1 ms)", func(i int) { Evaluate(ctx, p50, benchSQL, EvalOptions{}) }))
	out = append(out, measure("evaluate_50_rules_explain", 100_000, "≤ 3× without explain", func(i int) { Evaluate(ctx, p50, benchSQL, EvalOptions{Explain: true}) }))
	out = append(out, measure("evaluate_500_rules", 20_000, "p99 < 1 ms", func(i int) { Evaluate(ctx, p500, benchSQL, EvalOptions{}) }))
	out = append(out, measure("evaluate_500_rules_explain", 20_000, "", func(i int) { Evaluate(ctx, p500, benchSQL, EvalOptions{Explain: true}) }))
	for _, sz := range []struct {
		name  string
		bytes int
		n     int
		limit string
	}{{"analyze_200b", 200, 100_000, "< 5 µs"}, {"analyze_4kib", 4 << 10, 20_000, "< 40 µs"}, {"analyze_256kib", 256<<10 - 64, 300, "< 5 ms"}} {
		s := latencySQL(sz.bytes)
		out = append(out, measure(sz.name, sz.n, sz.limit, func(int) { sqlguard.Analyze(s, sqlguard.Options{}) }))
	}
	host, _ := os.Hostname()
	doc := map[string]any{"session": "S11", "date": time.Now().UTC().Format(time.RFC3339), "host": host,
		"go": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH, "cpus": runtime.NumCPU(), "results": out}
	if p := os.Getenv("DEEDGLASS_BENCH_OUT"); p != "" {
		b, _ := json.MarshalIndent(doc, "", "  ")
		if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range out {
		if r.Name == "evaluate_50_rules" && r.P99us > 1000 {
			t.Errorf("Evaluate at 50 rules: p99 %.0f µs is over the contract's 1 ms", r.P99us)
		}
	}
}

func latencySQL(n int) string {
	var b strings.Builder
	b.WriteString("SELECT o.id, o.status, c.name FROM public.orders o JOIN public.customers c ON c.id = o.customer_id WHERE o.id IN (")
	for i := 0; b.Len() < n-40; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("4242")
	}
	b.WriteString(") AND o.status = 'open' ORDER BY o.id")
	return b.String()
}
