package console

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
)

// evAuth stands in for S07's sessions and S08's keys: a known cookie is a viewer of evOrg; three
// keys are an agent key with actions:read, an agent key without it and an org admin key. revoked
// flips every credential to refused (the 60 s re-check).
type evAuth struct {
	org     ids.OrgID
	agent   ids.AgentID
	user    ids.UserID
	revoked atomic.Bool
}

const (
	evCookie   = "ev-session"
	evAgentKey = "dg_test_agent_with_read"
	evNoScope  = "dg_test_agent_without_read"
	evAdminKey = "dg_test_admin"
)

func (a *evAuth) AuthenticateKey(_ context.Context, bearer string) (httpx.Principal, error) {
	if a.revoked.Load() {
		return httpx.Principal{}, httpx.E(httpx.CodeAPIKeyRevoked)
	}
	switch bearer {
	case evAgentKey:
		return httpx.Principal{Kind: httpx.KindAgentKey, OrgID: a.org, AgentID: a.agent, KeyID: ids.New[ids.APIKeyKind](), AgentName: "support-bot",
			AgentStatus: "active", Scopes: []string{httpx.ScopeActionsExecute, httpx.ScopeActionsRead}, APIVersion: "2026-10-01"}, nil
	case evNoScope:
		return httpx.Principal{Kind: httpx.KindAgentKey, OrgID: a.org, AgentID: a.agent, KeyID: ids.New[ids.APIKeyKind](), AgentName: "support-bot",
			AgentStatus: "active", Scopes: []string{httpx.ScopeActionsExecute}, APIVersion: "2026-10-01"}, nil
	case evAdminKey:
		return httpx.Principal{Kind: httpx.KindAdminKey, OrgID: a.org, KeyID: ids.New[ids.APIKeyKind](), Role: httpx.RoleAdmin,
			Scopes: []string{httpx.ScopeAdmin}, APIVersion: "2026-10-01"}, nil
	}
	return httpx.Principal{}, httpx.E(httpx.CodeInvalidAPIKey)
}

func (a *evAuth) AuthenticateSession(_ context.Context, r *http.Request) (httpx.Principal, error) {
	c, err := r.Cookie("deedglass_session")
	if err != nil || c.Value != evCookie {
		return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	if a.revoked.Load() {
		return httpx.Principal{}, httpx.E(httpx.CodeSessionExpired, httpx.Msg("Your session ended after 12 hours without activity. Sign in again."))
	}
	return httpx.Principal{Kind: httpx.KindUser, OrgID: a.org, UserID: a.user, Role: httpx.RoleViewer, OrgName: "Acme", APIVersion: "2026-10-01"}, nil
}

// evServer mounts GET /v1/events with a running hub.
type evServer struct {
	auth *evAuth
	hub  *Hub
	in   chan ledger.BusMsg
	api  *httpx.Server
}

func newEvServer(t *testing.T, o HubOptions) *evServer {
	t.Helper()
	a := &evAuth{org: ids.New[ids.OrgKind](), agent: ids.New[ids.AgentKind](), user: ids.New[ids.UserKind]()}
	hub := NewHub(o)
	in := make(chan ledger.BusMsg, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = hub.Run(ctx, in) }()
	t.Cleanup(func() { cancel(); <-done })
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Keys: a, Sessions: a})
	api.Mount(EventRoutes(EventsOptions{Hub: hub, Recheck: api.Recheck})...)
	return &evServer{auth: a, hub: hub, in: in, api: api}
}

func (s *evServer) request(target string, cred string) *http.Request {
	r := httptest.NewRequest("GET", target, nil)
	switch {
	case cred == "session":
		r.AddCookie(&http.Cookie{Name: "deedglass_session", Value: evCookie})
	case cred != "":
		r.Header.Set("Authorization", "Bearer "+cred)
	}
	return r
}

// sseRecorder is a ResponseWriter that streams: Flush works and writes are readable while the
// handler runs.
type sseRecorder struct {
	mu     sync.Mutex
	h      http.Header
	status int
	b      bytes.Buffer
}

func newSSERecorder() *sseRecorder { return &sseRecorder{h: http.Header{}} }

func (w *sseRecorder) Header() http.Header { return w.h }
func (w *sseRecorder) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = code
	}
}

func (w *sseRecorder) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = 200
	}
	return w.b.Write(p)
}
func (w *sseRecorder) Flush() {}
func (w *sseRecorder) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func (w *sseRecorder) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

// sseMsg is one parsed SSE message.
type sseMsg struct {
	ID, Event, Data string
	Comment         bool
}

