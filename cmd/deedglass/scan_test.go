package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func scanFixture(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "internal", "scan", "testdata", "archived-pg"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

var reANSI = regexp.MustCompile("\x1b\\[")

// TestScanCLI: exit codes, the exact copy, --ci, --json, files written, no share link unless asked.
func TestScanCLI(t *testing.T) {
	fix := scanFixture(t)
	out := t.TempDir()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home, ".cursor", "mcp.json"), []byte(`{"mcpServers":{"user-canary-srv":{"command":"npx","args":["@stripe/mcp"]}}}`), 0o644)
	env := map[string]string{"HOME": home}

	// The fixture grades D; --fail-under C fails after writing, D passes, F is a usage error.
	r := cli(t, env, true, "scan", "--ci", "--dir", fix, "--out-dir", out, "--format", "md,html,json,sarif", "--fail-under", "C")
	if r.code != ExitFailure || !strings.Contains(r.stderr, "✕ Blast radius D is below C (--fail-under C). The report was written.") {
		t.Fatalf("%d %q", r.code, r.stderr)
	}
	for _, f := range []string{"md", "html", "json", "sarif"} {
		if _, err := os.Stat(filepath.Join(out, "deedglass-scan."+f)); err != nil {
			t.Errorf("deedglass-scan.%s wasn't written before the exit", f)
		}
	}
	if reANSI.MatchString(r.stdout) || strings.Contains(r.stdout, "user-canary-srv") || strings.Contains(r.stdout, "◖") {
		t.Fatalf("--ci printed colour, the mark, or read a user config:\n%s", r.stdout)
	}
	if !strings.HasPrefix(r.stdout, "deedglass scan · ") || !strings.Contains(r.stdout, "Wrote deedglass-scan.sarif (SARIF 2.1.0, 2 rules).") {
		t.Fatalf("%s", r.stdout)
	}
	if r = cli(t, env, false, "scan", "--ci", "--dir", fix, "--no-write", "--fail-under", "d"); r.code != ExitOK {
		t.Fatalf("--fail-under D: %d %s", r.code, r.stderr)
	}
	if r = cli(t, env, false, "scan", "--dir", fix, "--no-write", "--fail-under", "F"); r.code != ExitUsage {
		t.Fatalf("--fail-under F: %d", r.code)
	}
	// User configs are read without --ci (and the user's server shows up).
	if r = cli(t, env, false, "scan", "--dir", fix, "--no-write"); !strings.Contains(r.stdout, "3 MCP client configs") {
		t.Fatalf("%s", r.stdout)
	}
	// An unset DSN variable: exit 2 with the exact sentence.
	r = cli(t, env, false, "scan", "--dir", fix, "--no-write", "--postgres-dsn-env", "DEMO_DSN")
	if r.code != ExitUsage || r.stderr != "✕ DEMO_DSN isn't set. Export the database URL in that variable, then run deedglass scan again.\n" {
		t.Fatalf("%d %q", r.code, r.stderr)
	}
	// --json: one object, no files, the fields S37 reads.
	r = cli(t, env, false, "--json", "scan", "--ci", "--dir", fix)
	var rep map[string]any
	if r.code != ExitOK || json.Unmarshal([]byte(r.stdout), &rep) != nil {
		t.Fatalf("%d %s", r.code, r.stderr)
	}
	sc := rep["score"].(map[string]any)
	if rep["object"] != "scan_report" || rep["version"] != 1.0 || rep["footer"] != "Full report with fixes: deedglass.com/pro ($29 once)." ||
		sc["grade"] != "D" || sc["formula_version"] != 1.0 || sc["points"] != 56.0 {
		t.Fatalf("%v", rep)
	}
	top := regexp.MustCompile(`(?m)^  "(\w+)":`).FindAllStringSubmatch(r.stdout, -1)
	if len(top) < 8 || top[0][1] != "object" || top[5][1] != "offline" || top[6][1] != "score" {
		t.Fatalf("score must come right after offline: %v", top)
	}
	// --score: the score line, the small print and the deductions, nothing else.
	r = cli(t, env, false, "scan", "--ci", "--dir", fix, "--score", "--no-write")
	if !strings.Contains(r.stdout, "Blast radius: D · ") || !strings.Contains(r.stdout, "A means a small blast radius, not a safe agent.") ||
		!strings.Contains(r.stdout, "56 points") || strings.Contains(r.stdout, "Found") || strings.Contains(r.stdout, "Full report") {
		t.Fatalf("%s", r.stdout)
	}
	// No share link anywhere without --share; with it, one link and the sentence.
	for _, args := range [][]string{{"scan", "--ci", "--dir", fix, "--no-write"}, {"--json", "scan", "--ci", "--dir", fix}, {"scan", "--ci", "--dir", fix, "--no-write", "--badge"}} {
		if r = cli(t, env, false, args...); strings.Contains(r.stdout+r.stderr, "deedglass.com/s#") {
			t.Fatalf("%v printed a share link", args)
		}
	}
	_ = os.RemoveAll(filepath.Join(fix, ".deedglass"))
	r = cli(t, env, false, "scan", "--ci", "--dir", fix, "--no-write", "--share")
	if strings.Count(r.stdout, "https://deedglass.com/s#v1.") != 1 || !strings.Contains(r.stdout, "The part after # stays in your browser. Nothing was sent.") {
		t.Fatalf("%s", r.stdout)
	}
	b, _ := os.ReadFile(filepath.Join(out, "deedglass-scan.md"))
	if !strings.Contains(string(b), "Full report with fixes: deedglass.com/pro ($29 once).") || strings.Contains(string(b), "canary-pass-7f3") {
		t.Fatal("the md report")
	}
}

// TestScanBadgeCLI: the badge file, the README line, and no git process or file outside .deedglass/.
func TestScanBadgeCLI(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[remote \"origin\"]\n\turl = https://github.com/acme/agents.git\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	r := cli(t, map[string]string{"HOME": t.TempDir()}, false, "scan", "--dir", dir, "--no-write", "--badge")
	if r.code != ExitOK || !strings.Contains(r.stdout, "). Add this line to your README:\n[![agent blast radius](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2Facme%2Fagents%2Fmain%2F.deedglass%2Fscore.json)](https://deedglass.com/score)\nCommit it if you want it in your README. Nothing was committed.") {
		t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
	}
	var files []string
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, rel)
		}
		return nil
	})
	if strings.Join(files, ",") != ".deedglass/score.json,.git/HEAD,.git/config" {
		t.Fatalf("%v", files)
	}
}
