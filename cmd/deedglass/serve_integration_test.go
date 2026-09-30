//go:build integration

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/migrate"
	"github.com/deedglass/deedglass/internal/store/migrations"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type served struct {
	out, err *syncBuf
	code     chan int
	addr     chan net.Addr
	sigs     chan os.Signal
}

// startServe runs `deedglass serve` in the background with a fake signal channel.
func startServe(t *testing.T, env map[string]string, args ...string) *served {
	t.Helper()
	s := &served{out: &syncBuf{}, err: &syncBuf{}, code: make(chan int, 1), addr: make(chan net.Addr, 1), sigs: make(chan os.Signal, 2)}
	savedSig, savedListen := serveSignals, serveListening
	serveSignals = func() (<-chan os.Signal, func()) { return s.sigs, func() {} }
	serveListening = func(a net.Addr) { s.addr <- a }
	t.Cleanup(func() { serveSignals, serveListening = savedSig, savedListen })
	dir := t.TempDir()
	e := &Env{Stdout: s.out, Stderr: s.err, Getenv: func(k string) string { return env[k] },
		LoadOptions: func() config.LoadOptions { return config.LoadOptions{Env: env, FS: fstest.MapFS{}, Dir: dir} }}
	go func() { s.code <- run(context.Background(), e, append([]string{"serve"}, args...)) }()
	return s
}

func (s *served) wait(t *testing.T) int {
	t.Helper()
	select {
	case c := <-s.code:
		return c
	case <-time.After(30 * time.Second):
		t.Fatalf("serve didn't exit\nstdout:\n%s\nstderr:\n%s", s.out, s.err)
		return -1
	}
}

func prodEnv(url string) map[string]string {
	return map[string]string{
		"DEEDGLASS_ENV": "prod", "DEEDGLASS_DATABASE_URL": url, "DEEDGLASS_LISTEN": "127.0.0.1:0",
		"DEEDGLASS_BASE_URL":       "https://api.example.com",
		"DEEDGLASS_VAULT_KEY":      "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"DEEDGLASS_API_KEY_PEPPER": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
	}
}

func TestServeRefusesSuperuserInProd(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	s := startServe(t, prodEnv(e.AdminURL))
	if code := s.wait(t); code != 1 {
		t.Fatalf("exit %d\n%s%s", code, s.out, s.err)
	}
	if !strings.Contains(s.err.String(), "is a superuser, so row-level security would not apply.") ||
		!strings.Contains(s.err.String(), "Connect as deedglass_app (docs/DATABASE_SETUP.md).") ||
		strings.Contains(s.err.String()+s.out.String(), storetest.Password) {
		t.Fatalf("stderr:\n%s", s.err)
	}
	t.Logf("\n%s", s.err)
}

func TestServeRefusesPendingMigrationsInProd(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	s := startServe(t, prodEnv(e.AppURL))
	if code := s.wait(t); code != 1 || !strings.Contains(s.err.String(), "Refusing to start: 9 migrations are pending.") ||
		!strings.Contains(s.err.String(), "Run deedglass migrate up first.") {
		t.Fatalf("exit %d\n%s%s", code, s.out, s.err)
	}
	t.Logf("\n%s", s.err)
	s = startServe(t, prodEnv(e.AppURL), "--migrate")
	if code := s.wait(t); code != 2 || !strings.Contains(s.err.String(), "--migrate is for dev") {
		t.Fatalf("prod --migrate: exit %d %s", code, s.err)
	}
}

func TestServeUnreachableDatabase(t *testing.T) {
	s := startServe(t, map[string]string{"DEEDGLASS_DATABASE_URL": "postgres://deedglass_app:sekrit-pw@127.0.0.1:1/deedglass?sslmode=disable"})
	if code := s.wait(t); code != 1 || !strings.Contains(s.err.String(), "Can't connect to 127.0.0.1:1 as deedglass_app") ||
		!strings.Contains(s.err.String(), "Is Postgres running? Try make dev.") || strings.Contains(s.err.String(), "sekrit") {
		t.Fatalf("exit %d\n%s", code, s.err)
	}
}

// The dev loop end to end: serve --migrate on an empty database, health, 404, then SIGTERM.
func TestServeLive(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "warn"}
	start := time.Now()
	s := startServe(t, env, "--migrate")
	var base string
	select {
	case a := <-s.addr:
		base = "http://" + a.String()
	case c := <-s.code:
		t.Fatalf("exited %d\n%s%s", c, s.out, s.err)
	case <-time.After(30 * time.Second):
		t.Fatal("serve never listened")
	}
	t.Logf("startup (with 9 migrations) took %v", time.Since(start))

	get := func(p string) (int, http.Header, string) {
		res, err := http.Get(base + p)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header, string(b)
	}
	code, h, body := get("/v1/health")
	if code != 200 || !strings.Contains(body, `"status": "ok"`) || !strings.Contains(body, `"applied": 9`) ||
		!strings.HasPrefix(h.Get("Deedglass-Request-Id"), "req_") || h.Get("Deedglass-Version") != "2026-10-01" {
		t.Fatalf("health %d %v\n%s", code, h, body)
	}
	t.Logf("GET /v1/health → %d\n%s", code, body)
	if code, _, body = get("/v1/helth"); code != 404 || !strings.Contains(body, "Did you mean /v1/health?") {
		t.Fatalf("helth %d %s", code, body)
	}
	if code, _, body = get("/api/openapi.yaml"); code != 200 || !strings.HasPrefix(body, "openapi: 3.1.1\n") {
		t.Fatalf("spec %d", code)
	}
	s.sigs <- syscall.SIGTERM
	if c := s.wait(t); c != 0 {
		t.Fatalf("exit %d\n%s%s", c, s.out, s.err)
	}
	out := s.out.String()
	for _, want := range []string{"api 2026-10-01 · env dev", "✓ Migrations · 9 applied", "✓ Database · postgres ", "schema at 0009 · role deedglass_app",
		"○ Seal key · not configured", "✓ Listening on http://127.0.0.1:", "✓ Stopped cleanly in ", "· 0 requests cut off"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	t.Logf("\n%s", out)
	if strings.Contains(out+s.err.String(), storetest.Password) {
		t.Fatal("a password reached the output")
	}
}

