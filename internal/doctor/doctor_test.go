package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
)

// fakeServer answers the GET routes doctor reads. Fields change what it says.
type fakeServer struct {
	date      time.Time
	slot      string // decoder status
	reason    string
	lagBytes  int64
	chain     string
	enforce   bool
	keysJSON  []byte
	unauth    bool
	version   string
	health    string
	connector string // health status of the postgres connector
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Date", f.date.UTC().Format(http.TimeFormat))
		_ = json.NewEncoder(w).Encode(v)
	}
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if f.unauth || r.Header.Get("Authorization") != "Bearer dg_test_admin" {
				http.Error(w, `{"error":{}}`, http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) { j(w, map[string]any{"status": f.health}) })
	mux.HandleFunc("GET /v1/version", func(w http.ResponseWriter, _ *http.Request) {
		j(w, map[string]any{"version": f.version, "api_versions": []string{"2026-10-01"}})
	})
	mux.HandleFunc("GET /.well-known/deedglass/keys.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(f.keysJSON) })
	mux.HandleFunc("GET /v1/deeds", auth(func(w http.ResponseWriter, _ *http.Request) {
		j(w, map[string]any{"data": []any{map[string]any{"body": map[string]any{"seq": 1204}}}})
	}))
	mux.HandleFunc("GET /v1/ledger/check", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("from_seq") != "205" {
			http.Error(w, "from_seq", 400)
			return
		}
		j(w, map[string]any{"status": f.chain, "checked": 1000})
	}))
	mux.HandleFunc("GET /v1/connectors", auth(func(w http.ResponseWriter, _ *http.Request) {
		j(w, map[string]any{"data": []any{map[string]any{"id": "con_01", "name": "postgres", "type": "postgres", "health": map[string]any{"status": f.connector}}}})
	}))
	mux.HandleFunc("GET /v1/connectors/con_01/decoder", auth(func(w http.ResponseWriter, _ *http.Request) {
		var reason any
		if f.reason != "" {
			reason = f.reason
		}
		j(w, map[string]any{"name": "postgres", "slot": "deedglass_01k6abcdp46q", "status": f.slot, "reason": reason, "lag": map[string]any{"bytes": f.lagBytes}})
	}))
	mux.HandleFunc("GET /v1/policies/active", auth(func(w http.ResponseWriter, _ *http.Request) {
		if !f.enforce {
			j(w, map[string]any{"enforce": nil, "shadow": nil})
			return
		}
		j(w, map[string]any{"enforce": map[string]any{"name": "postgres-safe", "version": 1}, "shadow": nil})
	}))
	return mux
}

func goodEnv(t *testing.T) (*Env, *fakeServer) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".deedglass"), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, 32)
	kf, signer, err := deed.NewKeyFile("sk_01k6h7kp6msmzrhdzwtcr3jnkp", seed, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	p, err := deed.WriteKeyFile(filepath.Join(dir, ".deedglass", "keys"), kf)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, ".deedglass", "admin.key"), []byte("dg_test_admin"), 0o600)
	tp := deed.Thumbprint(signer.PublicKey())
	keys, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"id": signer.KeyID(), "status": "active", "thumbprint": tp}}})
	f := &fakeServer{date: time.Now(), slot: "running", chain: "intact", enforce: true, keysJSON: keys, version: "0.1.0", health: "ok", connector: "ok"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	env := &Env{Dir: dir, EnvName: "dev", DBURL: "postgres://x", SealKeyFile: p, VaultKey: "q83vEjRWeJq83vEjRWeJq83vEjRWeJq83vEjRWeJq80=",
		Server: srv.URL, APIKey: "dg_test_admin", HTTP: srv.Client(), CLIVersion: "0.1.0-dev", APIVersion: "2026-10-01",
		SlotLagAlertMB: 512, Now: time.Now, Statfs: func(string) (uint64, error) { return 50 << 30, nil },
		Connect: func(context.Context, string) (*pgx.Conn, error) {
			return nil, errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
		}}
	return env, f
}

