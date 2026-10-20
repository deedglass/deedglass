//go:build integration

package main

//deedglass:allow-exec benchmark tooling: runs the deedglass binary under test (connector postgres init, deeds export, verify) on a throwaway cluster

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/bench/internal/stack"
	"github.com/deedglass/deedglass/internal/corpus"
	"github.com/deedglass/deedglass/internal/corpus/bench"
)

// policy is the policy the Gate enforces in the benchmark (corpus/BENCHMARK.md, "The Deedglass
// subject"): the starter postgres-safe's refusals word for word, then writes allowed with a Proven
// (L2) check instead of postgres-safe's approval hold, and the orders API allowed.
//
//go:embed policy.yaml
var policy string

type deedglassSubject struct {
	st      *stack.Stack
	srv     *stack.Server
	org     *stack.Org
	agent   stack.Agent
	version string
}

func startDeedglass(_ context.Context, root string) (s *deedglassSubject, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("deedglass: %v", p)
		}
	}()
	st := stack.Boot(root)
	defer func() {
		if err != nil {
			st.Close()
		}
	}()
	srv := st.StartServer("DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1")
	org := st.NewOrg(srv, "corpus-bench", 1, policy)
	v := strings.TrimSpace(string(st.CLI("version")))
	if f := strings.Fields(v); len(f) >= 2 {
		v = f[1]
	}
	return &deedglassSubject{st: st, srv: srv, org: org, agent: org.Agents[0], version: v}, nil
}

func (d *deedglassSubject) Info() bench.Info {
	return bench.Info{Name: "deedglass", Kind: "gateway", Version: d.version,
		Config: "deedglass serve built from this commit. Every agent statement goes through the Gate as its own action, to a Postgres connector witnessed at L2 (Proven) on the scenario's watched tables, with default connector settings. The policy is the postgres-safe starter's refusals with writes allowed at L2 (bench/cmd/corpus-bench/policy.yaml). An agent that calls an HTTP API goes through a REST connector, which records the call (L0, Claimed)."}
}

func (d *deedglassSubject) Close() error {
	d.st.Close()
	return nil
}

func (d *deedglassSubject) Start(_ context.Context, s *corpus.Scenario) (bench.Session, error) {
	n := strings.ReplaceAll(s.ID, "_", "-")
	return &dgSession{d: d, s: s, pgName: trim("c" + n), apiName: trim("a" + n), since: time.Now()}, nil
}

func trim(s string) string {
	if len(s) > 32 {
		s = s[:32]
	}
	return strings.TrimRight(s, "-")
}

type dgSession struct {
	d       *deedglassSubject
	s       *corpus.Scenario
	dsn     string
	pgName  string
	pgID    string
	apiName string
	apiID   string
	api     *http.Server
	since   time.Time
	tools   map[string]bool
	ids     []string
}

func (g *dgSession) IgnoreSchemas() []string { return []string{"deedglass"} }

func (g *dgSession) do(method, path string, body any) (int, map[string]any) {
	st, m, err := g.d.st.Do(g.d.srv, method, path, g.d.org.AdminKey, body)
	if err != nil {
		return 0, map[string]any{"error": err.Error()}
	}
	return st, m
}

func (g *dgSession) usesSQL() bool {
	for _, st := range g.s.Steps {
		if st.Session == "agent" && st.Via == "" {
			return true
		}
	}
	return false
}

