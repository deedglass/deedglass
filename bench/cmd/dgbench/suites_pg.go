//go:build integration

package main

//deedglass:allow-exec benchmark tooling: runs go test for S21 and S14 budgets and samples the server with ps and lsof; never shipped

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/bench/internal/load"
	"github.com/deedglass/deedglass/bench/internal/stack"
)

func init() {
	suites["gate"] = gateSuite
	suites["stop"] = stopSuite
	suites["deed"] = deedSuite
	suites["wal"] = walSuite
	suites["witness"] = witnessSuite
	suites["mcp"] = mcpSuite
	suites["soak"] = soakSuite
}

// benchPolicyGate: fake.echo runs, fake.fail is refused by a rule, fake.write waits for a person.
const benchPolicyGate = `deedglass: policy/v1
name: bench-gate
description: The benchmark's policy. fake.echo runs, fake.fail is refused, fake.write waits for an admin.
defaults:
  decision: deny
  reason: No rule allows this tool.
rules:
  - id: bench-deny
    match: { tool: fake.fail }
    decision: deny
    reason: The benchmark refuses fake.fail.
  - id: bench-approval
    match: { tool: fake.write }
    decision: require_approval
    reason: The benchmark's writes wait for an admin.
    approval: { approvers: [role:admin], required: 1, expires_in: 30m }
  - id: bench-allow
    match: { tool: fake.echo }
    decision: allow
`

// boot starts a stack and recovers its panics as errors.
func boot(root string) (s *stack.Stack, err error) {
	defer func() {
		if p := recover(); p != nil {
			if s != nil {
				s.Close()
			}
			s, err = nil, fmt.Errorf("%v", p)
		}
	}()
	return stack.Boot(root), nil
}

func guard(err *error) {
	if p := recover(); p != nil {
		*err = fmt.Errorf("%v", p)
	}
}

// action sends one POST /v1/actions and names the outcome: the action's status, or the error code.
func action(ctx context.Context, s *stack.Stack, srv *stack.Server, key, tool string, wait int) (load.Outcome, error) {
	st, m, err := s.DoCtx(ctx, srv, "POST", "/v1/actions", key, map[string]any{"tool": tool, "args": map[string]any{"message": "bench"}, "wait": wait})
	if err != nil {
		return load.Outcome{}, err
	}
	if code := stack.Str(m, "error", "code"); code != "" {
		if st >= 500 || st == 429 {
			return load.Outcome{Status: code}, fmt.Errorf("%d %s", st, code)
		}
		return load.Outcome{Status: code}, nil
	}
	if status := stack.Str(m, "status"); status != "" {
		return load.Outcome{Status: status}, nil
	}
	return load.Outcome{}, fmt.Errorf("%d: no status", st)
}

func plan(o Opts, name string, rate float64) load.Plan {
	if o.Quick {
		return load.Plan{Name: name, Rate: rate, Warmup: 5 * time.Second, Duration: 15 * time.Second, Runs: 1}
	}
	return load.Plan{Name: name, Rate: rate, Warmup: 10 * time.Second, Duration: 60 * time.Second, Runs: 3}
}

func scenario(p load.Plan, rs []load.RunResult) load.Scenario {
	return load.Scenario{Name: p.Name, Rate: p.Rate, WarmupS: p.Warmup.Seconds(), DurationS: p.Duration.Seconds(), MaxInFlight: 256, Runs: load.FromRuns(rs)}
}

// --- gate: POST /v1/actions end to end over loopback, open loop at 200/s ---