func byID(rs []Result) map[string]Result {
	m := map[string]Result{}
	for _, r := range rs {
		m[r.ID] = r
	}
	return m
}

func nonDB(cs []Check) []Check {
	var out []Check
	for _, c := range cs {
		if !strings.HasPrefix(c.ID, "db.") {
			out = append(out, c)
		}
	}
	return out
}

func TestHealthyWithoutDatabase(t *testing.T) {
	env, _ := goodEnv(t)
	rs := Run(context.Background(), env, nonDB(Checks()))
	for _, r := range rs {
		if r.Status != OK {
			t.Errorf("%s: %s %s · %s", r.ID, r.Status, r.Detail, r.Fix)
		}
		if r.Doc != DocURL(r.ID) || !strings.HasPrefix(r.Doc, "https://docs.deedglass.com/doctor#") {
			t.Errorf("%s doc %s", r.ID, r.Doc)
		}
	}
	if len(Checks()) != 16 {
		t.Fatalf("%d checks", len(Checks()))
	}
}

// TestForcedFailures: each check's failure, forced, names the right id and a fix that starts with a verb.
func TestForcedFailures(t *testing.T) {
	for _, c := range []struct {
		id     string
		status Status
		force  func(env *Env, f *fakeServer)
		detail string
	}{
		{"state.dir", Fail, func(env *Env, _ *fakeServer) { _ = os.Chmod(filepath.Join(env.Dir, ".deedglass"), 0o755) }, "mode 0755"},
		{"state.dir", Fail, func(env *Env, _ *fakeServer) { _ = os.Chmod(filepath.Join(env.Dir, ".deedglass", "admin.key"), 0o644) }, "admin.key is mode 0644"},
		{"db.reachable", Fail, func(*Env, *fakeServer) {}, "can't connect"},
		{"db.reachable", Fail, func(env *Env, _ *fakeServer) { env.DBURL = "" }, "no database URL"},
		{"config.valid", Fail, func(env *Env, _ *fakeServer) { env.ConfigErr = errors.New("DEEDGLASS_LISTEN isn't a host:port.") }, "DEEDGLASS_LISTEN"},
		{"seal.key", Fail, func(env *Env, _ *fakeServer) { env.SealKeyFile = filepath.Join(env.Dir, "missing.json") }, "doesn't load"},
		{"seal.key", Fail, func(_ *Env, f *fakeServer) { f.keysJSON = []byte(`{"keys":[]}`) }, "not in keys.json"},
		{"vault.key", Fail, func(env *Env, _ *fakeServer) { env.VaultKey = "short" }, "doesn't parse"},
		{"server.health", Fail, func(env *Env, _ *fakeServer) { env.Server = "http://127.0.0.1:1" }, "no answer"},
		{"clock.skew", Fail, func(_ *Env, f *fakeServer) { f.date = time.Now().Add(-45 * time.Second) }, "s from the server"},
		{"clock.skew", Warn, func(_ *Env, f *fakeServer) { f.date = time.Now().Add(5 * time.Second) }, "s from the server"},
		{"ledger.chain", Fail, func(_ *Env, f *fakeServer) { f.chain = "broken" }, "chain broken"},
		{"decoder.slots", Fail, func(_ *Env, f *fakeServer) { f.slot, f.reason = "halted", "slot_invalidated" }, "halted"},
		{"decoder.slots", Warn, func(_ *Env, f *fakeServer) { f.lagBytes = 612 << 20 }, "lag 612 MB"},
		{"wal.level", Fail, func(_ *Env, f *fakeServer) { f.slot, f.reason = "halted", "wal_level" }, "logical"},
		{"connectors.postgres", Fail, func(_ *Env, f *fakeServer) { f.connector = "fail" }, "postgres failing"},
		{"connectors.postgres", Warn, func(_ *Env, f *fakeServer) { f.connector = "unknown" }, "not tested yet"},
		{"disk.space", Fail, func(env *Env, _ *fakeServer) { env.Statfs = func(string) (uint64, error) { return 100 << 20, nil } }, "GiB free"},
		{"disk.space", Warn, func(env *Env, _ *fakeServer) { env.Statfs = func(string) (uint64, error) { return 1 << 30, nil } }, "GiB free"},
		{"version.drift", Warn, func(_ *Env, f *fakeServer) { f.version = "0.2.0" }, "server 0.2.0"},
		{"policy.active", Warn, func(_ *Env, f *fakeServer) { f.enforce = false }, "no policy enforced"},
	} {
		t.Run(c.id+"/"+c.detail, func(t *testing.T) {
			env, f := goodEnv(t)
			c.force(env, f)
			r := byID(Run(context.Background(), env, Filter(Checks(), c.id)))[c.id]
			if r.Status != c.status || !strings.Contains(r.Detail, c.detail) {
				t.Fatalf("%s %q", r.Status, r.Detail)
			}
			first := strings.Fields(r.Fix + " x")[0]
			if r.Fix == "" || strings.HasPrefix(first, "the") || strings.ContainsAny(r.Fix, "!—") || !strings.HasSuffix(r.Fix, ".") {
				t.Fatalf("fix %q", r.Fix)
			}
			if strings.Contains(r.Detail+r.Fix, "dg_test_admin") || strings.Contains(r.Detail+r.Fix, "q83vEj") {
				t.Fatal("a secret reached the output")
			}
		})
	}
}

