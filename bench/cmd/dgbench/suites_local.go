package main

//deedglass:allow-exec benchmark tooling: builds the binary and times deedglass verify and scripts/quickstart_timed.sh; never shipped

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/deedglass/deedglass/bench/internal/load"
	"github.com/deedglass/deedglass/internal/policy"
)

func init() {
	suites["policy"] = policySuite
	suites["verify"] = verifySuite
	suites["quickstart"] = quickstartSuite
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// --- policy: in-process evaluation at 10, 50, 200 and 500 rules ---

// benchPolicy is S11's worst case: n rules that all match a Postgres action and whose conditions
// read args and the SQL facts, so every rule's CEL runs before the default decides.
func benchPolicy(n int) string {
	var b strings.Builder
	b.WriteString("deedglass: policy/v1\nname: bench-eval\ndefaults:\n  decision: deny\n  reason: No rule allows this action.\n")
	b.WriteString("budgets:\n  - id: agent-rate\n    window: minute\n    max_calls: 60\n  - id: agent-daily\n    window: day\n    max_calls: 5000\n")
	b.WriteString("rules:\n")
	whens := []string{
		`sql.kind in ["drop", "truncate"] && sql.multi`,
		`has(args.amount) && args.amount > minor("50.00 usd")`,
		`sql.writes && sql.tables.exists(t, glob(t, "audit.*"))`,
		`agent.labels["env"] == "staging" && time.hour < 6`,
		`size(sql.dangerous_functions) > 0 && !request.intent_signed`,
	}
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "  - id: rule-%d\n    match: { connector_type: postgres }\n    when: '%s'\n    decision: deny\n    reason: Rule %d decides this action.\n", i, whens[i%len(whens)], i)
	}
	return b.String()
}

func benchInput() policy.Input {
	args, _ := json.Marshal(map[string]string{"sql": "SELECT o.id, o.status FROM public.orders o JOIN public.customers c ON c.id = o.customer_id WHERE o.id = 42"})
	return policy.Input{V: 1,
		Org:   policy.OrgInput{OrgFacts: policy.OrgFacts{ID: "org_01jb8k0000000000000000000a", Timezone: "Asia/Kolkata"}},
		Agent: policy.AgentInput{AgentFacts: policy.AgentFacts{ID: "agt_01jb8k0000000000000000000a", Name: "support-bot", Labels: map[string]string{"env": "prod"}, Status: "active"}},
		Tool:  policy.ToolInput{ToolFacts: policy.ToolFacts{Name: "postgres.query", Connector: "postgres", ConnectorType: "postgres", SideEffect: "write"}},
		Args:  args, Time: time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC),
		Request: policy.RequestFacts{Mode: "gateway"}}
}

func policySuite(ctx context.Context, o Opts) (*load.File, error) {
	f := load.NewFile(o.Root, "policy", version(), o.Quick)
	f.Caveats = append(f.Caveats,
		"In process, one goroutine, each evaluation timed on its own: no HTTP, no database. The worst case: every rule's CEL runs before the default decides.",
		"S11's 500-rule size is included for comparison; the contract target is the 50-rule p99.")
	sizes := []int{10, 50, 200, 500}
	runs, n, warm := 3, 20000, 2000
	if o.Quick {
		sizes, runs, n, warm = []int{50}, 1, 5000, 500
	}
	in := benchInput()
	for _, size := range sizes {
		c, ds := policy.Compile([]byte(benchPolicy(size)), policy.Options{File: "bench.yaml"})
		if c == nil {
			return nil, fmt.Errorf("compile %d rules: %v", size, ds)
		}
		sc := load.Scenario{Name: fmt.Sprintf("evaluate.%d_rules", size)}
		for r := 0; r < runs; r++ {
			h := load.NewHist()
			for i := 0; i < warm+n; i++ {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				t0 := time.Now()
				d := policy.Evaluate(ctx, c, in, policy.EvalOptions{})
				el := time.Since(t0)
				if d.Outcome == "" {
					return nil, fmt.Errorf("no decision at %d rules", size)
				}
				if i >= warm {
					h.Record(el)
				}
			}
			rr := load.RunResult{Count: int(h.Count()), P50: h.Quantile(.5), P90: h.Quantile(.9), P95: h.Quantile(.95), P99: h.Quantile(.99),
				P999: h.Quantile(.999), Max: h.Max(), Mean: h.Mean(), Statuses: map[string]int{"deny": int(h.Count())}}
			sc.Runs = append(sc.Runs, load.FromRuns([]load.RunResult{rr})...)
		}
		if size == 50 {
			sc.Targets = []load.Goal{{Metric: "p99_ms", Op: "lt", Value: 1, Source: "contract §12", Label: "p99 < 1 ms"}}
		}
		f.Scenarios = append(f.Scenarios, sc)
	}
	return f, nil
}

// --- verify: deedglass verify on 100,000 deeds, two seal keys ---

func buildBinary(root string) (string, error) {
	dir := filepath.Join(root, ".tmp", "bench")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "deedglass-bench")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", bin, "./cmd/deedglass")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build: %v\n%s", err, out)
	}
	return bin, nil
}

