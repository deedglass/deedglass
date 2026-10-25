package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// rawGo matches the Go runtime, standard library and driver phrasings that must never reach a person
// (docs/COPY_CATALOG.md › CLI style: what happened · why · what to do). plainError rewrites the
// ones that leak through wrapped errors; a call site that still prints one fails here.
var rawGo = regexp.MustCompile(`no such file or directory|dial tcp|invalid character '|unexpected EOF|goroutine \d+|` +
	`panic:|x509:|context deadline exceeded|json: (?:cannot unmarshal|unknown field)|strconv\.|i/o timeout|` +
	`flag provided but not defined|failed to parse as|\.go:\d+|runtime error|nil pointer|pq: |` +
	`use of closed network connection|fork/exec|exec: "`)

// TestNoRawGoErrors runs commands that fail in the ways people hit first (a missing file, a bad
// URL, nothing listening, broken JSON, an unknown flag) and fails on any raw Go error text, in text
// and in --json. Every failure must also end with a next step.
func TestNoRawGoErrors(t *testing.T) {
	tmp := t.TempDir()
	bad := filepath.Join(tmp, "bad.jsonl")
	if err := os.WriteFile(bad, []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	badPolicy := filepath.Join(tmp, "bad.yaml")
	if err := os.WriteFile(badPolicy, []byte("rules: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(tmp, "missing")
	closed := "127.0.0.1:" + strconv.Itoa(closedPort(t))
	server := map[string]string{"DEEDGLASS_SERVER": "http://" + closed, "DEEDGLASS_API_KEY": "dg_test_0123456789abcdefghijklmnopqrstuvwxyzAB"}
	db := map[string]string{"DEEDGLASS_DATABASE_URL": "postgres://deedglass_app:x@" + closed + "/deedglass", "DEEDGLASS_ENV": "dev", "DEEDGLASS_VAULT_KEY": key32}
	cases := []struct {
		env  map[string]string
		args []string
	}{
		{nil, []string{"verify", missing + ".jsonl"}},
		{nil, []string{"verify", bad}},
		{nil, []string{"witness", "debezium", "--events", missing + ".gz", "--sql", "UPDATE t SET a = 1 WHERE id = 1"}},
		{nil, []string{"policy", "lint", missing + ".yaml"}},
		{nil, []string{"policy", "lint", badPolicy}},
		{nil, []string{"policy", "evaluate", "--policy", missing + ".yaml", "--tool", "postgres.query"}},
		{nil, []string{"export", missing, "--format", "ocsf"}},
		{nil, []string{"export", bad, "--format", "ocsf"}},
		{nil, []string{"connect", "notaurl"}},
		{nil, []string{"connect", "postgres://u:p@" + closed + "/db"}},
		{nil, []string{"version", "--bogus"}},
		{nil, []string{"scan", "--dir", missing}},
		{nil, []string{"init", "--stop", "--dir", tmp}},
		{nil, []string{"security-pack", "verify", missing}},
		{server, []string{"act", "postgres.query", "--args", `{"sql":"select 1"}`}},
		{server, []string{"approvals", "list"}},
		{server, []string{"agent", "list"}},
		{server, []string{"undo", "preview", "act_01k82m4v9q5n3d7f8g2h6j0kxp"}},
		{server, []string{"connector", "list"}},
		{db, []string{"deeds", "list"}},
		{db, []string{"keys", "list"}},
		{db, []string{"actions", "list"}},
	}
	for _, c := range cases {
		for _, json := range []bool{false, true} {
			args := c.args
			if json {
				args = append([]string{"--json"}, args...)
			}
			env := map[string]string{}
			for k, v := range c.env {
				env[k] = v
			}
			r := cli(t, env, false, args...)
			name := strings.Join(args, " ")
			if r.code == 0 {
				t.Errorf("%s: exit 0, want a failure\n%s%s", name, r.stdout, r.stderr)
				continue
			}
			if m := rawGo.FindString(r.stdout + r.stderr); m != "" {
				t.Errorf("%s: raw Go error text %q reaches the user:\n%s%s", name, m, r.stdout, r.stderr)
			}
			if !json && strings.TrimSpace(r.stderr) != "" && len(strings.Split(strings.TrimSpace(r.stderr), "\n")) < 2 {
				t.Errorf("%s: the error has no next step:\n%s", name, r.stderr)
			}
		}
	}
}
