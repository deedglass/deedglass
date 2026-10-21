//go:build integration

package main

//deedglass:allow-exec benchmark tooling: runs the deedglass binary under test (deeds export, verify) on a throwaway cluster

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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/bench/internal/stack"
	"github.com/deedglass/deedglass/internal/corpus/bench"
	"github.com/deedglass/deedglass/internal/corpus/fhirbench"
)

// policy is the policy the Gate enforces in the benchmark (corpus/fhir/BENCHMARK.md): the
// healthcare-fhir pack's refusals word for word, then writes allowed with a Checked (L1) check.
//
//go:embed policy.yaml
var policy string

type deedglassSubject struct {
	st      *stack.Stack
	srv     *stack.Server
	org     *stack.Org
	agent   stack.Agent
	version string
	types   []string
	n       int
}

func startDeedglass(root string, types []string) (s *deedglassSubject, err error) {
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
	org := st.NewOrg(srv, "fhir-bench", 1, policy)
	v := strings.TrimSpace(string(st.CLI("version")))
	if f := strings.Fields(v); len(f) >= 2 {
		v = f[1]
	}
	return &deedglassSubject{st: st, srv: srv, org: org, agent: org.Agents[0], version: v, types: types}, nil
}

func (d *deedglassSubject) Info() bench.Info {
	return bench.Info{Name: "deedglass", Kind: "gateway", Version: d.version,
		Config: "deedglass serve built from this commit. Every agent call goes through the Gate as its own action, to a fhir connector with default settings (if_match when_known), each write read back at the version the server reported (Checked, L1) and Proven (L2) from Medplum's change log, with meta.author checked against the agent's client. The policy is the healthcare-fhir pack's refusals with writes allowed at L1 (bench/cmd/fhir-bench/policy.yaml). A Bundle or a conditional update goes through a REST connector (the fhir connector takes one resource per call), which records the call (L0, Claimed)."}
}

func (d *deedglassSubject) Close() error {
	d.st.Close()
	return nil
}

func (d *deedglassSubject) Start(ctx context.Context, s *fhirbench.Scenario, env fhirbench.Env) (fhirbench.Session, error) {
	d.n++
	g := &dgSession{d: d, s: s, env: env, fhirName: fmt.Sprintf("fb%02d%s", d.n, slug(s.ID)), tools: map[string]bool{}}
	g.restName = "r" + g.fhirName[1:]
	if len(g.restName) > 32 {
		g.restName = g.restName[:32]
	}
	if len(g.fhirName) > 32 {
		g.fhirName = g.fhirName[:32]
	}
	if err := g.prepare(ctx); err != nil {
		g.cleanup(ctx)
		return nil, err
	}
	return g, nil
}

func slug(id string) string {
	var b strings.Builder
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return strings.ToLower(b.String())
}

type dgSession struct {
	d                  *deedglassSubject
	s                  *fhirbench.Scenario
	env                fhirbench.Env
	fhirName, restName string
	fhirID, restID     string
	tools              map[string]bool
	restTools          map[int]restTool
	sent               map[string]map[string]any // action id → the args the agent sent
}

type restTool struct {
	name  string
	query map[string]string // arg name → query parameter
	body  map[string]string // arg name → body key
}

func (g *dgSession) do(method, path string, body any) (int, map[string]any) {
	st, m, err := g.d.st.Do(g.d.srv, method, path, g.d.org.AdminKey, body)
	if err != nil {
		return 0, map[string]any{"error": err.Error()}
	}
	return st, m
}

// viaREST: the fhir connector takes one resource per call on a type and id, so a Bundle and a
// conditional update (a search in the path) go through a REST connector.
func viaREST(r fhirbench.Request) bool {
	p := strings.SplitN(r.Path, "?", 2)
	return r.Path == "" || len(p) == 2
}

