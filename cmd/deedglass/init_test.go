package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInitUsage(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"init", "--postgres", "url"}, "--postgres url isn't in this build"},
		{[]string{"init", "--client", "emacs"}, "--client takes claude-code"},
		{[]string{"init", "--reset-secrets", "--yes"}, "--reset-secrets needs you to type reset"},
		{[]string{"init", "extra"}, "takes no arguments"},
	} {
		r := cli(t, nil, false, c.args...)
		if r.code != ExitUsage || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%v: %d %q", c.args, r.code, r.stderr)
		}
	}
	if r := cli(t, nil, false, "init", "--stop", "--dir", t.TempDir()); r.code != ExitFailure || !strings.Contains(r.stderr, "there's no local stack in this directory") {
		t.Fatalf("%d %q", r.code, r.stderr)
	}
}

// TestDoctorCLI: with nothing set up, doctor still runs every check, prints a fix under each
// problem and exits 1; --json is one doctor_report; --only filters; an unknown id is a usage error.
func TestDoctorCLI(t *testing.T) {
	env := map[string]string{"DEEDGLASS_SERVER": "http://127.0.0.1:1", "DEEDGLASS_ENV": "dev"}
	r := cli(t, env, false, "doctor")
	if r.code != ExitFailure || !strings.HasPrefix(r.stdout, "deedglass doctor · 16 checks · ") {
		t.Fatalf("%d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{"✕ Database             no database URL", "Set DEEDGLASS_DATABASE_URL, or run deedglass doctor in the directory where you ran deedglass init.", "✕ Server               no answer from http://127.0.0.1:1"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("missing %q in\n%s", want, r.stdout)
		}
	}
	r = cli(t, env, false, "--json", "doctor", "--only", "db.*,disk.space")
	var rep struct {
		Object  string `json:"object"`
		Results []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Doc    string `json:"doc"`
		} `json:"results"`
		Summary map[string]int `json:"summary"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil || rep.Object != "doctor_report" || len(rep.Results) != 4 || rep.Results[0].Status != "fail" {
		t.Fatalf("%v %s", err, r.stdout)
	}
	if r = cli(t, env, false, "doctor", "--only", "nope.*"); r.code != ExitUsage {
		t.Fatalf("%d", r.code)
	}
}
