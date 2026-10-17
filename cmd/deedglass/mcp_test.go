package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/mcp/mcptest"
	"github.com/deedglass/deedglass/internal/setup"
)

// TestMain lets the integration tests run this test binary as a fake MCP server (S14).
func TestMain(m *testing.M) {
	if os.Getenv("DEEDGLASS_IT_CLI") == "1" { // the CLI itself (S14's wrapper memory budget)
		main()
		return
	}
	if mode := os.Getenv("FAKE_MCP_MODE"); mode != "" {
		mcptest.FakeServer(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestMCPSnippet: the CLI resolves absolute paths and matches the client goldens
// (testdata/mcp/snippets, written by go generate ./internal/mcp/snippet).
func TestMCPSnippet(t *testing.T) {
	savedLook, savedExe := lookPath, executable
	t.Cleanup(func() { lookPath, executable = savedLook, savedExe })
	executable = func() (string, error) { return "/Users/you/txv/product/bin/deedglass", nil }
	lookPath = func(name string) (string, error) {
		if name == "npx" {
			return "/usr/local/bin/npx", nil
		}
		return "", errors.New("not found")
	}
	env := map[string]string{"DEEDGLASS_SERVER": "http://127.0.0.1:8080"}
	for _, c := range []string{"claude-desktop", "claude-code", "cursor", "windsurf", "vscode"} {
		r := cli(t, env, false, "mcp", "snippet", "--client", c, "--connector", "scratch", "--",
			"npx", "-y", "@modelcontextprotocol/server-filesystem@2026.8.31", "/tmp/deedglass-demo")
		ext := ".json"
		if c == "claude-code" {
			ext = ".sh"
		}
		want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mcp", "snippets", c+ext))
		if err != nil || r.code != 0 || r.stdout != string(want) {
			t.Fatalf("%s: exit %d\n%s\n%s", c, r.code, r.stdout, r.stderr)
		}
		if ext == ".json" && !json.Valid([]byte(r.stdout)) {
			t.Fatal("not JSON")
		}
	}
	// --api-key-from-env, and a command that isn't on PATH.
	env["DEEDGLASS_API_KEY"] = "dg_test_fromenv"
	r := cli(t, env, false, "mcp", "snippet", "--client", "cursor", "--connector", "scratch", "--api-key-from-env", "--", "npx", "x")
	if r.code != 0 || !strings.Contains(r.stdout, `"DEEDGLASS_API_KEY": "dg_test_fromenv"`) {
		t.Fatalf("%+v", r)
	}
	r = cli(t, env, false, "mcp", "snippet", "--client", "cursor", "--connector", "scratch", "--", "nosuchcmd")
	if r.code != ExitFailure || !strings.Contains(r.stderr, `"nosuchcmd" isn't on your PATH`) {
		t.Fatalf("%+v", r)
	}
	r = cli(t, env, false, "mcp", "snippet", "--client", "emacs", "--connector", "scratch", "--", "npx")
	if r.code != ExitUsage {
		t.Fatalf("%+v", r)
	}
}

// TestMCPWrapRefusals: --fail-open outside dev exits 2 with the dev-only message; a missing -- or
// --connector is a usage error; nothing reaches stdout (it belongs to the MCP client).
func TestMCPWrapRefusals(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"fail-open in prod", map[string]string{"DEEDGLASS_ENV": "prod"}, []string{"--connector", "scratch", "--fail-open", "--", "npx"}, "--fail-open is for local development only"},
		{"no dashdash", nil, []string{"--connector", "scratch", "npx"}, "needs the MCP server's command after --"},
		{"no connector", nil, []string{"--", "npx"}, "needs --connector"},
		{"reserved connector", nil, []string{"--connector", "mcp", "--", "npx"}, "is reserved"},
		{"http remote", map[string]string{"DEEDGLASS_SERVER": "http://api.example.com"}, []string{"--connector", "scratch", "--", "npx"}, "would send the API key in clear"},
	}
	for _, c := range cases {
		r := cli(t, c.env, false, append([]string{"mcp", "wrap"}, c.args...)...)
		if r.code != ExitUsage || !strings.Contains(r.stderr, c.want) || r.stdout != "" {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
	r := cli(t, map[string]string{"DEEDGLASS_ENV": "prod"}, false, "mcp", "wrap", "--connector", "scratch", "--fail-open", "--", "npx")
	if !strings.Contains(r.stderr, "Set DEEDGLASS_ENV=dev to use it.") {
		t.Fatal(r.stderr)
	}
	r = cli(t, nil, false, "mcp", "wrap", "--help")
	if r.code != 0 || !strings.Contains(r.stdout, "What it does") || !strings.Contains(r.stdout, "Learn more: https://docs.deedglass.com/mcp") {
		t.Fatalf("%+v", r)
	}
}

// serveNoKey runs deedglass mcp serve in a directory with no .deedglass/ (it reads one from the
// working directory up), so no key is found.
func serveNoKey(t *testing.T, stdin string, env map[string]string, args ...string) result {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	return cliIn(t, []byte(stdin), env, false, append([]string{"mcp", "serve"}, args...)...)
}

// TestMCPServeNoKey (PRD-03): mcp serve with no key anywhere answers initialize and tools/list with
// the quickstart's catalog and the run-init notice, refuses the call, exits 0 at EOF and sends
// nothing, even with a server configured (testdata/mcp_serve_no_key.golden).
func TestMCPServeNoKey(t *testing.T) {
	serve := func(stdin string, env map[string]string, args ...string) result {
		return serveNoKey(t, stdin, env, args...)
	}
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer ts.Close()
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"inspector","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"postgres.query","arguments":{"sql":"SELECT 1"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`,
	}, "\n") + "\n"
	for _, env := range []map[string]string{nil, {"DEEDGLASS_SERVER": ts.URL}} {
		r := serve(in, env)
		if r.code != 0 {
			t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
		}
		golden(t, "mcp_serve_no_key", r)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("mcp serve with no key sent %d requests", n)
	}
	// An upstream's tools aren't known without the server: only the approval tool is listed.
	r := serve(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n", nil, "--connector", "everything")
	if r.code != 0 || strings.Contains(r.stdout, "postgres.") || !strings.Contains(r.stdout, `"name":"deedglass.check_approval"`) {
		t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

// TestMCPServeOfflineCatalog (PRD-03): the offline tools/list is what a quickstart's /mcp/native
// lists: the tools of the connector deedglass init creates, from the file init writes, read the way
// connector create reads it. So it has postgres.call and the writable tables, like the real one.
func TestMCPServeOfflineCatalog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "connector-postgres.yaml")
	if err := os.WriteFile(p, []byte(setup.ConnectorYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := readConnectorFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != postgres.Type || f.Name != setup.ConnectorName {
		t.Fatalf("init's connector file: type %q name %q", f.Type, f.Name)
	}
	raw, err := json.Marshal(f.Config)
	if err != nil {
		t.Fatal(err)
	}
	specs, err := postgres.ToolsFor(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, s := range specs {
		if !s.Internal {
			want[setup.ConnectorName+"."+s.Name] = s.Description
		}
	}
	r := serveNoKey(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n", nil)
	var resp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if r.code != 0 || json.Unmarshal([]byte(strings.TrimSpace(r.stdout)), &resp) != nil {
		t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	got := map[string]string{}
	for _, tl := range resp.Result.Tools {
		if strings.HasPrefix(tl.Name, setup.ConnectorName+".") {
			got[tl.Name] = tl.Description
		}
	}
	if len(got) != len(want) {
		t.Fatalf("offline lists %d postgres tools, the quickstart's connector serves %d:\n%v\n%v", len(got), len(want), got, want)
	}
	for name, d := range want {
		if got[name] != d {
			t.Errorf("%s: offline %q, quickstart %q", name, got[name], d)
		}
	}
	// The quickstart's own config, not a default one: its function and its writable tables.
	if !strings.Contains(got["postgres.call"], "public.ship_order(bigint)") ||
		!strings.HasSuffix(got["postgres.execute"], "Writable tables: public.orders, public.refunds.") {
		t.Fatalf("not the quickstart's connector: %v", got)
	}
}
