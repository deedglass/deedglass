package setup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	cpsql "github.com/deedglass/deedglass/deploy/postgres"
	demosql "github.com/deedglass/deedglass/examples/demo/sql"
	"github.com/deedglass/deedglass/examples/policies"
	"github.com/deedglass/deedglass/internal/shellguard"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// Image is the Postgres image init runs, pinned by digest (the same one deploy/ uses, ADR-024).
const Image = "postgres:18.6@sha256:5a5a84b19854a9ffaa54082c166ff4ec27473a361e496e5ea167f298f2da9722"

// The names init uses.
const (
	OrgName       = "Local"
	OrgSlug       = "local"
	DefaultOwner  = "you@localhost.test"
	AgentName     = "quickstart-agent"
	ConnectorName = "postgres"
	PolicyName    = "postgres-safe"
)

// Status of one step.
type Status string

const (
	Done    Status = "done"    // created something
	Changed Status = "changed" // fixed something that was there
	Skipped Status = "skipped" // already set up
	Failed  Status = "failed"
)

// StepResult is one line of init's output.
type StepResult struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status Status   `json:"status"`
	Detail string   `json:"detail"`
	Fix    string   `json:"fix,omitempty"`
	Lines  []string `json:"lines,omitempty"` // extra lines (text mode only; never a secret in --json)
	Secret string   `json:"-"`               // the key shown once (text mode only)
	Symbol string   `json:"symbol,omitempty"`
	TookMS int64    `json:"took_ms"`
}

// Runner is how init reaches Docker and this same binary (tests replace it).
type Runner interface {
	// Docker runs docker with args; stdin, when not nil, is written to it.
	Docker(ctx context.Context, stdin []byte, args ...string) (string, error)
	// Self runs this binary in dir with exactly env (no inherited DEEDGLASS_* variables).
	Self(ctx context.Context, env map[string]string, args ...string) (stdout, stderr string, err error)
	// StartServe starts `deedglass serve` detached, logging to logPath, and returns its pid.
	StartServe(ctx context.Context, env map[string]string, logPath string) (int, error)
	// Alive reports whether pid is a running process.
	Alive(pid int) bool
	// Stop asks pid to exit (SIGTERM) and waits up to 25 s.
	Stop(pid int) error
	// Healthy reports whether GET <server>/v1/health answers 200.
	Healthy(ctx context.Context, server string) bool
	// Open opens a URL in the browser.
	Open(url string) error
	// FreeDisk is the free space on the file system holding path.
	FreeDisk(path string) (uint64, error)
}

// Options are init's flags.
type Options struct {
	Dir          string
	Yes          bool
	Client       string // claude-code · claude-desktop · cursor · vscode · windsurf · codex · gemini · copilot · sdk-python · sdk-typescript · none
	Listen       string // default 127.0.0.1:8080
	PGPort       int    // default 55432
	DemoPort     int    // default PGPort + 10
	Project      string // default local-<8 hex of the directory>
	OwnerEmail   string
	NoSmoke      bool
	NoOpen       bool
	Smoke        bool // run the smoke actions even when nothing changed
	TTY          bool
	Runner       Runner
	Now          func() time.Time
	Progress     func(StepResult) // called as each step finishes (text mode streams)
	ResetSecrets bool             // the caller has the typed "reset"
}

// Result is init_result.
type Result struct {
	Object    string       `json:"object"`
	Steps     []StepResult `json:"steps"`
	Next      []string     `json:"next"`
	ElapsedMS int64        `json:"elapsed_ms"`
	Changed   bool         `json:"changed"`
	Dir       string       `json:"dir"`
}