// Prepare creates the scenario's connectors: a witnessed Postgres connector (initialised on the
// scenario's database with deedglass connector postgres init) when the agent sends SQL, and a
// loopback HTTP API with a REST connector when it calls one.
func (g *dgSession) Prepare(ctx context.Context, dsn string) error {
	g.dsn = dsn
	g.tools = map[string]bool{}
	if g.usesSQL() {
		cfg := map[string]any{"mode": "sql", "allowed_schemas": []string{"public"}, "writable_tables": g.s.WatchedTables(),
			"witness": map[string]any{"enabled": true, "watched_tables": g.s.WatchedTables(), "l2_timeout": "5s"}, "throwaway": true}
		st, m := g.do("POST", "/v1/connectors", map[string]any{"type": "postgres", "name": g.pgName, "description": "Corpus benchmark: " + g.s.ID, "config": cfg})
		if st != 201 {
			return fmt.Errorf("create the postgres connector: HTTP %d %v", st, m)
		}
		g.pgID = stack.Str(m, "id")
		if err := g.init(ctx); err != nil {
			return err
		}
		g.tools[g.pgName+".execute"] = true
	}
	var apiSteps []corpus.Step
	for _, st := range g.s.Steps {
		if st.Via == "api" {
			apiSteps = append(apiSteps, st)
		}
	}
	if len(apiSteps) > 0 {
		if err := g.startAPI(apiSteps); err != nil {
			return err
		}
	}
	return nil
}

// init runs deedglass connector postgres init --apply on the scenario's database, stores the
// DSNs it wrote, and waits for the decoder to read the database.
func (g *dgSession) init(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, g.d.st.Bin, "connector", "postgres", "init", "--connector", g.pgName, "--apply", "--no-store", "--rotate-passwords")
	cmd.Dir = g.d.st.Dir
	cmd.Env = g.d.st.Env("DEEDGLASS_INIT_DSN="+g.dsn, "DEEDGLASS_SERVER="+g.d.srv.URL, "DEEDGLASS_API_KEY="+g.d.org.AdminKey)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("connector postgres init: %w\n%s", err, out)
	}
	f, err := os.Open(filepath.Join(g.d.st.Dir, ".deedglass", "pg-"+g.pgName+".env"))
	if err != nil {
		return err
	}
	defer f.Close()
	secret := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		switch {
		case !ok:
		case k == "DSN":
			secret["dsn"] = v
		case k == "WITNESS_DSN":
			secret["witness_dsn"] = v
		}
	}
	if secret["dsn"] == "" {
		return errors.New("connector postgres init wrote no DSN")
	}
	if st, m := g.do("PUT", "/v1/connectors/"+g.pgID+"/credentials", map[string]any{"kind": "dsn", "secret": secret}); st != 200 && st != 201 {
		return fmt.Errorf("store the connector's credentials: HTTP %d %v", st, m)
	}
	return g.waitDecoder(ctx)
}

