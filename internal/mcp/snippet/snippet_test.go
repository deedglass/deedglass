package snippet

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/mcp/snippets and examples/clients")

// Demo is the snippet the docs and examples/clients show: the filesystem server through the wrapper.
// Paths use /Users/you so no real home directory is committed.
func Demo(client string) Options {
	return Options{Client: client, Connector: "scratch", Deedglass: "/Users/you/txv/product/bin/deedglass",
		Upstream: []string{"/usr/local/bin/npx", "-y", "@modelcontextprotocol/server-filesystem@2026.8.31", "/tmp/deedglass-demo"},
		Server:   "http://127.0.0.1:8080"}
}

// TestGoldens pins the five client snippets in testdata/mcp/snippets and examples/clients
// (go generate ./internal/mcp/snippet, or go test ./internal/mcp/snippet -update).
func TestGoldens(t *testing.T) {
	for _, c := range Clients {
		got, err := Build(Demo(c))
		if err != nil {
			t.Fatal(err)
		}
		if c != "claude-code" {
			var v map[string]map[string]struct {
				Command string
				Args    []string
				Env     map[string]string
			}
			if err := json.Unmarshal(got, &v); err != nil {
				t.Fatalf("%s: %v", c, err)
			}
			top := "mcpServers"
			if c == "vscode" {
				top = "servers"
			}
			s := v[top]["scratch"]
			if !filepath.IsAbs(s.Command) || !filepath.IsAbs(s.Args[5]) || s.Env["PATH"] == "" || s.Env["DEEDGLASS_API_KEY"] != PlaceholderKey {
				t.Fatalf("%s: %s", c, got)
			}
		}
		for _, dir := range []string{filepath.Join("..", "..", "..", "testdata", "mcp", "snippets"), filepath.Join("..", "..", "..", "examples", "clients")} {
			p := filepath.Join(dir, Filename(c))
			if *update {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, got, 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(p)
			if err != nil || string(want) != string(got) {
				t.Errorf("%s differs (go generate ./internal/mcp/snippet):\n%s", p, got)
			}
		}
	}
}

func TestBuild(t *testing.T) {
	o := Demo("cursor")
	o.WrapFlags = []string{"--connector-type", "postgres"}
	o.APIKey = "dg_test_fromenv"
	o.ExtraPATH = []string{"/opt/homebrew/bin", "/Users/you/.nvm/bin"}
	b, err := Build(o)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"--connector-type",`) || !strings.Contains(s, "dg_test_fromenv") ||
		!strings.Contains(s, `"PATH": "/usr/local/bin:/opt/homebrew/bin:/Users/you/.nvm/bin:/usr/bin:/bin"`) {
		t.Fatal(s)
	}
	if _, err := Build(Options{Client: "emacs"}); err == nil {
		t.Fatal("unknown client")
	}
	if _, err := Build(Options{Client: "cursor"}); err == nil {
		t.Fatal("empty")
	}
	if shQuote("a b'c") != `'a b'\''c'` || shQuote("plain/path-1.0") != "plain/path-1.0" || shQuote("") != "''" {
		t.Fatal(shQuote("a b'c"))
	}
}
