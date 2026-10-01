package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"unicode"

	"github.com/deedglass/deedglass/internal/buildinfo"
	"github.com/deedglass/deedglass/internal/config"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

const key32 = "q83vEjRWeJq83vEjRWeJq83vEjRWeJq83vEjRWeJq80="

type result struct {
	code           int
	stdout, stderr string
}

// cli runs the CLI in-process with an injected environment.
func cli(t *testing.T, env map[string]string, tty bool, args ...string) result {
	t.Helper()
	return cliIn(t, nil, env, tty, args...)
}

// cliIn is cli with stdin.
func cliIn(t *testing.T, stdin []byte, env map[string]string, tty bool, args ...string) result {
	t.Helper()
	saved := buildInfo
	buildInfo = func() buildinfo.Info {
		return buildinfo.Info{Object: "version", Version: "0.1.0-dev", Commit: "5495f7e", Modified: true,
			APIVersions: []string{"2026-10-01"}, Go: "go1.27.1", OS: "darwin", Arch: "arm64"}
	}
	t.Cleanup(func() { buildInfo = saved })
	if env == nil {
		env = map[string]string{}
	}
	if tty {
		if _, set := env["TERM"]; !set {
			env["TERM"] = "xterm-256color"
		}
	}
	dir := t.TempDir()
	var out, errb bytes.Buffer
	e := &Env{
		Stdin: bytes.NewReader(stdin), Stdout: &out, Stderr: &errb, IsTTY: tty,
		Getenv:      func(k string) string { return env[k] },
		LoadOptions: func() config.LoadOptions { return config.LoadOptions{Env: env, FS: fstest.MapFS{}, Dir: dir} },
	}
	code := run(context.Background(), e, args)
	return result{code, out.String(), errb.String()}
}

func golden(t *testing.T, name string, r result) {
	t.Helper()
	got := "exit " + string(rune('0'+r.code)) + "\n--- stdout\n" + r.stdout + "--- stderr\n" + r.stderr
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./cmd/deedglass -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s (run go test ./cmd/deedglass -update after checking the change)\n--- got\n%s--- want\n%s", name, path, got, want)
	}
}