// timed runs a command and returns its wall time and peak RSS in MiB.
func timed(root, bin string, args ...string) (wall float64, rssMiB float64, err error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "DEEDGLASS_NO_BANNER=1")
	t0 := time.Now()
	if out, err := cmd.CombinedOutput(); err != nil {
		return 0, 0, fmt.Errorf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
	wall = time.Since(t0).Seconds()
	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		rss := float64(ru.Maxrss)
		if runtime.GOOS == "darwin" {
			rss /= 1 << 20 // bytes on macOS
		} else {
			rss /= 1 << 10 // KiB on Linux
		}
		rssMiB = rss
	}
	return wall, rssMiB, nil
}

func verifySuite(ctx context.Context, o Opts) (*load.File, error) {
	f := load.NewFile(o.Root, "verify", version(), o.Quick)
	f.Caveats = append(f.Caveats, "Wall time of the whole process (start, read, check every seal and the chain, exit) with the file in the OS cache; peak RSS from getrusage.")
	jsonl, bundle := filepath.Join(o.Root, ".tmp", "deeds-100k.jsonl"), filepath.Join(o.Root, ".tmp", "deeds-100k.tar.gz")
	if _, err := os.Stat(bundle); err != nil {
		gen := exec.Command("go", "run", "./internal/deed/deedtest/cmd/genchain", "-n", "100000", "-keys", "2", "-out", jsonl, "-bundle", bundle)
		gen.Dir = o.Root
		if out, err := gen.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("genchain: %v\n%s", err, out)
		}
	}
	bin, err := buildBinary(o.Root)
	if err != nil {
		return nil, err
	}
	keys := filepath.Join(o.Root, "testdata", "deeds", "keys", "test-keys.json")
	runs := 3
	if o.Quick {
		runs = 1
	}
	for _, c := range []struct{ name, file string }{{"verify.100k_jsonl", jsonl}, {"verify.100k_bundle", bundle}} {
		sc := load.Scenario{Name: c.name}
		if _, _, err := timed(o.Root, bin, "verify", c.file, "--keys", keys, "-q"); err != nil { // warm the cache
			return nil, err
		}
		for r := 0; r < runs; r++ {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			w, rss, err := timed(o.Root, bin, "verify", c.file, "--keys", keys, "-q")
			if err != nil {
				return nil, err
			}
			sc.Runs = append(sc.Runs, load.RunJSON{Count: 100000, P50ms: round3(w * 1000), P99ms: round3(w * 1000), MaxMs: round3(w * 1000),
				Values: map[string]any{"wall_s": round3(w), "peak_rss_mib": round3(rss), "deeds": 100000}})
		}
		if c.name == "verify.100k_jsonl" {
			sc.Targets = []load.Goal{{Metric: "wall_s", Op: "lt", Value: 5, Source: "contract §12", Label: "< 5 s"},
				{Metric: "peak_rss_mib", Op: "lt", Value: 256, Source: "S05 §3.9", Label: "< 256 MiB"}}
		}
		f.Scenarios = append(f.Scenarios, sc)
	}
	return f, nil
}

// --- quickstart: scripts/quickstart_timed.sh from a clean directory ---

func quickstartSuite(ctx context.Context, o Opts) (*load.File, error) {
	if os.Getenv("DEEDGLASS_IT_THROWAWAY") != "1" {
		return nil, fmt.Errorf("set DEEDGLASS_IT_THROWAWAY=1: the quickstart creates and deletes Docker containers")
	}
	f := load.NewFile(o.Root, "quickstart", version(), o.Quick)
	f.Caveats = append(f.Caveats, "deedglass init --yes in a new directory each run (Postgres in Docker, serve, org, demo database, agent, policy, three smoke actions incl. an L2 VERIFIED update). Docker images were already pulled; a first pull adds its download time.")
	runs := "3"
	if o.Quick {
		runs = "1"
	}
	cmd := exec.CommandContext(ctx, "scripts/quickstart_timed.sh")
	cmd.Dir = o.Root
	cmd.Env = append(os.Environ(), "RUNS="+runs)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("quickstart_timed.sh: %v\n%s", err, out)
	}
	matches, _ := filepath.Glob(filepath.Join(o.Root, "bench", "results", time.Now().Format("2006-01-02")+"-*-quickstart.json"))
	var raw struct {
		Runs []struct {
			InitS      float64 `json:"init_s"`
			RerunS     float64 `json:"rerun_s"`
			L2Verified bool    `json:"l2_verified"`
		} `json:"runs"`
	}
	for _, m := range matches {
		b, _ := os.ReadFile(m)
		if strings.Contains(string(b), `"object":"bench"`) {
			if err := json.Unmarshal(b, &raw); err != nil {
				return nil, err
			}
		}
	}
	if len(raw.Runs) == 0 {
		return nil, fmt.Errorf("quickstart_timed.sh wrote no runs")
	}
	sc := load.Scenario{Name: "quickstart.clean_dir", Targets: []load.Goal{{Metric: "wall_s", Op: "lt", Value: 300, Source: "contract §12", Label: "< 5 min"}}}
	for _, r := range raw.Runs {
		errs := 0
		if !r.L2Verified {
			errs = 1
		}
		sc.Runs = append(sc.Runs, load.RunJSON{Count: 1, Errors: errs, P50ms: r.InitS * 1000, P99ms: r.InitS * 1000, MaxMs: r.InitS * 1000,
			Values: map[string]any{"wall_s": r.InitS, "rerun_s": r.RerunS, "l2_verified": r.L2Verified}})
	}
	f.Scenarios = append(f.Scenarios, sc)
	return f, nil
}
