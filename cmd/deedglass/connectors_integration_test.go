//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// sentinelToken is the canary credential of the live connector test (S16 §6).
const sentinelToken = "dgSENTINEL7Q2vK9xT4mB8nR3"

// connectorsPolicy allows the orders connector's reads and its status update, using S16's facts.
const connectorsPolicy = `deedglass: policy/v1
name: orders-rest
description: The REST orders connector, governed (S16).

defaults:
  decision: deny
  reason: No rule allows this action.

rules:
  - id: order-reads
    match: { tool: ["orders.get"] }
    when: tool.type == "rest" && request.http.method == "GET"
    decision: allow
  - id: order-status
    match: { tool: ["orders.update_status"] }
    when: tool.op == "update_status" && request.http.method == "PATCH" && request.http.path.startsWith("/v1/orders/")
    decision: allow
  - id: unknown-ops
    match: { tool: ["orders.*"] }
    decision: allow
    reason: Every other orders tool runs, so the connector's own checks are seen.
`

// mockOrders is an in-process copy of examples/mock-orders: 50 orders behind a bearer token.
type mockOrders struct {
	mu     sync.Mutex
	status map[int]string
	hits   atomic.Int64
	keys   []string
}

func (m *mockOrders) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.hits.Add(1)
	if r.Header.Get("Authorization") != "Bearer "+sentinelToken {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		// A careless upstream that echoes what it got: the connector must redact it.
		_, _ = fmt.Fprintf(w, `{"error":"bad token","got":%q}`, r.Header.Get("Authorization"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/v1/health" {
		_, _ = io.WriteString(w, `{"ok":true}`)
		return
	}
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/v1/orders/"))
	if err != nil || id < 1 || id > 50 {
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"error":"no such order"}`)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Method == http.MethodPatch {
		var in struct{ Status string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		m.status[id] = in.Status
		m.keys = append(m.keys, r.Header.Get("Idempotency-Key"))
	}
	st := m.status[id]
	if st == "" {
		st = "pending"
	}
	if id == 49 { // a careless upstream that echoes the credential on success too
		_, _ = fmt.Fprintf(w, `{"id":%d,"status":%q,"debug":{"authorization":%q}}`, id, st, r.Header.Get("Authorization"))
		return
	}
	_, _ = fmt.Fprintf(w, `{"id":%d,"status":%q,"customer":"acme-retail","total_cents":4200,"currency":"usd"}`, id, st)
}

// TestConnectorsLive is S16's acceptance (§9) on a real server: a REST connector over the mock
// orders API, its credential stored once, the connector test, governed calls sealed as deeds
// (including credentials_missing, validation_failed, tool_unknown and, after the dev override is
// removed, destination_blocked), the metadata refusal, the API's idempotency and conflict rules,
// vault rotation, an export that verifies offline, and the sentinel sweep: the token appears in
// no log line, response, CLI output, action, event, deed, admin event or idempotency row.
func TestConnectorsLive(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	mock := &mockOrders{status: map[int]string{}}
	up := httptest.NewServer(mock)
	defer up.Close()
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 9)
	newKEK := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "debug", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "true",
		"ORDERS_TOKEN": sentinelToken}
	var outputs []string // everything any client saw, for the sweep
	start := func(env map[string]string, args ...string) (*served, string) {
		s := startServe(t, env, args...)
		select {
		case a := <-s.addr:
			return s, "http://" + a.String()
		case c := <-s.code:
			t.Fatalf("exited %d\n%s%s", c, s.out, s.err)
		case <-time.After(30 * time.Second):
			t.Fatal("serve never listened")
		}
		return nil, ""
	}
	stop := func(s *served) {
		s.sigs <- syscall.SIGTERM
		if c := s.wait(t); c != 0 {
			t.Fatalf("exit %d\n%s", c, s.err)
		}
		outputs = append(outputs, s.out.String(), s.err.String())
	}
	s, base := start(env, "--migrate")
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
	admin := created.AdminKey.Secret
	env["DEEDGLASS_API_KEY"] = admin
	pf := filepath.Join(t.TempDir(), "orders-rest.yaml")
	if err := os.WriteFile(pf, []byte(connectorsPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	if r = run(env, "policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run(env, "policy", "activate", "orders-rest", "--version", "1", "--mode", "enforce", "--reason", "Orders connector", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	spec := apiSpec()
	call := func(key, method, path, body string, hdr ...string) (int, http.Header, string) {
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
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		outputs = append(outputs, string(b))
		return res.StatusCode, res.Header, string(b)
	}
	required := func(schema, body string) {
		t.Helper()
		for _, err := range httpx.CheckRequired(spec, schema, []byte(body)) {
			t.Errorf("%s: %v", schema, err)
		}
	}
	code, _, body := call(admin, "POST", "/v1/agents", `{"name":"orders-bot"}`)
	var agent struct{ ID string }
	if code != 201 || json.Unmarshal([]byte(body), &agent) != nil {
		t.Fatalf("%d %s", code, body)
	}
	code, _, body = call(admin, "POST", "/v1/agents/"+agent.ID+"/keys", `{}`)
	var ak struct{ Secret string }
	if code != 201 || json.Unmarshal([]byte(body), &ak) != nil {
		t.Fatalf("%d %s", code, body)
	}
	agentKey := ak.Secret

	// 1. Create the connector from the example file, pointed at the mock.
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "connectors", "orders.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cf := filepath.Join(t.TempDir(), "orders.yaml")
	if err := os.WriteFile(cf, bytes.Replace(src, []byte("http://127.0.0.1:8090"), []byte(up.URL), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	r = run(env, "connector", "create", "--config", cf)
	if r.code != 0 || !strings.Contains(r.stdout, "✓ Connector created · orders · con_") ||
		!strings.Contains(r.stdout, "2 tools · orders.get (read) · orders.update_status (write)") ||
		!strings.Contains(r.stdout, "○ No credentials yet. Next: deedglass connector credentials set orders --from-env ORDERS_TOKEN") {
		t.Fatalf("create: %+v", r)
	}
	t.Logf("\n%s", r.stdout)

	act := func(tool, args string) (int, map[string]any, string) {
		t.Helper()
		c, _, b := call(agentKey, "POST", "/v1/actions", `{"tool":"`+tool+`","args":`+args+`}`)
		var m map[string]any
		_ = json.Unmarshal([]byte(b), &m)
		return c, m, b
	}
	execErr := func(m map[string]any) string {
		ex, _ := m["execution"].(map[string]any)
		s, _ := ex["error_code"].(string)
		return s
	}
	// 2. No credentials yet: failed, credentials_missing, sealed, nothing sent.
	hits := mock.hits.Load()
	c, m, b := act("orders.get", `{"id":"42"}`)
	if c != 200 || m["state"] != "sealed" || m["status"] != "failed" || execErr(m) != "credentials_missing" || mock.hits.Load() != hits {
		t.Fatalf("credentials missing: %d %s", c, b)
	}
	// 3. Store the credential once.
	r = run(env, "connector", "credentials", "set", "orders", "--from-env", "ORDERS_TOKEN")
	if r.code != 0 || !strings.Contains(r.stdout, "✓ Credentials stored · orders · bearer · …"+sentinelToken[len(sentinelToken)-4:]+" · encrypted with kek:") ||
		!strings.Contains(r.stdout, "Agents never see this value.") {
		t.Fatalf("credentials set: %+v", r)
	}
	t.Logf("\n%s", r.stdout)
	if r = run(env, "connector", "credentials", "set", "orders", "--from-env", "NOT_SET_ANYWHERE"); r.code != 2 ||
		!strings.Contains(r.stderr, "NOT_SET_ANYWHERE is empty or unset. Export it in this shell and run the command again. Deedglass never reads secrets from flags") {
		t.Fatalf("empty env: %+v", r)
	}
	// 4. The test: 4 checks pass, tls is skipped for plain http in dev.
	r = run(env, "connector", "test", "orders")
	if r.code != 0 || !strings.Contains(r.stdout, "✓ orders · 4 checks passed · 1 skipped") || !strings.Contains(r.stdout, "✓ auth          GET /v1/health → 200") {
		t.Fatalf("test: %+v", r)
	}
	t.Logf("\n%s", r.stdout)
	// 5. Governed calls.
	c, m, b = act("orders.get", `{"id":"42"}`)
	ex, _ := m["execution"].(map[string]any)
	res, _ := ex["result"].(map[string]any)
	rb, _ := res["body"].(map[string]any)
	if c != 200 || m["state"] != "sealed" || m["status"] != "executed" || rb["status"] != "pending" || !strings.HasPrefix(fmt.Sprint(m["deed"]), "deed_") {
		t.Fatalf("get: %d %s", c, b)
	}
	required("Action", b)
	c, m, b = act("orders.update_status", `{"id":"42","status":"shipped"}`)
	if c != 200 || m["status"] != "executed" || mock.status[42] != "shipped" || len(mock.keys) != 1 || mock.keys[0] != m["id"] {
		t.Fatalf("update: %d %s (keys %v)", c, b, mock.keys)
	}
	c, m, b = act("orders.get", `{"id":"49"}`)
	if c != 200 || m["status"] != "executed" || !strings.Contains(b, `"authorization": "Bearer [redacted]"`) {
		t.Fatalf("echo: %d %s", c, b)
	}
	hits = mock.hits.Load()
	c, m, b = act("orders.get", `{"id":"../admin"}`)
	if c != 200 || m["status"] != "failed" || execErr(m) != "validation_failed" || mock.hits.Load() != hits {
		t.Fatalf("validation: %d %s", c, b)
	}
	c, m, b = act("orders.update_status", `{"id":"42","status":"lost"}`)
	if c != 200 || execErr(m) != "validation_failed" || !strings.Contains(b, "must be one of") {
		t.Fatalf("enum: %d %s", c, b)
	}
	c, _, b = act("orders.delete_all", `{}`)
	if c != 404 || !strings.Contains(b, `"code": "tool_unknown"`) || !strings.Contains(b, `"deed": "deed_`) {
		t.Fatalf("tool unknown: %d %s", c, b)
	}
	// 6. The metadata address is refused, even with the dev override.
	mf := filepath.Join(t.TempDir(), "probe.yaml")
	_ = os.WriteFile(mf, []byte("type: rest\nname: metadata-probe\nconfig:\n  base_url: http://169.254.169.254\n  allowed_methods: [GET]\n  allowed_path_prefixes: [/latest]\n  tools: [{name: creds, method: GET, path: /latest/meta-data}]\n"), 0o600)
	r = run(env, "connector", "create", "--config", mf)
	if r.code != 1 || !strings.Contains(r.stderr, "base_url is a cloud metadata address. Deedglass never connects to cloud metadata addresses, even in dev") {
		t.Fatalf("metadata: %+v", r)
	}
	c, _, b = call(admin, "POST", "/v1/connectors", `{"type":"rest","name":"metadata-probe","config":{"base_url":"http://169.254.169.254","allowed_methods":["GET"],"allowed_path_prefixes":["/"],"tools":[{"name":"x","method":"GET","path":"/x"}]}}`)
	if c != 400 || !strings.Contains(b, `"code": "destination_blocked"`) || !strings.Contains(b, `"param": "config.base_url"`) {
		t.Fatalf("metadata API: %d %s", c, b)
	}
	// 7. The API: agent keys can't read connectors; Idempotency-Key replays; a stale hash is 409.
	if c, _, b := call(agentKey, "GET", "/v1/connectors", ""); c != 403 || !strings.Contains(b, "Agents can't list, create or read connectors.") {
		t.Fatalf("agent key: %d %s", c, b)
	}
	body2 := `{"type":"rest","name":"orders-two","config":{"base_url":"` + up.URL + `","auth":{"kind":"none"},"allowed_methods":["GET"],"allowed_path_prefixes":["/v1/orders"],"tools":[{"name":"get","method":"GET","path":"/v1/orders/{id}","args":{"id":{"in":"path"}}}]}}`
	c1, _, b1 := call(admin, "POST", "/v1/connectors", body2, "Idempotency-Key", "conn-1")
	c2, h2, b2 := call(admin, "POST", "/v1/connectors", body2, "Idempotency-Key", "conn-1")
	if c1 != 201 || c2 != 201 || b1 != b2 || h2.Get("Deedglass-Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %d %v\n%s\n%s", c1, c2, h2, b1, b2)
	}
	required("Connector", b1)
	if c, _, b := call(admin, "PATCH", "/v1/connectors/orders-two", `{"description":"x","expected_config_hash":"sha256:`+strings.Repeat("0", 64)+`"}`); c != 409 ||
		!strings.Contains(b, `"code": "conflict"`) {
		t.Fatalf("stale hash: %d %s", c, b)
	}
	if c, _, b := call(admin, "GET", "/v1/connectors/orders", ""); c != 200 || !strings.Contains(b, `"set": true`) || strings.Contains(b, sentinelToken) {
		t.Fatalf("get: %d %s", c, b)
	} else {
		required("Connector", b)
	}
	if c, _, b := call(admin, "DELETE", "/v1/connectors/orders-two", ""); c != 200 || !strings.Contains(b, `"deleted": true`) {
		t.Fatalf("delete: %d %s", c, b)
	}
	// 8. Rotate the vault to a new key, then restart without the dev override: the credential still
	// opens with the new ring, and the loopback mock is now blocked at dial (destination_blocked).
	stop(s)
	rot := map[string]string{}
	for k, v := range env {
		rot[k] = v
	}
	rot["DEEDGLASS_VAULT_KEY"] = newKEK + "," + key32
	if r = run(rot, "vault", "status"); r.code != 0 || !strings.Contains(r.stdout, "1 credential · A256GCM-ENV-1 · active kek:") {
		t.Fatalf("status: %+v", r)
	}
	r = run(rot, "vault", "rotate")
	if r.code != 0 || !strings.Contains(r.stdout, "✓ All 1 credential uses kek:") || !strings.Contains(r.stdout, "You can remove kek:") {
		t.Fatalf("rotate: %+v", r)
	}
	t.Logf("\n%s", r.stdout)
	rot["DEEDGLASS_ALLOW_PRIVATE_NETWORKS"] = "false"
	s, base = start(rot)
	rot["DEEDGLASS_BASE_URL"], rot["DEEDGLASS_SERVER"] = base, base
	hits = mock.hits.Load()
	c, m, b = act2(t, base, agentKey, "orders.get", `{"id":"42"}`, &outputs)
	if c != 200 || m["status"] != "failed" || execErr(m) != "destination_blocked" || mock.hits.Load() != hits ||
		strings.Contains(b, "127.0.0.1") {
		t.Fatalf("blocked at dial: %d %s", c, b)
	}
	stop(s)
	// 9. The export verifies offline, and the chain is intact.
	er := run(env, "deeds", "export", "--since", "1h", "--format", "jsonl", "--out", "-")
	if er.code != 0 || strings.Count(er.stdout, "\n") < 10 {
		t.Fatalf("%+v", er)
	}
	bundle := filepath.Join(t.TempDir(), "deeds.tar.gz")
	if br := run(env, "deeds", "export", "--out", bundle); br.code != 0 {
		t.Fatalf("export bundle: %+v", br)
	}
	vr := run(env, "verify", bundle)
	if vr.code != 0 || !strings.Contains(vr.stdout, "chain intact") {
		t.Fatalf("verify: %+v", vr)
	}
	t.Logf("\n%s", vr.stdout)
	for _, kind := range []string{"connector.created", "connector.credentials_updated", "connector.deleted", "vault.rotated"} {
		if !strings.Contains(er.stdout, `"`+kind+`"`) {
			t.Errorf("no %s deed in the export", kind)
		}
	}
	// 10. The sentinel sweep.
	all := strings.Join(outputs, "\n")
	if n := strings.Count(all, sentinelToken); n != 0 {
		t.Fatalf("the token appears %d times in logs, responses or CLI output", n)
	}
	adm := e.Admin(t)
	lines := 0
	for _, q := range []string{
		`SELECT count(*) FROM deedglass.actions a WHERE a::text LIKE '%' || $1 || '%'`,
		`SELECT count(*) FROM deedglass.action_events a WHERE a::text LIKE '%' || $1 || '%'`,
		`SELECT count(*) FROM deedglass.admin_events a WHERE a::text LIKE '%' || $1 || '%'`,
		`SELECT count(*) FROM deedglass.idempotency_keys a WHERE a::text LIKE '%' || $1 || '%'`,
		`SELECT count(*) FROM deedglass.connectors a WHERE a::text LIKE '%' || $1 || '%'`,
		`SELECT count(*) FROM deedglass.deeds WHERE position(convert_to($1, 'UTF8') in envelope) > 0`,
		`SELECT count(*) FROM deedglass.credentials WHERE position(convert_to($1, 'UTF8') in ciphertext) > 0 OR hint LIKE '%' || $1 || '%'`,
	} {
		var n int
		if err := adm.QueryRow(context.Background(), q, sentinelToken).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%d rows (%v): %s", n, err, q)
		}
		lines++
	}
	t.Logf("sentinel sweep: %d bytes of logs, responses and CLI output, %d tables, 0 hits", len(all), lines)
}

// act2 sends an action to a restarted server.
func act2(t *testing.T, base, key, tool, args string, outputs *[]string) (int, map[string]any, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/v1/actions", strings.NewReader(`{"tool":"`+tool+`","args":`+args+`}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	*outputs = append(*outputs, string(b))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return res.StatusCode, m, string(b)
}