func gateSuite(ctx context.Context, o Opts) (f *load.File, err error) {
	defer guard(&err)
	s, err := boot(o.Root)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	srv := s.StartServer()
	org := s.NewOrg(srv, "bench", 8, benchPolicyGate)
	f = load.NewFile(o.Root, "gate", version(), o.Quick)
	f.Postgres = s.Postgres()
	f.Caveats = append(f.Caveats,
		"Client-observed end-to-end latency of POST /v1/actions over loopback, measured from each request's intended send time: it includes HTTP, JSON, the policy, the connector call (fake.echo, 0 ms), the database writes and sealing the deed, so it is an upper bound on the Gate's own overhead.",
		"Open loop at a fixed arrival rate, 8 agents in round robin, one org, one deedglass serve, Postgres on the same machine.")
	type sc struct {
		name, tool string
		wait       int
		targets    []load.Goal
	}
	all := []sc{
		{"actions.allow", "fake.echo", 5, []load.Goal{{Metric: "p50_ms", Op: "lt", Value: 5, Source: "contract §12", Label: "p50 < 5 ms"},
			{Metric: "p99_ms", Op: "lt", Value: 25, Source: "contract §12", Label: "p99 < 25 ms"}}},
		{"actions.deny", "fake.fail", 0, nil},
		{"actions.approval_create", "fake.write", 0, nil},
	}
	if o.Quick {
		all = all[:1]
	}
	for _, c := range all {
		p := plan(o, c.name, 200)
		var n atomic.Int64
		rs, err := load.Run(ctx, p, load.TargetFunc(func(ctx context.Context, i int) (load.Outcome, error) {
			a := org.Agents[int(n.Add(1))%len(org.Agents)]
			return action(ctx, s, srv, a.Key, c.tool, c.wait)
		}))
		if err != nil {
			return nil, err
		}
		x := scenario(p, rs)
		x.Targets = c.targets
		f.Scenarios = append(f.Scenarios, x)
	}
	return f, nil
}

// --- stop: stop-all on instance A while instance B serves 50 req/s ---

func stopSuite(ctx context.Context, o Opts) (f *load.File, err error) {
	defer guard(&err)
	s, err := boot(o.Root)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	a, b := s.StartServer(), s.StartServer()
	org := s.NewOrg(a, "bench", 1, benchPolicyGate)
	key := org.Agents[0].Key
	f = load.NewFile(o.Root, "stop", version(), o.Quick)
	f.Postgres = s.Postgres()
	f.Caveats = append(f.Caveats, "Two deedglass serve processes on one database. T0 is when POST /v1/org/stop-all returns on A; the time is until B first answers org_stopped. B gets fake.echo calls every 20 ms throughout; any call B sends after T0 + 2 s that still runs (status executed) is counted as late.")
	trials := 20
	if o.Quick {
		trials = 5
	}
	var props []float64
	late, sentAfter := 0, 0
	for tr := 0; tr < trials; tr++ {
		// B must be running actions again before the trial starts.
		deadline := time.Now().Add(10 * time.Second)
		for {
			if out, _ := action(ctx, s, b, key, "fake.echo", 5); out.Status == "executed" {
				break
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("trial %d: B never ran actions again after start-all", tr)
			}
			time.Sleep(50 * time.Millisecond)
		}
		type obs struct {
			sent, got time.Time
			status    string
		}
		var mu sync.Mutex
		var seen []obs
		stopLoad := make(chan struct{})
		var wg sync.WaitGroup
		go func() {
			tk := time.NewTicker(20 * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-stopLoad:
					return
				case <-tk.C:
					wg.Add(1)
					go func(sent time.Time) {
						defer wg.Done()
						out, _ := action(ctx, s, b, key, "fake.echo", 5)
						mu.Lock()
						seen = append(seen, obs{sent, time.Now(), out.Status})
						mu.Unlock()
					}(time.Now())
				}
			}
		}()
		time.Sleep(time.Second)
		st, m, err := s.Do(a, "POST", "/v1/org/stop-all", org.AdminKey, map[string]any{"reason": "Benchmark trial."})
		t0 := time.Now()
		if err != nil || st != 200 {
			close(stopLoad)
			wg.Wait()
			return nil, fmt.Errorf("stop-all: %d %v %v", st, m, err)
		}
		time.Sleep(3 * time.Second)
		close(stopLoad)
		wg.Wait()
		first := time.Duration(-1)
		for _, x := range seen {
			if x.status == "org_stopped" && x.got.After(t0) {
				if d := x.got.Sub(t0); first < 0 || d < first {
					first = d
				}
			}
			if x.sent.After(t0.Add(2 * time.Second)) {
				sentAfter++
				if x.status == "executed" {
					late++
				}
			}
		}
		if first < 0 {
			first = 3 * time.Second // never refused within the window: a miss, recorded as the window
		}
		props = append(props, float64(first.Microseconds())/1000)
		s.CLI("org", "start-all", "--org", org.Slug, "--reason", "Benchmark trial done.")
	}
	sorted := append([]float64(nil), props...)
	sort.Float64s(sorted)
	q := func(p float64) float64 { return sorted[min(len(sorted)-1, int(p*float64(len(sorted))))] }
	run := load.RunJSON{Count: trials, P50ms: q(.5), P99ms: q(.99), MaxMs: sorted[len(sorted)-1],
		Values: map[string]any{"max_ms": sorted[len(sorted)-1], "trials": float64(trials), "late_actions": float64(late),
			"calls_after_t0_plus_2s": float64(sentAfter), "trial_ms": props}}
	if late > 0 {
		run.Errors = late
	}
	f.Scenarios = append(f.Scenarios, load.Scenario{Name: "stop.across_instances", Rate: 50, Runs: []load.RunJSON{run},
		Targets: []load.Goal{{Metric: "max_ms", Op: "lt", Value: 2000, Source: "contract §12", Label: "< 2 s"}}})
	return f, nil
}