// parseSSE splits a stream into messages (comments included).
func parseSSE(s string) []sseMsg {
	var out []sseMsg
	for _, block := range strings.Split(s, "\n\n") {
		if block == "" {
			continue
		}
		var m sseMsg
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, ": "):
				m.Comment = true
				m.Data = line[2:]
			case strings.HasPrefix(line, "id: "):
				m.ID = line[4:]
			case strings.HasPrefix(line, "event: "):
				m.Event = line[7:]
			case strings.HasPrefix(line, "data: "):
				m.Data = line[6:]
			case strings.HasPrefix(line, "retry: "):
				m.Event, m.Data = "retry", line[7:]
			}
		}
		out = append(out, m)
	}
	return out
}

// TestEventsWire runs the route on a real listener: headers, retry, hello, a live event in S22's
// envelope plus hydrated, and resume.
func TestEventsWire(t *testing.T) {
	s := newEvServer(t, HubOptions{Instance: "wire01"})
	srv := httptest.NewServer(s.api)
	defer srv.Close()
	open := func(cursor string) (*http.Response, *bufio.Reader) {
		req, _ := http.NewRequest("GET", srv.URL+"/v1/events?types=action.*,deed.sealed", nil)
		req.AddCookie(&http.Cookie{Name: "deedglass_session", Value: evCookie})
		if cursor != "" {
			req.Header.Set("Last-Event-ID", cursor)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res, bufio.NewReader(res.Body)
	}
	next := func(br *bufio.Reader) sseMsg {
		var block strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended: %v (%q)", err, block.String())
			}
			if line == "\n" {
				ms := parseSSE(block.String())
				if len(ms) == 1 {
					return ms[0]
				}
				block.Reset()
				continue
			}
			block.WriteString(line)
		}
	}
	res, br := open("")
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", res.StatusCode, res.Header)
	}
	if m := next(br); m.Event != "retry" || m.Data != "3000" {
		t.Fatalf("first: %+v", m)
	}
	hm := next(br)
	var hello map[string]any
	_ = json.Unmarshal([]byte(hm.Data), &hello)
	if hm.Event != "hello" || hm.ID != "wire01.0" || hello["object"] != "stream" || hello["instance"] != "wire01" || hello["resumed"] != false ||
		hello["heartbeat_s"] != float64(15) || len(hello["server_time"].(string)) != 24 {
		t.Fatalf("hello: %+v", hm)
	}
	org := s.auth.org.String()
	s.in <- ledger.BusMsg{Event: evt(1, org, ledger.EventActionCreated, "act_01k6h7mz3c9d8e7f6g5h4j3k2m")}
	s.in <- ledger.BusMsg{Event: evt(2, org, ledger.EventAgentStopped, "agt_01k6h7mz3c9d8e7f6g5h4j3k2m")} // filtered out by types
	s.in <- ledger.BusMsg{Event: evt(3, org, ledger.EventDeedSealed, "deed_01k6h7mz3c9d8e7f6g5h4j3k2m", withAction("act_01k6h7mz3c9d8e7f6g5h4j3k2m"), withKind("action"))}
	s.in <- ledger.BusMsg{Event: evt(4, "org_someone_else", ledger.EventActionCreated, "act_x")}
	a := next(br)
	var env map[string]any
	_ = json.Unmarshal([]byte(a.Data), &env)
	if a.Event != "action.created" || a.ID != "wire01.1" || env["object"] != "event" || env["type"] != "action.created" || env["org"] != org ||
		env["hydrated"] != false || env["api_version"] != "2026-10-01" {
		t.Fatalf("action event: %+v", a)
	}
	d := next(br)
	if d.Event != "deed.sealed" || d.ID != "wire01.3" || !strings.Contains(d.Data, `"object":"deed_summary"`) {
		t.Fatalf("deed event: %+v", d)
	}
	res.Body.Close()

	// Resume from the first event: the deed again, nothing else, resumed true.
	res2, br2 := open(a.ID)
	defer res2.Body.Close()
	next(br2)
	h2 := next(br2)
	if !strings.Contains(h2.Data, `"resumed":true`) {
		t.Fatalf("resumed hello: %+v", h2)
	}
	if r := next(br2); r.ID != "wire01.3" || r.Event != "deed.sealed" {
		t.Fatalf("replay: %+v", r)
	}
	// A cursor from another instance: hello (not resumed), then resync, then live.
	res3, br3 := open("abc123.5")
	defer res3.Body.Close()
	next(br3)
	if h := next(br3); !strings.Contains(h.Data, `"resumed":false`) {
		t.Fatalf("%+v", h)
	}
	if r := next(br3); r.Event != "resync" || r.Data != `{"object":"resync","reason":"other_instance"}` || r.ID == "" {
		t.Fatalf("resync: %+v", r)
	}
}

