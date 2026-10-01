package httpx

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// Test principals.
var (
	testOrg    = ids.MustParse[ids.OrgKind]("org_01j9zr3m5k8q2w7x4t6v0b1c9d")
	testOrg2   = ids.MustParse[ids.OrgKind]("org_01j9zr3m5k8q2w7x4t6v0b1c9e")
	testKey    = ids.MustParse[ids.APIKeyKind]("key_01j9zr3m5k8q2w7x4t6v0b1c9d")
	testKey2   = ids.MustParse[ids.APIKeyKind]("key_01j9zr3m5k8q2w7x4t6v0b1c9e")
	testAgent  = ids.MustParse[ids.AgentKind]("agt_01j9zr3m5k8q2w7x4t6v0b1c9d")
	testAgent2 = ids.MustParse[ids.AgentKind]("agt_01j9zr3m5k8q2w7x4t6v0b1c9e")
)

const (
	goodKey  = "dg_test_AAAAAAAA_goodgoodgoodgoodgoodgoodgoodgood"
	otherKey = "dg_test_BBBBBBBB_otherotherotherotherotherotherot"
	oldKey   = "dg_test_CCCCCCCC_oldoldoldoldoldoldoldoldoldoldol"
	// canarySecret is returned by the Sensitive test route and must never reach the store or logs.
	canarySecret = "dg_test_ZZZZZZZZ_canarycanarycanarycanarycanaryca"
)

type fakeKeys struct{}

func (fakeKeys) AuthenticateKey(_ context.Context, bearer string) (Principal, error) {
	switch bearer {
	case goodKey:
		return Principal{Kind: "agent_key", OrgID: testOrg, AgentID: testAgent, KeyID: testKey,
			Scopes: []string{"actions:execute", "actions:read", "deeds:read"}, APIVersion: "2026-10-01"}, nil
	case otherKey:
		return Principal{Kind: "agent_key", OrgID: testOrg, AgentID: testAgent2, KeyID: testKey2,
			Scopes: []string{"actions:read"}, APIVersion: "2026-10-01"}, nil
	case oldKey:
		return Principal{}, E(CodeAPIKeyRevoked)
	}
	return Principal{}, E(CodeInvalidAPIKey)
}

// logBuf is a concurrency-safe log sink.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// harness is a server with the system routes and the test-only routes mounted.
type harness struct {
	srv     *Server
	log     *logBuf
	store   *MemoryStore
	runs    atomic.Int64 // echo handler runs
	release chan struct{}
	now     time.Time
	nowMu   sync.Mutex
}

func (h *harness) clock() time.Time { h.nowMu.Lock(); defer h.nowMu.Unlock(); return h.now }
func (h *harness) advance(d time.Duration) {
	h.nowMu.Lock()
	defer h.nowMu.Unlock()
	h.now = h.now.Add(d)
}

type harnessOpts struct {
	keys    KeyAuthenticator
	prod    bool
	baseURL string
	proxies []string
	health  HealthSource
}

func newHarness(t testing.TB, o harnessOpts) *harness {
	t.Helper()
	h := &harness{log: &logBuf{}, now: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)}
	h.store = NewMemoryStore(h.clock)
	opts := Options{
		Logger:      NewLogger(h.log, true, slog.LevelDebug),
		Prod:        o.prod,
		Keys:        o.keys,
		Idempotency: h.store,
	}
	if o.baseURL != "" {
		opts.BaseURL = mustURL(o.baseURL)
	}
	for _, p := range o.proxies {
		opts.TrustedProxies = append(opts.TrustedProxies, mustPrefix(p))
	}
	h.srv = New(opts)
	h.srv.Mount(h.srv.SystemRoutes(SystemDeps{Health: o.health, Spec: []byte("openapi: 3.1.1\n"), Now: h.clock})...)
	h.srv.Mount(h.testRoutes()...)
	return h
}

type echoIn struct {
	A     int    `json:"a"`
	B     string `json:"b"`
	Block bool   `json:"block"`
	Fail  int    `json:"fail"` // respond with this status via an *Error
}

