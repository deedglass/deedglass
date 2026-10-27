package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/setup"
)

// recorder is an httptest server that answers every request with a 404 error envelope and keeps
// the paths and Authorization headers it saw.
type recorder struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
	auths []string
}

func newRecorder(t *testing.T) *recorder {
	r := &recorder{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.mu.Lock()
		r.paths = append(r.paths, q.Method+" "+q.URL.Path)
		r.auths = append(r.auths, q.Header.Get("Authorization"))
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"not_found","message":"Not found. Check the id.","doc_url":"https://docs.deedglass.com/errors/not_found"}}`))
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *recorder) seen() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.paths...), append([]string{}, r.auths...)
}

func (r *recorder) reset() {
	r.mu.Lock()
	r.paths, r.auths = nil, nil
	r.mu.Unlock()
}

// closedPort is a loopback port nothing listens on.
func closedPort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

// initDir is a directory deedglass init set up with its server on a non-default port.
func initDir(t *testing.T, listen string) (string, string) {
	dir := t.TempDir()
	d := filepath.Join(dir, setup.DirName)
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	st := setup.Stack{Object: "init_stack", Version: 1, Project: "it-target", Container: "deedglass-it-target-pg", Volume: "deedglass-it-target-pgdata",
		PGPort: closedPort(t), DemoContainer: "deedglass-it-target-demo", DemoPort: closedPort(t), Listen: listen,
		AdminPassword: "a", MigratorPassword: "m", AppPassword: "p", DemoPassword: "d", OwnerEmail: setup.DefaultOwner}
	b, _ := json.Marshal(st)
	for name, body := range map[string]string{setup.StackFile: string(b), setup.AdminKeyFile: "dg_test_adminkeyfromthestackfile\n", setup.AgentKeyFile: "dg_test_agentkeyfromthestackfile\n"} {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, "dg_test_adminkeyfromthestackfile"
}

// runIn runs the CLI with dir as its working directory and nothing in the environment but HOME.
func runIn(t *testing.T, dir string, env map[string]string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	e := &Env{Stdin: bytes.NewReader(nil), Stdout: &out, Stderr: &errb,
		Getenv:      func(k string) string { return env[k] },
		LoadOptions: func() config.LoadOptions { return config.LoadOptions{Env: env, FS: os.DirFS(dir), Dir: dir} }}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code := run(ctx, e, args)
	return result{code, out.String(), errb.String()}
}

// TestServerResolver (adoption-v1 F2): every client command finds the server one way: --server,
// DEEDGLASS_SERVER, DEEDGLASS_BASE_URL, then the stack.json deedglass init wrote here or in a parent,
// then the default. Every subcommand runs in a directory whose stack listens on a non-default
// port; nothing may reach the default server, and the key from .deedglass/ only goes to the stack's.
func TestServerResolver(t *testing.T) {
	stack := newRecorder(t)
	sentinel := newRecorder(t) // stands in for http://127.0.0.1:8080
	saved := defaultServer
	defaultServer = sentinel.URL
	t.Cleanup(func() { defaultServer = saved })
	dir, adminKey := initDir(t, strings.TrimPrefix(stack.URL, "http://"))
	sub := filepath.Join(dir, "src", "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": t.TempDir()}

	// The resolver's order.
	e := &Env{Getenv: func(k string) string { return env[k] }, LoadOptions: func() config.LoadOptions { return config.LoadOptions{Dir: sub} }}
	if got := resolveServer(e); got.Server != stack.URL || got.From != fromStack || got.StackDir != dir {
		t.Fatalf("from a subfolder: %+v", got)
	}
	if k := resolveServer(e).keyFor(e, setup.AdminKeyFile); k != adminKey {
		t.Fatalf("stack key %q", k)
	}
	env["DEEDGLASS_BASE_URL"] = "http://127.0.0.1:1"
	if got := resolveServer(e); got.From != fromEnv || got.Server != "http://127.0.0.1:1" {
		t.Fatalf("DEEDGLASS_BASE_URL: %+v", got)
	}
	env["DEEDGLASS_SERVER"] = "http://127.0.0.1:2"
	if got := resolveServer(e); got.Server != "http://127.0.0.1:2" {
		t.Fatalf("DEEDGLASS_SERVER: %+v", got)
	}
	e.Globals.Server = "http://127.0.0.1:3"
	if got := resolveServer(e); got.From != fromFlag || got.Server != "http://127.0.0.1:3" {
		t.Fatalf("--server: %+v", got)
	}
	// A server named by a flag or the environment never gets the stack's key file.
	if k := resolveServer(e).keyFor(e, setup.AdminKeyFile); k != "" {
		t.Fatalf("the stack's key went to another server: %q", k)
	}
	delete(env, "DEEDGLASS_SERVER")
	delete(env, "DEEDGLASS_BASE_URL")
	e.Globals.Server = ""
	if got := resolveServer(&Env{Getenv: e.Getenv, LoadOptions: func() config.LoadOptions { return config.LoadOptions{Dir: t.TempDir()} }}); got.From != fromDefault || got.Server != sentinel.URL {
		t.Fatalf("no stack: %+v", got)
	}

	// Every subcommand, with placeholder arguments, from a subfolder of the init directory. health pilot is a local demo
	// with no server: like play, it is skipped (its Console link is only printed).
	skip := regexp.MustCompile(`^(serve|init|start|connect|play|health pilot|scan|help|version|completion .*|dev .*|licence .*|keys .*|mcp (serve|wrap)|a2a proxy|observe run|guard shell|deeds timestamp|witness debezium|verify|export|policy (packs .*|install|lint|fmt|test|evaluate)|security-pack (verify|build)|deeds seal-file)$`)
	reArg := regexp.MustCompile(`<([^>]+)>`)
	ran := 0
	for _, c := range commands {
		name := strings.Join(c.Path, " ")
		if skip.MatchString(name) || !c.Built() {
			continue
		}
		args := append([]string{}, c.Path...)
		usage := c.Usage
		if i := strings.Index(usage, "["); i >= 0 {
			usage = usage[:i]
		}
		for _, m := range reArg.FindAllStringSubmatch(usage, -1) {
			args = append(args, placeholder(m[1]))
		}
		sentinel.reset()
		r := runIn(t, sub, env, args...)
		if p, _ := sentinel.seen(); len(p) > 0 {
			t.Errorf("deedglass %s reached the default server instead of the stack's: %v (exit %d, %s)", name, p, r.code, r.stderr)
		}
		if name != "config print" && (strings.Contains(r.stderr, "127.0.0.1:8080") || strings.Contains(r.stdout, "127.0.0.1:8080")) {
			t.Errorf("deedglass %s named :8080: %s%s", name, r.stdout, r.stderr)
		}
		ran++
		if _, err := os.Stat(filepath.Join(dir, setup.DirName, setup.AdminKeyFile)); err != nil {
			t.Fatalf("deedglass %s removed the admin key file: %v", name, err)
		}
		if e := (&Env{Getenv: func(k string) string { return env[k] }, LoadOptions: func() config.LoadOptions { return config.LoadOptions{Dir: sub} }}); resolveServer(e).From != fromStack {
			t.Fatalf("after deedglass %s the stack isn't found: %+v %v", name, resolveServer(e), env)
		}
	}
	if ran < 60 {
		t.Fatalf("only %d subcommands ran", ran)
	}

	// The API commands reach the stack's server with the stack's admin key.
	for _, args := range [][]string{
		{"agent", "list"},
		{"agent", "show", "support-bot"},
		{"approvals", "list"},
		{"approvals", "show", "apr_01j9zw6q3m4n5p6q7r8s9t0v1w"},
		{"undo", "preview", "act_01j9zw6k8m4n5p6q7r8s9t0v1w"},
		{"undo", "run", "act_01j9zw6k8m4n5p6q7r8s9t0v1w", "--plan", "sha256:00", "--reason", "Put the rows back"},
		{"connector", "list"},
		{"actions", "cancel", "act_01j9zw6k8m4n5p6q7r8s9t0v1w"},
		{"webhooks", "list"},
		{"insights", "stats"},
		{"doctor", "--only", "server.health"},
	} {
		stack.reset()
		sentinel.reset()
		r := runIn(t, sub, env, args...)
		paths, auths := stack.seen()
		if len(paths) == 0 {
			t.Errorf("deedglass %s didn't reach the stack's server (exit %d): %s%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
			continue
		}
		for _, a := range auths {
			if a != "" && a != "Bearer "+adminKey && a != "Bearer dg_test_agentkeyfromthestackfile" {
				t.Errorf("deedglass %s sent %q", strings.Join(args, " "), a)
			}
		}
		if p, _ := sentinel.seen(); len(p) > 0 {
			t.Errorf("deedglass %s also reached the default server: %v", strings.Join(args, " "), p)
		}
	}
}

// TestNoKeyToAnotherServer (adoption-v1 F2, the security audit's regression test): inside an init
// directory, DEEDGLASS_API_KEY never goes to a server the environment names that differs from
// .deedglass/stack.json's (nor to the default :8080); only an explicit --server sends it elsewhere.
func TestNoKeyToAnotherServer(t *testing.T) {
	stack := newRecorder(t)
	other := newRecorder(t)
	sentinel := newRecorder(t)
	saved := defaultServer
	defaultServer = sentinel.URL
	t.Cleanup(func() { defaultServer = saved })
	dir, adminKey := initDir(t, strings.TrimPrefix(stack.URL, "http://"))
	const envKey = "dg_test_keyfromtheenvironment"
	cmds := [][]string{{"agent", "list"}, {"undo", "run", "act_01j9zw6k8m4n5p6q7r8s9t0v1w", "--plan", "sha256:00", "--reason", "Put the rows back"},
		{"approvals", "list"}, {"connector", "list"}, {"act", "fake.echo"}}
	noKeyTo := func(r *recorder, what string) {
		t.Helper()
		_, auths := r.seen()
		for _, a := range auths {
			if a != "" {
				t.Fatalf("%s got a key: %q", what, a)
			}
		}
	}
	for _, varName := range []string{"DEEDGLASS_SERVER", "DEEDGLASS_BASE_URL"} {
		for _, c := range cmds {
			other.reset()
			sentinel.reset()
			env := map[string]string{"HOME": t.TempDir(), varName: other.URL, "DEEDGLASS_API_KEY": envKey}
			r := runIn(t, dir, env, c...)
			noKeyTo(other, varName+" server for "+strings.Join(c, " "))
			noKeyTo(sentinel, "the default server for "+strings.Join(c, " "))
			if r.code != ExitNotAuthed || !strings.Contains(r.stderr, "so no API key was sent") || !strings.Contains(r.stderr, "--server "+other.URL) {
				t.Fatalf("%s %s: %d %s", varName, strings.Join(c, " "), r.code, r.stderr)
			}
		}
	}
	// The same server in the environment is fine, and gets the environment's key.
	stack.reset()
	if r := runIn(t, dir, map[string]string{"HOME": t.TempDir(), "DEEDGLASS_SERVER": stack.URL + "/", "DEEDGLASS_API_KEY": envKey}, "agent", "list"); r.code == ExitNotAuthed {
		t.Fatalf("same server refused: %s", r.stderr)
	}
	if _, auths := stack.seen(); len(auths) == 0 || auths[0] != "Bearer "+envKey {
		t.Fatalf("same server: %v", auths)
	}
	// No environment: the stack's own admin key goes to the stack only.
	stack.reset()
	runIn(t, dir, map[string]string{"HOME": t.TempDir()}, "agent", "list")
	if _, auths := stack.seen(); len(auths) == 0 || auths[0] != "Bearer "+adminKey {
		t.Fatalf("stack: %v", auths)
	}
	// An explicit --server sends the environment's key there, and never the stack's key file.
	other.reset()
	runIn(t, dir, map[string]string{"HOME": t.TempDir(), "DEEDGLASS_API_KEY": envKey}, "--server", other.URL, "agent", "list")
	if _, auths := other.seen(); len(auths) == 0 || auths[0] != "Bearer "+envKey {
		t.Fatalf("--server: %v", auths)
	}
	other.reset()
	r := runIn(t, dir, map[string]string{"HOME": t.TempDir()}, "--server", other.URL, "agent", "list")
	noKeyTo(other, "--server without a key")
	if r.code != ExitNotAuthed {
		t.Fatalf("--server without a key: %d %s", r.code, r.stderr)
	}
	// mcp serve holds the key back the same way.
	e := &Env{Getenv: func(k string) string {
		return map[string]string{"HOME": t.TempDir(), "DEEDGLASS_SERVER": other.URL, "DEEDGLASS_API_KEY": envKey}[k]
	}, LoadOptions: func() config.LoadOptions { return config.LoadOptions{Dir: dir} }}
	if s, k := mcpServeTarget(e); k != "" || s != other.URL {
		t.Fatalf("mcp serve: %s %q", s, k)
	}
}

func placeholder(arg string) string {
	switch {
	case strings.HasPrefix(arg, "act_"):
		return "act_01j9zw6k8m4n5p6q7r8s9t0v1w"
	case strings.HasPrefix(arg, "apr_"):
		return "apr_01j9zw6q3m4n5p6q7r8s9t0v1w"
	case strings.HasPrefix(arg, "obs_"):
		return "obs_01jb8m2x7k4q9r3t5v6w8y0z1a"
	case strings.HasPrefix(arg, "whk_"):
		return "whk_01jb8m2x7k4q9r3t5v6w8y0z1a"
	case strings.HasPrefix(arg, "deed_"):
		return "deed_01jb8m2x7k4q9r3t5v6w8y0z1a"
	}
	return "support-bot"
}

// TestMCPServeTarget (adoption-v1 F4): mcp serve uses the .deedglass/ of its folder or a parent,
// then ~/.deedglass/current.json, so a client added at user scope works in any project; a key from
// a file only goes to the server its stack names.
func TestMCPServeTarget(t *testing.T) {
	home := t.TempDir()
	dir, _ := initDir(t, "127.0.0.1:18090")
	env := map[string]string{"HOME": home}
	in := func(wd string) *Env {
		return &Env{Getenv: func(k string) string { return env[k] }, LoadOptions: func() config.LoadOptions { return config.LoadOptions{Dir: wd} }}
	}
	// Inside the init directory: its stack and its agent key.
	if s, k := mcpServeTarget(in(filepath.Join(dir))); s != "http://127.0.0.1:18090" || k != "dg_test_agentkeyfromthestackfile" {
		t.Fatalf("in the init dir: %s %q", s, k)
	}
	// Another project, before current.json: no key, the default server.
	other := t.TempDir()
	if s, k := mcpServeTarget(in(other)); k != "" || s != defaultServer {
		t.Fatalf("another project without current.json: %s %q", s, k)
	}
	// init wrote current.json: the other project finds the stack.
	st, _ := setup.ReadStack(dir)
	if err := setup.WriteCurrent(home, dir, st, time.Now()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(setup.CurrentPath(home)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("current.json mode: %v %v", fi, err)
	}
	b, _ := os.ReadFile(setup.CurrentPath(home))
	if bytes.Contains(b, []byte("dg_test_")) {
		t.Fatal("current.json holds a key")
	}
	if s, k := mcpServeTarget(in(other)); s != "http://127.0.0.1:18090" || k != "dg_test_agentkeyfromthestackfile" {
		t.Fatalf("another project with current.json: %s %q", s, k)
	}
	// A server named in the environment never gets the file's key.
	env["DEEDGLASS_SERVER"] = "https://deedglass.example.com"
	if s, k := mcpServeTarget(in(other)); s != "https://deedglass.example.com" || k != "" {
		t.Fatalf("explicit server: %s %q", s, k)
	}
	// DEEDGLASS_API_KEY_FILE names a key file (0600 only).
	kf := filepath.Join(t.TempDir(), "agent.key")
	_ = os.WriteFile(kf, []byte("dg_test_fromkeyfile\n"), 0o600)
	env["DEEDGLASS_API_KEY_FILE"] = kf
	if _, k := mcpServeTarget(in(other)); k != "dg_test_fromkeyfile" {
		t.Fatalf("DEEDGLASS_API_KEY_FILE: %q", k)
	}
	_ = os.Chmod(kf, 0o644)
	if _, k := mcpServeTarget(in(other)); k != "" {
		t.Fatalf("a world-readable key file was used: %q", k)
	}
	// --purge forgets current.json when it points at the purged directory.
	if !setup.RemoveCurrent(home, dir) {
		t.Fatal("RemoveCurrent")
	}
}

func TestInvocation(t *testing.T) {
	npx := map[string]string{"npm_config_user_agent": "npm/11.6.0 node/v24.9.0 darwin arm64 workspaces/false", "npm_command": "exec", "npm_lifecycle_event": "npx"}
	e := &Env{Getenv: func(k string) string { return npx[k] }}
	if invocation(e) != "npx deedglass" || cmdLine(e, "deedglass actions list") != "npx deedglass actions list" {
		t.Fatal(invocation(e))
	}
	e = &Env{Getenv: func(string) string { return "" }}
	if invocation(e) != "deedglass" || cmdLine(e, "deedglass actions list") != "deedglass actions list" {
		t.Fatal(invocation(e))
	}
	// npm run scripts aren't npx.
	run := map[string]string{"npm_config_user_agent": "npm/11.6.0", "npm_command": "run-script", "npm_lifecycle_event": "test"}
	if invocation(&Env{Getenv: func(k string) string { return run[k] }}) != "deedglass" {
		t.Fatal("npm run counted as npx")
	}
}

// TestDBCommandsUseTheStack (QA-1): in a directory deedglass init set up, the commands that talk to
// the database directly use the stack's database, as actions list and doctor already did, instead of
// asking for DEEDGLASS_DATABASE_URL. The stack's Postgres port is closed here, so each one gets as
// far as connecting and says it can't reach the database.
func TestDBCommandsUseTheStack(t *testing.T) {
	dir, _ := initDir(t, "127.0.0.1:"+strconv.Itoa(closedPort(t)))
	env := map[string]string{"HOME": t.TempDir()}
	for _, args := range [][]string{
		{"keys", "list"}, {"org", "keys", "list"}, {"vault", "status"}, {"migrate", "status"},
		{"approve", "apr_01k82m4v9q5n3d7f8g2h6j0kxp", "--as", "you@localhost.test"},
	} {
		r := runIn(t, dir, env, args...)
		if strings.Contains(r.stderr, "DATABASE_URL isn't set") {
			t.Errorf("%s ignored the stack in %s:\n%s", strings.Join(args, " "), dir, r.stderr)
		}
		if r.code == 0 {
			t.Errorf("%s exited 0 with the stack's Postgres down:\n%s%s", strings.Join(args, " "), r.stdout, r.stderr)
		}
	}
}