// waitDecoder returns once the decoder runs on the scenario's database and has read past now.
func (g *dgSession) waitDecoder(ctx context.Context) error {
	c, err := pgx.Connect(ctx, g.dsn)
	if err != nil {
		return err
	}
	defer c.Close(context.Background())
	deadline := time.Now().Add(30 * time.Second)
	var lsn string
	for time.Now().Before(deadline) {
		var ok bool
		_ = c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_replication_slots WHERE database = pg_catalog.current_database() AND slot_name LIKE 'deedglass\_%')`).Scan(&ok)
		if ok && lsn == "" {
			_ = c.QueryRow(ctx, `SELECT pg_catalog.pg_current_wal_lsn()::text`).Scan(&lsn)
		}
		if lsn != "" {
			var caught bool
			_ = c.QueryRow(ctx, `SELECT confirmed_flush_lsn >= $1::text::pg_lsn FROM pg_catalog.pg_replication_slots
			    WHERE database = pg_catalog.current_database() AND slot_name LIKE 'deedglass\_%'`, lsn).Scan(&caught)
			_, v := g.do("GET", "/v1/connectors/"+g.pgID+"/decoder", nil)
			if caught && stack.Str(v, "status") == "running" {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, v := g.do("GET", "/v1/connectors/"+g.pgID+"/decoder", nil)
	return fmt.Errorf("the decoder didn't start on %s within 30 s: %v", g.s.ID, v)
}

// startAPI starts the scenario's HTTP API on loopback: each api step's method and path runs that
// step's file on the scenario's database, as the API's own database user would. A REST connector
// points at it, one tool per step.
func (g *dgSession) startAPI(steps []corpus.Step) error {
	mux := http.NewServeMux()
	var tools []any
	methods := map[string]bool{}
	for i, st := range steps {
		st := st
		name := fmt.Sprintf("step%d", i+1)
		tools = append(tools, map[string]any{"name": name, "method": st.API.Method, "path": st.API.Path})
		methods[st.API.Method] = true
		g.tools[g.apiName+"."+name] = true
		mux.HandleFunc(st.API.Method+" "+st.API.Path, func(w http.ResponseWriter, r *http.Request) {
			src, err := os.ReadFile(filepath.Join(g.s.Dir, st.File))
			if err == nil {
				err = runSQL(r.Context(), g.dsn, corpus.SplitSQL(string(src)))
			}
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				w.WriteHeader(500)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "the request failed"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		})
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	g.api = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = g.api.Serve(l) }()
	var ms []string
	for m := range methods {
		ms = append(ms, m)
	}
	sort.Strings(ms)
	cfg := map[string]any{"base_url": "http://" + l.Addr().String(), "auth": map[string]any{"kind": "none"}, "allowed_methods": ms,
		"allowed_path_prefixes": []string{"/"}, "tools": tools}
	st, m := g.do("POST", "/v1/connectors", map[string]any{"type": "rest", "name": g.apiName, "description": "Corpus benchmark: the API of " + g.s.ID, "config": cfg})
	if st != 201 {
		return fmt.Errorf("create the rest connector: HTTP %d %v", st, m)
	}
	g.apiID = stack.Str(m, "id")
	return nil
}

func runSQL(ctx context.Context, dsn string, stmts []string) error {
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer c.Close(context.Background())
	for _, s := range stmts {
		if _, err := c.Exec(ctx, s, pgx.QueryExecModeSimpleProtocol); err != nil {
			return err
		}
	}
	return nil
}

// Agent sends each statement to the Gate as its own action, with the step's client timeout.
func (g *dgSession) Agent(ctx context.Context, i int, st corpus.Step, stmts []string) ([]string, bool, error) {
	timeout := 30 * time.Second
	if st.ClientTimeoutMS > 0 {
		timeout = time.Duration(st.ClientTimeoutMS) * time.Millisecond
	}
	type call struct {
		tool string
		args map[string]any
	}
	var calls []call
	if st.Via == "api" {
		n := 0
		for _, s := range g.s.Steps[:i+1] {
			if s.Via == "api" {
				n++
			}
		}
		calls = append(calls, call{fmt.Sprintf("%s.step%d", g.apiName, n), map[string]any{}})
	} else {
		for _, s := range stmts {
			calls = append(calls, call{g.pgName + ".execute", map[string]any{"sql": s}})
		}
	}
	var saw []string
	for _, c := range calls {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		code, m, err := g.d.st.DoCtx(cctx, g.d.srv, "POST", "/v1/actions", g.d.agent.Key,
			map[string]any{"tool": c.tool, "args": c.args, "mode": "gateway", "wait": 25})
		cancel()
		if err != nil {
			if errors.Is(cctx.Err(), context.DeadlineExceeded) {
				saw = append(saw, "no reply (client timeout)")
				if st.WaitForServer {
					if err := g.waitSettled(ctx, 30*time.Second); err != nil {
						return nil, true, err
					}
				}
				break
			}
			return nil, true, err
		}
		id := stack.Str(m, "id")
		if id == "" {
			id = stack.Str(m, "error", "action", "id")
		}
		if id != "" {
			g.ids = append(g.ids, id)
		}
		line, stop := sawLine(code, m)
		saw = append(saw, line)
		if stop && st.StopOnError {
			break
		}
	}
	return saw, true, nil
}

// sawLine is what the agent's client was told, in one line, and whether it was a failure.
func sawLine(code int, m map[string]any) (string, bool) {
	if code >= 300 {
		e := stack.Str(m, "error", "code")
		return fmt.Sprintf("HTTP %d %s: %s", code, e, stack.Str(m, "error", "message")), true
	}
	dec := stack.Str(m, "decision", "outcome")
	ex := stack.Str(m, "execution", "status")
	line := fmt.Sprintf("HTTP %d %s, execution %s", code, dec, ex)
	if v := stack.Str(m, "witness", "verdict"); v != "" {
		line += fmt.Sprintf(", %s at %s", v, stack.Str(m, "witness", "level"))
	}
	return line, dec != "allow" || ex == "failed"
}

// waitSettled waits until every action of the scenario's tools is sealed.
func (g *dgSession) waitSettled(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		acts, err := g.actions()
		if err != nil {
			return err
		}
		open := 0
		for _, a := range acts {
			if stack.Str(a, "state") != "sealed" {
				open++
			}
		}
		if open == 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("the scenario's actions didn't settle")
}

// actions lists this scenario's actions, oldest first (from the Ledger, not from what the agent
// was told: a timed-out call has no answer).
func (g *dgSession) actions() ([]map[string]any, error) {
	st, m := g.do("GET", "/v1/actions?limit=100", nil)
	if st != 200 {
		return nil, fmt.Errorf("list actions: HTTP %d %v", st, m)
	}
	data, _ := m["data"].([]any)
	var out []map[string]any
	for _, x := range data {
		a, _ := x.(map[string]any)
		if a != nil && g.tools[stack.Str(a, "tool")] {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return stack.Str(out[i], "id") < stack.Str(out[j], "id") })
	return out, nil
}

type actionView struct {
	id, tool, decision, rule, execution, level, verdict, summary string
	reasons, codes                                               []string
	observed                                                     corpus.Changes
	hasCounts                                                    bool
}

func (g *dgSession) view(id string) (actionView, error) {
	deadline := time.Now().Add(20 * time.Second)
	var a map[string]any
	for {
		st, m := g.do("GET", "/v1/actions/"+id+"?expand[]=witness.detail", nil)
		if st != 200 {
			return actionView{}, fmt.Errorf("get %s: HTTP %d %v", id, st, m)
		}
		a = m
		if stack.Str(a, "state") == "sealed" || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	v := actionView{id: id, tool: stack.Str(a, "tool"), decision: stack.Str(a, "decision", "outcome"), rule: stack.Str(a, "decision", "rule_id"),
		execution: stack.Str(a, "execution", "status"), level: stack.Str(a, "witness", "level"), verdict: stack.Str(a, "witness", "verdict"),
		summary: stack.Str(a, "witness", "summary")}
	w, _ := a["witness"].(map[string]any)
	det, _ := w["detail"].(map[string]any)
	rs, _ := det["reasons"].([]any)
	for _, x := range rs {
		r, _ := x.(map[string]any)
		v.codes = append(v.codes, stack.Str(r, "code"))
		if stack.Str(r, "class") != "pass" && stack.Str(r, "class") != "note" {
			v.reasons = append(v.reasons, stack.Str(r, "code"))
		}
	}
	if obs := findObserved(det); obs != nil {
		v.observed, v.hasCounts = obs, true
	}
	return v, nil
}

// findObserved finds the L2 detail's observed counts (observed.tables[]) anywhere in the witness
// detail.
func findObserved(x any) corpus.Changes {
	switch t := x.(type) {
	case map[string]any:
		if o, ok := t["observed"].(map[string]any); ok {
			if ts, ok := o["tables"].([]any); ok {
				out := corpus.Changes{}
				for _, y := range ts {
					r, _ := y.(map[string]any)
					tbl := stack.Str(r, "table")
					for _, op := range []string{"insert", "update", "delete"} {
						if n, ok := r[op].(float64); ok && n > 0 {
							if out[tbl] == nil {
								out[tbl] = map[string]int{}
							}
							out[tbl][op] = int(n)
						}
					}
				}
				return out
			}
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if c := findObserved(t[k]); c != nil {
				return c
			}
		}
	case []any:
		for _, y := range t {
			if c := findObserved(y); c != nil {
				return c
			}
		}
	}
	return nil
}

// Finish reads every action the scenario made from the Ledger, scores the flag and the effect
// from what Deedglass recorded, exports the deeds and checks them offline, then removes the
// scenario's connectors.
func (g *dgSession) Finish(ctx context.Context) (bench.Report, error) {
	defer g.cleanup(ctx)
	if err := g.waitSettled(ctx, 30*time.Second); err != nil {
		return bench.Report{}, err
	}
	acts, err := g.actions()
	if err != nil {
		return bench.Report{}, err
	}
	rep := bench.Report{Flag: bench.FlagNone}
	eff := bench.Effect{Changes: corpus.Changes{}}
	says := true
	var views []actionView
	for _, a := range acts {
		v, err := g.view(stack.Str(a, "id"))
		if err != nil {
			return bench.Report{}, err
		}
		views = append(views, v)
		note := fmt.Sprintf("action %d (%s): %s", len(views), v.tool[strings.Index(v.tool, ".")+1:], v.decision)
		switch {
		case v.decision == "deny" || v.decision == "require_approval":
			if bench.FlagBefore.Stronger(rep.Flag) {
				rep.Flag = bench.FlagBefore
			}
			note += " by " + v.rule
		default:
			note += ", execution " + v.execution
			if v.verdict != "" {
				note += fmt.Sprintf(", %s at %s", v.verdict, v.level)
				if len(v.reasons) > 0 {
					note += " (" + strings.Join(v.reasons, ", ") + ")"
				}
			}
			if v.verdict == "MISMATCH" && bench.FlagAfter.Stronger(rep.Flag) {
				rep.Flag = bench.FlagAfter
			}
			if v.verdict == "UNVERIFIABLE" && bench.FlagUnverifiable.Stronger(rep.Flag) {
				rep.Flag = bench.FlagUnverifiable
			}
		}
		rep.Notes = append(rep.Notes, note)
		rep.Actions = append(rep.Actions, bench.ActionOutcome{Tool: v.tool[strings.Index(v.tool, ".")+1:], Decision: v.decision, Rule: v.rule,
			Execution: v.execution, Verdict: v.verdict, Level: v.level, Reasons: v.codes, Summary: v.summary})
		// What Deedglass says committed: an action that never ran, or failed, changed nothing; one
		// that ran says what committed only when its L2 detail counted the rows.
		if v.decision != "allow" || v.execution == "failed" {
			continue
		}
		if v.level != "L2" || !v.hasCounts {
			says = false
			continue
		}
		for t, ops := range v.observed {
			if eff.Changes[t] == nil {
				eff.Changes[t] = map[string]int{}
			}
			for op, n := range ops {
				eff.Changes[t][op] += n
			}
		}
	}
	if len(acts) == 0 {
		rep.Notes = append(rep.Notes, "no action was recorded")
	}
	if says {
		rep.Effect = &eff
	}
	rep.Record = g.checkRecord(ctx, views)
	return rep, nil
}

// checkRecord exports the org's deeds as a bundle, runs the offline verifier on it, and checks
// that every action of the scenario has a deed whose decision, execution and witness verdict are
// what the API showed.
func (g *dgSession) checkRecord(ctx context.Context, views []actionView) *bench.Record {
	r := &bench.Record{Kind: "deed bundle (deedglass deeds export)", Check: "deedglass verify <bundle>"}
	path := filepath.Join(g.d.st.Dir, "bench-"+g.s.ID+".tar.gz")
	defer os.Remove(path)
	exp := exec.CommandContext(ctx, g.d.st.Bin, "deeds", "export", "--out", path)
	exp.Dir = g.d.st.Dir
	exp.Env = g.d.st.Env()
	if out, err := exp.CombinedOutput(); err != nil {
		r.Problem = "export failed: " + lastLine(string(out))
		return r
	}
	ver := exec.CommandContext(ctx, g.d.st.Bin, "verify", path)
	ver.Dir = os.TempDir()
	ver.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "NO_COLOR=1"}
	if out, err := ver.CombinedOutput(); err != nil {
		r.Problem = "verify failed: " + lastLine(string(out))
		return r
	}
	deeds, err := readDeeds(path)
	if err != nil {
		r.Problem = err.Error()
		return r
	}
	for _, v := range views {
		b, ok := deeds[v.id]
		if !ok {
			r.Problem = "no deed for " + v.id
			return r
		}
		if got := str(b, "policy", "decision"); got != v.decision {
			r.Problem = fmt.Sprintf("the deed for %s says decision %q, the API %q", v.id, got, v.decision)
			return r
		}
		if v.decision == "allow" {
			if got := str(b, "execution", "status"); got != v.execution {
				r.Problem = fmt.Sprintf("the deed for %s says execution %q, the API %q", v.id, got, v.execution)
				return r
			}
		}
		if got := str(b, "witness", "verdict"); got != v.verdict {
			r.Problem = fmt.Sprintf("the deed for %s says verdict %q, the API %q", v.id, got, v.verdict)
			return r
		}
	}
	if len(views) == 0 {
		r.Problem = "no action, so no deed to check"
		return r
	}
	r.Checked = true
	return r
}

// readDeeds reads deeds.jsonl from a bundle: the latest deed body per action id (an amendment
// supersedes the action's first deed).
func readDeeds(path string) (map[string]map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("the bundle has no deeds.jsonl")
		}
		if err != nil {
			return nil, err
		}
		if h.Name != "deeds.jsonl" {
			continue
		}
		out := map[string]map[string]any{}
		sc := bufio.NewScanner(tr)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var env struct {
				Body map[string]any `json:"body"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &env); err != nil {
				return nil, err
			}
			if id := str(env.Body, "action", "id"); id != "" {
				out[id] = env.Body
			}
		}
		return out, sc.Err()
	}
}

