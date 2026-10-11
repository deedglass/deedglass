//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// witnessPolicy asks for L1 on order updates (S19): the policy's verify: is the required level.
const witnessPolicy = `deedglass: policy/v1
name: orders-witnessed
description: Order updates must be confirmed by a read-back (S19).

defaults:
  decision: deny
  reason: No rule allows this action.

rules:
  - id: order-reads
    match: { tool: ["orders.get"] }
    decision: allow
  - id: order-status
    match: { tool: ["orders.update_status"] }
    decision: allow
    verify: L1
`

var (
	reISOTime = regexp.MustCompile(`"20[0-9]{2}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z"`)
	reDigest  = regexp.MustCompile(`sha256:[0-9a-f]{64}`)
	reLatency = regexp.MustCompile(`"latency_ms": [0-9]+`)
	reAgentID = regexp.MustCompile(`(agt|pol|pv)_[0-9a-z]{26}`)
	reWhen    = regexp.MustCompile(`[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2} UTC`)
	reShortPV = regexp.MustCompile(`(pv|sk)_[0-9a-z]{8}…`)
)

func normalizeJSON(s string) string {
	s = reISOTime.ReplaceAllString(s, `"T"`)
	s = reDigest.ReplaceAllString(s, "sha256:…")
	s = reLatency.ReplaceAllString(s, `"latency_ms": N`)
	return reAgentID.ReplaceAllString(s, "${1}_…")
}

