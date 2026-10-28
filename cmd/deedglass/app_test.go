package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/localstore"
)

func TestAppDir(t *testing.T) {
	env := func(m map[string]string) *Env { return &Env{Getenv: func(k string) string { return m[k] }} }
	d, err := appDir(env(map[string]string{"DEEDGLASS_APP_DIR": "/srv/dg", "HOME": "/home/a"}))
	if err != nil || d != filepath.Clean("/srv/dg") {
		t.Fatalf("DEEDGLASS_APP_DIR: %q %v", d, err)
	}
	d, err = appDir(env(map[string]string{"HOME": "/home/a", "XDG_CONFIG_HOME": "/xdg", "AppData": `C:\Users\a\AppData\Roaming`}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"darwin":  "/home/a/Library/Application Support/Deedglass",
		"windows": filepath.Join(`C:\Users\a\AppData\Roaming`, "Deedglass"),
	}[runtime.GOOS]
	if want == "" {
		want = "/xdg/deedglass"
	}
	if d != want {
		t.Fatalf("appDir = %q, want %q", d, want)
	}
	if _, err := appDir(env(map[string]string{})); err == nil {
		t.Fatal("no home: want an error")
	}
}

func TestInteractive(t *testing.T) {
	base := func(m map[string]string) *Env {
		return &Env{IsTTY: true, StdinTTY: true, Getenv: func(k string) string { return m[k] }}
	}
	if !interactive(base(nil)) {
		t.Fatal("a terminal on both ends opens the app")
	}
	for name, e := range map[string]*Env{
		"ci":        base(map[string]string{"CI": "true"}),
		"no_app":    base(map[string]string{"DEEDGLASS_NO_APP": "1"}),
		"piped_out": {IsTTY: false, StdinTTY: true, Getenv: func(string) string { return "" }},
		"piped_in":  {IsTTY: true, StdinTTY: false, Getenv: func(string) string { return "" }},
	} {
		if interactive(e) {
			t.Errorf("%s: bare deedglass must keep printing the welcome", name)
		}
	}
	j := base(nil)
	j.Globals.JSON = true
	if interactive(j) {
		t.Error("--json: bare deedglass must keep printing the welcome")
	}
}

// deedglass app opens a browser only for a person at a terminal; scripts, pipes, CI, --json and
// AI agents get the link printed instead.
func TestAppMayOpen(t *testing.T) {
	tty := func(m map[string]string) *Env {
		return &Env{IsTTY: true, StdinTTY: true, Getenv: func(k string) string { return m[k] }}
	}
	if !mayOpen(tty(nil), false) {
		t.Fatal("a terminal opens the browser")
	}
	if mayOpen(tty(nil), true) {
		t.Error("--no-open must not open the browser")
	}
	if mayOpen(tty(map[string]string{"CI": "1"}), false) {
		t.Error("CI must not open the browser")
	}
	j := tty(nil)
	j.Globals.JSON = true
	if mayOpen(j, false) {
		t.Error("--json must not open the browser")
	}
	if mayOpen(&Env{IsTTY: false, Getenv: func(string) string { return "" }}, false) {
		t.Error("piped output must not open the browser")
	}
}

// A bare deedglass that isn't at a terminal never starts a server: it prints the welcome.
func TestBareNotInteractivePrintsWelcome(t *testing.T) {
	r := cli(t, map[string]string{"DEEDGLASS_APP_DIR": t.TempDir()}, true)
	if r.code != 0 || !strings.Contains(r.stdout, "deedglass app") {
		t.Fatalf("code %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestAppStatusAndResolver(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"DEEDGLASS_APP_DIR": dir}
	r := cli(t, env, false, "app", "--status", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"running":false`) {
		t.Fatalf("status, nothing running: code %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	e := &Env{Getenv: func(k string) string { return env[k] }}
	if tg := resolveServer(e); tg.From != fromDefault {
		t.Fatalf("no app: From = %q", tg.From)
	}
	// This test process stands in for a running app.
	if err := writeAppState(dir, &appState{PID: os.Getpid(), Server: "http://127.0.0.1:4999", PGPort: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, appAdminKey), []byte("dg_admin_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tg := resolveServer(e)
	if tg.From != fromApp || tg.Server != "http://127.0.0.1:4999" {
		t.Fatalf("app running: %+v", tg)
	}
	if k := tg.keyFor(e, "agent.key"); k != "" {
		t.Fatalf("the app sends only its admin key, got %q for agent.key", k)
	}
	// --server elsewhere: the app's key never goes there.
	env["DEEDGLASS_SERVER"] = "http://example.test"
	if tg := resolveServer(e); tg.From == fromApp {
		t.Fatal("DEEDGLASS_SERVER wins over the app")
	}
	delete(env, "DEEDGLASS_SERVER")
	// Status says running only when the server answers: here nothing listens.
	r = cli(t, env, false, "app", "--status", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"running":false`) {
		t.Fatalf("status, no server answering: code %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestAppResetAsksFirst(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "store.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := cli(t, map[string]string{"DEEDGLASS_APP_DIR": dir}, false, "app", "--reset")
	if r.code != ExitUsage {
		t.Fatalf("reset without a terminal or --yes: code %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "store.json")); err != nil {
		t.Fatal("reset deleted data without asking")
	}
	if err := os.MkdirAll(filepath.Join(dir, "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	r = cli(t, map[string]string{"DEEDGLASS_APP_DIR": dir}, false, "app", "--reset", "--yes")
	if r.code != 0 {
		t.Fatalf("reset --yes: code %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "store.json")); err == nil {
		t.Fatal("reset --yes kept the data")
	}
	if _, err := os.Stat(filepath.Join(dir, "runtime")); err != nil {
		t.Fatal("reset deleted the downloaded PostgreSQL")
	}
}

func TestEverydayHelp(t *testing.T) {
	if n := len(everyday); n < 6 || n > 10 {
		t.Fatalf("deedglass help lists %d commands; keep it about 8", n)
	}
	for _, c := range everyday {
		cmd, n := lookup(strings.Fields(c.Name))
		if cmd == nil || n != len(strings.Fields(c.Name)) || !cmd.Built() {
			t.Errorf("everyday command %q isn't a built command", c.Name)
		}
	}
	r := cli(t, nil, false, "help")
	if r.code != 0 || !strings.Contains(r.stdout, "deedglass help all") || strings.Contains(r.stdout, "Policies") {
		t.Fatalf("help:\n%s", r.stdout)
	}
	r = cli(t, nil, false, "help", "all")
	if r.code != 0 || !strings.Contains(r.stdout, "Policies") {
		t.Fatalf("help all:\n%s", r.stdout)
	}
}

// An empty actions list on the local app points at Connect, not at a dev-only command that needs a
// database URL the app never exposes.
func TestEmptyActionsNext(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"DEEDGLASS_APP_DIR": dir}
	e := &Env{Getenv: func(k string) string { return env[k] }}
	if got := emptyActionsNext(e); !strings.Contains(got, "fake-action") {
		t.Fatalf("no app: %q", got)
	}
	if err := writeAppState(dir, &appState{PID: os.Getpid(), Server: "http://127.0.0.1:4999", PGPort: 1}); err != nil {
		t.Fatal(err)
	}
	if got := emptyActionsNext(e); !strings.Contains(got, "app connect") || strings.Contains(got, "DEEDGLASS_ENV") {
		t.Fatalf("app running: %q", got)
	}
}

// A bad DEEDGLASS_PG_BUNDLE (the offline path) says how to fix the file, not to set a proxy.
func TestAppInstallBadBundle(t *testing.T) {
	if _, ok := localstore.CurrentBundle(); !ok {
		t.Skip("no ready-made PostgreSQL for this platform")
	}
	dir := t.TempDir()
	jar := filepath.Join(dir, "pg.jar")
	if err := os.WriteFile(jar, []byte("not a jar"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"DEEDGLASS_PG_BUNDLE": jar, "DEEDGLASS_PG_SYSTEM": "off"}
	var out, errb bytes.Buffer
	e := &Env{Stdout: &out, Stderr: &errb, Getenv: func(k string) string { return env[k] }}
	_, err := appInstall(context.Background(), e, localstore.New(filepath.Join(dir, "app"), e.Getenv))
	var x *ExitError
	if !errors.As(err, &x) {
		t.Fatalf("want an ExitError, got %v", err)
	}
	all := out.String() + errb.String() + x.Error() + x.Next
	if strings.Contains(all, "HTTPS_PROXY") || !strings.Contains(all, "DEEDGLASS_PG_BUNDLE") {
		t.Fatalf("hint: %q", all)
	}
}
