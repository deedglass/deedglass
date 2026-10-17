package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/setup"
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

// TestDoctorProgress (PRD-06): in a directory init set up, a doctor run with no problem ends with the
// quickstart step line, read from the files and the Ledger; --quiet and --json print none, and a run
// with --only isn't the doctor step.
func TestDoctorProgress(t *testing.T) {
	mismatch := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := "[]"
		if r.URL.Path == "/v1/actions" && r.URL.Query().Get("verdict") == "MISMATCH" && mismatch != "" {
			data = `[{"id":"` + mismatch + `","status":"executed"}]`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":` + data + `,"has_more":false}`))
	}))
	defer ts.Close()
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, setup.DirName), 0o700)
	files := map[string]string{
		setup.StateFile:    `{"object":"init_state","version":1,"org":"org_x","agent":"agt_x"}`,
		setup.StackFile:    `{"object":"init_stack","listen":"` + strings.TrimPrefix(ts.URL, "http://") + `","pg_port":1}`,
		setup.AdminKeyFile: "dg_test_admin\n",
	}
	for name, body := range files {
		if err := os.WriteFile(setup.Path(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	doctor := func(args ...string) result {
		var out, errb bytes.Buffer
		e := &Env{Stdout: &out, Stderr: &errb, Getenv: func(string) string { return "" },
			LoadOptions: func() config.LoadOptions {
				return config.LoadOptions{Env: map[string]string{}, FS: fstest.MapFS{}, Dir: dir}
			}}
		code := run(context.Background(), e, append([]string{"doctor", "--only", "disk.space"}, args...))
		return result{code, out.String(), errb.String()}
	}
	r := doctor()
	if r.code != ExitOK || !strings.HasSuffix(r.stdout, "\n\nQuickstart step 2 of 5 · next: deedglass doctor\n") {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	mismatch = "act_01k6h7kp6msmzrhdzwtcr3jnkp"
	if r = doctor(); !strings.HasSuffix(r.stdout, "Quickstart step 4 of 5 · next: deedglass undo preview act_01k6h7kp6msmzrhdzwtcr3jnkp (it prints the undo run line)\n") {
		t.Fatalf("%s", r.stdout)
	}
	for _, args := range [][]string{{"--quiet"}, {"--json"}} {
		if r = doctor(args...); r.code != ExitOK || strings.Contains(r.stdout, "Quickstart step") {
			t.Fatalf("%v: %d %s", args, r.code, r.stdout)
		}
	}
	if l := progressLine(setup.Facts{SetUp: true, Checked: true}); l != "Quickstart step 3 of 5 · next: deedglass init --trigger" {
		t.Fatal(l)
	}
	if l := progressLine(setup.Facts{SetUp: true, Undo: true, Undone: true}); l != "" {
		t.Fatalf("all five done: %q", l)
	}
	// Outside an init directory doctor prints no step line.
	if r := cli(t, map[string]string{"DEEDGLASS_ENV": "dev"}, false, "doctor", "--only", "disk.space"); strings.Contains(r.stdout, "Quickstart") {
		t.Fatalf("%s", r.stdout)
	}
}