func (h *harness) testRoutes() []Route {
	return []Route{
		{Method: "POST", Pattern: "/v1/_test/echo", OperationID: "testEcho", Auth: AgentKey, Idempotency: IdemOptional, Extension: "test",
			Handler: func(w http.ResponseWriter, r *http.Request) error {
				var in echoIn
				if err := DecodeJSON(r, &in); err != nil {
					return err
				}
				n := h.runs.Add(1)
				if in.Block && h.release != nil {
					<-h.release
				}
				switch in.Fail {
				case 0:
				case 429:
					return ErrRateLimited(3)
				case 500:
					return errors.New("database exploded at 10.0.0.7 with password hunter2")
				default:
					return E(CodeConflict)
				}
				return WriteJSON(w, http.StatusCreated, map[string]any{"object": "echo", "a": in.A, "b": in.B, "run": n})
			}},
		{Method: "POST", Pattern: "/v1/_test/public", OperationID: "testPublic", Auth: Public, Idempotency: IdemOptional, Extension: "test",
			Handler: func(w http.ResponseWriter, r *http.Request) error {
				h.runs.Add(1)
				return WriteJSON(w, http.StatusOK, map[string]string{"object": "ok"})
			}},
		{Method: "POST", Pattern: "/v1/_test/secret", OperationID: "testSecret", Auth: AgentKey, Idempotency: IdemRequired, Sensitive: true, Extension: "test",
			Handler: func(w http.ResponseWriter, r *http.Request) error {
				h.runs.Add(1)
				return WriteJSON(w, http.StatusCreated, map[string]any{"object": "api_key", "id": testKey.String(), "secret": canarySecret, "livemode": false})
			}},
		{Method: "POST", Pattern: "/v1/_test/panic", OperationID: "testPanicPost", Auth: AgentKey, Idempotency: IdemOptional, Extension: "test",
			Handler: func(w http.ResponseWriter, r *http.Request) error {
				h.runs.Add(1)
				panic("boom at /Users/someone/secret/path.go")
			}},
		{Method: "GET", Pattern: "/v1/_test/panic", OperationID: "testPanic", Auth: Public, Extension: "test",
			Handler: func(w http.ResponseWriter, r *http.Request) error { panic("boom at /Users/someone/secret/path.go") }},
		{Method: "GET", Pattern: "/v1/_test/slow", OperationID: "testSlow", Auth: Public, Timeout: 50 * time.Millisecond, Extension: "test",
			Handler: func(w http.ResponseWriter, r *http.Request) error {
				<-r.Context().Done()
				return r.Context().Err()
			}},
		{Method: "GET", Pattern: "/v1/_test/scoped", OperationID: "testScoped", Auth: AgentKey, Scopes: []string{"deeds:read"}, Extension: "test",
			Handler: func(w http.ResponseWriter, r *http.Request) error {
				p, _ := PrincipalFrom(r.Context())
				return WriteJSON(w, http.StatusOK, map[string]string{"object": "whoami", "key": p.KeyID.String(), "version": APIVersion(r.Context())})
			}},
		{Method: "GET", Pattern: "/v1/_test/session", OperationID: "testSession", Auth: Session, MinRole: RoleViewer, Extension: "test",
			Handler: func(w http.ResponseWriter, r *http.Request) error {
				return WriteJSON(w, 200, map[string]string{"object": "me"})
			}},
		{Method: "GET", Pattern: "/v1/_test/things/{id}", OperationID: "testThing", Auth: Public, Extension: "test",
			Handler: func(w http.ResponseWriter, r *http.Request) error { return NotFound("thing", r.PathValue("id")) }},
	}
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) env(t testing.TB) envelopeError {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(r.body, &e, json.RejectUnknownMembers(true)); err != nil {
		t.Fatalf("not an error envelope (%v): %s", err, r.body)
	}
	return e.Error
}

type reqOpt func(*http.Request)

func hdr(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func bearer(k string) reqOpt { return hdr("Authorization", "Bearer "+k) }
func idem(k string) reqOpt   { return hdr("Idempotency-Key", k) }
func jsonCT() reqOpt         { return hdr("Content-Type", "application/json") }
func remote(a string) reqOpt { return func(r *http.Request) { r.RemoteAddr = a } }

func (h *harness) do(t testing.TB, method, target, body string, opts ...reqOpt) resp {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.srv.ServeHTTP(w, r)
	res := w.Result()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, body: b}
}

func (h *harness) post(t testing.TB, target, body string, opts ...reqOpt) resp {
	t.Helper()
	return h.do(t, "POST", target, body, append([]reqOpt{jsonCT()}, opts...)...)
}