// Refusals come before the first byte, as the error envelope.
func TestEventsRefusals(t *testing.T) {
	s := newEvServer(t, HubOptions{Instance: "ref001", MaxPerUser: 2})
	other := ids.New[ids.AgentKind]().String()
	for _, c := range []struct {
		target, cred string
		status       int
		code, param  string
	}{
		{"/v1/events", "", 401, "authentication_required", ""},
		{"/v1/events?types=nope.*", "session", 400, "validation_failed", "types"},
		{"/v1/events?types=action.created,webhook.test", "session", 400, "validation_failed", "types"},
		{"/v1/events?agent=act_1", "session", 400, "validation_failed", "agent"},
		{"/v1/events?since=1", "session", 400, "validation_failed", "since"},
		{"/v1/events?types=action.*&types=deed.sealed", "session", 400, "validation_failed", "types"},
		{"/v1/events?last_event_id=../../x", "session", 400, "validation_failed", "last_event_id"},
		{"/v1/events?agent=" + other, evAgentKey, 400, "validation_failed", "agent"},
		{"/v1/events", evNoScope, 403, "insufficient_scope", ""},
		{"/v1/events", "dg_test_unknown", 401, "invalid_api_key", ""},
	} {
		w := httptest.NewRecorder()
		s.api.ServeHTTP(w, s.request(c.target, c.cred))
		var env struct {
			Error struct{ Code, Param string } `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if w.Code != c.status || env.Error.Code != c.code || env.Error.Param != c.param || strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
			t.Errorf("%s as %q: %d %s", c.target, c.cred, w.Code, w.Body.String())
		}
	}
	// A header cursor wins over the query: a bad query value is ignored when the header is set.
	r := s.request("/v1/events?last_event_id=bad!", "session")
	r.Header.Set("Last-Event-ID", "ref001.0")
	ctx, cancel := context.WithCancel(r.Context())
	w := newSSERecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.api.ServeHTTP(w, r.WithContext(ctx)) }()
	waitFor(t, func() bool { return strings.Contains(w.String(), "event: hello") })
	cancel()
	<-done
	if !strings.Contains(w.String(), `"resumed":true`) {
		t.Fatalf("the header cursor: %s", w.String())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 500 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// The 9th connection of one person is 429 rate_limited with the Console's sentence.
func TestEventsPerUserLimit(t *testing.T) {
	s := newEvServer(t, HubOptions{Instance: "lim002"})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range 8 {
		w := newSSERecorder()
		wg.Go(func() { s.api.ServeHTTP(w, s.request("/v1/events", "session").WithContext(ctx)) })
		waitFor(t, func() bool { return strings.Contains(w.String(), "event: hello") })
	}
	w := httptest.NewRecorder()
	s.api.ServeHTTP(w, s.request("/v1/events", "session"))
	var env struct {
		Error struct{ Code, Message, Type string } `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if w.Code != 429 || env.Error.Code != "rate_limited" || env.Error.Message != "You have 8 live connections open. Close a Console tab and try again." {
		t.Fatalf("9th: %d %s", w.Code, w.Body.String())
	}
	// Another principal of the same org is unaffected.
	w2 := newSSERecorder()
	wg.Go(func() { s.api.ServeHTTP(w2, s.request("/v1/events", evAdminKey).WithContext(ctx)) })
	waitFor(t, func() bool { return strings.Contains(w2.String(), "event: hello") })
	cancel()
	wg.Wait()
	if s.hub.Stats().Streams != 0 {
		t.Fatalf("closed streams are released: %+v", s.hub.Stats())
	}
}

// An agent key's stream: its own agent's events only; the admin key sees the org.
func TestEventsAgentKey(t *testing.T) {
	hyd := newFakeHydrator(nil)
	s := newEvServer(t, HubOptions{Instance: "agk002", Hydrator: hyd, Coalesce: time.Millisecond})
	mine, theirs := "act_"+strings.Repeat("1", 26), "act_"+strings.Repeat("2", 26)
	hyd.agentOf = map[string]string{mine: s.auth.agent.String(), theirs: ids.New[ids.AgentKind]().String()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agentW, adminW := newSSERecorder(), newSSERecorder()
	var wg sync.WaitGroup
	wg.Go(func() { s.api.ServeHTTP(agentW, s.request("/v1/events", evAgentKey).WithContext(ctx)) })
	wg.Go(func() { s.api.ServeHTTP(adminW, s.request("/v1/events", evAdminKey).WithContext(ctx)) })
	waitFor(t, func() bool {
		return strings.Contains(agentW.String(), "event: hello") && strings.Contains(adminW.String(), "event: hello")
	})
	org := s.auth.org.String()
	s.in <- ledger.BusMsg{Event: evt(1, org, ledger.EventActionCreated, theirs)}
	s.in <- ledger.BusMsg{Event: evt(2, org, ledger.EventActionCreated, mine)}
	s.in <- ledger.BusMsg{Event: evt(3, org, ledger.EventOrgStopped, org)}
	waitFor(t, func() bool {
		return strings.Count(adminW.String(), "event: ") >= 4 && strings.Contains(agentW.String(), mine)
	})
	time.Sleep(50 * time.Millisecond)
	cancel()
	wg.Wait()
	if strings.Contains(agentW.String(), theirs) || !strings.Contains(agentW.String(), mine) || strings.Contains(agentW.String(), "org.stopped") {
		t.Fatalf("agent key stream:\n%s", agentW.String())
	}
	if !strings.Contains(adminW.String(), theirs) || !strings.Contains(adminW.String(), "org.stopped") {
		t.Fatalf("admin key stream:\n%s", adminW.String())
	}
}

// The timers, in a synctest bubble: a ping every 15 s while idle, the close at 30 minutes, and a
// credential revoked mid-stream ends it with session_ended within 60 s.
func TestEventsTimers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newEvServer(t, HubOptions{Instance: "tim001"})
		w := newSSERecorder()
		done := make(chan struct{})
		go func() { defer close(done); s.api.ServeHTTP(w, s.request("/v1/events", "session")) }()
		synctest.Wait()
		if !strings.Contains(w.String(), "event: hello") || strings.Contains(w.String(), ": ping") {
			t.Fatalf("at 0 s: %q", w.String())
		}
		time.Sleep(15*time.Second + time.Millisecond)
		synctest.Wait()
		if n := strings.Count(w.String(), ": ping\n\n"); n != 1 {
			t.Fatalf("at 15 s: %d pings", n)
		}
		time.Sleep(45 * time.Second)
		synctest.Wait()
		if n := strings.Count(w.String(), ": ping\n\n"); n != 4 {
			t.Fatalf("at 60 s: %d pings", n)
		}
		time.Sleep(30*time.Minute - 60*time.Second)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("the stream is still open at 30 minutes")
		}
		if strings.Contains(w.String(), "session_ended") {
			t.Fatal("a valid session isn't ended")
		}

		// Revoked: session_ended within 60 s, then the stream closes.
		w2 := newSSERecorder()
		done2 := make(chan struct{})
		go func() { defer close(done2); s.api.ServeHTTP(w2, s.request("/v1/events", evAgentKey)) }()
		synctest.Wait()
		time.Sleep(10 * time.Second)
		s.auth.revoked.Store(true)
		time.Sleep(50*time.Second + time.Millisecond)
		synctest.Wait()
		select {
		case <-done2:
		default:
			t.Fatal("a revoked key still streams after 60 s")
		}
		ms := parseSSE(w2.String())
		last := ms[len(ms)-1]
		want, _ := json.Marshal(SessionEnded{Code: "api_key_revoked", Message: httpx.E(httpx.CodeAPIKeyRevoked).Message})
		if last.Event != "session_ended" || last.Data != string(want) {
			t.Fatalf("last message: %+v", last)
		}
		s.hub.Close()
	})
}

