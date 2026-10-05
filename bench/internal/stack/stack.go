//go:build integration

// Package stack boots what a benchmark measures: a freshly migrated control-plane database on a
// THROWAWAY cluster (storetest), the real deedglass binary, one or more `deedglass serve`
// processes, an org with an admin key, agents with keys and an enforced policy. It refuses to run
// unless DEEDGLASS_IT_THROWAWAY=1 and DEEDGLASS_IT_ADMIN_URL are set (scripts/bench_pg.sh sets both
// for the cluster it starts and deletes).
package stack

//deedglass:allow-exec benchmark tooling: builds and starts deedglass serve processes on a throwaway cluster; never shipped

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/bench/internal/load"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// Stack is one benchmark environment.
type Stack struct {
	Root, Dir, Bin string
	CP             *storetest.Env
	Servers        []*Server
	cleanups       []func()
	HTTP           *http.Client
}

// Server is one `deedglass serve` process.
type Server struct {
	URL  string
	proc *exec.Cmd
	logs *bytes.Buffer
	mu   *sync.Mutex
}

// PID is the server's process id (the soak samples its memory).
func (s *Server) PID() int { return s.proc.Process.Pid }

// bootTB adapts the run's lifetime to storetest, which wants a testing.TB.
type bootTB struct {
	testing.TB
	s *Stack
}

func (bootTB) Helper()                      {}
func (b bootTB) Cleanup(f func())           { b.s.cleanups = append(b.s.cleanups, f) }
func (bootTB) Fatal(args ...any)            { panic(fmt.Sprint(args...)) }
func (bootTB) Fatalf(f string, args ...any) { panic(fmt.Sprintf(f, args...)) }
func (bootTB) Errorf(f string, args ...any) { fmt.Fprintf(os.Stderr, "bench: "+f+"\n", args...) }
func (bootTB) Logf(f string, args ...any)   {}

// secret is a fixed throwaway value per label (the run never reads .env).
func secret(label string) string {
	h := sha256.Sum256([]byte("deedglass bench · " + label))
	return base64.StdEncoding.EncodeToString(h[:])
}

// Boot builds the binary and creates the database. It panics on a failure (dgbench recovers).
func Boot(root string) *Stack {
	if os.Getenv("DEEDGLASS_IT_THROWAWAY") != "1" || os.Getenv("DEEDGLASS_IT_ADMIN_URL") == "" {
		panic("refusing: benchmarks that need Postgres run only on a throwaway cluster (make bench starts one)")
	}
	s := &Stack{Root: root, HTTP: egress.FirstParty(egress.FirstPartyOptions{Timeout: 60 * time.Second, MaxIdle: 512})}
	s.Dir = filepath.Join(root, ".tmp", "bench", "run-"+time.Now().UTC().Format("20060102T150405"))
	must(os.MkdirAll(s.Dir, 0o700))
	s.Bin = filepath.Join(s.Dir, "deedglass")
	build := exec.Command("go", "build", "-trimpath", "-o", s.Bin, "./cmd/deedglass")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("go build: %v\n%s", err, out))
	}
	s.CP = storetest.New(bootTB{s: s}, storetest.Options{MaxConns: 4})
	return s
}

// Env is the server and CLI environment: nothing is inherited from the shell.
func (s *Stack) Env(extra ...string) []string {
	return append([]string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + s.Dir, "TMPDIR=" + os.TempDir(), "LANG=en_US.UTF-8",
		"DEEDGLASS_ENV=dev",
		"DEEDGLASS_DATABASE_URL=" + s.CP.AppURL,
		"DEEDGLASS_MIGRATE_DATABASE_URL=" + s.CP.MigratorURL,
		"DEEDGLASS_VAULT_KEY=" + secret("vault"),
		"DEEDGLASS_API_KEY_PEPPER=" + secret("pepper"),
		"DEEDGLASS_LOG_LEVEL=warn",
		"DEEDGLASS_NO_BANNER=1",
		"NO_COLOR=1",
	}, extra...)
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type lockedBuf struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (l lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b.Len() > 1<<20 {
		l.b.Reset()
	}
	return l.b.Write(p)
}

// StartServer starts one serve process and waits until it is healthy.
func (s *Stack) StartServer(extra ...string) *Server {
	port := freePort()
	srv := &Server{URL: fmt.Sprintf("http://127.0.0.1:%d", port), logs: &bytes.Buffer{}, mu: &sync.Mutex{}}
	cmd := exec.Command(s.Bin, "serve")
	cmd.Dir = s.Dir
	cmd.Env = s.Env(append([]string{"DEEDGLASS_LISTEN=127.0.0.1:" + fmt.Sprint(port), "DEEDGLASS_BASE_URL=" + srv.URL}, extra...)...)
	w := lockedBuf{srv.mu, srv.logs}
	cmd.Stdout, cmd.Stderr = w, w
	must(cmd.Start())
	srv.proc = cmd
	s.Servers = append(s.Servers, srv)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if st, _, _ := s.Do(srv, "GET", "/v1/health", "", nil); st == 200 {
			return srv
		}
		if time.Now().After(deadline) {
			srv.mu.Lock()
			defer srv.mu.Unlock()
			panic("serve didn't become healthy in 30 s:\n" + srv.logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Close stops every server and drops the database.
func (s *Stack) Close() {
	for _, srv := range s.Servers {
		if srv.proc != nil && srv.proc.Process != nil {
			_ = srv.proc.Process.Signal(os.Interrupt)
			done := make(chan struct{})
			go func(p *exec.Cmd) { _ = p.Wait(); close(done) }(srv.proc)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = srv.proc.Process.Kill()
				<-done
			}
		}
	}
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		func() {
			defer func() { _ = recover() }()
			s.cleanups[i]()
		}()
	}
	if os.Getenv("DEEDGLASS_IT_KEEP") != "1" {
		_ = os.RemoveAll(s.Dir)
	}
}

// CLI runs the binary with the stack's environment and returns stdout.
func (s *Stack) CLI(args ...string) []byte {
	cmd := exec.Command(s.Bin, args...)
	cmd.Dir = s.Dir
	cmd.Env = s.Env()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		panic(fmt.Sprintf("deedglass %s: %v\n%s%s", strings.Join(args, " "), err, out.String(), errb.String()))
	}
	return out.Bytes()
}