// --- deed: seal throughput through the Gate, one org and ten orgs in parallel ---

func deedSuite(ctx context.Context, o Opts) (f *load.File, err error) {
	defer guard(&err)
	s, err := boot(o.Root)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	srv := s.StartServer()
	var orgs []*stack.Org
	for i := 0; i < 10; i++ {
		orgs = append(orgs, s.NewOrg(srv, fmt.Sprintf("bench%d", i), 4, benchPolicyGate))
	}
	f = load.NewFile(o.Root, "deed", version(), o.Quick)
	f.Postgres = s.Postgres()
	f.Caveats = append(f.Caveats, "Closed loop, 16 callers, each sealing one allowed fake.echo action at a time through POST /v1/actions. One org's deed chain is serial by design (each deed names the previous one's hash), so one org's rate is bounded by one chain lock; ten orgs seal in parallel.")
	dur := 30 * time.Second
	runs := 3
	if o.Quick {
		dur, runs = 10*time.Second, 1
	}
	for _, c := range []struct {
		name string
		orgs []*stack.Org
	}{{"seal.one_org", orgs[:1]}, {"seal.ten_orgs", orgs}} {
		sc := load.Scenario{Name: c.name}
		for r := 0; r < runs; r++ {
			h := load.NewHist()
			var mu sync.Mutex
			var count, errs int
			end := time.Now().Add(dur)
			var wg sync.WaitGroup
			for w := 0; w < 16; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					org := c.orgs[w%len(c.orgs)]
					key := org.Agents[w%len(org.Agents)].Key
					for time.Now().Before(end) && ctx.Err() == nil {
						t0 := time.Now()
						out, err := action(ctx, s, srv, key, "fake.echo", 5)
						d := time.Since(t0)
						mu.Lock()
						h.Record(d)
						count++
						if err != nil || out.Status != "executed" {
							errs++
						}
						mu.Unlock()
					}
				}(w)
			}
			wg.Wait()
			rr := load.FromRuns([]load.RunResult{{Count: count, Errors: errs, AchievedRate: float64(count) / dur.Seconds(),
				P50: h.Quantile(.5), P90: h.Quantile(.9), P95: h.Quantile(.95), P99: h.Quantile(.99), P999: h.Quantile(.999), Max: h.Max(), Mean: h.Mean()}})[0]
			rr.Values = map[string]any{"deeds_per_s": round3(float64(count) / dur.Seconds()), "orgs": float64(len(c.orgs))}
			sc.Runs = append(sc.Runs, rr)
		}
		f.Scenarios = append(f.Scenarios, sc)
	}
	return f, nil
}

// --- wal: WAL bytes per transaction, three shapes, pg_walinspect ---

func adminDB(db string) (string, error) {
	u, err := url.Parse(os.Getenv("DEEDGLASS_IT_ADMIN_URL"))
	if err != nil {
		return "", err
	}
	u.Path = "/" + db
	return u.String(), nil
}