func TestGoldens(t *testing.T) {
	prod := map[string]string{"DEEDGLASS_ENV": "prod"}
	injected := map[string]string{
		"DEEDGLASS_DATABASE_URL": "postgres://deedglass_app:local-dev-password@127.0.0.1:55432/deedglass",
		"DEEDGLASS_LOG_LEVEL":    "debug",
	}
	cases := []struct {
		name string
		env  map[string]string
		tty  bool
		args []string
		code int
	}{
		{"help", nil, false, []string{"help"}, 0},
		{"help_color", nil, true, []string{"help"}, 0},
		{"help_no_args", nil, false, nil, 0},
		{"help_agent", nil, false, []string{"help", "agent"}, 0},
		{"help_flag_on_command", nil, false, []string{"config", "print", "--help"}, 0},
		{"version", nil, false, []string{"version"}, 0},
		{"version_json", nil, false, []string{"version", "--json"}, 0},
		{"config_print", injected, false, []string{"config", "print"}, 0},
		{"config_print_color", injected, true, []string{"config", "print"}, 0},
		{"config_print_prod", prod, false, []string{"config", "print"}, 1},
		{"config_print_txv", map[string]string{"TXV_DATABASE_URL": "x"}, false, []string{"config", "print"}, 1},
		{"unknown_command", nil, false, []string{"polcy", "lint"}, 2},
		{"unknown_no_match", nil, false, []string{"frobnicate"}, 2},
		{"unbuilt_command", nil, false, []string{"approvals", "list", "--org", "org_x"}, 2},
		{"json_error", nil, false, []string{"approvals", "list", "--json"}, 2},
		{"json_config_error", prod, false, []string{"--json", "config", "print"}, 1},
		{"global_flag_after_subcommand", nil, false, []string{"version", "--no-color", "--json"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := cli(t, c.env, c.tty, c.args...)
			if r.code != c.code {
				t.Errorf("exit %d, want %d\nstdout %s\nstderr %s", r.code, c.code, r.stdout, r.stderr)
			}
			golden(t, c.name, r)
		})
	}
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		args []string
		code int
		errs string // stderr substring
		outs string // stdout substring
	}{
		{[]string{"executor", "serve"}, 2, `"executor serve" was a V0 command. Its successor is deedglass serve (S03).`, ""},
		{[]string{"statement", "verify", "s.cose"}, 2, "Its successor is deedglass verify (S05)", ""},
		{[]string{"keygen", "--principal"}, 2, "Its successor is deedglass keys gen (S04)", ""},
		{[]string{"sign"}, 2, "deedglass dev sign-intent", ""},
		{[]string{"agent"}, 2, "deedglass agent needs a subcommand.", ""},
		{[]string{"agent", "frob"}, 2, `Unknown command "agent frob".`, ""},
		{[]string{"agent", "kyes", "list"}, 2, `Did you mean "agent keys"?`, ""},
		{[]string{"policy", "test", "--json"}, 2, `"code":"not_built"`, ""},
		{[]string{"policy", "lint", "--json"}, 2, `"code":"usage_error"`, ""},
		{[]string{"agent", "keys", "create", "--json"}, 2, `"code":"usage_error"`, ""},
		{[]string{"agent", "start", "support-bot"}, 2, `Starting an agent needs a reason. Add --reason "why it's safe now"; it goes into the deed.`, ""},
		{[]string{"agent", "list"}, 3, "This command needs an admin API key.", ""},
		{[]string{"org", "stop-all"}, 2, "needs a typed confirmation", ""},
		{[]string{"approve", "apr_x"}, 2, "deedglass approve isn't in this build yet.", ""},
		{[]string{"help", "nope"}, 2, `Unknown command "nope"`, ""},
		{[]string{"help", "agent", "keys"}, 0, "", "deedglass agent keys"},
		{[]string{"help", "--json"}, 0, "", `"object":"command_list"`},
		{[]string{"--json", "help"}, 0, "", `"path":"agent keys create"`},
		{[]string{"version", "extra"}, 2, "takes no arguments", ""},
		{[]string{"version", "--check"}, 2, "deedglass version --check isn't in this build yet.", ""},
		{[]string{"version", "--bogus"}, 2, "flag provided but not defined: -bogus", ""},
		{[]string{"version", "--server", "http://127.0.0.1:1"}, 1, "Can't reach the server at http://127.0.0.1:1", ""},
		{[]string{"--server=ftp://x.example", "version"}, 2, "--server must be an http or https URL", ""},
		{[]string{"serve", "extra"}, 2, "deedglass serve takes no arguments", ""},
		{[]string{"version", "--server"}, 2, "flag --server needs a value", ""},
		{[]string{"-vv", "--org", "org_x", "--api-key", "k", "version"}, 0, "", "deedglass 0.1.0-dev"},
		{[]string{"--verbose", "version"}, 0, "", "deedglass 0.1.0-dev"},
		{[]string{"config", "print", "extra"}, 2, "takes no arguments", ""},
		{[]string{"--bogus"}, 2, "flag --bogus needs a command before it", ""},
		{[]string{"-h"}, 0, "", "deedglass · the execution and accountability layer"},
		{[]string{"verify", "--", "-h"}, 2, "Can't read -h: no such file", ""},
		{[]string{"dev", "id", "agt"}, 0, "", "agt_"},
		{[]string{"dev", "id", "AGT_"}, 0, "", "agt_"},
		{[]string{"--json", "dev", "id", "deed"}, 0, "", `"id":"deed_`},
		{[]string{"dev", "id"}, 2, "needs one prefix", ""},
		{[]string{"dev", "id", "zzz"}, 2, `"zzz" isn't an id prefix`, ""},
	}
	for _, c := range cases {
		r := cli(t, nil, false, c.args...)
		if r.code != c.code || !strings.Contains(r.stderr, c.errs) || !strings.Contains(r.stdout, c.outs) {
			t.Errorf("%v: exit %d (want %d)\nstdout %q (want %q)\nstderr %q (want %q)", c.args, r.code, c.code, r.stdout, c.outs, r.stderr, c.errs)
		}
	}
}

func TestEveryCommandDispatches(t *testing.T) {
	for _, c := range commands {
		if c.Built() {
			continue
		}
		r := cli(t, nil, false, c.Path...)
		want := "deedglass " + strings.Join(c.Path, " ") + " isn't in this build yet.\nRun deedglass help to see what this build can do.\n"
		if r.code != ExitUsage || r.stderr != want || r.stdout != "" {
			t.Errorf("%v: exit %d stderr %q", c.Path, r.code, r.stderr)
		}
		if c.Owner == "" || !regexp.MustCompile(`^S(0[2-9]|[1-3][0-9])$`).MatchString(c.Owner) {
			t.Errorf("%v: unbuilt commands name their owner session, got %q", c.Path, c.Owner)
		}
	}
}

func TestRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range commands {
		path := strings.Join(c.Path, " ")
		if seen[path] {
			t.Errorf("duplicate command %q", path)
		}
		seen[path] = true
		s := c.Summary
		if s == "" || len([]rune(s)) > 48 || strings.HasSuffix(s, ".") || !unicode.IsUpper([]rune(s)[0]) {
			t.Errorf("%q: summary %q must be sentence case, at most 48 characters, no final period", path, s)
		}
		if c.Built() && c.Owner != "" {
			t.Errorf("%q is built, so its Owner must be empty", path)
		}
		// No command path is a prefix of another built leaf's path in a way that shadows it.
		if cmd, n := lookup(c.Path); cmd != c || n != len(c.Path) {
			t.Errorf("lookup(%q) found %v", path, cmd.Path)
		}
	}
	// Every visible command appears in exactly one help line, and every line points at real commands.
	inLine := map[string]int{}
	for _, l := range lines {
		if len([]rune(l.Summary)) > 48 || strings.HasSuffix(l.Summary, ".") {
			t.Errorf("line %q summary %q", l.Name, l.Summary)
		}
		for _, p := range l.Paths {
			if !seen[p] {
				t.Errorf("help line %q lists unknown command %q", l.Name, p)
			}
			inLine[p]++
		}
	}
	for _, c := range commands {
		path := strings.Join(c.Path, " ")
		if !c.Hidden && inLine[path] != 1 {
			t.Errorf("%q appears in %d help lines, want 1", path, inLine[path])
		}
	}
	// The contract §10 families are all present.
	for _, fam := range []string{"init", "doctor", "serve", "migrate up", "config print", "keys gen", "version", "vault rotate",
		"help", "completion fish", "agent start", "agent keys roll", "agent intent mode", "org stop-all", "org start-all",
		"org create", "org login-link", "org keys revoke", "policy diff", "act", "actions cancel", "undo run", "approve", "deny",
		"approvals show", "webhooks delete", "verify", "deeds export", "ledger prune", "connector types",
		"connector credentials remove", "connector postgres check", "decoder drop", "slack manifest", "connector gmail authorize",
		"mcp wrap", "mcp serve", "mcp snippet", "insights settings", "insights suggestions generate", "insights digest",
		"dev seed", "dev fake-action", "dev simulate", "dev agent-script", "dev mail-preview", "dev sign-intent", "dev stripe-fixtures"} {
		if !seen[fam] {
			t.Errorf("contract command %q is missing from the registry", fam)
		}
	}
	if Group(0).String() != "Setup" || Dev.String() != "Dev" {
		t.Error("group names")
	}
}

func TestParseGlobals(t *testing.T) {
	g, rest, err := parseGlobals([]string{"agent", "--json", "list", "-vvv", "--server=https://a", "--org", "org_1", "--limit", "5", "--", "--json"})
	if err != nil || !g.JSON || g.Verbose != 3 || g.Server != "https://a" || g.Org != "org_1" {
		t.Fatalf("%+v %v", g, err)
	}
	if strings.Join(rest, " ") != "agent list --limit 5 -- --json" {
		t.Fatalf("rest %q", rest)
	}
}

