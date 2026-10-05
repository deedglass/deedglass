package load

import (
	"context"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector/schema"
)

// TestHistogramAccuracy: 10⁶ log-normal samples; p50, p99 and p99.9 within 1% of the exact values.
func TestHistogramAccuracy(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	const n = 1_000_000
	xs := make([]time.Duration, n)
	h := NewHist()
	for i := range xs {
		// median ≈ 4 ms, a long tail into hundreds of ms
		xs[i] = time.Duration(math.Exp(r.NormFloat64()*1.1+math.Log(4e6))) + time.Microsecond
		h.Record(xs[i])
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	for _, q := range []float64{0.5, 0.9, 0.99, 0.999} {
		exact := xs[int(math.Ceil(q*n))-1]
		got := h.Quantile(q)
		if e := math.Abs(float64(got-exact)) / float64(exact); e > 0.01 {
			t.Errorf("q%.3f: got %v, exact %v (%.2f%% off)", q, got, exact, e*100)
		}
	}
	if h.Max() != xs[n-1] || h.Count() != n {
		t.Errorf("max %v count %d", h.Max(), h.Count())
	}
}

func TestHistogramEdges(t *testing.T) {
	h := NewHist()
	if h.Quantile(0.5) != 0 || h.Mean() != 0 || h.Max() != 0 {
		t.Fatal("empty histogram")
	}
	h.Record(0)
	h.Record(2 * time.Minute) // above the range: the last bucket, the exact max kept
	if h.Quantile(1) != 2*time.Minute {
		t.Fatalf("q1 = %v", h.Quantile(1))
	}
	for b := 0; b < numBuckets; b++ {
		lo, hi := bounds(b)
		if hi <= lo {
			t.Fatalf("bucket %d empty range", b)
		}
		if mid := lo + (hi-lo)/2; b >= subBuckets && bucketOf(mid) != b {
			t.Fatalf("bucket %d: its midpoint %v maps to %d", b, mid, bucketOf(mid))
		}
	}
	o := NewHist()
	o.Record(time.Millisecond)
	h.Merge(o)
	if h.Count() != 3 {
		t.Fatal("merge")
	}
}

// TestOpenLoopSleep: a target that takes 5 ms, at 100/s, reports a p50 of about 5 ms.
func TestOpenLoopSleep(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	rs, err := Run(context.Background(), Plan{Rate: 100, Warmup: 300 * time.Millisecond, Duration: 2 * time.Second, Runs: 1},
		TargetFunc(func(ctx context.Context, i int) (Outcome, error) {
			time.Sleep(5 * time.Millisecond)
			return Outcome{Status: "sealed"}, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	r := rs[0]
	if r.Count < 190 || r.Count > 210 || r.Errors != 0 || r.Statuses["sealed"] != r.Count {
		t.Fatalf("%+v", r)
	}
	if r.P50 < 5*time.Millisecond || r.P50 > 9*time.Millisecond {
		t.Fatalf("p50 %v, want about 5 ms", r.P50)
	}
	if r.AchievedRate < 85 || r.AchievedRate > 115 {
		t.Fatalf("achieved %.1f/s", r.AchievedRate)
	}
}

// TestStallShowsInTail: a one-at-a-time server stalls 1 s once at 100/s. Every request queued
// behind it is counted from its intended time, so p99 shows the stall; a closed-loop client would
// have recorded one slow request and simply sent fewer (coordinated omission).
func TestStallShowsInTail(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	var mu sync.Mutex
	rs, err := Run(context.Background(), Plan{Rate: 100, Warmup: -1, Duration: 3 * time.Second, Runs: 1, MaxInFlight: 512},
		TargetFunc(func(ctx context.Context, i int) (Outcome, error) {
			mu.Lock()
			defer mu.Unlock()
			if i == 50 {
				time.Sleep(time.Second)
			}
			time.Sleep(200 * time.Microsecond)
			return Outcome{Status: "sealed"}, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	r := rs[0]
	if r.Count != 300 {
		t.Fatalf("count %d: every intended request must be counted", r.Count)
	}
	if r.P99 < 900*time.Millisecond || r.Max < time.Second {
		t.Fatalf("p99 %v max %v: the stall must show in the tail", r.P99, r.Max)
	}
	if r.P50 > 5*time.Millisecond {
		t.Fatalf("p50 %v: requests before the stall were fast", r.P50)
	}
	slow := 0
	for _, d := range []float64{0.6, 0.7} {
		if r.Hist.Quantile(d) > 100*time.Millisecond {
			slow++
		}
	}
	if slow == 0 {
		t.Fatal("about a third of the requests queued behind the stall; their wait must count")
	}
}

func TestErrorsCount(t *testing.T) {
	rs, err := Run(context.Background(), Plan{Rate: 200, Warmup: -1, Duration: 200 * time.Millisecond, Runs: 2},
		TargetFunc(func(ctx context.Context, i int) (Outcome, error) {
			if i%2 == 0 {
				return Outcome{}, context.DeadlineExceeded
			}
			return Outcome{Status: "denied"}, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.Errors != r.Count/2 || r.Statuses["error"] != r.Errors || r.Statuses["denied"] != r.Count-r.Errors {
			t.Fatalf("%+v", r)
		}
	}
}

// --- results, check, report ---

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range SourceDirs {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	_ = os.WriteFile(filepath.Join(root, "internal/gate/gate.go"), []byte("package gate\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "bench", "results"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "docs", "BENCHMARKS.md"), []byte("# Benchmarks\n\n"+BeginMarker+"\nold\n"+EndMarker+"\n\nAfter.\n"), 0o644)
	return root
}

func latencyRuns(p50, p99 float64) []RunJSON {
	var out []RunJSON
	for i := 0; i < 3; i++ {
		out = append(out, RunJSON{Count: 12000, P50ms: p50, P99ms: p99, MaxMs: p99 * 2, Statuses: map[string]int{"sealed": 12000}})
	}
	return out
}

func valueRuns(kv map[string]any) []RunJSON {
	return []RunJSON{{Count: 1, Values: kv}, {Count: 1, Values: kv}, {Count: 1, Values: kv}}
}

func writeAll(t *testing.T, root string, power string, mutate func(f *File)) {
	t.Helper()
	sh, _ := SourceHash(root)
	host := Host{Name: "testhost", CPU: "Test CPU", Cores: 8, MemGB: 8, OS: "Darwin", Power: power}
	mk := func(suite string, sc Scenario) {
		f := &File{Schema: Schema, Suite: suite, StartedAt: "2026-10-12T05:41:09Z", Commit: "abc", SourceHash: sh, Version: "0.1.0-rc.1",
			Host: host, Go: "go1.27.1", Postgres: &Postgres{Version: "18.6", Mode: "native"}, Scenarios: []Scenario{sc}, Caveats: []string{"x"}}
		if mutate != nil {
			mutate(f)
		}
		if _, err := f.Write(root); err != nil {
			t.Fatal(err)
		}
	}
	mk("gate", Scenario{Name: "actions.allow", Runs: latencyRuns(3.1, 14.2), Targets: []Goal{
		{Metric: "p50_ms", Op: "lt", Value: 5, Label: "p50 < 5 ms"}, {Metric: "p99_ms", Op: "lt", Value: 25, Label: "p99 < 25 ms"}}})
	mk("policy", Scenario{Name: "evaluate.50_rules", Runs: latencyRuns(0.04, 0.12), Targets: []Goal{{Metric: "p99_ms", Op: "lt", Value: 1, Label: "p99 < 1 ms"}}})
	mk("witness", Scenario{Name: "l2.100_per_s", Runs: latencyRuns(180, 420), Targets: []Goal{{Metric: "p50_ms", Op: "lt", Value: 1000, Label: "p50 < 1 s"}}})
	mk("verify", Scenario{Name: "verify.100k_jsonl", Runs: valueRuns(map[string]any{"wall_s": 1.9, "peak_rss_mib": 61.0}), Targets: []Goal{{Metric: "wall_s", Op: "lt", Value: 5, Label: "< 5 s"}}})
	mk("stop", Scenario{Name: "stop.across_instances", Runs: valueRuns(map[string]any{"max_ms": 640.0, "trials": 20.0}), Targets: []Goal{{Metric: "max_ms", Op: "lt", Value: 2000, Label: "< 2 s"}}})
	mk("quickstart", Scenario{Name: "quickstart.clean_dir", Runs: valueRuns(map[string]any{"wall_s": 101.0}), Targets: []Goal{{Metric: "wall_s", Op: "lt", Value: 300, Label: "< 5 min"}}})
}

func TestCheckAllMet(t *testing.T) {
	root := fixtureRoot(t)
	writeAll(t, root, "ac", nil)
	s, problems, err := Check(Options{Root: root})
	if err != nil || len(problems) > 0 {
		t.Fatal(err, problems)
	}
	if s.Met != 6 || s.Missed != 0 {
		t.Fatalf("\n%s", s.Text())
	}
	if !strings.Contains(s.Text(), "6 targets met · 0 missed · raw files in bench/results/") {
		t.Fatal(s.Text())
	}
}

func TestCheckRefusals(t *testing.T) {
	t.Run("missed target", func(t *testing.T) {
		root := fixtureRoot(t)
		writeAll(t, root, "ac", func(f *File) {
			if f.Suite == "gate" {
				f.Scenarios[0].Runs = latencyRuns(3.1, 30)
			}
		})
		s, _, _ := Check(Options{Root: root})
		if s.Missed != 1 || s.Rows[0].Result != "missed" || !strings.Contains(s.Text(), "✕ Gate · allow, no witness") ||
			!strings.Contains(s.Text(), "p99 < 25 ms") {
			t.Fatalf("\n%s", s.Text())
		}
	})
	t.Run("battery", func(t *testing.T) {
		root := fixtureRoot(t)
		writeAll(t, root, "battery", nil)
		s, _, _ := Check(Options{Root: root})
		if s.Met != 0 || s.Rows[0].Result != "refused" || !strings.Contains(s.Rows[0].Problem, "battery") {
			t.Fatalf("\n%s", s.Text())
		}
	})
	t.Run("stale", func(t *testing.T) {
		root := fixtureRoot(t)
		writeAll(t, root, "ac", nil)
		_ = os.WriteFile(filepath.Join(root, "internal/gate/gate.go"), []byte("package gate // changed\n"), 0o644)
		s, _, _ := Check(Options{Root: root})
		if s.Met != 0 || !strings.Contains(s.Rows[0].Problem, "Stale") {
			t.Fatalf("\n%s", s.Text())
		}
	})
	t.Run("missing host or power", func(t *testing.T) {
		root := fixtureRoot(t)
		writeAll(t, root, "ac", nil)
		p := filepath.Join(root, "bench", "results", "2026-10-12-testhost-bench-gate.json")
		b, _ := os.ReadFile(p)
		_ = os.WriteFile(p, []byte(strings.Replace(string(b), `"power": "ac"`, `"power": ""`, 1)), 0o644)
		s, problems, _ := Check(Options{Root: root})
		if len(problems) != 1 || !strings.Contains(problems[0], "host.power") || s.Rows[0].Result != "missing" {
			t.Fatalf("%v\n%s", problems, s.Text())
		}
	})
	t.Run("quick files only for the RC", func(t *testing.T) {
		root := fixtureRoot(t)
		writeAll(t, root, "ac", func(f *File) { f.Quick = true })
		if s, _, _ := Check(Options{Root: root}); s.Met != 0 {
			t.Fatal("docs mode accepted a quick file")
		}
		if s, _, _ := Check(Options{Root: root, AllowQuick: true}); s.Met != 6 {
			t.Fatalf("\n%s", s.Text())
		}
	})
	t.Run("errors fail a run", func(t *testing.T) {
		root := fixtureRoot(t)
		writeAll(t, root, "ac", func(f *File) {
			if f.Suite == "policy" {
				f.Scenarios[0].Runs[1].Errors = 1
			}
		})
		if s, _, _ := Check(Options{Root: root}); s.Rows[1].Result != "missed" {
			t.Fatalf("\n%s", s.Text())
		}
	})
}

// TestReportDeterministic: two reports give the same bytes; a hand edit to the block is caught.
func TestReportDeterministic(t *testing.T) {
	root := fixtureRoot(t)
	writeAll(t, root, "ac", nil)
	read := func() (string, string) {
		s, _, _ := Check(Options{Root: root})
		if err := WriteSummary(root, s); err != nil {
			t.Fatal(err)
		}
		a, _ := os.ReadFile(filepath.Join(root, "bench", "results", "SUMMARY.json"))
		b, _ := os.ReadFile(filepath.Join(root, "docs", "BENCHMARKS.md"))
		return string(a), string(b)
	}
	s1, d1 := read()
	s2, d2 := read()
	if s1 != s2 || d1 != d2 || !strings.HasSuffix(d1, EndMarker+"\n\nAfter.\n") || !strings.Contains(d1, "| Gate · allow, no witness | p50 3.10 ms · p99 14.2 ms |") {
		t.Fatalf("not deterministic or wrong:\n%s", d1)
	}
	edited := strings.Replace(d1, "14.2", "12.0", 1)
	if err := DocFresh(root, edited); err == nil {
		t.Fatal("a hand edit must be caught")
	}
	if err := DocFresh(root, d1); err != nil {
		t.Fatal(err)
	}
}

// TestRepoBenchmarksDocFresh: the committed docs/BENCHMARKS.md block equals SUMMARY.json rendered.
func TestRepoBenchmarksDocFresh(t *testing.T) {
	root, _ := filepath.Abs("../../..")
	doc, err := os.ReadFile(filepath.Join(root, "docs", "BENCHMARKS.md"))
	if err != nil {
		t.Skip("no docs/BENCHMARKS.md")
	}
	if _, err := os.Stat(filepath.Join(root, "bench", "results", "SUMMARY.json")); err != nil {
		t.Skip("no SUMMARY.json yet")
	}
	if err := DocFresh(root, string(doc)); err != nil {
		t.Fatal(err)
	}
}

// TestRepoResultsValidate: every v1 results file in bench/results validates against bench/schema.json.
func TestRepoResultsValidate(t *testing.T) {
	root, _ := filepath.Abs("../../..")
	raw, err := os.ReadFile(filepath.Join(root, "bench", "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	sch, err := schema.Compile(raw)
	if err != nil {
		t.Fatalf("bench/schema.json: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(root, "bench", "results", "*.json"))
	for _, p := range files {
		b, _ := os.ReadFile(p)
		if !strings.Contains(string(b), `"schema": "`+Schema+`"`) {
			continue
		}
		if err := sch.Validate(b); err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
		}
		if _, err := Read(p); err != nil {
			t.Error(err)
		}
	}
	// A file without host or power fails both.
	bad := []byte(`{"schema":"` + Schema + `","suite":"gate","started_at":"2026-10-12T00:00:00Z","source_hash":"sha256:x","scenarios":[]}`)
	if sch.Validate(bad) == nil {
		t.Error("schema.json accepted a file without host")
	}
	var f File
	_ = json.Unmarshal(bad, &f)
	if f.Validate() == nil {
		t.Error("Validate accepted a file without host")
	}
}

func TestPowerParsing(t *testing.T) {
	if powerFromPmset("Now drawing from 'AC Power'\n") != "ac" || powerFromPmset("Now drawing from 'Battery Power'") != "battery" || powerFromPmset("") != "unknown" {
		t.Fatal("pmset")
	}
	dir := t.TempDir()
	if powerFromSysfs(filepath.Join(dir, "none")) != "ac" {
		t.Fatal("no power_supply: a server")
	}
	_ = os.MkdirAll(filepath.Join(dir, "BAT0"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "BAT0", "type"), []byte("Battery\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "AC"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "AC", "type"), []byte("Mains\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "AC", "online"), []byte("0\n"), 0o644)
	if powerFromSysfs(dir) != "battery" {
		t.Fatal("laptop unplugged")
	}
	_ = os.WriteFile(filepath.Join(dir, "AC", "online"), []byte("1\n"), 0o644)
	if powerFromSysfs(dir) != "ac" {
		t.Fatal("laptop plugged in")
	}
}
