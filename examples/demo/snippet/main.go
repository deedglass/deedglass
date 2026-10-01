// Command snippet prints the Acme demo's MCP client config (make demo-snippet CLIENT=…). It reads
// the orders password from examples/demo/.env and, with -keys, the agent's key from
// examples/demo/.demo-keys; otherwise the key is a placeholder you replace yourself.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/deedglass/deedglass/internal/mcp/snippet"
)

func main() {
	client := flag.String("client", "claude-desktop", "claude-desktop, claude-code, cursor, vscode or windsurf")
	demoDir := flag.String("dir", "examples/demo", "the demo folder")
	port := flag.String("port", "8080", "the demo server's port on 127.0.0.1")
	withKey := flag.Bool("keys", false, "fill in the agent's key from .demo-keys (default: a placeholder)")
	flag.Parse()
	fail := func(msg, next string) {
		fmt.Fprintf(os.Stderr, "✕ %s\n  %s\n", msg, next)
		os.Exit(1)
	}
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	look := func(name string) string {
		p, err := exec.LookPath(name)
		if err != nil {
			fail(name+" isn't on your PATH.", "Install it (Docker Desktop, Node.js 24), then run make demo-snippet again.")
		}
		return abs(p)
	}
	env := readKV(filepath.Join(*demoDir, ".env"))
	if env["ACME_OWNER_PASSWORD"] == "" {
		fail("examples/demo/.env has no ACME_OWNER_PASSWORD.", "Run make demo-up first.")
	}
	agent := map[string]string{"claude-desktop": "claude-desktop", "claude-code": "claude-code", "cursor": "cursor", "vscode": "cursor", "windsurf": "cursor"}[*client]
	key := ""
	if *withKey {
		key = readKV(filepath.Join(*demoDir, ".demo-keys"))[agent]
		if key == "" {
			fail("examples/demo/.demo-keys has no key for "+agent+".", "Run make demo-up (it seeds the agents and writes their keys).")
		}
	}
	docker, npx := look("docker"), look("npx")
	path := strings.Join(uniq(filepath.Dir(docker), filepath.Dir(npx), "/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin"), ":")
	b, err := snippet.BuildDemo(snippet.DemoOptions{Client: *client, Deedglass: abs("bin/deedglass"), Docker: docker, Npx: npx,
		Server: "http://127.0.0.1:" + *port, APIKey: key, PATH: path,
		OwnerDSN: "postgresql://acme_owner:" + env["ACME_OWNER_PASSWORD"] + "@localhost:55434/acme"})
	if err != nil {
		fail(err.Error(), "CLIENT must be claude-desktop, claude-code, cursor, vscode or windsurf.")
	}
	os.Stdout.Write(b)
}

func readKV(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok && !strings.HasPrefix(k, "#") {
			out[k] = v
		}
	}
	return out
}

func uniq(xs ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