func walSuite(ctx context.Context, o Opts) (f *load.File, err error) {
	if os.Getenv("DEEDGLASS_IT_THROWAWAY") != "1" || os.Getenv("DEEDGLASS_IT_ADMIN_URL") == "" {
		return nil, fmt.Errorf("refusing: the WAL suite runs only on a throwaway cluster (make bench starts one)")
	}
	f = load.NewFile(o.Root, "wal", version(), o.Quick)
	f.Postgres = stack.PostgresOf(os.Getenv("DEEDGLASS_IT_ADMIN_URL"))
	f.Caveats = append(f.Caveats, "WAL per committed single-row UPDATE with warm pages (no full-page images), read with pg_walinspect between LSNs. The ledger table and the transactional logical message have the shapes the Postgres connector writes; this measures their WAL, not the connector's latency. The spike's laptop WAL figures are never quoted.")
	adm, _ := adminDB("postgres")
	c, err := pgx.Connect(ctx, adm)
	if err != nil {
		return nil, err
	}
	_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS deedglass_bench_wal WITH (FORCE)")
	if _, err := c.Exec(ctx, "CREATE DATABASE deedglass_bench_wal"); err != nil {
		return nil, err
	}
	defer func() {
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS deedglass_bench_wal WITH (FORCE)")
		_ = c.Close(context.Background())
	}()
	wu, _ := adminDB("deedglass_bench_wal")
	w, err := pgx.Connect(ctx, wu)
	if err != nil {
		return nil, err
	}
	defer w.Close(context.Background())
	setup := `CREATE EXTENSION pg_walinspect;
CREATE SCHEMA s_default; CREATE SCHEMA s_full; CREATE SCHEMA deedglass;
CREATE TABLE s_default.orders (id bigint PRIMARY KEY, customer_id bigint NOT NULL, status text NOT NULL, total_cents bigint NOT NULL, note text, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE s_full.orders (LIKE s_default.orders INCLUDING ALL);
ALTER TABLE s_full.orders REPLICA IDENTITY FULL;
CREATE TABLE deedglass.ledger (action_id text PRIMARY KEY, org_id text NOT NULL, agent_id text NOT NULL, intent_hash text, sql_digest text NOT NULL, xid8 xid8 NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
ALTER TABLE deedglass.ledger REPLICA IDENTITY FULL;
INSERT INTO s_default.orders SELECT g, g % 97, 'paid', 1000 + g, 'Order ' || g, now() FROM generate_series(1, 2000) g;
INSERT INTO s_full.orders SELECT * FROM s_default.orders;
CHECKPOINT;`
	if _, err := w.Exec(ctx, setup); err != nil {
		return nil, err
	}
	shapes := []struct{ name, sql string }{
		{"wal.update_identity_default", "UPDATE s_default.orders SET status = $2, updated_at = now() WHERE id = $1"},
		{"wal.update_identity_full", "UPDATE s_full.orders SET status = $2, updated_at = now() WHERE id = $1"},
		{"wal.full_plus_ledger_and_marker", ""},
	}
	n := 50
	if o.Quick {
		n = 10
	}
	medBytes := map[string]float64{}
	for _, sh := range shapes {
		var bytes, records []float64
		for i := 0; i < n+5; i++ { // the first 5 warm the pages
			id := 1 + i*7%2000
			status := []string{"shipped", "paid"}[i%2]
			var a, b string
			if err := w.QueryRow(ctx, "SELECT pg_catalog.pg_current_wal_insert_lsn()::text").Scan(&a); err != nil {
				return nil, err
			}
			tx, err := w.Begin(ctx)
			if err != nil {
				return nil, err
			}
			if sh.sql != "" {
				_, err = tx.Exec(ctx, sh.sql, id, status)
			} else {
				act := fmt.Sprintf("act_01k7bench%016d", i)
				if _, err = tx.Exec(ctx, "INSERT INTO deedglass.ledger (action_id, org_id, agent_id, sql_digest, xid8) VALUES ($1, 'org_01k7bench0000000000000000', 'agt_01k7bench0000000000000000', 'sha256:00', pg_catalog.pg_current_xact_id())", act); err == nil {
					if _, err = tx.Exec(ctx, "SELECT pg_catalog.pg_logical_emit_message(true, 'deedglass', $1)", act); err == nil {
						_, err = tx.Exec(ctx, "UPDATE s_full.orders SET status = $2, updated_at = now() WHERE id = $1", id, status)
					}
				}
			}
			if err != nil {
				_ = tx.Rollback(ctx)
				return nil, err
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			if err := w.QueryRow(ctx, "SELECT pg_catalog.pg_current_wal_insert_lsn()::text").Scan(&b); err != nil {
				return nil, err
			}
			if i < 5 {
				continue
			}
			var recs, recBytes, fpi int64
			if err := w.QueryRow(ctx, `SELECT count(*), coalesce(sum(record_length), 0), coalesce(sum(fpi_length), 0)
			    FROM pg_get_wal_records_info($1::pg_lsn, $2::pg_lsn) WHERE xid = 0 OR xid IS NOT NULL`, a, b).Scan(&recs, &recBytes, &fpi); err != nil {
				return nil, err
			}
			bytes = append(bytes, float64(recBytes))
			records = append(records, float64(recs))
		}
		sort.Float64s(bytes)
		sort.Float64s(records)
		mb := bytes[len(bytes)/2]
		medBytes[sh.name] = mb
		f.Scenarios = append(f.Scenarios, load.Scenario{Name: sh.name, Runs: []load.RunJSON{{Count: n,
			Values: map[string]any{"record_bytes_per_txn": mb, "records_per_txn": records[len(records)/2], "max_record_bytes": bytes[len(bytes)-1]}}}})
	}
	base := medBytes["wal.update_identity_default"]
	for i := range f.Scenarios {
		if base > 0 {
			f.Scenarios[i].Runs[0].Values["overhead_vs_default_pct"] = round3((medBytes[f.Scenarios[i].Name]/base - 1) * 100)
		}
	}
	return f, nil
}

// --- witness: L2 commit → verdict through a real serve, the decoder and the Postgres connector ---

// runL2 runs S21's TestL2Budget at a rate and returns its raw JSON.
func runL2(ctx context.Context, root string, rate, seconds int) (map[string]any, string, error) {
	out := filepath.Join(root, ".tmp", "bench", fmt.Sprintf("l2-%d-%d.json", rate, time.Now().UnixNano()))
	_ = os.MkdirAll(filepath.Dir(out), 0o755)
	cmd := exec.CommandContext(ctx, "go", "test", "-tags", "integration", "-count=1", "-timeout", "900s", "-run", "^TestL2Budget$", "./cmd/deedglass/")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEEDGLASS_IT_BENCH=1", "DEEDGLASS_L2_BENCH_OUT="+out,
		"DEEDGLASS_L2_BENCH_RATE="+strconv.Itoa(rate), "DEEDGLASS_L2_BENCH_SECONDS="+strconv.Itoa(seconds))
	log, runErr := cmd.CombinedOutput()
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, string(log), fmt.Errorf("TestL2Budget at %d/s wrote nothing: %v", rate, runErr)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, string(log), err
	}
	return m, string(log), runErr
}