// TestConfigPrintCanary: a canary vault key never reaches stdout or stderr, in text or JSON.
func TestConfigPrintCanary(t *testing.T) {
	const canary = "canary"
	env := map[string]string{
		"DEEDGLASS_VAULT_KEY":      "Y2FuYXJ5Y2FuYXJ5Y2FuYXJ5Y2FuYXJ5Y2FuYXJ5MTI=", // "canarycanary…" as base64, 32 bytes
		"DEEDGLASS_API_KEY_PEPPER": key32,
		"DEEDGLASS_DATABASE_URL":   "postgres://u:" + canary + "@127.0.0.1/db",
		"DEEDGLASS_RESEND_API_KEY": "re_" + canary,
		"DEEDGLASS_MAIL_FROM":      "Deedglass <signin@example.com>",
	}
	for _, args := range [][]string{{"config", "print"}, {"config", "print", "--json"}} {
		r := cli(t, env, false, args...)
		if r.code != 0 {
			t.Fatalf("%v: %d %s", args, r.code, r.stderr)
		}
		all := r.stdout + r.stderr
		if strings.Contains(all, canary) || strings.Contains(all, env["DEEDGLASS_VAULT_KEY"]) {
			t.Fatalf("secret printed by %v:\n%s", args, all)
		}
		if args[len(args)-1] == "--json" {
			var v map[string]any
			if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v["object"] != "config" {
				t.Fatalf("json: %v %s", err, r.stdout)
			}
		}
	}
	// Warnings go to stderr and don't change the exit code.
	r := cli(t, map[string]string{"DEEDGLASS_DATABSE_URL": "x"}, false, "config", "print")
	if r.code != 0 || !strings.Contains(r.stderr, "○ DEEDGLASS_DATABSE_URL isn't a Deedglass setting. Did you mean DEEDGLASS_DATABASE_URL?") {
		t.Fatalf("warning: %d %q", r.code, r.stderr)
	}
	// A single problem prints on one line.
	r = cli(t, map[string]string{"DEEDGLASS_SIGNUP": "open"}, false, "config", "print")
	if r.code != 1 || r.stderr != "✕ DEEDGLASS_SIGNUP must be one of invite, closed (got \"open\").\n" {
		t.Fatalf("single problem: %d %q", r.code, r.stderr)
	}
	r = cli(t, map[string]string{"DEEDGLASS_SIGNUP": "open"}, false, "config", "print", "--json")
	if r.code != 1 || !strings.Contains(r.stderr, `"message":"1 problem in the dev config.`) {
		t.Fatalf("single problem json: %d %q", r.code, r.stderr)
	}
	// Dev secrets already on disk are reported as "from".
	dir := t.TempDir()
	e := &Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Getenv: func(string) string { return "" },
		LoadOptions: func() config.LoadOptions {
			return config.LoadOptions{Env: map[string]string{"DEEDGLASS_VAULT_KEY": key32}, FS: fstest.MapFS{}, Dir: dir}
		}}
	run(context.Background(), e, []string{"config", "print"})
	out := &bytes.Buffer{}
	e.Stdout = out
	if code := run(context.Background(), e, []string{"config", "print"}); code != 0 || !strings.Contains(out.String(), "1 local secret from .deedglass/") {
		t.Fatalf("second run: %d %s", code, out)
	}
}

func TestReportPlainError(t *testing.T) {
	var errb bytes.Buffer
	e := &Env{Stderr: &errb}
	if code := report(e, os.ErrNotExist); code != ExitFailure || errb.String() != "✕ file does not exist.\n" {
		t.Fatalf("%d %q", code, errb.String())
	}
	errb.Reset()
	e.Globals.JSON = true
	if code := report(e, &ExitError{Code: ExitNotAuthed, ErrCode: "invalid_api_key", Err: os.ErrPermission}); code != ExitNotAuthed ||
		!strings.Contains(errb.String(), `"type":"authentication_error"`) {
		t.Fatalf("%d %q", code, errb.String())
	}
	if sentence("ok?") != "ok?" || sentence("") != "" || (&ExitError{Err: os.ErrClosed}).Unwrap() != os.ErrClosed {
		t.Fatal("helpers")
	}
}

// TestDocsTree keeps the command table in docs/CLI.md in step with the registry.
func TestDocsTree(t *testing.T) {
	const path = "../../docs/CLI.md"
	const begin, end = "<!-- commands:begin (generated from cmd/deedglass/registry.go; go test ./cmd/deedglass -run TestDocsTree -update) -->\n", "<!-- commands:end -->"
	var b strings.Builder
	b.WriteString("| Command | Group | What it does | Status |\n|---|---|---|---|\n")
	for _, c := range commands {
		status := "built"
		if !c.Built() {
			status = "soon (" + c.Owner + ")"
		}
		if c.Hidden {
			status += ", hidden"
		}
		usage := strings.Join(c.Path, " ")
		if c.Usage != "" {
			usage += " " + c.Usage
		}
		b.WriteString("| `deedglass " + usage + "` | " + c.Group.String() + " | " + c.Summary + " | " + status + " |\n")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	i, j := strings.Index(doc, begin), strings.Index(doc, end)
	if i < 0 || j < i {
		t.Fatalf("docs/CLI.md needs the markers %q and %q", begin, end)
	}
	if doc[i+len(begin):j] == b.String() {
		return
	}
	if !*update {
		t.Fatal("docs/CLI.md command table drifted from the registry. Run: go test ./cmd/deedglass -run TestDocsTree -update")
	}
	if err := os.WriteFile(path, []byte(doc[:i+len(begin)]+b.String()+doc[j:]), 0o644); err != nil {
		t.Fatal(err)
	}
}
