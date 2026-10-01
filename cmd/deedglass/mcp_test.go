package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/mcp/mcptest"
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