func num64(m map[string]any, path ...string) float64 {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return 0
		}
		cur = mm[p]
	}
	f, _ := cur.(float64)
	return f
}

func witnessSuite(ctx context.Context, o Opts) (*load.File, error) {
	f := load.NewFile(o.Root, "witness", version(), o.Quick)
	f.Postgres = stack.PostgresOf(os.Getenv("DEEDGLASS_IT_ADMIN_URL"))
	f.Caveats = append(f.Caveats,
		"Commit → verdict uses database timestamps, not client clocks: the decoded transaction's commit_time to the deed's issued_at, on one cluster with one clock.",
		"Single-row UPDATEs sent on schedule through a real deedglass serve, the native Postgres connector and the decoder, with the Deedglass control plane and the watched database on the same Postgres server and machine.")
	type spec struct{ rate, runs int }
	specs := []spec{{100, 3}, {10, 1}, {500, 1}}
	seconds := 60
	if o.Quick {
		specs, seconds = []spec{{100, 1}}, 15
	}
	for _, sp := range specs {
		sc := load.Scenario{Name: fmt.Sprintf("l2.%d_per_s", sp.rate), Rate: float64(sp.rate), DurationS: float64(seconds)}
		for r := 0; r < sp.runs; r++ {
			m, log, err := runL2(ctx, o.Root, sp.rate, seconds)
			if m == nil {
				return nil, fmt.Errorf("%v\n%s", err, tailLines(log, 30))
			}
			l2, _ := m["l2"].(map[string]any)
			lat, _ := l2["commit_to_verdict_ms"].(map[string]any)
			actions := num64(l2, "actions")
			sent := num64(l2, "sent_in_s")
			run := load.RunJSON{Count: int(actions), P50ms: num64(lat, "p50"), P90ms: num64(lat, "p90"), P99ms: num64(lat, "p99"), MaxMs: num64(lat, "max"),
				Values: map[string]any{"verdicts": l2["verdicts"], "sent_in_s": sent, "segments_ms": m["segments_ms"]}}
			if sent > 0 {
				run.AchievedRate = round3(actions / sent)
			}
			if err != nil {
				run.Errors = 1
				sc.Notes = append(sc.Notes, "The run reported a problem: "+firstFail(log))
			}
			sc.Runs = append(sc.Runs, run)
		}
		if sp.rate == 100 {
			sc.Targets = []load.Goal{{Metric: "p50_ms", Op: "lt", Value: 1000, Source: "contract §12", Label: "p50 < 1 s"}}
		}
		f.Scenarios = append(f.Scenarios, sc)
	}
	return f, nil
}

