package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hookInput(cmd, cwd string) []byte {
	b, _ := json.Marshal(map[string]any{
		"session_id": "abc123", "cwd": cwd, "permission_mode": "default", "hook_event_name": "PreToolUse",
		"tool_name": "Bash", "tool_input": map[string]any{"command": cmd, "description": "Run it"}, "tool_use_id": "toolu_01",
	})
	return b
}

type hookOut struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// TestGuardShellHook: the documented PreToolUse contract end to end through the CLI. An ask (or a
// deny with --deny) is one JSON object on stdout and exit 0; nothing to ask about is no output.
func TestGuardShellHook(t *testing.T) {
	cases := []struct {
		cmd, flag, decision, reason string
	}{
		{"npx prisma migrate reset --force", "", "ask", "Deedglass asks first: prisma migrate reset drops the database"},
		{"cd /path/to/backend && .venv/bin/python -m alembic downgrade base", "", "ask", "alembic downgrade base runs every downgrade"},
		{"ssh prod 'psql -c \"ALTER TABLE orders DROP COLUMN total\"'", "", "ask", "psql would run ALTER TABLE … DROP COLUMN"},
		{"dropdb app", "--deny", "deny", "Deedglass denied this command because dropdb drops a whole Postgres database; if it is intended, ask the user to run it themselves."},
		{"git status && npm test", "", "", ""},
		{"psql -c 'SELECT count(*) FROM users'", "--deny", "", ""},
	}
	for _, c := range cases {
		args := []string{"guard", "shell"}
		if c.flag != "" {
			args = append(args, c.flag)
		}
		r := cliIn(t, hookInput(c.cmd, t.TempDir()), nil, false, args...)
		if r.code != 0 || r.stderr != "" {
			t.Fatalf("%s: exit %d, stderr %q", c.cmd, r.code, r.stderr)
		}
		if c.decision == "" {
			if r.stdout != "" {
				t.Errorf("%s: want no output, got %q", c.cmd, r.stdout)
			}
			continue
		}
		if strings.Count(r.stdout, "\n") != 1 {
			t.Errorf("%s: want one JSON line, got %q", c.cmd, r.stdout)
		}
		var o hookOut
		if err := json.Unmarshal([]byte(r.stdout), &o); err != nil {
			t.Fatalf("%s: %v in %q", c.cmd, err, r.stdout)
		}
		h := o.HookSpecificOutput
		if h.HookEventName != "PreToolUse" || h.PermissionDecision != c.decision || !strings.Contains(h.PermissionDecisionReason, c.reason) {
			t.Errorf("%s: got %+v", c.cmd, h)
		}
	}
}

// TestGuardShellHookIgnores: other tools and other events get no answer.
func TestGuardShellHookIgnores(t *testing.T) {
	for _, in := range []string{
		`{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"x.sh","content":"dropdb app"}}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"dropdb app"}}`,
	} {
		if r := cliIn(t, []byte(in), nil, false, "guard", "shell"); r.code != 0 || r.stdout != "" || r.stderr != "" {
			t.Errorf("%s: exit %d stdout %q stderr %q", in, r.code, r.stdout, r.stderr)
		}
	}
}

// TestGuardShellBadInputNeverBlocks: Claude Code reads exit 2 from a PreToolUse hook as a block,
// so input the guard can't read exits 1 (a non-blocking error), with nothing on stdout.
func TestGuardShellBadInputNeverBlocks(t *testing.T) {
	for _, in := range []string{"", "not json", "[1,2]", `{"tool_input":`} {
		r := cliIn(t, []byte(in), nil, false, "guard", "shell")
		if r.code != ExitFailure || r.stdout != "" || !strings.Contains(r.stderr, "Deedglass couldn't read the hook input") {
			t.Errorf("%q: exit %d stdout %q stderr %q", in, r.code, r.stdout, r.stderr)
		}
	}
}

// TestGuardShellFiles: the hook reads the package.json script from the hook's cwd, and --no-files
// turns that off; a FIFO or a directory is never opened.
func TestGuardShellFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"db:reset":"prisma migrate reset --force"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := cliIn(t, hookInput("npm run db:reset", dir), nil, false, "guard", "shell")
	if r.code != 0 || !strings.Contains(r.stdout, "prisma migrate reset (in npm run db:reset)") {
		t.Fatalf("exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	if r := cliIn(t, hookInput("npm run db:reset", dir), nil, false, "guard", "shell", "--no-files"); r.code != 0 || r.stdout != "" {
		t.Errorf("--no-files: exit %d stdout %q", r.code, r.stdout)
	}
	if _, err := readGuardFile(dir); err == nil {
		t.Error("a directory was read")
	}
	big := filepath.Join(dir, "big.sql")
	if err := os.WriteFile(big, make([]byte, 1<<20+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readGuardFile(big); err == nil {
		t.Error("a file over 1 MiB was read")
	}
}

func TestGuardShellCommand(t *testing.T) {
	r := cli(t, nil, false, "guard", "shell", "--command", "npx prisma db push --accept-data-loss; psql -c 'TRUNCATE jobs'")
	want := "Deedglass asks first: prisma db push --accept-data-loss applies schema changes that Prisma warns will delete data (and 1 more in this command).\n" +
		"  prisma-db-push-accept-data-loss  prisma db push --accept-data-loss applies schema changes that Prisma warns will delete data\n" +
		"  sql-truncate                     psql would run TRUNCATE, which deletes every row in the table\n"
	if r.code != ExitFailure || r.stdout != want || r.stderr != "" {
		t.Errorf("exit %d\nstdout %q\nwant   %q\nstderr %q", r.code, r.stdout, want, r.stderr)
	}
	r = cli(t, nil, false, "guard", "shell", "--command", "git status")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "Nothing to ask about") {
		t.Errorf("nothing: exit %d stdout %q", r.code, r.stdout)
	}
	r = cli(t, nil, false, "--json", "guard", "shell", "--deny", "--command", "dropdb app")
	var j struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
		Findings []struct{ ID, What string }
	}
	if err := json.Unmarshal([]byte(r.stdout), &j); err != nil || r.code != ExitFailure || j.Decision != "deny" || len(j.Findings) != 1 || j.Findings[0].ID != "dropdb" {
		t.Errorf("json: exit %d %q %v", r.code, r.stdout, err)
	}
	r = cli(t, nil, false, "--json", "guard", "shell", "--command", "ls")
	if r.code != 0 || !strings.Contains(r.stdout, `"decision":"none"`) || !strings.Contains(r.stdout, `"findings":[]`) {
		t.Errorf("json none: exit %d %q", r.code, r.stdout)
	}
	if r := cli(t, nil, false, "guard", "shell", "extra"); r.code != ExitUsage {
		t.Errorf("extra argument: exit %d", r.code)
	}
}

func TestGuardShellList(t *testing.T) {
	r := cli(t, nil, false, "--json", "guard", "shell", "--list")
	var rows []struct{ ID, Shape, Why, Source, Checked string }
	if err := json.Unmarshal([]byte(r.stdout), &rows); err != nil || r.code != 0 || len(rows) < 30 {
		t.Fatalf("exit %d, %d rows, %v", r.code, len(rows), err)
	}
	for _, row := range rows {
		if row.ID == "" || row.Source == "" || row.Checked == "" {
			t.Errorf("row %+v", row)
		}
	}
	if r := cli(t, nil, false, "guard", "shell", "--list"); r.code != 0 || !strings.Contains(r.stdout, "prisma migrate reset\n") {
		t.Errorf("text: exit %d", r.code)
	}
}
