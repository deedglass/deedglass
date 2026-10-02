//go:build attack

package harness

//deedglass:allow-sql-build test harness: database and table names are generated here or fixed, and quoted with pgx.Identifier.Sanitize; every value is a $n parameter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/store/storetest"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// Env is one booted attack environment: the server process, its control-plane database, the
// target template database, the upstreams and the orgs. One Env serves every case of a run.
type Env struct {
	Root      string // the repo root
	Dir       string // .tmp/attack/run-<time>: the binary, .deedglass/, the hosts file
	Bin       string
	AdminURL  string // superuser on the throwaway cluster, database postgres
	CP        *storetest.Env
	Server    string
	Logs      *LogCapture
	Bodies    *BodyLog
	Up        *Upstreams
	Orgs      map[string]*Org
	Sentinel  string
	Target    *TargetTemplate
	PGVersion string
	BootMS    int64

	proc      *exec.Cmd
	hostsPath string
	hostsMu   sync.Mutex
	hosts     map[string][]string
	cleanups  []func()
}

// Org is one org with its admin key, agents and connectors.
type Org struct {
	ID       string
	Slug     string
	AdminKey string
	Admin    *Client
	Agents   map[string]*Agent
	Conns    map[string]string // name → con_ id
	Policy   string            // pol_ id of the enforced policy
	PolicyV  string            // pv_ id
	Session  *Client           // an owner's session (globex, for the isolation sweep)
	Deeds    []string          // some of its deed ids (fixtures)
	Actions  []string          // some of its action ids (fixtures)
}

// Agent is one agent with one key.
type Agent struct {
	ID, Name, Key, KeyID string
	API                  *Client
	Signer               *deedglass.IntentSigner
	IntentKID            string
}

// bootTB adapts the run's lifetime to storetest, which wants a testing.TB: cleanups run when the
// Env closes, after every test.
type bootTB struct {
	testing.TB
	env *Env
}

func (b bootTB) Helper()                      {}
func (b bootTB) Cleanup(f func())             { b.env.cleanups = append(b.env.cleanups, f) }
func (b bootTB) Fatal(args ...any)            { panic(fmt.Sprint(args...)) }
func (b bootTB) Fatalf(f string, args ...any) { panic(fmt.Sprintf(f, args...)) }
func (b bootTB) Errorf(f string, args ...any) { fmt.Fprintf(os.Stderr, "attack: "+f+"\n", args...) }
func (b bootTB) Logf(f string, args ...any)   { fmt.Fprintf(os.Stderr, "attack: "+f+"\n", args...) }

// Fixed throwaway secrets for the server process (never real ones; the harness never reads .env).
func harnessKey(label string) string {
	h := sha256.Sum256([]byte("deedglass attack harness · " + label))
	return base64.StdEncoding.EncodeToString(h[:])
}

// Boot starts everything. It refuses to run outside a throwaway cluster.
func Boot() (env *Env, err error) {
	start := time.Now()
	if os.Getenv("DEEDGLASS_IT_THROWAWAY") != "1" || os.Getenv("DEEDGLASS_IT_ADMIN_URL") == "" {
		return nil, fmt.Errorf("refusing: the attack suite runs only on a throwaway cluster (make attack sets DEEDGLASS_IT_THROWAWAY=1 and DEEDGLASS_IT_ADMIN_URL)")
	}
	e := &Env{AdminURL: os.Getenv("DEEDGLASS_IT_ADMIN_URL"), Logs: &LogCapture{}, Bodies: &BodyLog{}, Orgs: map[string]*Org{},
		Sentinel: NewSentinel(), hosts: map[string][]string{}}
	defer func() {
		if p := recover(); p != nil {
			e.Close()
			env, err = nil, fmt.Errorf("attack boot: %v", p)
		}
	}()
	e.Root = repoRoot()
	e.Dir = filepath.Join(e.Root, ".tmp", "attack", "run-"+time.Now().UTC().Format("20060102T150405"))
	must(os.MkdirAll(e.Dir, 0o700))
	e.Bin = filepath.Join(e.Dir, "deedglass")
	build := exec.Command("go", "build", "-tags", "attackhooks", "-o", e.Bin, "./cmd/deedglass")
	build.Dir = e.Root
	if out, err := build.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("go build: %v\n%s", err, out))
	}
	// The control plane: a fresh, migrated database on the throwaway cluster (S02's helper).
	e.CP = storetest.New(bootTB{env: e}, storetest.Options{})
	e.PGVersion = e.adminScalar("SELECT current_setting('server_version')")
	// The upstreams and the names the server resolves through the hosts file.
	e.hostsPath = filepath.Join(e.Dir, "hosts.json")
	e.SetHost("metadata.attack.test", "169.254.169.254")
	e.SetHost("other.attack.test", "127.0.0.1")
	e.SetHost("rebind.attack.test", "93.184.216.34")
	e.Up = startUpstreams("other.attack.test")
	e.cleanups = append(e.cleanups, e.Up.Close)
	e.startServer()
	e.setupOrgs()
	e.Target = e.setupTarget()
	e.BootMS = time.Since(start).Milliseconds()
	return e, nil
}