func tailLines(s string, n int) string {
	l := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

func firstFail(log string) string {
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "witness_l2_bench") && (strings.Contains(l, "Error") || strings.Contains(l, "only") || strings.Contains(l, "≥")) {
			return strings.TrimSpace(l)
		}
	}
	return "see the go test output"
}

// --- mcp: S14's wrapped vs direct stdio measurement ---

func mcpSuite(ctx context.Context, o Opts) (*load.File, error) {
	f := load.NewFile(o.Root, "mcp", version(), o.Quick)
	f.Postgres = stack.PostgresOf(os.Getenv("DEEDGLASS_IT_ADMIN_URL"))
	f.Caveats = append(f.Caveats, "tools/call through deedglass mcp wrap against the same stdio test server called directly; stdio is one pipe, so calls are sequential by nature. The added time includes the Gate round trip over loopback HTTP.")
	out := filepath.Join(o.Root, ".tmp", "bench", fmt.Sprintf("mcp-%d.json", time.Now().UnixNano()))
	_ = os.MkdirAll(filepath.Dir(out), 0o755)
	cmd := exec.CommandContext(ctx, "go", "test", "-tags", "integration", "-count=1", "-timeout", "900s", "-run", "^TestMCPBudget$", "./cmd/deedglass/")
	cmd.Dir = o.Root
	cmd.Env = append(os.Environ(), "DEEDGLASS_IT_BENCH=1", "DEEDGLASS_MCP_BENCH_OUT="+out)
	log, runErr := cmd.CombinedOutput()
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("TestMCPBudget wrote nothing: %v\n%s", runErr, tailLines(string(log), 30))
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	run := load.RunJSON{Count: 1, P50ms: num64(m, "tools_call_added_p50_ms"), P99ms: num64(m, "tools_call_added_p99_ms"), MaxMs: num64(m, "tools_call_added_p99_ms"),
		Values: map[string]any{"tools_call_added_p50_ms": num64(m, "tools_call_added_p50_ms"), "tools_call_added_p99_ms": num64(m, "tools_call_added_p99_ms"),
			"tools_list_added_p50_ms": num64(m, "tools_list_added_p50_ms"), "relay_passthrough_p99_ms": num64(m, "relay_passthrough_overhead_p99_ms"),
			"wrapper_peak_rss_mib": num64(m, "wrapper_peak_rss_mib_100x64kib")}}
	if runErr != nil {
		run.Errors = 1
	}
	f.Scenarios = append(f.Scenarios, load.Scenario{Name: "mcp.wrap_added", Runs: []load.RunJSON{run}})
	return f, nil
}

