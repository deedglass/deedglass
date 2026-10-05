// Package rctest tests S37's release scripts: scripts/rc.sh and scripts/prepublic_audit.sh.
package rctest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func run(t *testing.T, dir string, env []string, name string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	_ = cmd.Run()
	return out.String(), cmd.ProcessState.ExitCode()
}

func TestRCScript(t *testing.T) {
	root, _ := filepath.Abs("../..")
	out := t.TempDir()
	env := []string{"RC_OUT=" + out, "RC_DRY=1", "NO_COLOR=1"}
	got, code := run(t, root, append(env, "RC_ONLY=ci words history", "RC_FAIL=words"), "bash", "scripts/rc.sh")
	if code != 1 || !strings.Contains(got, "✕ words") || !strings.Contains(got, filepath.Join(out, "words.log")) ||
		!strings.Contains(got, "✓ ci") || !strings.Contains(got, "Not ready.") {
		t.Fatalf("a forced failure: exit %d\n%s", code, got)
	}
	got, code = run(t, root, env, "bash", "scripts/rc.sh", "--quick")
	if code != 0 || !strings.Contains(got, "Ready for the founder. 0 blockers") {
		t.Fatalf("--quick: exit %d\n%s", code, got)
	}
	for _, g := range []string{"ci", "integration", "attack", "demo", "site", "release"} {
		if !strings.Contains(got, "✓ "+g) {
			t.Fatalf("--quick misses %s:\n%s", g, got)
		}
	}
	for _, g := range []string{"matrix", "numbers", "cloud", "quickstart", "history"} {
		if strings.Contains(got, " "+g+" ") {
			t.Fatalf("--quick ran %s:\n%s", g, got)
		}
	}
}

func TestPrepublicAudit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	root, _ := filepath.Abs("../..")
	dir := t.TempDir()
	git := func(args ...string) {
		if out, code := run(t, dir, []string{"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test"}, "git", args...); code != 0 {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	git("init", "-q")
	secret := "ghp_" + strings.Repeat("Q", 36)
	_ = os.WriteFile(filepath.Join(dir, "config.go"), []byte("package x\nconst token = \""+secret+"\"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte("Built in /Users/someone/code.\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "big.bin"), bytes.Repeat([]byte("x"), 6<<20), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "first")
	_ = os.WriteFile(filepath.Join(dir, "config.go"), []byte("package x\n"), 0o644) // removed later, still in history
	git("commit", "-q", "-am", "second")
	got, code := run(t, root, []string{"NO_COLOR=1"}, "bash", "scripts/prepublic_audit.sh", "--repo", dir)
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, got)
	}
	for _, want := range []string{"✕ secret 1 finding", "config.go · ghp_QQQQ…", "✕ big", "big.bin · 6.0 MB", "○ path", "NOTES.md"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, secret) {
		t.Fatal("the audit printed a whole secret")
	}
}

// TestIssueForms: every issue form parses, has a name, a description and a body of known field
// types, and the I ran it form carries its label (S37 §6).
func TestIssueForms(t *testing.T) {
	root, _ := filepath.Abs("../..")
	files, _ := filepath.Glob(filepath.Join(root, ".github/ISSUE_TEMPLATE/*.yml"))
	if len(files) != 4 {
		t.Fatalf("%d issue form files, want bug, feature, i-ran-it and config", len(files))
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		var form struct {
			Name, Description string
			Labels            []string
			Body              []struct {
				Type string
				ID   string
			}
			BlankIssuesEnabled *bool `yaml:"blank_issues_enabled"`
		}
		if err := yaml.Unmarshal(b, &form); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if filepath.Base(f) == "config.yml" {
			if form.BlankIssuesEnabled == nil {
				t.Fatal("config.yml: blank_issues_enabled missing")
			}
			continue
		}
		if form.Name == "" || form.Description == "" || len(form.Body) == 0 {
			t.Fatalf("%s: name, description and body are required", f)
		}
		for _, x := range form.Body {
			switch x.Type {
			case "markdown", "input", "textarea", "dropdown", "checkboxes":
			default:
				t.Fatalf("%s: unknown field type %q", f, x.Type)
			}
		}
		if filepath.Base(f) == "i-ran-it.yml" && (len(form.Labels) != 1 || form.Labels[0] != "i-ran-it") {
			t.Fatalf("i-ran-it.yml labels %v", form.Labels)
		}
	}
}

// TestPublicSwitchPatch: the prepared (never applied) patch applies cleanly to HEAD, removes
// the until-public phrase from both banned lists and changes nothing else (S37 §6). Skipped where the ops
// folder isn't next to the repo (CI).
func TestPublicSwitchPatch(t *testing.T) {
	root, _ := filepath.Abs("../..")
	patch := filepath.Join(root, "..", "ops", "launch", "public-switch.patch")
	if _, err := os.Stat(patch); err != nil {
		t.Skip("no ~/txv/ops/launch/public-switch.patch here")
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if out, code := run(t, root, nil, "git", "worktree", "add", "-q", "--detach", wt, "HEAD"); code != 0 {
		t.Fatal(out)
	}
	defer run(t, root, nil, "git", "worktree", "remove", "--force", wt)
	if out, code := run(t, wt, nil, "git", "apply", patch); code != 0 {
		t.Fatalf("git apply: %s", out)
	}
	stat, _ := run(t, wt, nil, "git", "diff", "--name-only")
	want := "docs-site/scripts/check-pricing.mjs\ndocs-site/scripts/test/gates.test.mjs\ninternal/policy/bannedwords.txt\nscripts/banned_words.txt\n"
	if stat != want {
		t.Fatalf("the patch touches:\n%s", stat)
	}
	for _, f := range []string{"scripts/banned_words.txt", "internal/policy/bannedwords.txt"} {
		b, _ := os.ReadFile(filepath.Join(wt, f))
		phrase := "open" + " source" // spelled in two parts: the phrase stays banned in this repo until the switch
		if strings.Contains(strings.ToLower(string(b)), phrase) || strings.Contains(string(b), "open"+"-source") {
			t.Fatalf("%s still bans the until-public phrase", f)
		}
	}
}
