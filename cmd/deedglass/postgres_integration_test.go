//go:build integration

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// postgresPolicy allows every postgres tool, so the connector's own refusals are what the test sees
// (S17 §9: "under an allow-all test policy").
const postgresPolicy = `deedglass: policy/v1
name: postgres-allow-all
description: Every Postgres tool runs, so the connector's admission is what decides (S17 tests).

defaults:
  decision: deny
  reason: No rule allows this action.

rules:
  - id: postgres-tools
    match: { tool: ["postgres.*"] }
    decision: allow
`

// TestPostgresLive is S17's acceptance (§9) on a real server: create the connector, init (plan,
// then apply with the passwords stored in the vault and the setup sealed), a second init with
// nothing to change, check (ready for L2), postgres.query and postgres.execute through the Gate
// with the ledger row in the customer database, a stacked statement refused under an allow-all
// policy, the plan files free of secrets, a Stop, and no DSN password in any output.
func TestPostgresLive(t *testing.T) {
	admin := pgtest.AdminURL(t)
	unlock, err := pgtest.Lock(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	cust := pgtest.NewDB(t, admin, "deedglass_it_pglive")
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 9)
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "debug", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "true"}
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
	stopped := false
	defer func() {
		if !stopped {
			s.sigs <- syscall.SIGTERM
			s.wait(t)
		}
	}()
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base
	run := func(env map[string]string, args ...string) result {
		t.Helper()
		r := cli(t, env, false, args...)
		outputs = append(outputs, r.stdout, r.stderr)
		return r
	}
	r := run(env, "org", "create", "--name", "Acme Robotics", "--slug", "acme", "--owner-email", "priya@acme.test", "--admin-key", "--json")
	var created struct {
		AdminKey struct{ Secret string } `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatalf("org create: %+v", r)
	}
	adminKey := created.AdminKey.Secret
	env["DEEDGLASS_API_KEY"] = adminKey
	pf := filepath.Join(t.TempDir(), "postgres-allow-all.yaml")
	_ = os.WriteFile(pf, []byte(postgresPolicy), 0o600)
	if r = run(env, "policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run(env, "policy", "activate", "postgres-allow-all", "--version", "1", "--mode", "enforce", "--reason", "S17 acceptance", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
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
		res, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		outputs = append(outputs, string(b))
		return res.StatusCode, string(b)
	}
	code, body := call(adminKey, "POST", "/v1/agents", `{"name":"orders-bot"}`)
	var agent struct{ ID string }
	if code != 201 || json.Unmarshal([]byte(body), &agent) != nil {
		t.Fatalf("%d %s", code, body)
	}
	code, body = call(adminKey, "POST", "/v1/agents/"+agent.ID+"/keys", `{}`)
	var ak struct{ Secret string }
	if code != 201 || json.Unmarshal([]byte(body), &ak) != nil {
		t.Fatalf("%d %s", code, body)
	}
	agentKey := ak.Secret

	// 1. The connector, then init: the plan, then the apply.
	cf := filepath.Join(t.TempDir(), "postgres.json")
	_ = os.WriteFile(cf, []byte(`{"type":"postgres","name":"postgres","description":"Acme (test)","config":`+pgtest.ConfigJSON+`}`), 0o600)
	if r = run(env, "connector", "create", "--config", cf); r.code != 0 || !strings.Contains(r.stdout, "4 tools · postgres.query (read) · postgres.execute (write) · postgres.call (write) · postgres.describe (read)") {
		t.Fatalf("create: %+v", r)
	}
	plans := filepath.Join(t.TempDir(), "plans")
	env["DEEDGLASS_INIT_DSN"] = cust.AdminURL
	r = run(env, "connector", "postgres", "init", "--connector", "postgres", "--plan-dir", plans)
	if r.code != 0 || !strings.Contains(r.stdout, "Deedglass · Postgres setup plan · deedglass_it_pglive on ") || !strings.Contains(r.stdout, "+ schema deedglass · ledger · begin_action (sha256:2f3f…7bd4)") ||
		!strings.Contains(r.stdout, "SQL for review: "+plans) || !strings.Contains(r.stdout, "Next: deedglass connector postgres init --connector postgres --apply") {
		t.Fatalf("plan: %+v", r)
	}
	t.Logf("\n%s", r.stdout)
	r = run(env, "connector", "postgres", "init", "--connector", "postgres", "--plan-dir", plans, "--apply", "--rotate-passwords")
	if r.code != 0 || !strings.Contains(r.stdout, "✓ Applied · ") || !strings.Contains(r.stdout, "✓ Credentials stored · postgres · deedglass_agent@") ||
		!strings.Contains(r.stdout, "Passwords were generated here and sent to Postgres as SCRAM verifiers. They weren't printed or logged.") ||
		!strings.Contains(r.stdout, "Sealed connector.postgres_initialized · deed_") {
		t.Fatalf("apply: %+v", r)
	}
	t.Logf("\n%s", r.stdout)
	// a second init: nothing to change
	if r = run(env, "connector", "postgres", "init", "--connector", "postgres", "--plan-dir", plans); r.code != 0 || !strings.Contains(r.stdout, "Nothing to change.") {
		t.Fatalf("second init: %+v", r)
	}
	// plans hold no secrets
	files, _ := filepath.Glob(filepath.Join(plans, "*.sql"))
	if len(files) == 0 {
		t.Fatal("no plan file")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "SCRAM-SHA-256") || strings.Contains(string(b), "PASSWORD") {
			t.Fatalf("%s holds a password clause", f)
		}
		if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s is %v", f, fi.Mode().Perm())
		}
	}
	// 2. check, through the server
	r = run(env, "connector", "postgres", "check", "--connector", "postgres")
	if r.code != 0 || !regexp.MustCompile(`Ready for L2 · \d+ passed · \d+ notes · 0 failed`).MatchString(r.stdout) {
		t.Fatalf("check: %+v", r)
	}
	t.Logf("\n%s", r.stdout)
	// 3. the agent's calls
	act := func(args string) (int, map[string]any, string) {
		t.Helper()
		c, b := call(agentKey, "POST", "/v1/actions", args)
		var m map[string]any
		_ = json.Unmarshal([]byte(b), &m)
		return c, m, b
	}
	c, m, b := act(`{"tool":"postgres.query","args":{"sql":"SELECT id, status FROM orders ORDER BY id DESC LIMIT 5"}}`)
	ex, _ := m["execution"].(map[string]any)
	res, _ := ex["result"].(map[string]any)
	if c != 200 || m["state"] != "sealed" || m["status"] != "executed" || res["row_count"] != float64(5) {
		t.Fatalf("query: %d %s", c, b)
	}
	c, m, b = act(`{"tool":"postgres.execute","args":{"sql":"UPDATE orders SET status = 'shipped' WHERE id = $1","params":[42]}}`)
	ex, _ = m["execution"].(map[string]any)
	res, _ = ex["result"].(map[string]any)
	if c != 200 || m["state"] != "sealed" || m["status"] != "executed" || res["rows_affected"] != float64(1) {
		t.Fatalf("execute: %d %s", c, b)
	}
	cc, err := pgx.Connect(context.Background(), cust.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close(context.Background())
	var lastAct string
	_ = cc.QueryRow(context.Background(), "SELECT action_id FROM deedglass.ledger ORDER BY created_at DESC LIMIT 1").Scan(&lastAct)
	if lastAct != m["id"] {
		t.Fatalf("ledger's last action %s, the execute was %v", lastAct, m["id"])
	}
	// the policy saw the SQL facts and the default schema
	c, m, b = act(`{"tool":"postgres.execute","args":{"sql":"SELECT 1; DROP TABLE orders"}}`)
	if c != 200 || m["state"] != "sealed" || m["status"] != "failed" || !strings.Contains(b, `"error_code": "statement_refused"`) ||
		!strings.Contains(b, "Refused by the Postgres connector: the statement has 2 commands. Deedglass runs one statement per action.") {
		t.Fatalf("stacked: %d %s", c, b)
	}
	var n int
	if err := cc.QueryRow(context.Background(), "SELECT count(*) FROM public.orders").Scan(&n); err != nil || n != 60 {
		t.Fatalf("orders after the refusal: %d %v", n, err)
	}
	// a DELETE is destructive in the policy input
	c, m, b = act(`{"tool":"postgres.execute","args":{"sql":"DELETE FROM refunds WHERE id = $1","params":[999999]}}`)
	if c != 200 || m["status"] != "executed" {
		t.Fatalf("delete: %d %s", c, b)
	}
	var side string
	if err := e.Admin(t).QueryRow(context.Background(), "SELECT policy_input->'tool'->>'side_effect' || ' ' || COALESCE(policy_input->'tool'->>'default_schema', '') FROM deedglass.actions WHERE id = $1", m["id"]).Scan(&side); err != nil || side != "destructive public" {
		t.Fatalf("policy input: %q %v", side, err)
	}
	// 4. Stop: new executes are refused at once.
	stopAt := time.Now()
	if c, b := call(adminKey, "POST", "/v1/agents/"+agent.ID+"/stop", `{"reason":"S17 acceptance"}`); c != 200 {
		t.Fatalf("stop: %d %s", c, b)
	}
	for {
		c, _, b = act(`{"tool":"postgres.execute","args":{"sql":"UPDATE orders SET status = 'paid' WHERE id = $1","params":[41]}}`)
		if c == 403 && strings.Contains(b, "agent_stopped") {
			break
		}
		if time.Since(stopAt) > 2*time.Second {
			t.Fatalf("not refused 2 s after Stop: %d %s", c, b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// 5. The sweep: no DSN with a password, no verifier, anywhere a client or the log saw.
	s.sigs <- syscall.SIGTERM
	s.wait(t)
	stopped = true
	outputs = append(outputs, s.out.String(), s.err.String())
	reDSN := regexp.MustCompile(`deedglass_(agent|witness):[^@\s]+@`)
	for _, o := range outputs {
		if reDSN.MatchString(o) || strings.Contains(o, "SCRAM-SHA-256$") {
			t.Fatalf("a credential reached an output: %s", clipOut(o))
		}
	}
}

func clipOut(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