// A stalled client is cut off with resync lagged; the others carry on.
func TestEventsLaggedWriter(t *testing.T) {
	s := newEvServer(t, HubOptions{Instance: "lag001", SubBuffer: 64})
	pr, pw := io.Pipe() // nobody reads: the handler's first event write blocks
	stalled := &pipeWriter{h: http.Header{}, w: pw}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.api.ServeHTTP(stalled, s.request("/v1/events", "session").WithContext(ctx))
	}()
	// Read only the preamble, then stop reading.
	br := bufio.NewReader(pr)
	for {
		line, _ := br.ReadString('\n')
		if strings.HasPrefix(line, "data: {\"object\":\"stream\"") {
			_, _ = br.ReadString('\n')
			break
		}
	}
	fast := newSSERecorder()
	go s.api.ServeHTTP(fast, s.request("/v1/events", evAdminKey).WithContext(ctx))
	waitFor(t, func() bool { return strings.Contains(fast.String(), "event: hello") })
	org := s.auth.org.String()
	for n := range 200 {
		s.in <- ledger.BusMsg{Event: evt(n, org, ledger.EventActionCreated, "act_"+strings.Repeat("3", 26))}
	}
	waitFor(t, func() bool { return strings.Count(fast.String(), "event: action.created") == 200 })
	waitFor(t, func() bool { return s.hub.Stats().Lagged == 1 })
	go func() { _, _ = io.Copy(io.Discard, br) }() // let the stalled writer finish
	<-done
	pr.Close()
}

type pipeWriter struct {
	h http.Header
	w *io.PipeWriter
}

func (p *pipeWriter) Header() http.Header         { return p.h }
func (p *pipeWriter) WriteHeader(int)             {}
func (p *pipeWriter) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeWriter) Flush()                      {}
