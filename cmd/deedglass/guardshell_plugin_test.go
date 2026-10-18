package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The Claude Code plugin's hook (integrations/claude-code/hooks): hooks.json runs shell-guard.sh,
// which runs deedglass guard shell only when DEEDGLASS_SHELL_GUARD is ask or deny. Off, or unable to
// check, it must never stand between Claude and a Bash command: Claude Code blocks on exit 2, and on
// Windows without Git Bash on any failure of a shell-form hook unless onFailure is "continue".

const pluginHooks = "../../integrations/claude-code/hooks"

type hookRun struct {
	code           int
	stdout, stderr string
}

// runPluginHook runs shell-guard.sh the way hooks.json does (sh <script>), with only PATH and the
// guard variable in its environment. guard "-" leaves the variable unset.
func runPluginHook(t *testing.T, guard, path string, stdin []byte) hookRun {
	t.Helper()
	sh := lookSh(t)
	script, err := filepath.Abs(filepath.Join(pluginHooks, "shell-guard.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return runSh(t, exec.Command(sh, script), guard, path, stdin)
}

func runSh(t *testing.T, cmd *exec.Cmd, guard, path string, stdin []byte, env ...string) hookRun {
	t.Helper()
	cmd.Env = append([]string{"PATH=" + path}, env...)
	if guard != "-" {
		cmd.Env = append(cmd.Env, "DEEDGLASS_SHELL_GUARD="+guard)
	}
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return hookRun{code, out.String(), errb.String()}
}

func lookSh(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the hook script runs under sh (Git Bash on Windows); this test runs it on macOS and Linux")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	return sh
}

// binDir is a PATH holding cat (the script drains its input with it) and, if script isn't empty, a
// deedglass that runs it.
func binDir(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("no cat")
	}
	if err := os.Symlink(cat, filepath.Join(dir, "cat")); err != nil {
		t.Fatal(err)
	}
	if script != "" {
		if err := os.WriteFile(filepath.Join(dir, "deedglass"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// realDeedglass is a PATH whose deedglass is this test binary running main() (TestMain).
func realDeedglass(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return binDir(t, "DEEDGLASS_IT_CLI=1 exec '"+exe+"' \"$@\"")
}

// TestPluginHookConfig: one PreToolUse hook on Bash, in shell form, that never blocks when it fails.
// The string is the same on every platform: sh -c on macOS and Linux, Git Bash on Windows; on
// Windows without Git Bash, Claude Code registers no Bash tool, and if this hook ran there under
// PowerShell (no sh), "onFailure": "continue" lets the command go ahead.
func TestPluginHookConfig(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(pluginHooks, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Hooks map[string][]struct {
			Matcher string
			Hooks   []map[string]any
		}
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	groups := cfg.Hooks["PreToolUse"]
	if len(cfg.Hooks) != 1 || len(groups) != 1 || groups[0].Matcher != "Bash" || len(groups[0].Hooks) != 1 {
		t.Fatalf("want one PreToolUse hook on Bash, got %s", b)
	}
	h := groups[0].Hooks[0]
	want := map[string]any{"type": "command", "command": `sh "${CLAUDE_PLUGIN_ROOT}/hooks/shell-guard.sh"`, "timeout": 10.0, "onFailure": "continue"}
	if len(h) != len(want) {
		t.Errorf("hook fields %v, want exactly %v (no shell, no args)", h, want)
	}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("hook %s = %v, want %v", k, h[k], v)
		}
	}
}

// TestPluginHookCommandString runs the hooks.json command string through sh -c, as Claude Code does
// on macOS, Linux and (Git Bash) Windows, with the plugin root substituted as a Windows-style
// forward-slash path with spaces and an apostrophe, and with it left to the environment variable.
func TestPluginHookCommandString(t *testing.T) {
	sh := lookSh(t)
	root := filepath.Join(t.TempDir(), "C", "Users", "A B", ".claude", "plugins", "cache", "o'brien", "deedglass", "0.2.1")
	if err := os.MkdirAll(filepath.Join(root, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(pluginHooks, "shell-guard.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hooks", "shell-guard.sh"), src, 0o644); err != nil { // not executable: sh reads it
		t.Fatal(err)
	}
	command := `sh "${CLAUDE_PLUGIN_ROOT}/hooks/shell-guard.sh"`
	path := realDeedglass(t) + ":" + filepath.Dir(sh)
	env := "CLAUDE_PLUGIN_ROOT=" + root // Claude Code exports it either way
	for _, substituted := range []bool{true, false} {
		c := command
		if substituted {
			c = strings.ReplaceAll(command, "${CLAUDE_PLUGIN_ROOT}", filepath.ToSlash(root))
		}
		r := runSh(t, exec.Command(sh, "-c", c), "-", path, hookInput("dropdb app", t.TempDir()), env)
		if r.code != 0 || r.stdout != "" || r.stderr != "" {
			t.Errorf("off (substituted %v): exit %d, stdout %q, stderr %q", substituted, r.code, r.stdout, r.stderr)
		}
		r = runSh(t, exec.Command(sh, "-c", c), "ask", path, hookInput("dropdb app", t.TempDir()), env)
		if r.code != 0 || !strings.Contains(r.stdout, `"permissionDecision":"ask"`) {
			t.Errorf("ask (substituted %v): exit %d, stdout %q, stderr %q", substituted, r.code, r.stdout, r.stderr)
		}
	}
}

// TestPluginHookOff: unset or any value but ask and deny is no output and exit 0, without running
// deedglass at all (here a deedglass that would block if it ran).
func TestPluginHookOff(t *testing.T) {
	blocker := binDir(t, `echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}'; echo ran >&2; exit 2`)
	for _, path := range []string{blocker, binDir(t, "")} {
		for _, v := range []string{"-", "", "off", "ASK", "Deny", "allow", "1", "true"} {
			r := runPluginHook(t, v, path, hookInput("npx prisma migrate reset --force", t.TempDir()))
			if r.code != 0 || r.stdout != "" || r.stderr != "" {
				t.Errorf("DEEDGLASS_SHELL_GUARD=%q: exit %d, stdout %q, stderr %q", v, r.code, r.stdout, r.stderr)
			}
		}
	}
}

// TestPluginHookCannotCheck: on, but deedglass is missing or can't run guard shell (an older
// deedglass exits 2 on an unknown command). ask fails open: exit 0, no decision, a systemMessage.
// deny keeps the documented behaviour: a message on stderr and exit 1, which doesn't block. Never 2.
func TestPluginHookCannotCheck(t *testing.T) {
	paths := map[string]string{
		"missing": binDir(t, ""),
		"older":   binDir(t, `cat >/dev/null; echo 'unknown command "guard"' >&2; exit 2`),
		"failing": binDir(t, `exit 1`),
	}
	for name, path := range paths {
		r := runPluginHook(t, "ask", path, hookInput("dropdb app", t.TempDir()))
		var o map[string]any
		if r.code != 0 || json.Unmarshal([]byte(r.stdout), &o) != nil || len(o) != 1 ||
			!strings.HasPrefix(o["systemMessage"].(string), "The Deedglass shell guard is on (DEEDGLASS_SHELL_GUARD=ask) but ") {
			t.Errorf("%s, ask: exit %d, stdout %q, stderr %q", name, r.code, r.stdout, r.stderr)
		}
		r = runPluginHook(t, "deny", path, hookInput("dropdb app", t.TempDir()))
		if r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, "The Deedglass shell guard is on (DEEDGLASS_SHELL_GUARD=deny) but ") {
			t.Errorf("%s, deny: exit %d, stdout %q, stderr %q", name, r.code, r.stdout, r.stderr)
		}
	}
	if r := runPluginHook(t, "ask", paths["missing"], hookInput("ls", t.TempDir())); !strings.Contains(r.stdout, "deedglass isn't on PATH") {
		t.Errorf("missing: %q", r.stdout)
	}
	if r := runPluginHook(t, "deny", paths["older"], hookInput("ls", t.TempDir())); !strings.Contains(r.stderr, "exited 2") {
		t.Errorf("older: %q", r.stderr)
	}
}

// TestPluginHookOn: with the real CLI on PATH, ask and deny behave as before: the CLI's one JSON line
// on stdout and exit 0, nothing for a harmless command.
func TestPluginHookOn(t *testing.T) {
	path := realDeedglass(t)
	cases := []struct{ guard, cmd, decision string }{
		{"ask", "npx prisma migrate reset --force", "ask"},
		{"deny", "dropdb app", "deny"},
		{"ask", "git status && npm test", ""},
		{"deny", "ls", ""},
	}
	for _, c := range cases {
		r := runPluginHook(t, c.guard, path, hookInput(c.cmd, t.TempDir()))
		if r.code != 0 || r.stderr != "" {
			t.Fatalf("%s %q: exit %d, stderr %q", c.guard, c.cmd, r.code, r.stderr)
		}
		if c.decision == "" {
			if r.stdout != "" {
				t.Errorf("%s %q: want no output, got %q", c.guard, c.cmd, r.stdout)
			}
			continue
		}
		var o hookOut
		if err := json.Unmarshal([]byte(r.stdout), &o); err != nil || o.HookSpecificOutput.PermissionDecision != c.decision ||
			!strings.HasPrefix(o.HookSpecificOutput.PermissionDecisionReason, "Deedglass ") || strings.Count(r.stdout, "\n") != 1 {
			t.Errorf("%s %q: %q (%v)", c.guard, c.cmd, r.stdout, err)
		}
	}
}