func str(m map[string]any, path ...string) string { return stack.Str(m, path...) }

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(ls[len(ls)-1])
}

// cleanup removes the scenario's connectors and any replication slot left in its database, so the
// runner can drop it.
func (g *dgSession) cleanup(ctx context.Context) {
	if g.api != nil {
		_ = g.api.Close()
	}
	if g.pgID != "" {
		g.do("DELETE", "/v1/connectors/"+g.pgID+"/decoder", map[string]any{"confirm": g.pgName})
		g.do("DELETE", "/v1/connectors/"+g.pgID, nil)
	}
	if g.apiID != "" {
		g.do("DELETE", "/v1/connectors/"+g.apiID, nil)
	}
	if g.dsn == "" {
		return
	}
	c, err := pgx.Connect(ctx, g.dsn)
	if err != nil {
		return
	}
	defer c.Close(context.Background())
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := c.Query(ctx, `SELECT slot_name, coalesce(active_pid, 0) FROM pg_catalog.pg_replication_slots
		    WHERE database = pg_catalog.current_database() AND slot_name LIKE 'deedglass\_%'`)
		if err != nil {
			return
		}
		type slot struct {
			name string
			pid  int32
		}
		var ss []slot
		for rows.Next() {
			var s slot
			_ = rows.Scan(&s.name, &s.pid)
			ss = append(ss, s)
		}
		rows.Close()
		if len(ss) == 0 {
			return
		}
		for _, s := range ss {
			if s.pid != 0 {
				_, _ = c.Exec(ctx, "SELECT pg_catalog.pg_terminate_backend($1)", s.pid)
				continue
			}
			_, _ = c.Exec(ctx, "SELECT pg_catalog.pg_drop_replication_slot($1)", s.name)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
