//go:build integration

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// gatePolicy is the policy the live Gate test enforces (S13 §5's demo rules on the fake connector).
const gatePolicy = `deedglass: policy/v1
name: gate-demo
description: S13's demo rules on the fake connector.

defaults:
  decision: deny
  reason: No rule allows this action.

rules:
  - id: no-bulk-writes
    match: { tool: [fake.write] }
    when: has(args.rows) && args.rows > 100
    decision: deny
    reason: writes over 100 rows need a person, not an agent.
  - id: writes-need-approval
    match: { tool: [fake.write] }
    when: has(args.table) && args.table == "approvals"
    decision: require_approval
    reason: Writes wait for a human.
    approval: { approvers: [role:admin], required: 1, expires_in: 1h }
  - id: allow-fake
    match: { tool: ["fake.*"] }
    decision: allow
    reason: Fake tools are safe to run.
`

var (
	reActID   = regexp.MustCompile(`act_[0-9a-z]{26}`)
	reDeedID  = regexp.MustCompile(`deed_[0-9a-z]{26}`)
	reShortID = regexp.MustCompile(`(act|deed|apr)_[0-9a-z]{8}…`)
	reAprID   = regexp.MustCompile(`apr_[0-9a-z]{26}`)
	reSeq     = regexp.MustCompile(`seq [0-9,]+`)
	reMS      = regexp.MustCompile(`(·|within) [0-9]+ ms`)
	reSecs    = regexp.MustCompile(`[0-9]+\.[0-9] s`)
	reExpires = regexp.MustCompile(`Expires [0-9]{2}:[0-9]{2} [A-Z+0-9-]+\.`)
	reBefore  = regexp.MustCompile(`before [0-9]{2}:[0-9]{2} [A-Z+0-9-]+\.`)
)

// normalizeGate masks what changes between runs (ids, seqs, times) in CLI output.
func normalizeGate(s string) string {
	s = reActID.ReplaceAllString(s, "act_…")
	s = reDeedID.ReplaceAllString(s, "deed_…")
	s = reAprID.ReplaceAllString(s, "apr_…")
	s = reShortID.ReplaceAllString(s, "${1}_…")
	s = reSeq.ReplaceAllString(s, "seq N")
	s = reMS.ReplaceAllString(s, "$1 N ms")
	s = reSecs.ReplaceAllString(s, "N.N s")
	s = reExpires.ReplaceAllString(s, "Expires HH:MM TZ.")
	s = reBefore.ReplaceAllString(s, "before HH:MM TZ.")
	return s
}

func gateGolden(t *testing.T, name string, r result) {
	t.Helper()
	got := normalizeGate("exit " + strconv.Itoa(r.code) + "\n--- stdout\n" + r.stdout + "--- stderr\n" + r.stderr)
	p := filepath.Join("..", "..", "testdata", "gate", "cli", name+".txt")
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil || string(want) != got {
		t.Errorf("%s differs from testdata/gate/cli/%s.txt (go test -tags integration ./cmd/deedglass -run TestGateLive -update):\n%s", name, name, got)
	}
}