// prepare creates the scenario's fhir connector (Proven on, the agent's client as the expected
// meta.author) and, when a step needs one, a REST connector with one tool per such step.
func (g *dgSession) prepare(ctx context.Context) error {
	cfg := map[string]any{"base_url": g.env.BaseURL, "auth": map[string]any{"kind": "bearer"}, "allowed_resource_types": g.d.types,
		"allowed_interactions": []string{"read", "vread", "search", "history", "create", "update", "patch", "delete"},
		"witness":              map[string]any{"enabled": true, "server": "medplum", "author": "ClientApplication/" + g.env.ClientID}}
	st, m := g.do("POST", "/v1/connectors", map[string]any{"type": "fhir", "name": g.fhirName, "description": "FHIR benchmark: " + g.s.ID, "config": cfg})
	if st != 201 {
		return fmt.Errorf("create the fhir connector: HTTP %d %v", st, m)
	}
	g.fhirID = stack.Str(m, "id")
	if st, m = g.do("PUT", "/v1/connectors/"+g.fhirID+"/credentials", map[string]any{"kind": "bearer",
		"secret": map[string]string{"token": g.env.Token, "witness_dsn": g.env.WitnessDSN}}); st != 200 && st != 201 {
		return fmt.Errorf("store the fhir connector's credentials: HTTP %d %v", st, m)
	}
	for _, t := range []string{"create", "update", "patch", "delete"} {
		g.tools[g.fhirName+"."+t] = true
	}
	g.restTools = map[int]restTool{}
	var tools []any
	methods := map[string]bool{}
	for i, step := range g.s.Steps {
		if step.Session != "agent" || !viaREST(step.Request) {
			continue
		}
		rt := restTool{name: fmt.Sprintf("step%d", i+1), query: map[string]string{}, body: map[string]string{}}
		path, q, _ := strings.Cut(step.Path, "?")
		args := map[string]any{}
		for _, kv := range strings.Split(q, "&") {
			if k, _, ok := strings.Cut(kv, "="); ok {
				a := k // the REST connector sends a query arg under its own name
				rt.query[a] = k
				args[a] = map[string]any{"in": "query", "type": "string", "required": true}
			}
		}
		var body map[string]any
		_ = json.Unmarshal(step.Body, &body)
		for k, v := range body {
			a := argName("b", k)
			rt.body[a] = k
			typ := "string"
			switch v.(type) {
			case []any:
				typ = "array"
			case map[string]any:
				typ = "object"
			}
			args[a] = map[string]any{"in": "body", "type": typ, "field": k, "required": true}
		}
		tools = append(tools, map[string]any{"name": rt.name, "method": step.Method, "path": "/" + path, "args": args})
		methods[step.Method] = true
		g.restTools[i] = rt
		g.tools[g.restName+"."+rt.name] = true
	}
	if len(tools) > 0 {
		var ms []string
		for k := range methods {
			ms = append(ms, k)
		}
		sort.Strings(ms)
		cfg := map[string]any{"base_url": g.env.BaseURL, "auth": map[string]any{"kind": "bearer"}, "allowed_methods": ms,
			"allowed_path_prefixes": []string{"/"}, "tools": tools}
		st, m := g.do("POST", "/v1/connectors", map[string]any{"type": "rest", "name": g.restName, "description": "FHIR benchmark, Bundles and conditional updates: " + g.s.ID, "config": cfg})
		if st != 201 {
			return fmt.Errorf("create the rest connector: HTTP %d %v", st, m)
		}
		g.restID = stack.Str(m, "id")
		if st, m = g.do("PUT", "/v1/connectors/"+g.restID+"/credentials", map[string]any{"kind": "bearer", "secret": map[string]string{"token": g.env.Token}}); st != 200 && st != 201 {
			return fmt.Errorf("store the rest connector's credentials: HTTP %d %v", st, m)
		}
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, v := g.do("GET", "/v1/connectors/"+g.fhirID+"/decoder", nil)
		if stack.Str(v, "status") == "running" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the fhir connector's decoder isn't running: %v", v)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

var reArg = regexp.MustCompile(`[^a-z0-9_]`)

func argName(prefix, k string) string {
	var b strings.Builder
	for i, r := range k {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return prefix + "_" + reArg.ReplaceAllString(b.String(), "_")
}

// call maps one agent request to a tool and its arguments.
func (g *dgSession) call(i int, r fhirbench.Request) (string, map[string]any, error) {
	if rt, ok := g.restTools[i]; ok {
		args := map[string]any{}
		_, q, _ := strings.Cut(r.Path, "?")
		for _, kv := range strings.Split(q, "&") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				for a, name := range rt.query {
					if name == k {
						args[a] = v
					}
				}
			}
		}
		var body map[string]any
		_ = json.Unmarshal(r.Body, &body)
		for a, k := range rt.body {
			args[a] = body[k]
		}
		return g.restName + "." + rt.name, args, nil
	}
	typ, id, _ := strings.Cut(r.Path, "/")
	args := map[string]any{"resource_type": typ}
	if id != "" {
		args["id"] = id
	}
	if v := r.Headers["If-Match"]; v != "" {
		args["if_match"] = strings.Trim(strings.TrimPrefix(v, "W/"), `"`)
	}
	var body any
	if len(r.Body) > 0 {
		if err := json.Unmarshal(r.Body, &body); err != nil {
			return "", nil, err
		}
	}
	switch r.Method {
	case "POST":
		args["resource"] = body
		if v := r.Headers["If-None-Exist"]; v != "" {
			args["if_none_exist"] = v
		}
		return g.fhirName + ".create", args, nil
	case "PUT":
		args["resource"] = body
		return g.fhirName + ".update", args, nil
	case "PATCH":
		args["json_patch"] = body
		return g.fhirName + ".patch", args, nil
	case "DELETE":
		return g.fhirName + ".delete", args, nil
	}
	return "", nil, fmt.Errorf("no tool for %s %s", r.Method, r.Path)
}

// Agent sends the call to the Gate as one action and returns what the agent's client saw: the
// FHIR server's answer as the connector passed it on, or the Gate's refusal.
func (g *dgSession) Agent(ctx context.Context, i int, _ fhirbench.Step, r fhirbench.Request) (fhirbench.Exchange, bool, error) {
	tool, args, err := g.call(i, r)
	if err != nil {
		return fhirbench.Exchange{}, true, err
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	code, m, err := g.d.st.DoCtx(cctx, g.d.srv, "POST", "/v1/actions", g.d.agent.Key,
		map[string]any{"tool": tool, "args": args, "mode": "gateway", "wait": 25})
	if err != nil {
		return fhirbench.Exchange{Error: err.Error()}, true, nil
	}
	if code >= 300 {
		return fhirbench.Exchange{Error: fmt.Sprintf("Gate HTTP %d %s: %s", code, stack.Str(m, "error", "code"), stack.Str(m, "error", "message"))}, true, nil
	}
	if id := stack.Str(m, "id"); id != "" {
		if g.sent == nil {
			g.sent = map[string]map[string]any{}
		}
		g.sent[id] = args
	}
	ex := fhirbench.Exchange{}
	exe, _ := m["execution"].(map[string]any)
	res, _ := exe["result"].(map[string]any)
	if s, ok := res["status"].(float64); ok {
		ex.Status = int(s)
	}
	if h, ok := res["headers"].(map[string]any); ok {
		ex.Location, _ = h["location"].(string)
		ex.ETag, _ = h["etag"].(string)
	}
	if b, ok := res["body"]; ok && b != nil {
		raw, _ := json.Marshal(b)
		ex.Body = scrubTracing(raw)
	}
	if dec := stack.Str(m, "decision", "outcome"); dec != "allow" {
		ex.Error = "Gate decision " + dec
	} else if ex.Status == 0 {
		ex.Error = strings.TrimSpace("execution " + stack.Str(m, "execution", "status") + " " + stack.Str(m, "execution", "error_code") + " " + stack.Str(m, "execution", "error_message"))
	}
	return ex, true, nil
}

// actions lists this scenario's actions, oldest first, from the Ledger.
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
	codes, reasons                                               []string
	fields                                                       []string // elements a Doesn't match names
	args                                                         map[string]any
	errCode, errMsg                                              string
}

func (g *dgSession) view(id string) (actionView, error) {
	deadline := time.Now().Add(60 * time.Second)
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
		time.Sleep(200 * time.Millisecond)
	}
	v := actionView{id: id, tool: stack.Str(a, "tool"), decision: stack.Str(a, "decision", "outcome"), rule: stack.Str(a, "decision", "rule_id"),
		execution: stack.Str(a, "execution", "status"), level: stack.Str(a, "witness", "level"), verdict: stack.Str(a, "witness", "verdict"),
		summary: stack.Str(a, "witness", "summary")}
	v.args = g.sent[id]
	v.errCode, v.errMsg = stack.Str(a, "execution", "error_code"), stack.Str(a, "execution", "error_message")
	w, _ := a["witness"].(map[string]any)
	det, _ := w["detail"].(map[string]any)
	rs, _ := det["reasons"].([]any)
	for _, x := range rs {
		r, _ := x.(map[string]any)
		code := stack.Str(r, "code")
		v.codes = append(v.codes, code)
		if c := stack.Str(r, "class"); c != "pass" && c != "note" {
			v.reasons = append(v.reasons, code)
		}
		if code == "fhir_field_changed" || code == "version_field_changed" {
			if p := findPath(r); p != "" {
				v.fields = append(v.fields, p)
			}
		}
	}
	return v, nil
}