// --- soak: 30 minutes at 50 req/s, 70% allow, 20% deny, 10% approval ---

func rssKiB(pid int) float64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return v
}

func openFDs(pid int) float64 {
	if entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid)); err == nil {
		return float64(len(entries))
	}
	out, err := exec.Command("lsof", "-n", "-P", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	return float64(strings.Count(string(out), "\n") - 1)
}

func soakSuite(ctx context.Context, o Opts) (f *load.File, err error) {
	defer guard(&err)
	dur := 30 * time.Minute
	if v, e := time.ParseDuration(os.Getenv("DEEDGLASS_BENCH_SOAK")); e == nil && v > 0 {
		dur = v
	}
	if o.Quick {
		dur = 2 * time.Minute
	}
	s, err := boot(o.Root)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	srv := s.StartServer()
	org := s.NewOrg(srv, "bench", 8, benchPolicyGate)
	f = load.NewFile(o.Root, "soak", version(), o.Quick)
	f.Postgres = s.Postgres()
	f.Caveats = append(f.Caveats, fmt.Sprintf("%s at 50 calls/s: 70%% allowed (fake.echo), 20%% denied, 10%% waiting for approval. Sampled every 30 s: the server's RSS, its open files and the app role's database connections. The binary exposes no goroutine count, so connections and files stand in for leak detection. No L2 share in this run.", dur))
	type sample struct{ at, rss, fds, conns float64 }
	var samples []sample
	adm := os.Getenv("DEEDGLASS_IT_ADMIN_URL")
	ac, err := pgx.Connect(ctx, adm)
	if err != nil {
		return nil, err
	}
	defer ac.Close(context.Background())
	start := time.Now()
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(30 * time.Second)
		defer tk.Stop()
		for {
			var conns float64
			_ = ac.QueryRow(context.Background(), "SELECT count(*)::float8 FROM pg_catalog.pg_stat_activity WHERE usename = 'deedglass_app'").Scan(&conns)
			samples = append(samples, sample{time.Since(start).Minutes(), rssKiB(srv.PID()) / 1024, openFDs(srv.PID()), conns})
			select {
			case <-done:
				return
			case <-tk.C:
			}
		}
	}()
	var n atomic.Int64
	p := load.Plan{Name: "soak.mixed", Rate: 50, Warmup: -1, Duration: dur, Runs: 1}
	rs, err := load.Run(ctx, p, load.TargetFunc(func(ctx context.Context, i int) (load.Outcome, error) {
		k := n.Add(1)
		a := org.Agents[int(k)%len(org.Agents)]
		tool := "fake.echo"
		switch k % 10 {
		case 0:
			tool = "fake.write"
		case 1, 2:
			tool = "fake.fail"
		}
		return action(ctx, s, srv, a.Key, tool, 5)
	}))
	close(done)
	time.Sleep(100 * time.Millisecond)
	if err != nil {
		return nil, err
	}
	x := scenario(p, rs)
	var after5 []sample
	for _, sm := range samples {
		if sm.at >= 5 || o.Quick {
			after5 = append(after5, sm)
		}
	}
	growth := 0.0
	if len(after5) > 1 && after5[0].rss > 0 {
		growth = (after5[len(after5)-1].rss/after5[0].rss - 1) * 100
	}
	var curve []map[string]float64
	for _, sm := range samples {
		curve = append(curve, map[string]float64{"minute": round3(sm.at), "rss_mib": round3(sm.rss), "open_files": sm.fds, "db_connections": sm.conns})
	}
	x.Runs[0].Values = map[string]any{"rss_growth_after_5min_pct": round3(growth), "samples": curve}
	x.Targets = []load.Goal{{Metric: "rss_growth_after_5min_pct", Op: "lt", Value: 10, Source: "S35 §3.2", Label: "RSS growth < 10% after minute 5"}}
	f.Scenarios = append(f.Scenarios, x)
	return f, nil
}