// TestGateLive is S13's acceptance (§9) on a real server and database: an allowed action is
// sealed L0 VERIFIED; the same Idempotency-Key replays it; another body with that key is 422; a
// denial is 403 with error.action and its deed; proxy mode authorizes, then takes the edge's
// result; an export of the last hour verifies offline; the chain is intact. The CLI output of act
// is pinned in testdata/gate/cli for executed, denied, pending and unverifiable, in TTY colour,
// plain and --json.
func TestGateLive(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 7)
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "warn", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32,
		"DEEDGLASS_IT_YAML": "gate:\n  execute_timeout: 300ms\n  wait_timeout: 5s\n"}
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
	defer func() {
		s.sigs <- syscall.SIGTERM
		s.wait(t)
	}()
	env["DEEDGLASS_BASE_URL"] = base
	env["DEEDGLASS_SERVER"] = base
	r := cli(t, env, false, "org", "create", "--name", "Acme Robotics", "--slug", "acme", "--owner-email", "priya@acme.test", "--admin-key", "--json")
	var created struct {
		AdminKey struct {
			Secret string `json:"secret"`
		} `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatalf("org create: %+v", r)
	}
	admin := created.AdminKey.Secret
	env["DEEDGLASS_API_KEY"] = admin
	pf := filepath.Join(t.TempDir(), "gate-demo.yaml")
	if err := os.WriteFile(pf, []byte(gatePolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	if r = cli(t, env, false, "policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = cli(t, env, false, "policy", "activate", "gate-demo", "--version", "1", "--mode", "enforce", "--reason", "Gate demo", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	spec := apiSpec()
	call := func(key, method, path, body string, hdr ...string) (int, http.Header, string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		req.Header.Set("Authorization", "Bearer "+key)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header, string(b)
	}
	required := func(schema, body string) {
		t.Helper()
		for _, err := range httpx.CheckRequired(spec, schema, []byte(body)) {
			t.Errorf("%s: %v", schema, err)
		}
	}
	code, _, body := call(admin, "POST", "/v1/agents", `{"name":"support-bot"}`)
	var agent struct {
		ID string `json:"id"`
	}
	if code != 201 || json.Unmarshal([]byte(body), &agent) != nil {
		t.Fatalf("%d %s", code, body)
	}
	code, _, body = call(admin, "POST", "/v1/agents/"+agent.ID+"/keys", `{}`)
	var ak struct {
		Secret string `json:"secret"`
	}
	if code != 201 || json.Unmarshal([]byte(body), &ak) != nil {
		t.Fatalf("%d %s", code, body)
	}
	key := ak.Secret

	// Allowed, then replayed, then a conflicting body (§9).
	c1, h1, b1 := call(key, "POST", "/v1/actions", `{"tool":"fake.echo","args":{"message":"hello"}}`, "Idempotency-Key", "demo-1")
	var a1 struct {
		ID, State, Status string
		Witness           struct{ Verdict string }
		Deed              string
	}
	if c1 != 200 || json.Unmarshal([]byte(b1), &a1) != nil || a1.State != "sealed" || a1.Status != "executed" || a1.Witness.Verdict != "VERIFIED" ||
		!strings.HasPrefix(a1.Deed, "deed_") || h1.Get("Deedglass-Idempotent-Replayed") != "" {
		t.Fatalf("%d %s", c1, b1)
	}
	required("Action", b1)
	c2, h2, b2 := call(key, "POST", "/v1/actions", `{"tool":"fake.echo","args":{"message":"hello"}}`, "Idempotency-Key", "demo-1")
	if c2 != 200 || b2 != b1 || h2.Get("Deedglass-Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %v\n%s\n%s", c2, h2, b1, b2)
	}
	if c3, _, b3 := call(key, "POST", "/v1/actions", `{"tool":"fake.echo","args":{"message":"bye"}}`, "Idempotency-Key", "demo-1"); c3 != 422 ||
		!strings.Contains(b3, `"code": "idempotency_conflict"`) {
		t.Fatalf("conflict: %d %s", c3, b3)
	}
	// Denied: 403 with the sealed action.
	c4, _, b4 := call(key, "POST", "/v1/actions", `{"tool":"fake.write","args":{"rows":301,"table":"orders"}}`)
	var denied struct {
		Error struct {
			Code    string
			Message string
			Action  struct{ Deed, Status string }
		}
	}
	if c4 != 403 || json.Unmarshal([]byte(b4), &denied) != nil || denied.Error.Code != "policy_denied" ||
		!strings.HasPrefix(denied.Error.Message, `Blocked by policy "`) || !strings.HasPrefix(denied.Error.Action.Deed, "deed_") ||
		denied.Error.Action.Status != "denied" {
		t.Fatalf("deny: %d %s", c4, b4)
	}
	required("ErrorEnvelope", b4)
	// GET with a wait on a sealed action answers at once; the detail carries the new fields.
	c5, _, b5 := call(key, "GET", "/v1/actions/"+a1.ID+"?wait=5", "")
	if c5 != 200 || !strings.Contains(b5, `"status": "executed"`) {
		t.Fatalf("%d %s", c5, b5)
	}
	required("ActionDetail", b5)
	if c, _, b := call(key, "GET", "/v1/actions/"+a1.ID+"?wait=26", ""); c != 400 || !strings.Contains(b, `"param": "wait"`) {
		t.Fatalf("%d %s", c, b)
	}

	// The CLI, as the agent.
	agentEnv := map[string]string{}
	for k, v := range env {
		agentEnv[k] = v
	}
	agentEnv["DEEDGLASS_API_KEY"] = key
	for _, tty := range []bool{true, false} {
		suffix := map[bool]string{true: "_tty", false: ""}[tty]
		gateGolden(t, "act_executed"+suffix, cli(t, agentEnv, tty, "act", "fake.echo", "--args", `{"message":"hello"}`))
		gateGolden(t, "act_denied"+suffix, cli(t, agentEnv, tty, "act", "fake.write", "--args", `{"rows":301,"table":"orders"}`))
		gateGolden(t, "act_pending"+suffix, cli(t, agentEnv, tty, "act", "fake.write", "--args", `{"rows":1,"table":"approvals"}`))
		gateGolden(t, "act_unverifiable"+suffix, cli(t, agentEnv, tty, "act", "fake.slow", "--args", `{"_fake":{"latency_ms":5000}}`))
	}
	jr := cli(t, agentEnv, false, "--json", "act", "fake.write", "--args", `{"rows":1,"table":"t"}`)
	if jr.code != 0 || !strings.Contains(jr.stdout, `"status": "executed"`) || !strings.Contains(jr.stdout, `"object": "action"`) {
		t.Fatalf("%+v", jr)
	}
	// Pending, then cancelled with the CLI.
	pr := cli(t, agentEnv, false, "--json", "act", "fake.write", "--args", `{"rows":1,"table":"approvals"}`)
	pid := reActID.FindString(pr.stdout)
	if pr.code != 2 || pid == "" {
		t.Fatalf("%+v", pr)
	}
	if cr := cli(t, agentEnv, false, "actions", "cancel", pid); cr.code != 0 || !strings.Contains(cr.stdout, "✓ Cancelled · fake.write · "+pid) {
		t.Fatalf("%+v", cr)
	}
	// Proxy: authorized, then the edge's result seals it (§9).
	xr := cli(t, agentEnv, false, "--json", "act", "fake.write", "--mode", "proxy", "--args", `{"rows":1,"table":"t"}`)
	xid := reActID.FindString(xr.stdout)
	if xr.code != 0 || !strings.Contains(xr.stdout, `"status": "authorized"`) || xid == "" {
		t.Fatalf("%+v", xr)
	}
	now := time.Now().UTC()
	res := `{"status":"succeeded","started_at":"` + now.Add(-40*time.Millisecond).Format(time.RFC3339Nano) + `","finished_at":"` +
		now.Format(time.RFC3339Nano) + `","result":{"content":[{"type":"text","text":"Wrote 12 bytes"}],"isError":false}}`
	c6, _, b6 := call(key, "POST", "/v1/actions/"+xid+"/result", res)
	if c6 != 200 || !strings.Contains(b6, `"state": "sealed"`) || !strings.Contains(b6, `"method": "edge_reported"`) {
		t.Fatalf("result: %d %s", c6, b6)
	}
	required("Action", b6)
	if c, _, b := call(key, "POST", "/v1/actions/"+xid+"/result", res); c != 409 || !strings.Contains(b, "A result was already reported for this action.") {
		t.Fatalf("%d %s", c, b)
	}
	// actions show --wait on a sealed action; deeds export --since 1h | verify; ledger check.
	if sr := cli(t, env, false, "actions", "show", xid, "--wait"); sr.code != 0 || !strings.Contains(sr.stdout, "fake.write") {
		t.Fatalf("%+v", sr)
	}
	er := cli(t, env, false, "deeds", "export", "--since", "1h", "--format", "jsonl", "--out", "-")
	if er.code != 0 || strings.Count(er.stdout, "\n") < 10 {
		t.Fatalf("%+v", er)
	}
	vr := cliIn(t, []byte(er.stdout), env, false, "verify", "-", "--keys", base+"/.well-known/deedglass/keys.json")
	if vr.code != 0 || !strings.Contains(vr.stdout, "chain intact") {
		t.Fatalf("verify: %+v", vr)
	}
	if lr := cli(t, env, false, "ledger", "check", "--org", "acme"); lr.code != 0 || !strings.Contains(lr.stdout, "Chain intact") {
		t.Fatalf("%+v", lr)
	}
}
