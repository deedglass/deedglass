package console

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
)

// ovClock is a settable clock.
type ovClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *ovClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *ovClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// ovFake counts reads and checks, and answers a minimal overview.
type ovFake struct {
	loads, checks atomic.Int64
	gate          chan struct{} // when set, load waits for it
	checkGate     chan struct{} // when set, check waits for it
	fail          atomic.Bool
	chainSeen     atomic.Pointer[ledger.ChainCheck]
	broken        atomic.Bool
}

func (f *ovFake) load(ctx context.Context, org ids.OrgID, o ledger.OverviewOptions) (*ledger.Overview, error) {
	f.loads.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	if f.fail.Load() {
		return nil, errors.New("database down")
	}
	f.chainSeen.Store(o.Chain)
	from, to := o.Window.Bounds(o.Now)
	return &ledger.Overview{Object: "overview", Window: o.Window.Name, From: httpx.Time(from), To: httpx.Time(to),
		GeneratedAt: httpx.Time(o.Now), Timezone: "UTC", Agents: ledger.OverviewAgents{Object: "list", Data: []ledger.OverviewAgent{}},
		Attention: []ledger.AttentionItem{}}, nil
}

func (f *ovFake) check(ctx context.Context, org ids.OrgID, since time.Time) (*ledger.ChainCheck, error) {
	f.checks.Add(1)
	if f.checkGate != nil {
		<-f.checkGate
	}
	if f.broken.Load() {
		return &ledger.ChainCheck{Status: ledger.CheckBroken, Break: &ledger.ChainBreak{Code: "body_hash_mismatch"}}, nil
	}
	return &ledger.ChainCheck{Status: ledger.CheckIntact}, nil
}

type ovServer struct {
	auth  *evAuth
	api   *httpx.Server
	ov    *Overview
	fake  *ovFake
	clock *ovClock
}

func newOvServer(t *testing.T, f *ovFake) *ovServer {
	t.Helper()
	a := &evAuth{org: ids.New[ids.OrgKind](), agent: ids.New[ids.AgentKind](), user: ids.New[ids.UserKind]()}
	c := &ovClock{t: time.Date(2026, 10, 9, 13, 59, 58, 0, time.UTC)}
	ov := NewOverview(OverviewOptions{Now: c.Now, load: f.load, check: f.check})
	t.Cleanup(ov.Wait)
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Keys: a, Sessions: a})
	api.Mount(ov.Routes()...)
	return &ovServer{auth: a, api: api, ov: ov, fake: f, clock: c}
}

func (s *ovServer) get(target, cred string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", target, nil)
	switch {
	case cred == "session":
		r.AddCookie(&http.Cookie{Name: "deedglass_session", Value: evCookie})
	case cred != "":
		r.Header.Set("Authorization", "Bearer "+cred)
	}
	w := httptest.NewRecorder()
	s.api.ServeHTTP(w, r)
	return w
}