func idemKey() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "bench_" + hex.EncodeToString(b[:])
}

// Do sends one request. POSTs get a fresh Idempotency-Key.
func (s *Stack) Do(srv *Server, method, path, key string, body any) (int, map[string]any, error) {
	return s.DoCtx(context.Background(), srv, method, path, key, body)
}

// DoCtx is Do with a context.
func (s *Stack) DoCtx(ctx context.Context, srv *Server, method, path, key string, body any) (int, map[string]any, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == "POST" {
		req.Header.Set("Idempotency-Key", idemKey())
	}
	res, err := s.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return res.StatusCode, nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return res.StatusCode, m, nil
}

// Org is a benchmark org.
type Org struct {
	ID, Slug, AdminKey string
	Agents             []Agent
}

// Agent is an agent and its key.
type Agent struct{ ID, Name, Key string }

// NewOrg creates an org (DB-direct, with an admin key), n agents with test keys, and enforces policy.
func (s *Stack) NewOrg(srv *Server, slug string, agents int, policy string) *Org {
	out := s.CLI("org", "create", "--name", "Bench "+slug, "--slug", slug, "--owner-email", "owner@"+slug+".example.test", "--admin-key", "--json")
	var res struct {
		Org      struct{ ID string }     `json:"org"`
		AdminKey struct{ Secret string } `json:"admin_key"`
	}
	must(json.Unmarshal(out, &res))
	o := &Org{ID: res.Org.ID, Slug: slug, AdminKey: res.AdminKey.Secret}
	for i := 0; i < agents; i++ {
		name := fmt.Sprintf("bench-bot-%d", i+1)
		st, m, err := s.Do(srv, "POST", "/v1/agents", o.AdminKey, map[string]any{"name": name, "description": "Benchmark agent."})
		if err != nil || st != 201 {
			panic(fmt.Sprintf("create agent: %d %v %v", st, m, err))
		}
		id := str(m, "id")
		st, km, err := s.Do(srv, "POST", "/v1/agents/"+id+"/keys", o.AdminKey, map[string]any{"name": "bench", "mode": "test"})
		if err != nil || st != 201 {
			panic(fmt.Sprintf("create key: %d %v %v", st, km, err))
		}
		key := str(km, "secret")
		if key == "" {
			if k, ok := km["key"].(map[string]any); ok {
				key = str(k, "secret")
			}
		}
		o.Agents = append(o.Agents, Agent{ID: id, Name: name, Key: key})
	}
	if policy != "" {
		st, pm, err := s.Do(srv, "POST", "/v1/policies", o.AdminKey, map[string]string{"source": policy})
		if err != nil || st != 201 {
			panic(fmt.Sprintf("push policy: %d %v %v", st, pm, err))
		}
		st, am, err := s.Do(srv, "POST", "/v1/policies/"+str(pm, "id")+"/versions/1/activate", o.AdminKey,
			map[string]any{"mode": "enforce", "reason": "The benchmark's policy."})
		if err != nil || (st != 200 && st != 201) {
			panic(fmt.Sprintf("activate policy: %d %v %v", st, am, err))
		}
		time.Sleep(500 * time.Millisecond) // the registry swaps on NOTIFY
	}
	return o
}

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// Str reads a string field of a JSON object, following a path.
func Str(m map[string]any, path ...string) string {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[p]
	}
	s, _ := cur.(string)
	return s
}

// Postgres describes the cluster for the results file.
func (s *Stack) Postgres() *load.Postgres {
	return PostgresOf(os.Getenv("DEEDGLASS_IT_ADMIN_URL"))
}

// PostgresOf reads the server's version and the settings that move write latency.
func PostgresOf(adminURL string) *load.Postgres {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return nil
	}
	defer c.Close(ctx)
	p := &load.Postgres{Mode: "native", Settings: map[string]string{}}
	if os.Getenv("DEEDGLASS_BENCH_PG_MODE") == "docker" {
		p.Mode = "docker"
	}
	_ = c.QueryRow(ctx, "SELECT pg_catalog.current_setting('server_version')").Scan(&p.Version)
	for _, k := range []string{"synchronous_commit", "fsync", "shared_buffers", "wal_level", "max_connections"} {
		var v string
		if c.QueryRow(ctx, "SELECT pg_catalog.current_setting($1)", k).Scan(&v) == nil {
			p.Settings[k] = v
		}
	}
	return p
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
