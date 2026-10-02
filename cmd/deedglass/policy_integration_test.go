//go:build integration

package main

import (
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

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// TestPolicyAPILive runs S12's acceptance on a real server and database: org creation enforces
// default-deny; the CLI pushes, shadows and enforces postgres-safe; a repeated Idempotency-Key
// replays one activation; the activation deed verifies offline; the chain is intact; the dry run
// and policy_invalid answer as specified; agent keys are refused on policy routes; responses carry
// every property api/openapi.yaml marks required.
func TestPolicyAPILive(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 7)
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "warn", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32}
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

	// org create enforces default-deny in the same transaction.
	r := cli(t, env, false, "org", "create", "--name", "Acme Robotics", "--slug", "acme", "--owner-email", "priya@acme.test", "--admin-key", "--json")
	if r.code != 0 {
		t.Fatalf("org create: %+v", r)
	}
	var created struct {
		AdminKey struct {
			Secret string `json:"secret"`
		} `json:"admin_key"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &created); err != nil || created.AdminKey.Secret == "" {
		t.Fatalf("%v %s", err, r.stdout)
	}
	key := created.AdminKey.Secret
	env["DEEDGLASS_API_KEY"] = key
	spec := apiSpec()
	call := func(method, path, body string, hdr ...string) (int, http.Header, string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
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
	code, _, body := call("GET", "/v1/policies/active", "")
	if code != 200 || !strings.Contains(body, `"name": "default-deny"`) {
		t.Fatalf("%d %s", code, body)
	}
	required("PolicyActiveSet", body)

	// The CLI flow (§9).
	ps := filepath.Join("..", "..", "examples", "policies", "postgres-safe.yaml")
	expectOut := func(r result, code int, wants ...string) {
		t.Helper()
		if r.code != code {
			t.Fatalf("exit %d, want %d\n%s%s", r.code, code, r.stdout, r.stderr)
		}
		for _, w := range wants {
			if !strings.Contains(r.stdout+r.stderr, w) {
				t.Fatalf("output lacks %q:\n%s%s", w, r.stdout, r.stderr)
			}
		}
	}
	expectOut(cli(t, env, false, "policy", "push", ps, "--notes", "Orders DB, first cut"), 0, "✓ postgres-safe v1 saved · pv_", "12 rules · 2 budgets")
	expectOut(cli(t, env, false, "policy", "push", ps), 0, "○ postgres-safe v1 already has this content · nothing saved")
	expectOut(cli(t, env, false, "policy", "activate", "postgres-safe", "--version", "1", "--mode", "shadow", "--reason", "Watch first"), 0,
		"◐ postgres-safe v1 is now the shadow policy · deed_", "next to default-deny v1")
	r = cli(t, env, false, "policy", "activate", "postgres-safe", "--version", "1", "--mode", "enforce", "--reason", "Guard orders", "--yes")
	expectOut(r, 0, "✓ postgres-safe v1 enforced · deed_", "shadow cleared")
	deedID := regexp.MustCompile(`deed_[0-9a-z]{26}`).FindString(r.stdout)
	code, _, body = call("GET", "/v1/policies/active", "")
	var set struct {
		Enforce struct {
			VersionHash string `json:"version_hash"`
			Deed        string `json:"deed"`
		} `json:"enforce"`
	}
	_ = json.Unmarshal([]byte(body), &set)
	if code != 200 || set.Enforce.VersionHash != "sha256:01aeae9dbb518cf6d5ead7ed0b042bc0ed4700856c123694954de3b8369a76f6" || set.Enforce.Deed != deedID {
		t.Fatalf("%d %s", code, body)
	}
	// The activation deed verifies offline against the server's keys; the chain is intact.
	r = cli(t, env, false, "deeds", "show", deedID, "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	deedFile := filepath.Join(t.TempDir(), "act.json")
	if err := os.WriteFile(deedFile, []byte(r.stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	expectOut(cli(t, env, false, "verify", deedFile, "--keys", base+"/.well-known/deedglass/keys.json"), 0, "✓ 1 deed")
	expectOut(cli(t, env, false, "ledger", "check", "--org", "acme"), 0, "Chain intact")
	expectOut(cli(t, env, false, "policy", "evaluate", "--server", "--tool", "postgres.execute", "--sql", "DELETE FROM public.orders", "--explain"), 0,
		"✕ deny · block-unbounded-writes")

	// The same Idempotency-Key twice: the same answer, one activation.
	if code, _, body = call("POST", "/v1/policies", `{"source":`+jsonString(t, "github-read-mostly")+`}`); code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	required("PolicyCreated", body)
	act := `{"mode":"shadow","reason":"Try the GitHub rules."}`
	c1, h1, b1 := call("POST", "/v1/policies/github-read-mostly/versions/1/activate", act, "Idempotency-Key", "pilot-1")
	c2, h2, b2 := call("POST", "/v1/policies/github-read-mostly/versions/1/activate", act, "Idempotency-Key", "pilot-1")
	if c1 != 200 || c2 != 200 || b1 != b2 || h1.Get("Deedglass-Idempotent-Replayed") != "" || h2.Get("Deedglass-Idempotent-Replayed") != "true" {
		t.Fatalf("%d %d\n%s\n%s", c1, c2, b1, b2)
	}
	required("PolicyActivation", b1)
	if c3, _, b3 := call("POST", "/v1/policies/github-read-mostly/versions/1/activate", act, "Idempotency-Key", "pilot-2"); c3 != 409 ||
		!strings.Contains(b3, "github-read-mostly v1 is already the shadow policy. Nothing changed.") {
		t.Fatalf("a new key repeats the change: %d %s", c3, b3)
	}
	// Reads, lists and the dry run, against the spec.
	for path, schema := range map[string]string{"/v1/policies": "PolicyList", "/v1/policies/postgres-safe": "Policy",
		"/v1/policies/postgres-safe/versions": "PolicyVersionList", "/v1/policies/postgres-safe/versions/1": "PolicyVersion"} {
		code, _, body = call("GET", path, "")
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		required(schema, body)
	}
	code, _, body = call("POST", "/v1/policies/evaluate", `{"input":{"tool":"postgres.execute","sql":"DELETE FROM public.orders"},"explain":true}`)
	if code != 200 || !strings.Contains(body, `"rule_id": "block-unbounded-writes"`) || !strings.Contains(body, `"shadow": {`) {
		t.Fatalf("%d %s", code, body)
	}
	required("PolicyDecision", body)
	code, _, body = call("POST", "/v1/policies/test", `{"policy":"postgres-safe","version":1,"cases":[{"name":"reads","input":{"tool":"postgres.query","sql":"SELECT 1"},"expect":{"decision":"allow","rule":"allow-reads"}}]}`)
	if code != 200 || !strings.Contains(body, `"ok": true`) {
		t.Fatalf("%d %s", code, body)
	}
	required("PolicyTestResult", body)
	code, _, body = call("POST", "/v1/policies/lint", `{"source":"nope"}`)
	if code != 200 || !strings.Contains(body, `"valid": false`) {
		t.Fatalf("%d %s", code, body)
	}
	required("PolicyLintResult", body)
	// policy_invalid with diagnostics (§9).
	code, _, body = call("POST", "/v1/policies", `{"source":"deedglass: policy/v1\nname: demo\nrules:\n  - id: a\n    desicion: deny\n"}`)
	if code != 400 || !strings.Contains(body, `"code": "policy_invalid"`) || !strings.Contains(body, `"hint": "Did you mean \"decision\"?"`) ||
		!strings.Contains(body, `"line": 5`) || !strings.Contains(body, `"doc_url": "https://docs.deedglass.com/policies/errors/unknown_key"`) {
		t.Fatalf("%d %s", code, body)
	}
	required("ErrorEnvelope", body)
	// An agent key is refused on every policy route, reads included.
	code, _, body = call("POST", "/v1/agents", `{"name":"support-bot"}`)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var agent struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &agent)
	code, _, body = call("POST", "/v1/agents/"+agent.ID+"/keys", `{}`)
	var agentKey struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal([]byte(body), &agentKey); code != 201 || err != nil || agentKey.Secret == "" {
		t.Fatalf("%d %s", code, body)
	}
	req, _ := http.NewRequest("GET", base+"/v1/policies/active", nil)
	req.Header.Set("Authorization", "Bearer "+agentKey.Secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 403 || !strings.Contains(string(b), "Agents cannot read or test policies. Use the Console or an admin key.") {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
	// Health reports the registry.
	code, _, body = call("GET", "/v1/health", "")
	if code != 200 || !strings.Contains(body, `"policies": {
      "status": "ok"`) {
		t.Fatalf("%d %s", code, body)
	}
	required("Health", body)
}

func jsonString(t *testing.T, starter string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "policies", starter+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(string(b))
	return string(q)
}