func errCode(t *testing.T, body string) (code, param, msg string) {
	t.Helper()
	var e struct {
		Error struct {
			Code    string  `json:"code"`
			Param   *string `json:"param"`
			Message string  `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &e)
	p := ""
	if e.Error.Param != nil {
		p = *e.Error.Param
	}
	return e.Error.Code, p, e.Error.Message
}

// TestOverviewRoleMatrix: a viewer session and an org admin key read it; agent keys (with or
// without actions:read) get 403 insufficient_scope; no credential is 401.
func TestOverviewRoleMatrix(t *testing.T) {
	s := newOvServer(t, &ovFake{})
	for _, c := range []struct {
		cred   string
		status int
		code   string
	}{
		{"session", 200, ""},
		{evAdminKey, 200, ""},
		{evAgentKey, 403, "insufficient_scope"},
		{evNoScope, 403, "insufficient_scope"},
		{"", 401, "authentication_required"},
		{"dg_test_nonsense", 401, "invalid_api_key"},
	} {
		w := s.get("/v1/overview", c.cred)
		if w.Code != c.status {
			t.Errorf("%q: HTTP %d %s, want %d", c.cred, w.Code, w.Body, c.status)
			continue
		}
		code, _, msg := errCode(t, w.Body.String())
		if code != c.code {
			t.Errorf("%q: code %q, want %q", c.cred, code, c.code)
		}
		if c.code == "insufficient_scope" && msg != MsgOverviewAgentKey {
			t.Errorf("agent key message %q", msg)
		}
		if c.status == 200 {
			var ov ledger.Overview
			if err := json.Unmarshal(w.Body.Bytes(), &ov); err != nil || ov.Object != "overview" || ov.Window != "24h" {
				t.Errorf("%q: body %s", c.cred, w.Body)
			}
			if w.Header().Get("Cache-Control") != "private, no-store" {
				t.Errorf("Cache-Control %q", w.Header().Get("Cache-Control"))
			}
		}
	}
	if r := s.ov.Routes()[0]; r.MinRole != httpx.RoleViewer || len(r.Scopes) != 1 || r.Scopes[0] != httpx.ScopeAdmin || r.Auth != httpx.SessionOrKey {
		t.Errorf("route declaration: %+v", r)
	}
}

// TestOverviewQuery: window is 24h or 7d, once; nothing else is accepted.
func TestOverviewQuery(t *testing.T) {
	s := newOvServer(t, &ovFake{})
	for _, c := range []struct {
		q, param, window string
	}{
		{"", "", "24h"},
		{"?window=24h", "", "24h"},
		{"?window=7d", "", "7d"},
		{"?window=1h", "window", ""},
		{"?window=7D", "window", ""},
		{"?window=", "window", ""},
		{"?window=24h&window=7d", "window", ""},
		{"?window=24h&agent=agt_x", "agent", ""},
		{"?expand[]=usage", "expand[]", ""},
	} {
		w := s.get("/v1/overview"+c.q, "session")
		if c.param != "" {
			code, param, msg := errCode(t, w.Body.String())
			if w.Code != 400 || code != "validation_failed" || param != c.param || msg == "" {
				t.Errorf("%s: HTTP %d %s, want 400 validation_failed on %s", c.q, w.Code, w.Body, c.param)
			}
			continue
		}
		var ov ledger.Overview
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ov) != nil || ov.Window != c.window {
			t.Errorf("%s: HTTP %d %s", c.q, w.Code, w.Body)
		}
	}
	if n := s.fake.loads.Load(); n != 2 {
		t.Errorf("%d reads for two windows within 10 s, want 2", n)
	}
}

// TestOverviewCache: one read per org and window per 10 s, shared by concurrent callers; errors
// are never cached; old entries are swept.
func TestOverviewCache(t *testing.T) {
	f := &ovFake{gate: make(chan struct{})}
	s := newOvServer(t, f)
	org := s.auth.org
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ov.Get(context.Background(), org, ledger.Window24h); err != nil {
				t.Error(err)
			}
		}()
	}
	for f.loads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the others join the flight
	close(f.gate)
	wg.Wait()
	if n := f.loads.Load(); n != 1 {
		t.Fatalf("50 concurrent misses made %d reads, want 1", n)
	}
	s.clock.Add(9 * time.Second)
	_, _ = s.ov.Get(context.Background(), org, ledger.Window24h)
	if n := f.loads.Load(); n != 1 {
		t.Fatalf("a read at 9 s: %d reads, want 1 (cached)", n)
	}
	s.clock.Add(time.Second)
	_, _ = s.ov.Get(context.Background(), org, ledger.Window24h)
	if n := f.loads.Load(); n != 2 {
		t.Fatalf("a read at 10 s: %d reads, want 2", n)
	}
	// Another org never shares an answer.
	_, _ = s.ov.Get(context.Background(), ids.New[ids.OrgKind](), ledger.Window24h)
	if n := f.loads.Load(); n != 3 {
		t.Fatalf("another org: %d reads, want 3", n)
	}
	// Errors aren't cached.
	f.fail.Store(true)
	s.clock.Add(11 * time.Second)
	if _, err := s.ov.Get(context.Background(), org, ledger.Window24h); err == nil {
		t.Fatal("no error")
	}
	f.fail.Store(false)
	if _, err := s.ov.Get(context.Background(), org, ledger.Window24h); err != nil {
		t.Fatal(err)
	}
	if n := f.loads.Load(); n != 5 {
		t.Fatalf("after a failure: %d reads, want 5", n)
	}
	// Sweeping: entries older than a minute go.
	s.clock.Add(2 * time.Minute)
	_, _ = s.ov.Get(context.Background(), org, ledger.Window7d)
	s.ov.mu.Lock()
	n := len(s.ov.cache)
	s.ov.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d cache entries after the sweep, want 1", n)
	}
	// A negative TTL turns the cache off (the bench).
	s.ov.o.TTL = -1
	before := f.loads.Load()
	_, _ = s.ov.Get(context.Background(), org, ledger.Window7d)
	_, _ = s.ov.Get(context.Background(), org, ledger.Window7d)
	if f.loads.Load() != before+2 {
		t.Fatal("the cache answered with the cache off")
	}
}

// TestOverviewChainCheck: the first overview has no chain result and never waits for the check;
// a finished check is seen by the next read; checks run at most once per 5 minutes per org.
func TestOverviewChainCheck(t *testing.T) {
	f := &ovFake{checkGate: make(chan struct{})}
	f.broken.Store(true)
	s := newOvServer(t, f)
	s.ov.o.TTL = -1
	org := s.auth.org
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := s.ov.Get(context.Background(), org, ledger.Window24h); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the overview waited for the chain check")
	}
	if f.chainSeen.Load() != nil {
		t.Fatal("the first overview had a chain result")
	}
	_, _ = s.ov.Get(context.Background(), org, ledger.Window24h) // the check still runs: no second one
	close(f.checkGate)
	s.ov.Wait()
	if n := f.checks.Load(); n != 1 {
		t.Fatalf("%d checks, want 1", n)
	}
	_, _ = s.ov.Get(context.Background(), org, ledger.Window24h)
	if c := f.chainSeen.Load(); c == nil || c.Status != ledger.CheckBroken {
		t.Fatalf("the finished check wasn't passed on: %+v", c)
	}
	s.clock.Add(4 * time.Minute)
	_, _ = s.ov.Get(context.Background(), org, ledger.Window24h)
	s.ov.Wait()
	if n := f.checks.Load(); n != 1 {
		t.Fatalf("a check within 5 minutes: %d checks, want 1", n)
	}
	s.clock.Add(time.Minute)
	f.broken.Store(false)
	_, _ = s.ov.Get(context.Background(), org, ledger.Window24h)
	s.ov.Wait()
	if n := f.checks.Load(); n != 2 {
		t.Fatalf("after 5 minutes: %d checks, want 2", n)
	}
	_, _ = s.ov.Get(context.Background(), org, ledger.Window24h)
	if c := f.chainSeen.Load(); c == nil || c.Status != ledger.CheckIntact {
		t.Fatalf("the newer check wasn't passed on: %+v", c)
	}
}

// TestOverviewNoLedger: the route-table wiring answers 503 with a next step.
func TestOverviewNoLedger(t *testing.T) {
	a := &evAuth{org: ids.New[ids.OrgKind](), agent: ids.New[ids.AgentKind](), user: ids.New[ids.UserKind]()}
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Keys: a, Sessions: a})
	api.Mount(NewOverview(OverviewOptions{}).Routes()...)
	r := httptest.NewRequest("GET", "/v1/overview", nil)
	r.AddCookie(&http.Cookie{Name: "deedglass_session", Value: evCookie})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	code, _, msg := errCode(t, w.Body.String())
	if w.Code != 503 || code != "service_unavailable" || !strings.Contains(msg, "Try again") {
		t.Fatalf("HTTP %d %s", w.Code, w.Body)
	}
}