func (o *Options) defaults() {
	if o.Listen == "" {
		o.Listen = "127.0.0.1:8080"
	}
	if o.PGPort == 0 {
		o.PGPort = 55432
	}
	if o.DemoPort == 0 {
		o.DemoPort = o.PGPort + 10
	}
	if o.OwnerEmail == "" {
		o.OwnerEmail = DefaultOwner
	}
	if o.Project == "" {
		abs, _ := filepath.Abs(o.Dir)
		h := sha256.Sum256([]byte(abs))
		o.Project = "local-" + hex.EncodeToString(h[:4])
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Client == "" {
		o.Client = "none"
	}
}

// Plan is what init will create and what is already there (shown before Continue? [Y/n]).
func Plan(o Options) (create, have []string) {
	o.defaults()
	st, _ := ReadState(o.Dir)
	stack, _ := ReadStack(o.Dir)
	if fi, err := os.Stat(filepath.Join(o.Dir, DirName)); err == nil && fi.IsDir() {
		have = append(have, ".deedglass/")
	} else {
		create = append(create, ".deedglass/ (keys, local secrets)")
	}
	if stack != nil {
		have = append(have, "Postgres 18.6 on 127.0.0.1:"+strconv.Itoa(stack.PGPort))
	} else {
		create = append(create, "Postgres 18.6 in Docker (127.0.0.1:"+strconv.Itoa(o.PGPort)+")")
	}
	if st.Org != "" {
		have = append(have, `org "Local"`, "agent "+AgentName, "policy "+PolicyName, "demo database")
	} else {
		create = append(create, `org "Local"`, "agent "+AgentName, "policy "+PolicyName+" (enforced)", "demo database")
	}
	return create, have
}

type run struct {
	o       Options
	r       Runner
	stack   *Stack
	state   State
	admin   string // the org admin key (never printed)
	agent   string // the agent key (shown once)
	steps   []StepResult
	changed bool
}

func (x *run) add(s StepResult) StepResult {
	if s.Status == Done || s.Status == Changed {
		x.changed = true
	}
	x.steps = append(x.steps, s)
	if x.o.Progress != nil {
		x.o.Progress(s)
	}
	return s
}

// ErrStep is returned when a step failed; the steps so far are in the Result.
var ErrStep = errors.New("setup: a step failed")

// Init runs every step in order and stops at the first failure.
func Init(ctx context.Context, o Options) (*Result, error) {
	o.defaults()
	t0 := o.Now()
	x := &run{o: o, r: o.Runner}
	res := &Result{Object: "init_result", Dir: o.Dir}
	steps := []func(context.Context) StepResult{
		x.preflight, x.stateDir, x.secrets, x.postgres, x.server, x.org, x.demoDB, x.agentStep, x.policy, x.client,
	}
	for _, f := range steps {
		ts := o.Now()
		r := f(ctx)
		r.TookMS = o.Now().Sub(ts).Milliseconds()
		s := x.add(r)
		if s.Status == Failed {
			res.Steps, res.ElapsedMS = x.steps, o.Now().Sub(t0).Milliseconds()
			return res, ErrStep
		}
	}
	if !o.NoSmoke && (x.changed || o.Smoke) {
		for _, s := range x.smoke(ctx) {
			x.add(s)
			if s.Status == Failed {
				res.Steps, res.ElapsedMS = x.steps, o.Now().Sub(t0).Milliseconds()
				return res, ErrStep
			}
		}
	}
	x.state.Object, x.state.Version, x.state.Stack = "init_state", 1, "docker"
	x.state.UpdatedAt = o.Now().UTC().Format(time.RFC3339)
	if x.changed {
		if err := writeJSON(Path(o.Dir, StateFile), x.state); err != nil {
			x.add(StepResult{ID: "state", Title: "State", Status: Failed, Detail: "couldn't write .deedglass/state.json: " + err.Error(), Fix: "Check that .deedglass is writable, then run deedglass init again."})
			return res, ErrStep
		}
	}
	if x.changed && !o.NoOpen && o.TTY {
		_ = x.r.Open(x.stack.Server() + "/app")
	}
	res.Steps, res.Changed = x.steps, x.changed
	res.Next = []string{
		"export DEEDGLASS_API_KEY=\"$(cat .deedglass/admin.key)\"",
		"deedglass actions list",
		"open " + x.stack.Server() + "/app",
	}
	if o.Client == "claude-code" || o.Client == "none" {
		res.Next = append([]string{ClaudeCodeLine(x.stack.Server())}, res.Next...)
	}
	res.ElapsedMS = o.Now().Sub(t0).Milliseconds()
	return res, nil
}

// ClaudeCodeLine is the one-liner that adds Deedglass to Claude Code (the key stays in its file).
func ClaudeCodeLine(server string) string {
	return `claude mcp add --transport http deedglass ` + server + `/mcp/native --header "Authorization: Bearer $(cat .deedglass/quickstart.key)"`
}

func fail(id, title, detail, fix string) StepResult {
	return StepResult{ID: id, Title: title, Status: Failed, Detail: detail, Fix: fix}
}

// api is a GET to the local server with key.
func (x *run) api(ctx context.Context, key, path string, out any) error {
	c := &deedglass.Client{BaseURL: x.stack.Server(), APIKey: key, UserAgent: "deedglass-init"}
	_, err := c.Do(ctx, "GET", path, nil, out)
	return err
}

// --- 1 · preflight ---

func (x *run) preflight(ctx context.Context) StepResult {
	const id, title = "preflight", "Preflight"
	if runtime.GOOS == "windows" {
		return fail(id, title, "Windows isn't supported by deedglass init yet.", "Use WSL 2, then run deedglass init there.")
	}
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ver, err := x.r.Docker(dctx, nil, "info", "--format", "{{.ServerVersion}}")
	if err != nil || strings.TrimSpace(ver) == "" {
		return fail(id, title, "Docker isn't running.", "Start Docker Desktop, then run deedglass init again.")
	}
	stack, _ := ReadStack(x.o.Dir)
	ours := stack != nil
	listen, pg := x.o.Listen, x.o.PGPort
	if ours {
		listen, pg = stack.Listen, stack.PGPort
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return fail(id, title, "--listen must be host:port, like 127.0.0.1:8080.", "Run deedglass init --listen 127.0.0.1:8090.")
	}
	if !portFree(listen) && !(ours && x.r.Healthy(ctx, "http://"+listen)) {
		return fail(id, title, "Port "+port+" is in use by another program.", "Stop it, or run deedglass init --listen 127.0.0.1:8090.")
	}
	pgAddr := "127.0.0.1:" + strconv.Itoa(pg)
	if !portFree(pgAddr) && !(ours && x.containerRunning(ctx, stack.Container)) {
		return fail(id, title, "Port "+strconv.Itoa(pg)+" is in use by another program.", "Stop it, or run deedglass init --pg-port 55440.")
	}
	demo := x.o.DemoPort
	if ours {
		demo = stack.DemoPort
	}
	if !portFree("127.0.0.1:"+strconv.Itoa(demo)) && !(ours && x.containerRunning(ctx, stack.DemoContainer)) {
		return fail(id, title, "Port "+strconv.Itoa(demo)+" is in use by another program.", "Stop it, or run deedglass init --pg-port 55440 (the demo database uses the port 10 above).")
	}
	if free, err := x.r.FreeDisk(x.o.Dir); err == nil && free < 2<<30 {
		return fail(id, title, fmt.Sprintf("Only %.1f GiB of disk is free; Deedglass needs 2 GiB.", float64(free)/(1<<30)), "Free some disk space, then run deedglass init again.")
	}
	return StepResult{ID: id, Title: "Docker", Status: Skipped,
		Detail: fmt.Sprintf("Docker %s · %s/%s · ports %s, %d %s", strings.TrimSpace(ver), runtime.GOOS, runtime.GOARCH, port, pg, map[bool]string{true: "ours", false: "free"}[ours])}
}

func portFree(addr string) bool {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// --- 2 · .deedglass/ ---

func (x *run) stateDir(context.Context) StepResult {
	const id, title = "state_dir", "Local state"
	d := filepath.Join(x.o.Dir, DirName)
	fi, err := os.Lstat(d)
	status, detail := Skipped, ".deedglass/ (0700)"
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(d, 0o700); err != nil {
			return fail(id, title, "couldn't create .deedglass: "+err.Error(), "Check that this directory is writable, then run deedglass init again.")
		}
		status, detail = Done, "created .deedglass/ (0700)"
	case err != nil:
		return fail(id, title, "couldn't read .deedglass: "+err.Error(), "Check this directory's permissions.")
	case fi.Mode()&fs.ModeSymlink != 0:
		return fail(id, title, ".deedglass is a symlink. Deedglass keeps keys there, so it must be a real directory.", "Remove the symlink, then run deedglass init again.")
	case !fi.IsDir():
		return fail(id, title, ".deedglass isn't a directory.", "Move it away, then run deedglass init again.")
	case fi.Mode().Perm() != 0o700:
		was := fi.Mode().Perm()
		if err := os.Chmod(d, 0o700); err != nil {
			return fail(id, title, "couldn't set .deedglass to 0700: "+err.Error(), "Run chmod 700 .deedglass.")
		}
		status, detail = Changed, fmt.Sprintf(".deedglass/ was %04o · fixed to 0700", was)
	}
	_ = os.Chmod(d, 0o700)
	if added := gitignore(x.o.Dir); added {
		detail += " · .gitignore gains .deedglass/"
		if status == Skipped {
			status = Changed
		}
	}
	return StepResult{ID: id, Title: title, Status: status, Detail: detail}
}

// gitignore adds .deedglass/ to .gitignore when dir is a git checkout and it isn't there yet.
func gitignore(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	p := filepath.Join(dir, ".gitignore")
	b, _ := os.ReadFile(p)
	for _, l := range strings.Split(string(b), "\n") {
		if t := strings.TrimSpace(l); t == ".deedglass/" || t == ".deedglass" || t == "/.deedglass/" || t == "/.deedglass" || t == ".deedglass/*" || t == "/.deedglass/*" {
			return false
		}
	}
	s := string(b)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	// Everything under .deedglass/ stays out of git except the badge file scan --badge writes (S47).
	s += "# Deedglass local keys and secrets (deedglass init); the badge file may be committed\n.deedglass/*\n!.deedglass/score.json\n"
	return os.WriteFile(p, []byte(s), 0o644) == nil
}

// --- 3 · secrets ---

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randB64() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func (x *run) secrets(ctx context.Context) StepResult {
	const id, title = "secrets", "Keys"
	var made []string
	sp := Path(x.o.Dir, SecretsFile)
	if _, err := os.Lstat(sp); errors.Is(err, fs.ErrNotExist) || x.o.ResetSecrets {
		b, _ := json.MarshalIndent(map[string]string{"object": "dev_secrets", "vault_key": randB64(), "api_key_pepper": randB64(),
			"created_at": x.o.Now().UTC().Format(time.RFC3339), "note": "Local development only. Deedglass never reads this file in prod."}, "", "  ")
		if err := writeSecret(sp, append(b, '\n'), x.o.ResetSecrets); err != nil {
			return fail(id, title, "couldn't write .deedglass/dev-secrets.json: "+err.Error(), "Check .deedglass, then run deedglass init again.")
		}
		made = append(made, "vault key", "pepper")
	}
	stack, err := ReadStack(x.o.Dir)
	if err != nil {
		return fail(id, title, err.Error(), "Delete .deedglass/stack.json and the container it names, then run deedglass init again.")
	}
	if stack == nil {
		stack = &Stack{Object: "init_stack", Version: 1, Project: x.o.Project, Container: "deedglass-" + x.o.Project + "-pg",
			Volume: "deedglass-" + x.o.Project + "-pgdata", Image: Image, PGPort: x.o.PGPort, Listen: x.o.Listen,
			DemoContainer: "deedglass-" + x.o.Project + "-demo", DemoVolume: "deedglass-" + x.o.Project + "-demodata", DemoPort: x.o.DemoPort,
			AdminPassword: randHex(16), MigratorPassword: randHex(16), AppPassword: randHex(16), DemoPassword: randHex(16), OwnerEmail: x.o.OwnerEmail}
		made = append(made, "database passwords")
	}
	keysDir := Path(x.o.Dir, KeysDir)
	if stack.SealKeyFile == "" || fileMissing(stack.SealKeyFile) {
		if existing := newestKey(keysDir); existing != "" {
			stack.SealKeyFile = existing
		} else {
			if _, _, err := x.r.Self(ctx, map[string]string{"DEEDGLASS_ENV": "dev"}, "keys", "gen", "--dir", keysDir); err != nil {
				return fail(id, title, "the seal key couldn't be created: "+firstLine(err), "Run deedglass keys gen --dir .deedglass/keys to see why.")
			}
			stack.SealKeyFile = newestKey(keysDir)
			made = append(made, "seal key "+shortKey(stack.SealKeyFile))
		}
	}
	_ = os.Chmod(keysDir, 0o700)
	if err := writeJSON(Path(x.o.Dir, StackFile), stack); err != nil {
		return fail(id, title, "couldn't write .deedglass/stack.json: "+err.Error(), "Check .deedglass, then run deedglass init again.")
	}
	x.stack = stack
	if len(made) == 0 {
		return StepResult{ID: id, Title: title, Status: Skipped, Detail: "seal key " + shortKey(stack.SealKeyFile) + " · vault key · pepper · .deedglass/ (0600)"}
	}
	return StepResult{ID: id, Title: title, Status: Done, Detail: strings.Join(made, " · ") + " · .deedglass/ (0600)"}
}

func fileMissing(p string) bool { _, err := os.Stat(p); return err != nil }

func newestKey(dir string) string {
	es, _ := os.ReadDir(dir)
	var ks []string
	for _, e := range es {
		if strings.HasPrefix(e.Name(), "sk_") && strings.HasSuffix(e.Name(), ".json") {
			ks = append(ks, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(ks)
	if len(ks) == 0 {
		return ""
	}
	abs, _ := filepath.Abs(ks[len(ks)-1])
	return abs
}

func shortKey(p string) string {
	b := strings.TrimSuffix(filepath.Base(p), ".json")
	if len(b) > 10 {
		return b[:9] + "…"
	}
	return b
}

func firstLine(err error) string {
	s := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	return s
}

// --- 4 · Postgres ---

func (x *run) containerRunning(ctx context.Context, name string) bool {
	out, err := x.r.Docker(ctx, nil, "inspect", "-f", "{{.State.Running}}", name)
	return err == nil && strings.TrimSpace(out) == "true"
}

func (x *run) psql(ctx context.Context, db, sql string) (string, error) {
	return x.psqlIn(ctx, x.stack.Container, db, sql)
}

func (x *run) psqlIn(ctx context.Context, container, db, sql string) (string, error) {
	return x.r.Docker(ctx, []byte(sql), "exec", "-i", container, "psql", "-X", "-q", "-At", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", db)
}

// ensureContainer starts (or creates) one Postgres container and waits until it answers.
func (x *run) ensureContainer(ctx context.Context, name, volume, envName, password string, port int) (Status, error) {
	status := Skipped
	_, inspectErr := x.r.Docker(ctx, nil, "inspect", "-f", "{{.State.Running}}", name)
	switch {
	case x.containerRunning(ctx, name):
	case inspectErr == nil:
		if _, err := x.r.Docker(ctx, nil, "start", name); err != nil {
			return Failed, fmt.Errorf("the container %s didn't start: %s", name, firstLine(err))
		}
		status = Changed
	default:
		envFile := Path(x.o.Dir, envName)
		if err := writeSecret(envFile, []byte("POSTGRES_PASSWORD="+password+"\n"), true); err != nil {
			return Failed, err
		}
		args := []string{"run", "-d", "--name", name, "--label", "com.deedglass.init=" + x.stack.Project,
			"-p", "127.0.0.1:" + strconv.Itoa(port) + ":5432", "--env-file", envFile,
			"-v", volume + ":/var/lib/postgresql", "--shm-size", "256m", x.stack.Image,
			"postgres", "-c", "wal_level=logical", "-c", "max_slot_wal_keep_size=2GB"}
		if _, err := x.r.Docker(ctx, nil, args...); err != nil {
			return Failed, fmt.Errorf("the container %s couldn't be created: %s", name, firstLine(err))
		}
		status = Done
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := x.r.Docker(ctx, nil, "exec", name, "pg_isready", "-q", "-h", "127.0.0.1", "-U", "postgres"); err == nil {
			if out, err := x.psqlIn(ctx, name, "postgres", "SELECT 1"); err == nil && strings.TrimSpace(out) == "1" {
				return status, nil
			}
		}
		if time.Now().After(deadline) {
			return Failed, fmt.Errorf("Postgres didn't become healthy in 90 s")
		}
		select {
		case <-ctx.Done():
			return Failed, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (x *run) postgres(ctx context.Context) StepResult {
	const id, title = "postgres", "Postgres"
	s := x.stack
	status, err := x.ensureContainer(ctx, s.Container, s.Volume, "pg.env", s.AdminPassword, s.PGPort)
	if err != nil {
		return fail(id, title, err.Error()+".", "Run deedglass doctor, or docker logs "+s.Container+".")
	}
	dstatus, err := x.ensureContainer(ctx, s.DemoContainer, s.DemoVolume, "demo-pg.env", s.DemoPassword, s.DemoPort)
	if err != nil {
		return fail(id, title, err.Error()+".", "Run docker logs "+s.DemoContainer+".")
	}
	if dstatus != Skipped && status == Skipped {
		status = dstatus
	}
	has, err := x.psql(ctx, "postgres", "SELECT count(*) FROM pg_catalog.pg_roles WHERE rolname = 'deedglass_migrator'")
	if err != nil {
		return fail(id, title, "couldn't read the roles: "+firstLine(err), "Run docker logs "+s.Container+".")
	}
	if strings.TrimSpace(has) == "0" {
		// Passwords travel on stdin (\set), never on a command line.
		sql := "\\set migrator_password '" + s.MigratorPassword + "'\n\\set app_password '" + s.AppPassword + "'\n" + cpsql.ControlPlaneRoles
		if _, err := x.psql(ctx, "postgres", sql); err != nil {
			return fail(id, title, "the control-plane roles couldn't be created: "+firstLine(err), "Delete the containers and volumes ("+s.Container+", "+s.Volume+"), then run deedglass init again.")
		}
		if status == Skipped {
			status = Changed
		}
	}
	if db, err := x.psqlIn(ctx, s.DemoContainer, "postgres", "SELECT count(*) FROM pg_catalog.pg_database WHERE datname = 'demo'"); err == nil && strings.TrimSpace(db) == "0" {
		if _, err := x.psqlIn(ctx, s.DemoContainer, "postgres", "CREATE DATABASE demo"); err != nil {
			return fail(id, title, "the demo database couldn't be created: "+firstLine(err), "Run docker logs "+s.DemoContainer+".")
		}
	}
	return StepResult{ID: id, Title: title, Status: status, Detail: fmt.Sprintf("127.0.0.1:%d · wal_level logical · demo database on 127.0.0.1:%d", s.PGPort, s.DemoPort)}
}

// --- 5 · migrations and the server ---

func (x *run) serverEnv() map[string]string {
	s := x.stack
	return map[string]string{"DEEDGLASS_ENV": "dev", "DEEDGLASS_DATABASE_URL": s.AppURL(), "DEEDGLASS_MIGRATE_DATABASE_URL": s.MigratorURL(),
		"DEEDGLASS_LISTEN": s.Listen, "DEEDGLASS_BASE_URL": s.Server(), "DEEDGLASS_SEAL_KEY_FILE": s.SealKeyFile, "DEEDGLASS_LOG_LEVEL": "info",
		// The demo database is on this machine (127.0.0.1): dev lets a connector reach it.
		"DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "1"}
}

func (x *run) clientEnv() map[string]string {
	return map[string]string{"DEEDGLASS_ENV": "dev", "DEEDGLASS_SERVER": x.stack.Server(), "DEEDGLASS_API_KEY": x.admin}
}

func (x *run) server(ctx context.Context) StepResult {
	const id, title = "server", "Deedglass"
	s := x.stack
	out, _, err := x.r.Self(ctx, x.serverEnv(), "--json", "migrate", "up")
	if err != nil {
		return fail(id, title, "migrations failed: "+firstLine(err), "Run deedglass doctor to see why.")
	}
	var up struct {
		Current int `json:"current"`
		Applied []struct {
			Version int `json:"version"`
		} `json:"applied"`
	}
	_ = json.Unmarshal([]byte(out), &up)
	if _, _, err := x.r.Self(ctx, x.serverEnv(), "migrate", "verify"); err != nil {
		return fail(id, title, "the applied migrations don't match this build: "+firstLine(err), "Run deedglass migrate verify to see which.")
	}
	status := Skipped
	if len(up.Applied) > 0 {
		status = Done
	}
	pidFile := Path(x.o.Dir, ServePID)
	pid := readPID(pidFile)
	if !(pid > 0 && x.r.Alive(pid) && x.r.Healthy(ctx, s.Server())) {
		if pid > 0 && x.r.Alive(pid) {
			_ = x.r.Stop(pid)
		}
		npid, err := x.r.StartServe(ctx, x.serverEnv(), Path(x.o.Dir, ServeLog))
		if err != nil {
			return fail(id, title, "deedglass serve didn't start: "+firstLine(err), "Read .deedglass/serve.log, then run deedglass init again.")
		}
		_ = writeSecret(pidFile, []byte(strconv.Itoa(npid)+"\n"), true)
		deadline := time.Now().Add(30 * time.Second)
		for !x.r.Healthy(ctx, s.Server()) {
			if !x.r.Alive(npid) || time.Now().After(deadline) {
				return fail(id, title, "deedglass serve didn't answer on "+s.Server()+".", "Read .deedglass/serve.log, then run deedglass init again.")
			}
			time.Sleep(200 * time.Millisecond)
		}
		if status == Skipped {
			status = Changed
		}
	}
	return StepResult{ID: id, Title: title, Status: status, Detail: fmt.Sprintf("%s · schema at %04d", s.Server(), up.Current)}
}

func readPID(p string) int {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// --- 6 · org and owner ---

func (x *run) org(ctx context.Context) StepResult {
	const id, title = "org", "Org"
	st, _ := ReadState(x.o.Dir)
	x.state = st
	key, _ := ReadSecretFile(x.o.Dir, AdminKeyFile)
	x.admin = key
	if key != "" && st.Org != "" {
		if _, _, err := x.r.Self(ctx, x.clientEnv(), "--json", "connector", "list"); err == nil {
			return StepResult{ID: id, Title: title, Status: Skipped, Detail: "Local · owner " + x.stack.OwnerEmail}
		}
	}
	env := x.serverEnv()
	out, errOut, err := x.r.Self(ctx, env, "--json", "org", "create", "--name", OrgName, "--slug", OrgSlug, "--owner-email", x.stack.OwnerEmail, "--admin-key")
	var created struct {
		Org struct {
			ID string `json:"id"`
		} `json:"org"`
		AdminKey struct {
			Secret string `json:"secret"`
		} `json:"admin_key"`
	}
	switch {
	case err == nil && json.Unmarshal([]byte(out), &created) == nil && created.AdminKey.Secret != "":
		x.state.Org, x.admin = created.Org.ID, created.AdminKey.Secret
	case strings.Contains(out+errOut, "slug") || strings.Contains(out+errOut, "already"):
		// The org exists (an earlier run stopped before saving the key): a new admin key for it.
		kout, _, kerr := x.r.Self(ctx, env, "--json", "org", "keys", "create", "--org", OrgSlug, "--name", "init-admin", "--mode", "test")
		var k struct {
			Secret string `json:"secret"`
			Org    string `json:"org"`
		}
		if kerr != nil || json.Unmarshal([]byte(kout), &k) != nil || k.Secret == "" {
			return fail(id, title, "the org exists but no admin key could be made", "Run deedglass org keys create --org local --name init-admin to see why.")
		}
		x.admin = k.Secret
	default:
		return fail(id, title, "the org couldn't be created: "+strings.TrimSpace(firstNonEmpty(errOut, out)), "Run deedglass doctor, then deedglass init again.")
	}
	if err := writeSecret(Path(x.o.Dir, AdminKeyFile), []byte(x.admin+"\n"), true); err != nil {
		return fail(id, title, "couldn't save .deedglass/admin.key: "+err.Error(), "Check .deedglass, then run deedglass init again.")
	}
	if x.state.Org == "" {
		var w struct {
			Org struct {
				ID string `json:"id"`
			} `json:"org"`
		}
		if err := x.api(ctx, x.admin, "/v1/whoami", &w); err == nil {
			x.state.Org = w.Org.ID
		}
	}
	return StepResult{ID: id, Title: title, Status: Done, Detail: "Local · owner " + x.stack.OwnerEmail + " · admin key in .deedglass/admin.key"}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// --- 7 · the demo database and its connector ---

// ConnectorYAML is the demo connector, on the local demo database (S15's, with the same rules):
// the file init writes to .deedglass/connector-postgres.yaml and creates the connector from.
const ConnectorYAML = `type: postgres
name: postgres
description: The Acme demo orders database (deedglass init, local and throwaway).
config:
  mode: sql
  allowed_schemas: [public]
  writable_tables: [public.orders, public.refunds]
  row_limit: 500
  max_rows_affected: 1000
  statement_timeout_ms: 5000
  lock_timeout_ms: 2000
  witness:
    enabled: true
    watched_tables: [public.orders, public.refunds, public.customers]
    sensitive_columns: [public.customers.email]
  functions:
    - name: public.ship_order
      args: [{name: p_order_id, type: bigint}]
      side_effect: write
      description: Mark a paid order shipped.
  throwaway: true
`

// ConnectorConfig is ConnectorYAML's config block as JSON, as connector create sends it: mcp serve
// lists the quickstart's tools from it before init (PRD-03), so the two lists can't drift apart.
func ConnectorConfig() (json.RawMessage, error) {
	var doc struct {
		Config map[string]any `yaml:"config"`
	}
	if err := yaml.Unmarshal([]byte(ConnectorYAML), &doc); err != nil {
		return nil, err
	}
	return json.Marshal(doc.Config)
}

// demoRoles replaces 03_roles.sql: the owner role without a login (no password to keep).
const demoRoles = `\set ON_ERROR_STOP on
BEGIN;
DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'acme_owner') THEN CREATE ROLE acme_owner NOLOGIN; END IF; END $$;
ALTER SCHEMA public OWNER TO acme_owner;
ALTER TABLE public.customers OWNER TO acme_owner;
ALTER TABLE public.products OWNER TO acme_owner;
ALTER TABLE public.orders OWNER TO acme_owner;
ALTER TABLE public.order_items OWNER TO acme_owner;
ALTER TABLE public.refunds OWNER TO acme_owner;
COMMIT;
`

func (x *run) demoDB(ctx context.Context) StepResult {
	const id, title = "demo_db", "Demo database"
	status := Skipped
	dc := x.stack.DemoContainer
	has, err := x.psqlIn(ctx, dc, "demo", "SELECT pg_catalog.to_regclass('public.orders') IS NOT NULL")
	if err != nil {
		return fail(id, title, "couldn't read the demo database: "+firstLine(err), "Run docker logs "+x.stack.DemoContainer+".")
	}
	if strings.TrimSpace(has) != "t" {
		for _, f := range []string{"01_schema.sql", "02_seed.sql", "", "20_functions.sql"} {
			sql := demoRoles
			if f != "" {
				b, err := demosql.FS.ReadFile(f)
				if err != nil {
					return fail(id, title, err.Error(), "Rebuild deedglass.")
				}
				sql = string(b)
			}
			if _, err := x.psqlIn(ctx, dc, "demo", sql); err != nil {
				return fail(id, title, "the demo data didn't load ("+firstNonEmpty(f, "roles")+"): "+firstLine(err), "Delete the container and volume, then run deedglass init again.")
			}
		}
		status = Done
	}
	cenv := x.clientEnv()
	if _, _, err := x.r.Self(ctx, cenv, "--json", "connector", "show", ConnectorName); err != nil {
		p := Path(x.o.Dir, "connector-postgres.yaml")
		if err := os.WriteFile(p, []byte(ConnectorYAML), 0o600); err != nil {
			return fail(id, title, err.Error(), "Check .deedglass.")
		}
		if _, e2, err := x.r.Self(ctx, cenv, "connector", "create", "--config", p); err != nil {
			return fail(id, title, "the postgres connector couldn't be created: "+strings.TrimSpace(e2), "Run deedglass connector create --config .deedglass/connector-postgres.yaml to see why.")
		}
		status = Done
	}
	ienv := map[string]string{"DEEDGLASS_INIT_DSN": x.stack.DemoURL()}
	for k, v := range cenv {
		ienv[k] = v
	}
	out, e2, err := x.r.Self(ctx, ienv, "connector", "postgres", "init", "--connector", ConnectorName, "--apply", "--plan-dir", Path(x.o.Dir, "plans"))
	if err != nil {
		return fail(id, title, "deedglass connector postgres init failed: "+firstNonEmpty(lastLine(e2), lastLine(out)), "Run deedglass connector postgres check --connector postgres.")
	}
	if !strings.Contains(out, "Nothing to change") && status == Skipped {
		status = Changed
	}
	// Record the connector's health now (doctor reads it): its checks only read the database.
	if status != Skipped {
		_, _, _ = x.r.Self(ctx, cenv, "connector", "test", ConnectorName)
	}
	// The decoder starts on its own once the connector is ready; wait for it to run.
	running := false
	for i := 0; i < 60 && !running; i++ {
		o, _, err := x.r.Self(ctx, cenv, "--json", "decoder", "status", "--connector", ConnectorName)
		running = err == nil && strings.Contains(o, `"running"`)
		if !running {
			time.Sleep(500 * time.Millisecond)
		}
	}
	n, _ := x.psqlIn(ctx, dc, "demo", "SELECT count(*) FROM public.orders")
	detail := fmt.Sprintf("%s orders · connector %s ready for Proven checks", thousands(strings.TrimSpace(n)), ConnectorName)
	if !running {
		detail = fmt.Sprintf("%s orders · connector %s · decoder not running yet (deedglass decoder status says why)", thousands(strings.TrimSpace(n)), ConnectorName)
	}
	x.state.Connector = ConnectorName
	return StepResult{ID: id, Title: title, Status: status, Detail: detail}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func thousands(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil {
		return s
	}
	out := strconv.Itoa(n)
	if len(out) > 3 {
		out = out[:len(out)-3] + "," + out[len(out)-3:]
	}
	return out
}

// --- 8 · the agent and its key ---

func (x *run) agentStep(ctx context.Context) StepResult {
	const id, title = "agent", "Agent"
	cenv := x.clientEnv()
	status := Skipped
	out, _, err := x.r.Self(ctx, cenv, "--json", "agent", "show", AgentName)
	if err != nil {
		if out, _, err = x.r.Self(ctx, cenv, "--json", "agent", "create", AgentName, "--description", "The quickstart agent (deedglass init)."); err != nil {
			return fail(id, title, "the agent couldn't be created", "Run deedglass agent create quickstart-agent to see why.")
		}
		status = Done
	}
	var a struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(out), &a)
	x.state.Agent = a.ID
	existing, _ := ReadSecretFile(x.o.Dir, AgentKeyFile)
	if existing != "" {
		x.agent = existing
		if status == Done {
			return StepResult{ID: id, Title: title, Status: status, Symbol: "○",
				Detail: ".deedglass/quickstart.key already exists, so no new key was created. Delete it to get a new key."}
		}
		return StepResult{ID: id, Title: title, Status: status, Detail: AgentName + " · key in .deedglass/quickstart.key"}
	}
	kout, _, err := x.r.Self(ctx, cenv, "--json", "agent", "keys", "create", AgentName, "--mode", "test")
	var k struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err != nil || json.Unmarshal([]byte(kout), &k) != nil || k.Secret == "" {
		return fail(id, title, "the agent's key couldn't be created", "Run deedglass agent keys create quickstart-agent to see why.")
	}
	if err := writeSecret(Path(x.o.Dir, AgentKeyFile), []byte(k.Secret+"\n"), false); err != nil {
		return fail(id, title, "couldn't save .deedglass/quickstart.key: "+err.Error(), "Check .deedglass, then run deedglass init again.")
	}
	x.agent, x.state.KeyID = k.Secret, k.ID
	return StepResult{ID: id, Title: title, Status: Done, Detail: AgentName + " · key saved to .deedglass/quickstart.key (shown once below)", Secret: k.Secret}
}

// --- 9 · the policy ---

func (x *run) policy(ctx context.Context) StepResult {
	const id, title = "policy", "Policies"
	cenv := x.clientEnv()
	var hashes map[string]string
	_ = json.Unmarshal(policies.Hashes(), &hashes)
	want := hashes[PolicyName]
	var active struct {
		Enforce *struct {
			Name        string `json:"name"`
			Version     int    `json:"version"`
			VersionID   string `json:"version_id"`
			VersionHash string `json:"version_hash"`
		} `json:"enforce"`
	}
	_ = x.api(ctx, x.admin, "/v1/policies/active", &active)
	if e := active.Enforce; e != nil && e.Name == PolicyName && (want == "" || e.VersionHash == want) { //deedglass:allow-compare a policy version's public digest
		x.state.Policies = []string{e.VersionID}
		return StepResult{ID: id, Title: title, Status: Skipped, Detail: fmt.Sprintf("%s v%d · enforce", PolicyName, e.Version)}
	}
	src, err := policies.Source(PolicyName)
	if err != nil {
		return fail(id, title, err.Error(), "Rebuild deedglass.")
	}
	p := Path(x.o.Dir, PolicyName+".yaml")
	if err := os.WriteFile(p, src, 0o600); err != nil {
		return fail(id, title, err.Error(), "Check .deedglass.")
	}
	pout, perr, err := x.r.Self(ctx, cenv, "--json", "policy", "push", p)
	var pushed struct {
		Version struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		} `json:"version"`
	}
	if err != nil || json.Unmarshal([]byte(pout), &pushed) != nil || pushed.Version.Version == 0 {
		return fail(id, title, "postgres-safe couldn't be pushed: "+lastLine(perr), "Run deedglass policy push "+p+" to see why.")
	}
	if _, aerr, err := x.r.Self(ctx, cenv, "policy", "activate", PolicyName, "--version", strconv.Itoa(pushed.Version.Version), "--mode", "enforce",
		"--reason", "deedglass init: the quickstart's starter policy", "--yes"); err != nil {
		return fail(id, title, "postgres-safe couldn't be activated: "+lastLine(aerr), "Run deedglass policy activate postgres-safe --mode enforce to see why.")
	}
	x.state.Policies = []string{pushed.Version.ID}
	return StepResult{ID: id, Title: title, Status: Done, Detail: fmt.Sprintf("%s v%d · enforce (replaces default-deny, the onboarding policy)", PolicyName, pushed.Version.Version)}
}

// --- 10 · the client snippet ---

func (x *run) client(ctx context.Context) StepResult {
	const id, title = "client", "Client"
	switch x.o.Client {
	case "none":
		return StepResult{ID: id, Title: title, Status: Skipped, Detail: "none chosen · the Claude Code line is under Next"}
	case "claude-code":
		return StepResult{ID: id, Title: title, Status: Skipped, Detail: "Claude Code · run the line under Next"}
	case "sdk-python":
		return StepResult{ID: id, Title: title, Status: Skipped, Detail: "Python",
			Lines: []string{"pip install \"deedglass @ git+https://github.com/deedglass/deedglass#subdirectory=sdk/python\"", "export DEEDGLASS_API_KEY=\"$(cat .deedglass/quickstart.key)\" DEEDGLASS_SERVER=" + x.stack.Server()}}
	case "sdk-typescript":
		return StepResult{ID: id, Title: title, Status: Skipped, Detail: "TypeScript",
			Lines: []string{"npm install @deedglass/sdk", "export DEEDGLASS_API_KEY=\"$(cat .deedglass/quickstart.key)\" DEEDGLASS_SERVER=" + x.stack.Server()}}
	case "claude-desktop", "cursor", "vscode", "windsurf":
		out, _, err := x.r.Self(ctx, map[string]string{"DEEDGLASS_SERVER": x.stack.Server()}, "mcp", "snippet", "--client", x.o.Client, "--transport", "http", "--connector", "native")
		if err != nil {
			return fail(id, title, "the snippet couldn't be made", "Run deedglass mcp snippet --client "+x.o.Client+" --transport http.")
		}
		return StepResult{ID: id, Title: title, Status: Skipped, Detail: x.o.Client + " · paste this into its MCP config (init never writes it for you)",
			Lines: strings.Split(strings.TrimRight(out, "\n"), "\n")}
	case "codex", "gemini", "copilot":
		return AgentClientStep(x.o.Client)
	}
	return fail(id, title, "unknown --client "+x.o.Client, "Use claude-code, claude-desktop, cursor, vscode, windsurf, codex, gemini, copilot, sdk-python, sdk-typescript or none.")
}

// agentMCPLines is how each coding agent adds the Deedglass MCP server (the same server object as
// every install link, internal/installlinks), from its own docs (checked 2026-10-20):
// learn.chatgpt.com/docs/extend/mcp (codex mcp add), geminicli.com/docs/tools/mcp-server
// (settings.json mcpServers), docs.github.com …/copilot-cli/customize-copilot/add-mcp-servers
// (copilot mcp add). The key stays in .deedglass/quickstart.key, which mcp serve reads.
var agentMCPLines = map[string][]string{
	"codex":   {"codex mcp add deedglass -- npx -y deedglass mcp serve"},
	"gemini":  {`In .gemini/settings.json: "mcpServers": {"deedglass": {"command": "npx", "args": ["-y", "deedglass", "mcp", "serve"]}}`},
	"copilot": {"copilot mcp add deedglass -- npx -y deedglass mcp serve"},
}

// AgentClientStep prints the MCP line for a coding agent and its optional shell guard hook config
// (TY-08), both from tables; init never writes either file.
func AgentClientStep(client string) StepResult {
	const id, title = "client", "Client"
	c, _ := shellguard.LookupClient(client)
	lines := append([]string{}, agentMCPLines[client]...)
	lines = append(lines, "Optional shell guard, in "+c.File+":")
	lines = append(lines, strings.Split(strings.TrimRight(c.Config, "\n"), "\n")...)
	return StepResult{ID: id, Title: title, Status: Skipped, Detail: c.Title + " · run or paste these (init never writes them for you)", Lines: lines}
}