// Close stops the server and drops what the run created.
func (e *Env) Close() {
	if e.proc != nil && e.proc.Process != nil {
		_ = e.proc.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = e.proc.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = e.proc.Process.Kill()
			<-done
		}
		e.proc = nil
	}
	for i := len(e.cleanups) - 1; i >= 0; i-- {
		func() {
			defer func() { _ = recover() }()
			e.cleanups[i]()
		}()
	}
	e.cleanups = nil
	// The run directory holds the binary, the throwaway DSNs init wrote and a dev seal key.
	if e.Dir != "" && os.Getenv("DEEDGLASS_IT_KEEP") != "1" {
		_ = os.RemoveAll(e.Dir)
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func repoRoot() string {
	dir, err := os.Getwd()
	must(err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("no go.mod above the working directory")
		}
		dir = parent
	}
}

// SetHost makes the server resolve host to addrs (the attackhooks resolver re-reads the file on
// every lookup, so this takes effect at once).
func (e *Env) SetHost(host string, addrs ...string) {
	e.hostsMu.Lock()
	defer e.hostsMu.Unlock()
	e.hosts[host] = addrs
	b, err := json.Marshal(e.hosts)
	must(err)
	tmp := e.hostsPath + ".tmp"
	must(os.WriteFile(tmp, b, 0o600))
	must(os.Rename(tmp, e.hostsPath))
}

