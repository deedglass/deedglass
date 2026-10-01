package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPolicyGoldens covers deedglass policy lint and fmt (S10 §5): the demo layout, colour only on
// a terminal, NO_COLOR, JSON, GitHub annotations, directories, stdin and every exit code.
func TestPolicyGoldens(t *testing.T) {
	const dir = "testdata/policy/"
	alias := []byte("deedglass: policy/v1\na: &a [1]\nb: *a\n")
	cases := []struct {
		name  string
		env   map[string]string
		tty   bool
		stdin []byte
		args  []string
	}{
		{"lint_demo", nil, false, nil, []string{"policy", "lint", dir + "refunds.yaml"}},
		{"lint_demo_no_color", map[string]string{"NO_COLOR": "1"}, true, nil, []string{"policy", "lint", dir + "refunds.yaml"}},
		{"lint_demo_color", nil, true, nil, []string{"policy", "lint", dir + "refunds.yaml"}},
		{"lint_clean", nil, false, nil, []string{"policy", "lint", dir + "policies/postgres-safe.yaml"}},
		{"lint_json", nil, false, nil, []string{"policy", "lint", "--json", dir + "policies/postgres-safe.yaml"}},
		{"lint_json_invalid", nil, false, nil, []string{"policy", "lint", dir + "refunds.yaml", "--format", "json"}},
		{"lint_github", nil, false, nil, []string{"policy", "lint", "--format", "github", dir + "refunds.yaml"}},
		{"lint_dir", nil, false, nil, []string{"policy", "lint", dir + "policies/"}},
		{"lint_dir_json", nil, false, nil, []string{"--json", "policy", "lint", dir + "policies/"}},
		{"lint_stdin_alias", nil, false, alias, []string{"policy", "lint", "-"}},
		{"lint_voice", nil, false, []byte("deedglass: policy/v1\nname: voice\ndefaults:\n  decision: deny\n  reason: blocked!\n"), []string{"policy", "lint", "--voice", "-"}},
		{"lint_missing_file", nil, false, nil, []string{"policy", "lint", dir + "nope.yaml"}},
		{"lint_no_args", nil, false, nil, []string{"policy", "lint"}},
		{"lint_bad_format", nil, false, nil, []string{"policy", "lint", "--format", "xml", dir + "refunds.yaml"}},
		{"lint_empty_dir", nil, false, nil, []string{"policy", "lint", "testdata/intent"}},
		{"fmt_stdout", nil, false, nil, []string{"policy", "fmt", dir + "messy.yaml"}},
		{"fmt_check_dir", nil, false, nil, []string{"policy", "fmt", "--check", dir + "policies/"}},
		{"fmt_check_messy", nil, false, nil, []string{"policy", "fmt", "--check", dir + "messy.yaml"}},
		{"fmt_invalid", nil, false, nil, []string{"policy", "fmt", dir + "refunds.yaml"}},
		{"fmt_many_no_flag", nil, false, nil, []string{"policy", "fmt", dir + "messy.yaml", dir + "policies/postgres-safe.yaml"}},
		{"fmt_both_flags", nil, false, nil, []string{"policy", "fmt", "-w", "--check", dir + "messy.yaml"}},
		{"fmt_stdin_w", nil, false, nil, []string{"policy", "fmt", "-w", "-"}},
		{"help_policy", nil, false, nil, []string{"help", "policy"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			golden(t, "policy/"+c.name, cliIn(t, c.stdin, c.env, c.tty, c.args...))
		})
	}
}

// TestPolicyFmtWrite: -w rewrites a messy file once, keeps its comments and its hash, and a second
// --check passes.
func TestPolicyFmtWrite(t *testing.T) {
	src, err := os.ReadFile("testdata/policy/messy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "messy.yaml")
	if err := os.WriteFile(p, append([]byte("# Acme\n"), src...), 0o640); err != nil {
		t.Fatal(err)
	}
	before := cli(t, nil, false, "policy", "lint", "--json", p)
	r := cli(t, nil, false, "policy", "fmt", "-w", p)
	if r.code != 0 || !strings.Contains(r.stdout, "formatted") {
		t.Fatalf("fmt -w: %+v", r)
	}
	after := cli(t, nil, false, "policy", "lint", "--json", p)
	if hashOf(before.stdout) == "" || hashOf(before.stdout) != hashOf(after.stdout) {
		t.Fatalf("fmt -w moved the hash:\n%s\n%s", before.stdout, after.stdout)
	}
	if r := cli(t, nil, false, "policy", "fmt", "--check", p); r.code != 0 {
		t.Fatalf("second --check: %+v", r)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), "# Acme\n") {
		t.Fatalf("comment lost:\n%s", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed to %v", fi.Mode().Perm())
	}
}

func hashOf(js string) string {
	i := strings.Index(js, `"version_hash": "`)
	if i < 0 {
		return ""
	}
	rest := js[i+len(`"version_hash": "`):]
	return rest[:strings.IndexByte(rest, '"')]
}
