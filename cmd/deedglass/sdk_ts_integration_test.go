//go:build integration

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/store/storetest"
)

// TestSDKTypeScriptLive is S31 §4 step 14 (make sdk-ts-it): a real serve on a throwaway Postgres
// with sdk-demo enforced, an agent and its key, and a loopback test hook that approves as the org's
// owner; then sdk/typescript's integration project runs against it: act → sealed, a denial with
// error.action, pending → approve → waitUntilSettled, wrapTool proxy → reported (connector_type
// sdk), intents conformance on POST /v1/intents/verify, deeds.get and an offline verify.
func TestSDKTypeScriptLive(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_SDK_TS") != "1" {
		t.Skip("set DEEDGLASS_IT_SDK_TS=1 (make sdk-ts-it) to run the TypeScript SDK against a real server")
	}
	sdk, _ := filepath.Abs(filepath.Join("..", "..", "sdk", "typescript"))
	if _, err := os.Stat(filepath.Join(sdk, "node_modules", ".package-lock.json")); err != nil {
		t.Skip("sdk/typescript has no node_modules: run make sdk-ts first")
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		t.Skip("npx isn't on PATH: install Node.js 24 LTS")
	}
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 9)
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
	m := &mcpLive{base: base, env: env, s: s}
	t.Cleanup(func() { m.stop(t) })
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base
	r := cli(t, env, false, "org", "create", "--name", "Acme Robotics", "--slug", "acme", "--owner-email", "priya@acme.test", "--admin-key", "--json")
	var created struct {
		ID       string                  `json:"id"`
		AdminKey struct{ Secret string } `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatalf("org create: %+v", r)
	}
	env["DEEDGLASS_API_KEY"] = created.AdminKey.Secret
	if r = cli(t, env, false, "policy", "push", filepath.Join("..", "..", "examples", "policies", "sdk-demo.yaml")); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = cli(t, env, false, "policy", "activate", "sdk-demo", "--version", "1", "--mode", "enforce", "--reason", "SDK integration", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = cli(t, env, false, "--json", "agent", "create", "support-bot"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	var agent struct{ ID string }
	_ = json.Unmarshal([]byte(r.stdout), &agent)
	r = cli(t, env, false, "--json", "agent", "keys", "create", "support-bot")
	var k struct{ Secret string }
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &k) != nil || !strings.HasPrefix(k.Secret, "dg_") {
		t.Fatalf("%+v", r)
	}

	// The test hook: POST /approve?id=apr_… approves as the owner through the dev CLI.
	var mu sync.Mutex
	hook := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		res := cli(t, env, false, "approve", req.URL.Query().Get("id"), "--as", "priya@acme.test", "--reason", "Checked by the SDK test")
		if res.code != 0 {
			http.Error(w, res.stdout+res.stderr, http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = hook.Serve(ln) }()
	t.Cleanup(func() { _ = hook.Close() })

	cmd := exec.Command(npx, "vitest", "run", "--project", "integration")
	cmd.Dir = sdk
	cmd.Env = append(os.Environ(), "DEEDGLASS_SERVER="+base, "DEEDGLASS_API_KEY="+k.Secret,
		"DEEDGLASS_IT_ADMIN_KEY="+created.AdminKey.Secret, "DEEDGLASS_IT_AGENT_ID="+agent.ID,
		"DEEDGLASS_IT_HOOK=http://"+ln.Addr().String(), "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	t.Logf("vitest:\n%s", out)
	if err != nil {
		t.Fatalf("the TypeScript integration project failed: %v", err)
	}
	// make sdk-examples against this server: the six sdk-demo examples, then claude-agent-sdk with
	// claude-agent-local active.
	examples := func(extra ...string) string {
		cmd := exec.Command(filepath.Join("..", "..", "scripts", "sdk_examples.sh"))
		cmd.Env = append(os.Environ(), append([]string{"DEEDGLASS_SERVER=" + base, "DEEDGLASS_API_KEY=" + k.Secret}, extra...)...)
		eo, err := cmd.CombinedOutput()
		t.Logf("examples:\n%s", eo)
		if err != nil {
			t.Fatalf("an example failed: %v", err)
		}
		return string(eo)
	}
	ex := examples()
	for _, want := range []string{"✓ fake.echo · executed", "✓ billing.refund_customer · executed", "✕ billing.delete_customer · denied · Blocked by policy \"no-deletes\": Agents never delete customers. Nothing was run.",
		"◐ billing.refund_customer · pending approval · apr_", "seals intact · chain not checked · 1 seal key   (checked offline with @deedglass/sdk/verify)"} {
		if !strings.Contains(ex, want) {
			t.Errorf("the examples never printed %q", want)
		}
	}
	if r = cli(t, env, false, "policy", "push", filepath.Join("..", "..", "examples", "policies", "claude-agent-local.yaml")); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = cli(t, env, false, "policy", "activate", "claude-agent-local", "--version", "1", "--mode", "enforce", "--reason", "SDK integration", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	ex = examples("SDK_EXAMPLES=claude-agent-sdk", "SDK_EXAMPLES_CLAUDE=1")
	for _, want := range []string{"✓ claude.Read · allowed", "✕ claude.Bash · denied · Blocked by policy \"destructive-shell\"", "✕ claude.Write · denied · Blocked by policy \"no-env-files\""} {
		if !strings.Contains(ex, want) {
			t.Errorf("the claude example never printed %q", want)
		}
	}
	if strings.Contains(string(out)+ex, k.Secret) || strings.Contains(s.out.String()+s.err.String(), k.Secret) {
		t.Fatal("the agent key appeared in the output")
	}
}