// TestWitnessLive is S19's acceptance (§9) on a real server: the REST orders connector with its
// read-back, a policy that asks for L1, and the mock orders API in honest and lying mode. Honest
// gives L1 VERIFIED; lying gives L1 MISMATCH with the body.status diff; a read-back that fails
// gives L0 UNVERIFIABLE (the policy asked for L1); GET /v1/actions/{id} returns the witness object
// (and the detail with expand[]=witness.detail); actions list --verdict MISMATCH finds it; the
// export verifies offline; the sentinel sweep finds no credential in any witness detail. The act
// output is pinned in testdata/gate/cli (TTY colour, plain, --json).
func TestWitnessLive(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	mock := &mockOrders{status: map[int]string{}}
	var lying, readDown bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && readDown && strings.HasPrefix(r.URL.Path, "/v1/orders/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"error":"down"}`)
			return
		case r.Method == http.MethodPatch && lying:
			// examples/mock-orders' lying mode: answer 200 with the new status, save nothing.
			mock.hits.Add(1)
			var in struct{ Status string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":42,"status":"`+in.Status+`"}`)
			return
		}
		mock.ServeHTTP(w, r)
	}))
	defer up.Close()
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 11)
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "debug", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "true",
		"ORDERS_TOKEN": sentinelToken}
	var outputs []string
	s := startServe(t, env, "--migrate")
	var base string
	select {
	case a := <-s.addr:
		base = "http://" + a.String()
	case c := <-s.code:
		t.Fatalf("exited %d\n%s%s", c, s.out, s.err)
	case <-time.After(30 * time.Second):
		t.Fatal("serve never listened")
	}
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base
	run := func(env map[string]string, tty bool, args ...string) result {
		t.Helper()
		r := cli(t, env, tty, args...)
		outputs = append(outputs, r.stdout, r.stderr)
		return r
	}
	r := run(env, false, "org", "create", "--name", "Acme Robotics", "--slug", "acme", "--owner-email", "priya@acme.test", "--admin-key", "--json")
	var created struct {
		AdminKey struct{ Secret string } `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatalf("org create: %+v", r)
	}
	admin := created.AdminKey.Secret
	env["DEEDGLASS_API_KEY"] = admin
	pf := filepath.Join(t.TempDir(), "orders-witnessed.yaml")
	_ = os.WriteFile(pf, []byte(witnessPolicy), 0o600)
	if r = run(env, false, "policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run(env, false, "policy", "activate", "orders-witnessed", "--version", "1", "--mode", "enforce", "--reason", "Witnessed orders", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "connectors", "orders.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cf := filepath.Join(t.TempDir(), "orders.yaml")
	_ = os.WriteFile(cf, bytes.Replace(src, []byte("http://127.0.0.1:8090"), []byte(up.URL), 1), 0o600)
	if r = run(env, false, "connector", "create", "--config", cf); r.code != 0 {
		t.Fatalf("create: %+v", r)
	}
	if r = run(env, false, "connector", "credentials", "set", "orders", "--from-env", "ORDERS_TOKEN"); r.code != 0 {
		t.Fatalf("credentials: %+v", r)
	}
	call := func(key, method, path, body string) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequestWithContext(context.Background(), method, base+path, rd)
		req.Header.Set("Authorization", "Bearer "+key)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		outputs = append(outputs, string(b))
		return res.StatusCode, string(b)
	}
	code, body := call(admin, "POST", "/v1/agents", `{"name":"support-bot"}`)
	var agent struct{ ID string }
	if code != 201 || json.Unmarshal([]byte(body), &agent) != nil {
		t.Fatalf("%d %s", code, body)
	}
	code, body = call(admin, "POST", "/v1/agents/"+agent.ID+"/keys", `{}`)
	var ak struct{ Secret string }
	if code != 201 || json.Unmarshal([]byte(body), &ak) != nil {
		t.Fatalf("%d %s", code, body)
	}
	agentEnv := map[string]string{}
	for k, v := range env {
		agentEnv[k] = v
	}
	agentEnv["DEEDGLASS_API_KEY"] = ak.Secret
	args := `{"id":"42","status":"shipped"}`

	// 1. Honest: L1 VERIFIED.
	r = run(agentEnv, false, "act", "orders.update_status", "--args", args)
	if r.code != 0 || !strings.Contains(r.stdout, "✓ executed · orders.update_status") ||
		!strings.Contains(r.stdout, `check    Checked · Matches · Read back orders.get: status is "shipped".`) {
		t.Fatalf("honest: %+v", r)
	}
	gateGolden(t, "witness-verified", r)
	t.Logf("\n%s", r.stdout)

	// 2. Lying: the PATCH answers 200 and saves nothing; the read-back says "shipped", not "cancelled".
	lying = true
	cancel := `{"id":"42","status":"cancelled"}`
	r = run(agentEnv, false, "act", "orders.update_status", "--args", cancel)
	want := `check    Checked · Doesn't match · orders.update_status reported success, but orders.get says status is "shipped", not "cancelled".`
	if r.code != 1 || !strings.Contains(r.stdout, "✕ executed · orders.update_status") || !strings.Contains(r.stdout, want) ||
		!regexp.MustCompile(`body\.status\s+cancelled\s+shipped`).MatchString(r.stdout) {
		t.Fatalf("lying: %+v", r)
	}
	gateGolden(t, "witness-mismatch", r)
	t.Logf("\n%s", r.stdout)
	rc := run(agentEnv, true, "act", "orders.update_status", "--args", cancel)
	gateGolden(t, "witness-mismatch-color", rc)
	rj := run(agentEnv, false, "act", "orders.update_status", "--args", cancel, "--json")
	var act struct {
		ID string `json:"id"`
	}
	if rj.code != 1 || json.Unmarshal([]byte(rj.stdout), &act) != nil {
		t.Fatalf("json: %+v", rj)
	}
	rj.stdout = normalizeJSON(rj.stdout)
	gateGolden(t, "witness-mismatch-json", rj)
	// The API: the witness object, and the full detail on request.
	code, body = call(admin, "GET", "/v1/actions/"+act.ID, "")
	var got struct {
		Witness struct {
			Object        string            `json:"object"`
			Level         string            `json:"level"`
			Verdict       string            `json:"verdict"`
			Method        string            `json:"method"`
			RequiredLevel string            `json:"required_level"`
			Summary       string            `json:"summary"`
			Reasons       []json.RawMessage `json:"reasons"`
			DetailDigest  string            `json:"detail_digest"`
			Detail        json.RawMessage   `json:"detail"`
		} `json:"witness"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || got.Witness.Object != "witness" || got.Witness.Level != "L1" ||
		got.Witness.Verdict != "MISMATCH" || got.Witness.Method != "readback.rest.get" || got.Witness.RequiredLevel != "L1" ||
		len(got.Witness.Reasons) != 1 || got.Witness.Detail != nil || !strings.HasPrefix(got.Witness.DetailDigest, "sha256:") {
		t.Fatalf("GET: %d %s", code, body)
	}
	for _, err := range httpx.CheckRequired(apiSpec(), "ActionDetail", []byte(body)) {
		t.Errorf("ActionDetail: %v", err)
	}
	code, body = call(admin, "GET", "/v1/actions/"+act.ID+"?expand[]=witness.detail", "")
	if code != 200 || !strings.Contains(body, `"object": "witness_detail"`) || !strings.Contains(body, `"tool_reported_success"`) {
		t.Fatalf("expand: %d %s", code, body)
	}
	for _, err := range httpx.CheckRequired(apiSpec(), "WitnessDetail", []byte(mustField(t, body, "witness", "detail"))) {
		t.Errorf("WitnessDetail: %v", err)
	}
	r = run(env, false, "actions", "show", act.ID)
	if r.code != 0 || !strings.Contains(r.stdout, "Checked · Doesn't match · readback.rest.get") || !strings.Contains(r.stdout, "✓ Claimed  The tool reported success.") ||
		!strings.Contains(r.stdout, "✕ Checked  orders.update_status reported success") {
		t.Fatalf("show: %+v", r)
	}
	r.stdout = reShortPV.ReplaceAllString(reWhen.ReplaceAllString(r.stdout, "YYYY-MM-DD HH:MM:SS UTC"), "${1}_…")
	gateGolden(t, "witness-actions-show", r)
	t.Logf("\n%s", r.stdout)

	// 3. The read-back API is down: a gap. The policy asked for L1, so L0 · UNVERIFIABLE.
	lying, readDown = false, true
	r = run(agentEnv, false, "act", "orders.update_status", "--args", args)
	if r.code != 0 || !strings.Contains(r.stdout, "○ executed · orders.update_status") ||
		!strings.Contains(r.stdout, "check    Claimed · Can't check · The policy asks for L1. The strongest clean evidence was L0.") ||
		!strings.Contains(r.stdout, "○ Checked  The read-back orders.get failed (upstream_error), so the result couldn't be confirmed.") {
		t.Fatalf("read-back down: %+v", r)
	}
	gateGolden(t, "witness-unverifiable", r)
	t.Logf("\n%s", r.stdout)
	readDown = false

	// 4. actions list --verdict MISMATCH finds the lying runs.
	r = run(env, false, "actions", "list", "--verdict", "MISMATCH", "--json")
	var list struct{ Data []json.RawMessage }
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &list) != nil || len(list.Data) < 1 {
		t.Fatalf("list: %+v", r)
	}
	code, body = call(admin, "GET", "/v1/actions?verdict=MISMATCH&level=L1", "")
	if code != 200 || strings.Count(body, `"verdict": "MISMATCH"`) < 3 {
		t.Fatalf("API list: %d %s", code, body)
	}

	// 5. The export verifies offline; the deeds carry the witness section.
	s.sigs <- syscall.SIGTERM
	if c := s.wait(t); c != 0 {
		t.Fatalf("exit %d\n%s", c, s.err)
	}
	outputs = append(outputs, s.out.String(), s.err.String())
	er := run(env, false, "deeds", "export", "--since", "1h", "--format", "jsonl", "--out", "-")
	if er.code != 0 || !strings.Contains(er.stdout, `"method":"readback.rest.get"`) || !strings.Contains(er.stdout, `"verdict":"MISMATCH"`) {
		t.Fatalf("export: %+v", er)
	}
	bundle := filepath.Join(t.TempDir(), "deeds.tar.gz")
	if br := run(env, false, "deeds", "export", "--out", bundle); br.code != 0 {
		t.Fatalf("export bundle: %+v", br)
	}
	if vr := run(env, false, "verify", bundle); vr.code != 0 || !strings.Contains(vr.stdout, "chain intact") {
		t.Fatalf("verify: %+v", vr)
	}
	// 6. The deed's detail digest is sha256(JCS(the stored detail)), and the sweep finds no token.
	adm := e.Admin(t)
	var mismatch int
	if err := adm.QueryRow(context.Background(), `SELECT count(*) FROM deedglass.actions AS a JOIN deedglass.deeds AS d ON d.id = a.deed_id
	    WHERE a.witness_verdict = 'MISMATCH' AND convert_from(d.envelope, 'UTF8')::jsonb #>> '{body,witness,detail_digest}' = a.witness_detail_digest`).
		Scan(&mismatch); err != nil || mismatch < 3 {
		t.Fatalf("deeds whose detail digest is the action's: %d (%v)", mismatch, err)
	}
	all := strings.Join(outputs, "\n")
	if n := strings.Count(all, sentinelToken); n != 0 {
		t.Fatalf("the token appears %d times in logs, responses or CLI output", n)
	}
	for _, q := range []string{
		`SELECT count(*) FROM deedglass.actions a WHERE a::text LIKE '%' || $1 || '%'`,
		`SELECT count(*) FROM deedglass.action_events a WHERE a::text LIKE '%' || $1 || '%'`,
		`SELECT count(*) FROM deedglass.witness_jobs a WHERE a::text LIKE '%' || $1 || '%'`,
		`SELECT count(*) FROM deedglass.deeds WHERE position(convert_to($1, 'UTF8') in envelope) > 0`,
	} {
		var n int
		if err := adm.QueryRow(context.Background(), q, sentinelToken).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%d rows (%v): %s", n, err, q)
		}
	}
}

// mustField returns the JSON at obj.a.b of a response body.
func mustField(t *testing.T, body string, path ...string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	for _, p := range path {
		m, _ := v.(map[string]any)
		v = m[p]
	}
	b, _ := json.Marshal(v)
	return string(b)
}