func TestUnauthenticated(t *testing.T) {
	env, f := goodEnv(t)
	f.unauth = true
	r := byID(Run(context.Background(), env, Filter(Checks(), "ledger.chain")))["ledger.chain"]
	if r.Status != Fail || !env.Unauthenticated.Load() || !strings.Contains(r.Fix, "DEEDGLASS_API_KEY") {
		t.Fatalf("%+v", r)
	}
	env2, _ := goodEnv(t)
	env2.APIKey = ""
	r = byID(Run(context.Background(), env2, Filter(Checks(), "policy.active")))["policy.active"]
	if r.Status != Skip {
		t.Fatalf("%+v", r)
	}
}

func TestTimeoutAndPanic(t *testing.T) {
	slow := Check{ID: "slow", Title: "Slow", Run: func(ctx context.Context, _ *Env) Result {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond)
		return ok("late")
	}}
	boom := Check{ID: "boom", Title: "Boom", Run: func(context.Context, *Env) Result { panic("x") }}
	t0 := time.Now()
	rs := Run(context.Background(), &Env{}, []Check{slow, boom})
	if rs[0].Status != Fail || rs[1].Status != Fail || time.Since(t0) > Timeout+time.Second {
		t.Fatalf("%+v", rs)
	}
}

func TestFilterAndDocs(t *testing.T) {
	if n := len(Filter(Checks(), "db.*")); n != 3 {
		t.Fatalf("%d", n)
	}
	if n := len(Filter(Checks(), "seal.key,clock.skew")); n != 2 {
		t.Fatalf("%d", n)
	}
	if DocURL("db.reachable") != "https://docs.deedglass.com/doctor#db-reachable" {
		t.Fatal(DocURL("db.reachable"))
	}
	b, err := os.ReadFile("../../docs/INIT_AND_DOCTOR.md")
	if err != nil {
		t.Skip("docs/INIT_AND_DOCTOR.md isn't written yet")
	}
	for _, c := range Checks() {
		anchor := strings.TrimPrefix(DocURL(c.ID), "https://docs.deedglass.com/doctor#")
		if !strings.Contains(string(b), "<a id=\""+anchor+"\"></a>") {
			t.Errorf("docs/INIT_AND_DOCTOR.md lacks the anchor %s", anchor)
		}
	}
}