type pgKeys struct{ p httpx.Principal }

func (k pgKeys) AuthenticateKey(_ context.Context, bearer string) (httpx.Principal, error) {
	if bearer == "dg_test_good" {
		return k.p, nil
	}
	return httpx.Principal{}, httpx.E(httpx.CodeInvalidAPIKey)
}

// The httpx pipeline on the Postgres store: replay, concurrency, and a Sensitive secret that never
// reaches the database.
func TestIdempotencyOnPostgres(t *testing.T) {
	e := storetest.New(t, storetest.Options{MaxConns: 16})
	ctx := context.Background()
	o, _, _, err := e.DB.CreateOrg(ctx, store.NewOrg{Name: "Acme", Slug: "acme", OwnerEmail: "o@acme.test"})
	if err != nil {
		t.Fatal(err)
	}
	p := httpx.Principal{Kind: "agent_key", OrgID: o.ID, AgentID: ids.New[ids.AgentKind](), KeyID: ids.New[ids.APIKeyKind](), APIVersion: "2026-10-01"}
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Keys: pgKeys{p}, Idempotency: idemStore{e.DB}})
	var runs sync.WaitGroup
	var mu sync.Mutex
	count := 0
	secret := "dg_test_SECRETSS_pgcanarypgcanarypgcanarypgcanary"
	api.Mount(
		httpx.Route{Method: "POST", Pattern: "/v1/_test/slow", OperationID: "a", Auth: httpx.AgentKey, Idempotency: httpx.IdemRequired, Extension: "test",
			Handler: func(w http.ResponseWriter, r *http.Request) error {
				mu.Lock()
				count++
				mu.Unlock()
				time.Sleep(300 * time.Millisecond)
				return httpx.WriteJSON(w, 201, map[string]string{"object": "thing"})
			}},
		httpx.Route{Method: "POST", Pattern: "/v1/_test/secret", OperationID: "b", Auth: httpx.AgentKey, Idempotency: httpx.IdemRequired, Sensitive: true, Extension: "test",
			Handler: func(w http.ResponseWriter, r *http.Request) error {
				return httpx.WriteJSON(w, 201, map[string]string{"object": "api_key", "id": "key_x", "secret": secret})
			}},
	)
	post := func(path, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer dg_test_good")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	statuses := make([]int, 10)
	for i := range 10 {
		runs.Add(1)
		go func() { defer runs.Done(); statuses[i] = post("/v1/_test/slow", "C10").Code }()
	}
	runs.Wait()
	created, busy := 0, 0
	for _, s := range statuses {
		switch s {
		case 201:
			created++
		case 409:
			busy++
		}
	}
	if count != 1 || created+busy != 10 || created < 1 {
		t.Fatalf("handler ran %d times; statuses %v", count, statuses)
	}
	if w := post("/v1/_test/slow", "C10"); w.Code != 201 || w.Header().Get(httpx.HeaderReplayed) != "true" {
		t.Fatalf("replay: %d", w.Code)
	}

	first := post("/v1/_test/secret", "S1")
	replay := post("/v1/_test/secret", "S1")
	if !strings.Contains(first.Body.String(), secret) || strings.Contains(replay.Body.String(), secret) || !strings.Contains(replay.Body.String(), `"secret": null`) {
		t.Fatalf("first %s\nreplay %s", first.Body, replay.Body)
	}
	var n int
	if err := e.Admin(t).QueryRow(ctx, `SELECT count(*) FROM deedglass.idempotency_keys
	    WHERE position(convert_to($1, 'UTF8') in response_body) > 0 OR position(convert_to('SECRETSS', 'UTF8') in response_body) > 0`, secret).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the secret is in %d stored responses", n)
	}
}

// The uncached health check on a real database (S03 §3.13: p50 < 3 ms): a clock that jumps 2 s per
// call defeats the 1 s cache, so every probe pings Postgres and reads the schema version.
func TestHealthUncachedBudget(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	ms := mustMigrations(t)
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); now = now.Add(2 * time.Second); return now }
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler)})
	api.Mount(api.SystemRoutes(httpx.SystemDeps{Health: healthSource{db: e.DB, ms: ms}, Spec: apiSpec(), Now: clock})...)
	var d []time.Duration
	for i := 0; i < 520; i++ {
		w := httptest.NewRecorder()
		t0 := time.Now()
		api.ServeHTTP(w, httptest.NewRequest("GET", "/v1/health", nil))
		if i >= 20 {
			d = append(d, time.Since(t0))
		}
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	slices.Sort(d)
	p50, p99 := d[len(d)/2], d[len(d)*99/100]
	t.Logf("uncached /v1/health on PostgreSQL: p50 %v · p99 %v (500 probes)", p50, p99)
	if p50 > 3*time.Millisecond {
		t.Errorf("uncached health p50 %v, budget 3 ms", p50)
	}
}

func mustMigrations(t *testing.T) []migrate.Migration {
	ms, err := migrate.Load(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}