// serverEnv is the server's whole environment: nothing is inherited from the shell, so no
// developer setting (and nothing from any .env) reaches the run.
func (e *Env) serverEnv(extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + e.Dir, "TMPDIR=" + os.TempDir(), "LANG=en_US.UTF-8",
		"DEEDGLASS_ENV=dev",
		"DEEDGLASS_DATABASE_URL=" + e.CP.AppURL,
		"DEEDGLASS_VAULT_KEY=" + harnessKey("vault"),
		"DEEDGLASS_API_KEY_PEPPER=" + harnessKey("pepper"),
		"DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1",
		"DEEDGLASS_LOG_LEVEL=debug",
		"DEEDGLASS_NO_BANNER=1",
		"NO_COLOR=1",
	}
	return append(env, extra...)
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func (e *Env) startServer() {
	port := freePort()
	e.Server = fmt.Sprintf("http://127.0.0.1:%d", port)
	cmd := exec.Command(e.Bin, "serve")
	cmd.Dir = e.Dir
	cmd.Env = e.serverEnv("DEEDGLASS_LISTEN=127.0.0.1:"+fmt.Sprint(port), "DEEDGLASS_BASE_URL="+e.Server,
		"DEEDGLASS_DEV_ATTACK_HOSTS="+e.hostsPath)
	cmd.Stdout, cmd.Stderr = e.Logs, e.Logs
	must(cmd.Start())
	e.proc = cmd
	cl := newClient(e.Server, "", nil)
	deadline := time.Now().Add(20 * time.Second)
	for {
		ok := func() (ok bool) {
			defer func() {
				if recover() != nil {
					ok = false
				}
			}()
			return cl.Do("GET", "/v1/health", nil).Status == 200
		}()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			panic("the server didn't become healthy in 20 s:\n" + tail(e.Logs.Bytes(), 40))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func tail(b []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// CLI runs the deedglass binary (DB-direct operator commands, connector postgres init) with the
// run's environment plus extra, and returns stdout.
func (e *Env) CLI(extra []string, args ...string) []byte {
	cmd := exec.Command(e.Bin, args...)
	cmd.Dir = e.Dir
	cmd.Env = e.serverEnv(extra...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		panic(fmt.Sprintf("deedglass %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, out.String(), errb.String()))
	}
	// The CLI's output is swept like the server's.
	_, _ = e.Logs.Write(errb.Bytes())
	e.Bodies.add(out.Bytes())
	return out.Bytes()
}

// ---- admin SQL on the throwaway cluster ----

func (e *Env) adminConn(db string) *pgx.Conn {
	u, err := url.Parse(e.AdminURL)
	must(err)
	u.Path = "/" + db
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, u.String())
	must(err)
	return c
}

func (e *Env) adminScalar(q string, args ...any) string {
	c := e.adminConn("postgres")
	defer c.Close(context.Background())
	var s string
	must(c.QueryRow(context.Background(), q, args...).Scan(&s))
	return s
}

// CPQuery runs a query on the control-plane database as the superuser (fixtures and sweeps).
func (e *Env) CPQuery(q string, args ...any) [][]string {
	c := e.adminConn(e.CP.Name)
	defer c.Close(context.Background())
	rows, err := c.Query(context.Background(), q, args...)
	must(err)
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		vals, err := rows.Values()
		must(err)
		row := make([]string, len(vals))
		for i, v := range vals {
			row[i] = fmt.Sprint(v)
		}
		out = append(out, row)
	}
	must(rows.Err())
	return out
}

// ---- orgs, agents, policies, connectors ----

var reToken = regexp.MustCompile(`[#?&]t=([A-Za-z0-9_.~-]+)`)

func (e *Env) createOrg(name, slug string) *Org {
	out := e.CLI(nil, "org", "create", "--name", name, "--slug", slug, "--owner-email", "owner@"+slug+".example.test", "--admin-key", "--json")
	var res struct {
		Org struct {
			ID string `json:"id"`
		} `json:"org"`
		Link     string `json:"link"`
		AdminKey struct {
			Secret string `json:"secret"`
		} `json:"admin_key"`
		Deed string `json:"deed"`
	}
	must(json.Unmarshal(out, &res))
	if res.AdminKey.Secret == "" {
		panic("org create returned no admin key")
	}
	o := &Org{ID: res.Org.ID, Slug: slug, AdminKey: res.AdminKey.Secret, Agents: map[string]*Agent{}, Conns: map[string]string{}}
	o.Admin = newClient(e.Server, o.AdminKey, e.Bodies)
	if res.Deed != "" {
		o.Deeds = append(o.Deeds, res.Deed)
	}
	// An owner session, from the printed sign-in link (dev prints it).
	if m := reToken.FindStringSubmatch(res.Link); m != nil {
		jar, _ := cookiejar.New(nil)
		sc := newClient(e.Server, "", e.Bodies).WithSession(jar, nil)
		r := sc.Do("POST", "/v1/auth/callback", map[string]string{"token": m[1]})
		if r.Status == 200 {
			base, _ := url.Parse(e.Server)
			csrf := func() string {
				for _, c := range jar.Cookies(base) {
					if strings.Contains(c.Name, "csrf") {
						return c.Value
					}
				}
				return ""
			}
			o.Session = sc.WithSession(jar, csrf)
		}
	}
	e.Orgs[slug] = o
	return o
}

// ok panics unless r has one of the statuses.
func ok(r Response, what string, statuses ...int) Response {
	for _, s := range statuses {
		if r.Status == s {
			return r
		}
	}
	panic(fmt.Sprintf("%s: HTTP %d: %s", what, r.Status, r.Body))
}

// AddAgent creates an agent and one key.
func (e *Env) AddAgent(o *Org, name string, budgets map[string]any) *Agent {
	body := map[string]any{"name": name, "description": "Attack suite agent."}
	if budgets != nil {
		body["budgets"] = budgets
	}
	r := ok(o.Admin.Do("POST", "/v1/agents", body), "create agent "+name, 201)
	m := r.Map()
	a := &Agent{ID: Str(m, "agent", "id"), Name: name}
	if a.ID == "" {
		a.ID = Str(m, "id")
	}
	kr := ok(o.Admin.Do("POST", "/v1/agents/"+a.ID+"/keys", map[string]any{"name": "attack", "mode": "test"}), "create key", 201)
	km := kr.Map()
	a.Key, a.KeyID = Str(km, "secret"), Str(km, "id")
	if a.Key == "" {
		a.Key, a.KeyID = Str(km, "key", "secret"), Str(km, "key", "id")
	}
	if a.Key == "" {
		panic("no key secret in " + string(kr.Body))
	}
	a.API = newClient(e.Server, a.Key, e.Bodies)
	o.Agents[name] = a
	return a
}

// PushPolicy pushes a policy source and enforces it.
func (e *Env) PushPolicy(o *Org, src []byte) {
	r := ok(o.Admin.Do("POST", "/v1/policies", map[string]string{"source": string(src)}), "push policy", 201)
	m := r.Map()
	o.Policy, o.PolicyV = Str(m, "id"), Str(m, "version", "id")
	ok(o.Admin.Do("POST", "/v1/policies/"+o.Policy+"/versions/1/activate", map[string]any{"mode": "enforce", "reason": "The attack suite's policy."}),
		"activate policy", 200, 201)
	// Wait until the Gate sees it (the registry swaps on NOTIFY).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		act := o.Admin.Do("GET", "/v1/policies/active", nil).Map()
		if Str(act, "enforce", "version", "id") == o.PolicyV || Str(act, "enforce", "id") == o.PolicyV || strings.Contains(fmt.Sprint(act), o.PolicyV) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// AddConnector creates a connector and, when secret is set, stores its credential.
func (e *Env) AddConnector(o *Org, typ, name string, cfg map[string]any, kind string, secret map[string]string) Response {
	r := o.Admin.Do("POST", "/v1/connectors", map[string]any{"type": typ, "name": name, "description": "Attack suite connector.", "config": cfg})
	if r.Status != 201 {
		return r
	}
	id := Str(r.Map(), "id")
	o.Conns[name] = id
	if secret != nil {
		ok(o.Admin.Do("PUT", "/v1/connectors/"+id+"/credentials", map[string]any{"kind": kind, "secret": secret}), "credentials "+name, 200, 201)
	}
	return r
}

func (e *Env) setupOrgs() {
	acme := e.createOrg("Acme", "acme")
	globex := e.createOrg("Globex", "globex")
	src, err := os.ReadFile(filepath.Join(e.Root, "attack", "fixtures", "policies", "attack-baseline.yaml"))
	must(err)
	for _, n := range []string{"attacker", "permissive-bot", "suspend-bot", "revoke-bot", "intent-bot", "witness-bot"} {
		e.AddAgent(acme, n, nil)
	}
	e.AddAgent(acme, "burst-bot", map[string]any{"calls_per_minute": 60})
	e.PushPolicy(acme, src)
	s := e.Sentinel
	ordersURL := e.Up.Orders.URL
	orders := map[string]any{"base_url": ordersURL, "auth": map[string]any{"kind": "bearer"}, "allowed_methods": []string{"GET"},
		"allowed_path_prefixes": []string{"/v1/orders"}, "tools": []any{map[string]any{"name": "get", "method": "GET", "path": "/v1/orders/{id}",
			"args": map[string]any{"id": map[string]any{"in": "path", "type": "string", "pattern": "^[0-9]{1,12}$", "required": true}}}}}
	ok(e.AddConnector(acme, "rest", "orders", orders, "bearer", map[string]string{"token": s}), "orders", 201)
	// K04's MCP server as an mcp_http row (S18 carry-over: its credential is in the vault).
	ok(e.AddConnector(acme, "mcp_http", "fs", map[string]any{"url": e.Up.MCP.URL + "/mcp", "auth": map[string]any{"kind": "bearer"},
		"timeout_ms": 5000, "tools_ttl_ms": 100}, "bearer", map[string]string{"token": s}), "fs", 201)
	// Globex: the isolation org, populated with one of everything.
	g := e.AddAgent(globex, "globex-bot", nil)
	e.PushPolicy(globex, []byte(strings.Replace(strings.Replace(string(src), "name: attack-baseline", "name: globex-policy", 1),
		"agent: permissive-bot", "agent: nobody", 1)))
	ok(e.AddConnector(globex, "rest", "orders", orders, "bearer", map[string]string{"token": s}), "globex orders", 201)
	r := g.API.Do("POST", "/v1/actions", map[string]any{"tool": "fake.echo", "args": map[string]any{"message": "globex"}, "wait": 5})
	if id := Str(r.Map(), "id"); id != "" {
		globex.Actions = append(globex.Actions, id)
	}
}

// ---- the target database ----

// TargetTemplate is attack_target_tpl after init: the DSNs init wrote (with --no-store) and the
// watched tables.
type TargetTemplate struct {
	Name       string
	AgentDSN   string
	WitnessDSN string
	Tables     []string
}

func (e *Env) setupTarget() *TargetTemplate {
	t := &TargetTemplate{Name: "attack_target_tpl", Tables: []string{"public.customers", "public.orders"}}
	c := e.adminConn("postgres")
	ctx := context.Background()
	_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS attack_target_tpl WITH (FORCE)")
	_, err := c.Exec(ctx, "CREATE DATABASE attack_target_tpl")
	must(err)
	c.Close(ctx)
	e.cleanups = append(e.cleanups, func() { e.dropDB("attack_target_tpl") })
	sqlText, err := os.ReadFile(filepath.Join(e.Root, "attack", "fixtures", "sql", "target.sql"))
	must(err)
	tc := e.adminConn("attack_target_tpl")
	_, err = tc.Exec(ctx, string(sqlText))
	must(err)
	tc.Close(ctx)
	acme := e.Orgs["acme"]
	cfg := map[string]any{"mode": "sql", "allowed_schemas": []string{"public"}, "writable_tables": []string{"public.orders", "public.customers"},
		"row_limit": 500, "max_rows_affected": 1000, "statement_timeout_ms": 5000, "lock_timeout_ms": 2000,
		"witness": map[string]any{"enabled": true, "watched_tables": t.Tables, "l2_timeout": "5s"}, "throwaway": true}
	ok(e.AddConnector(acme, "postgres", "postgres", cfg, "", nil), "postgres connector", 201)
	u, err := url.Parse(e.AdminURL)
	must(err)
	u.Path = "/attack_target_tpl"
	e.CLI([]string{"DEEDGLASS_INIT_DSN=" + u.String(), "DEEDGLASS_SERVER=" + e.Server, "DEEDGLASS_API_KEY=" + acme.AdminKey},
		"connector", "postgres", "init", "--connector", "postgres", "--apply", "--no-store")
	f, err := os.Open(filepath.Join(e.Dir, ".deedglass", "pg-postgres.env"))
	must(err)
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		switch {
		case !ok:
		case k == "DSN":
			t.AgentDSN = v
		case k == "WITNESS_DSN":
			t.WitnessDSN = v
		}
	}
	if t.AgentDSN == "" {
		panic("init wrote no DSN")
	}
	return t
}

func (e *Env) dropDB(name string) {
	c := e.adminConn("postgres")
	defer c.Close(context.Background())
	_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
}

// withDB swaps the database name in a DSN.
func withDB(dsn, db string) string {
	u, err := url.Parse(dsn)
	must(err)
	u.Path = "/" + db
	return u.String()
}

// ListenAlerts listens on deedglass_events on the control plane (as the superuser) and returns a
// wait function that reports whether a decoder.alert of kind arrived before d passed, and a stop
// function. Start it before the attack.
func (e *Env) ListenAlerts(kind string) (wait func(d time.Duration) bool, stop func()) {
	c := e.adminConn(e.CP.Name)
	if _, err := c.Exec(context.Background(), "LISTEN deedglass_events"); err != nil {
		c.Close(context.Background())
		panic(err)
	}
	wait = func(d time.Duration) bool {
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			n, err := c.WaitForNotification(ctx)
			cancel()
			if err != nil {
				return false
			}
			if strings.Contains(n.Payload, `"type":"decoder.alert"`) && strings.Contains(n.Payload, `"kind":"`+kind+`"`) {
				return true
			}
		}
		return false
	}
	return wait, func() { c.Close(context.Background()) }
}

// Repoint stores the clone's credentials again: a connector write, which makes the decoder retry
// at once (S21's Resync).
func (tg *Target) Repoint() {
	e := tg.env
	secret := map[string]string{"dsn": tg.DSN}
	if e.Target.WitnessDSN != "" {
		secret["witness_dsn"] = withDB(e.Target.WitnessDSN, tg.Name)
	}
	acme := e.Orgs["acme"]
	r := acme.Admin.Do("PUT", "/v1/connectors/"+acme.Conns["postgres"]+"/credentials", map[string]any{"kind": "dsn", "secret": secret})
	if r.Status != 200 && r.Status != 201 {
		panic(fmt.Sprintf("repoint %s: HTTP %d %s", tg.Name, r.Status, r.Body))
	}
}

// AdminExec runs SQL as the superuser on the cluster's postgres database (roles are cluster-wide).
func (e *Env) AdminExec(sql string, args ...any) error {
	c := e.adminConn("postgres")
	defer c.Close(context.Background())
	_, err := c.Exec(context.Background(), sql, args...)
	return err
}