// findPath finds a "path" (L2) or "field" (L1) string anywhere in a reason, like "note" or
// "/note/0".
func findPath(x any) string {
	switch t := x.(type) {
	case map[string]any:
		for _, k := range []string{"path", "field"} {
			if p, ok := t[k].(string); ok && p != "" {
				return p
			}
		}
		ks := make([]string, 0, len(t))
		for k := range t {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			if p := findPath(t[k]); p != "" {
				return p
			}
		}
	case []any:
		for _, y := range t {
			if p := findPath(y); p != "" {
				return p
			}
		}
	}
	return ""
}

func top(path string) string {
	p := strings.TrimPrefix(path, "/")
	for i, r := range p {
		if r == '/' || r == '.' || r == '[' {
			return p[:i]
		}
	}
	return p
}

// Finish reads every action of the scenario from the Ledger, scores the flag and Deedglass's
// account of what was stored from what it recorded, exports the deeds and checks them offline,
// then removes the scenario's connectors.
func (g *dgSession) Finish(ctx context.Context, _ *fhirbench.Run) (fhirbench.Report, error) {
	defer g.cleanup(ctx)
	acts, err := g.actions()
	if err != nil {
		return fhirbench.Report{}, err
	}
	rep := fhirbench.Report{Flag: bench.FlagNone}
	eff := fhirbench.Effect{Changes: map[string]map[string]int{}}
	says := true
	var views []actionView
	for _, a := range acts {
		v, err := g.view(stack.Str(a, "id"))
		if err != nil {
			return fhirbench.Report{}, err
		}
		views = append(views, v)
		tool := v.tool[strings.Index(v.tool, ".")+1:]
		note := fmt.Sprintf("action %d (%s): %s", len(views), tool, v.decision)
		switch {
		case v.decision == "deny" || v.decision == "require_approval":
			if bench.FlagBefore.Stronger(rep.Flag) {
				rep.Flag = bench.FlagBefore
			}
			note += " by " + v.rule
		default:
			note += ", execution " + v.execution
			if v.execution == "failed" && v.errCode != "" {
				note += " (" + v.errCode + ")"
			}
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
		rep.Actions = append(rep.Actions, bench.ActionOutcome{Tool: tool, Decision: v.decision, Rule: v.rule, Execution: v.execution,
			Verdict: v.verdict, Level: v.level, Reasons: v.codes, Summary: v.summary})
		// Deedglass's account: an action that never ran, or failed, stored nothing; one that ran
		// says what it stored only when it was checked (L1 or L2).
		if v.decision != "allow" || v.execution == "failed" {
			continue
		}
		if v.level != "L1" && v.level != "L2" {
			says = false
			continue
		}
		typ, _ := v.args["resource_type"].(string)
		if has(v.codes, "fhir_conditional_matched") || has(v.codes, "fhir_no_new_version") {
			continue
		}
		op := map[string]string{"create": "create", "update": "update", "patch": "update", "delete": "delete"}[tool]
		if op == "" || typ == "" {
			says = false
			continue
		}
		if eff.Changes[typ] == nil {
			eff.Changes[typ] = map[string]int{}
		}
		eff.Changes[typ][op]++
		for _, f := range v.fields {
			eff.Differs = append(eff.Differs, typ+"."+top(f))
		}
	}
	if len(acts) == 0 {
		rep.Notes = append(rep.Notes, "no action was recorded")
	}
	if says {
		n := eff.Normal()
		rep.Effect = &n
	}
	rep.Record = g.checkRecord(ctx, views)
	return rep, nil
}

func has(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// checkRecord exports the org's deeds, runs the offline verifier, and checks that every action of
// the scenario has a deed whose decision, execution and witness verdict are what the API showed.
func (g *dgSession) checkRecord(ctx context.Context, views []actionView) *bench.Record {
	r := &bench.Record{Kind: "deed bundle (deedglass deeds export)", Check: "deedglass verify <bundle>"}
	path := filepath.Join(g.d.st.Dir, "fhir-bench-"+g.s.ID+".tar.gz")
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
		if got := stack.Str(b, "policy", "decision"); got != v.decision {
			r.Problem = fmt.Sprintf("the deed for %s says decision %q, the API %q", v.id, got, v.decision)
			return r
		}
		if v.decision == "allow" {
			if got := stack.Str(b, "execution", "status"); got != v.execution {
				r.Problem = fmt.Sprintf("the deed for %s says execution %q, the API %q", v.id, got, v.execution)
				return r
			}
		}
		if got := stack.Str(b, "witness", "verdict"); got != v.verdict {
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

// readDeeds reads deeds.jsonl from a bundle: the latest deed body per action id.
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
			if id := stack.Str(env.Body, "action", "id"); id != "" {
				out[id] = env.Body
			}
		}
		return out, sc.Err()
	}
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(ls[len(ls)-1])
}

// cleanup removes the scenario's connectors and the decoder's replication slot on Medplum's
// database.
func (g *dgSession) cleanup(ctx context.Context) {
	if g.fhirID != "" {
		g.do("DELETE", "/v1/connectors/"+g.fhirID+"/decoder", map[string]any{"confirm": g.fhirName})
		g.do("DELETE", "/v1/connectors/"+g.fhirID, nil)
	}
	if g.restID != "" {
		g.do("DELETE", "/v1/connectors/"+g.restID, nil)
	}
	c, err := pgx.Connect(ctx, g.env.AdminDSN)
	if err != nil {
		return
	}
	defer c.Close(context.Background())
	slot := "deedglass_" + g.fhirName
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var pid *int32
		err := c.QueryRow(ctx, `SELECT active_pid FROM pg_catalog.pg_replication_slots WHERE slot_name = $1`, slot).Scan(&pid)
		if err == pgx.ErrNoRows {
			return
		}
		if err != nil {
			return
		}
		if pid != nil {
			_, _ = c.Exec(ctx, "SELECT pg_catalog.pg_terminate_backend($1)", *pid)
		} else {
			_, _ = c.Exec(ctx, "SELECT pg_catalog.pg_drop_replication_slot($1)", slot)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
