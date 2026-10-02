package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// fakeStreamServer scripts GET /v1/events connection by connection, and answers whoami, single
// actions and the actions list from a fixed set.
type fakeStreamServer struct {
	mu      sync.Mutex
	conns   []func(w http.ResponseWriter, lastID string)
	seen    []string // Last-Event-ID per connection
	actions map[string]ledger.Action
	list    []string // ids GET /v1/actions returns, newest first
	status  int      // non-zero: /v1/events answers this with an error envelope
	code    string
}

func (f *fakeStreamServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/whoami":
		_, _ = w.Write([]byte(`{"object":"whoami","org":{"id":"org_1","name":"Acme","slug":"acme","stopped":false},"agent":null}`))
	case r.URL.Path == "/v1/events":
		if f.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.status)
			fmt.Fprintf(w, `{"error":{"type":"x","code":%q,"message":"m","param":null,"request_id":"req_1","doc_url":"d"}}`, f.code)
			return
		}
		if r.URL.Query().Get("types") != "action.*,deed.sealed" {
			w.WriteHeader(400)
			return
		}
		f.seen = append(f.seen, r.Header.Get("Last-Event-ID"))
		if len(f.conns) == 0 {
			w.WriteHeader(503)
			return
		}
		next := f.conns[0]
		f.conns = f.conns[1:]
		w.Header().Set("Content-Type", "text/event-stream")
		next(w, r.Header.Get("Last-Event-ID"))
	case strings.HasPrefix(r.URL.Path, "/v1/actions/"):
		a, ok := f.actions[strings.TrimPrefix(r.URL.Path, "/v1/actions/")]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(a)
	case r.URL.Path == "/v1/actions":
		l := ledger.List[ledger.Action]{Object: "list", Data: []ledger.Action{}}
		for _, id := range f.list {
			l.Data = append(l.Data, f.actions[id])
		}
		_ = json.NewEncoder(w).Encode(l)
	default:
		w.WriteHeader(404)
	}
}

func sse(w http.ResponseWriter, id, typ string, data any) {
	b, _ := json.Marshal(data)
	if id != "" {
		fmt.Fprintf(w, "id: %s\n", id)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
}

func streamEnvelope(typ string, hydrated bool, obj any) map[string]any {
	return map[string]any{"object": "event", "id": "evt_1", "type": typ, "api_version": "2026-10-01", "created_at": "2026-10-05T10:14:03.000Z",
		"livemode": false, "org": "org_1", "hydrated": hydrated, "data": map[string]any{"object": obj}}
}

// followSSESetup pins the clock, the time zone and the reconnect schedule.
func followSSESetup(t *testing.T) {
	t.Helper()
	savedLocal, savedNow, savedStream := time.Local, followNow, followStream
	time.Local = time.UTC
	followNow = func() time.Time { return time.Date(2026, 10, 5, 10, 14, 0, 0, time.UTC) }
	followStream = func(s *deedglass.EventStream) {
		s.Sleep = func(context.Context, time.Duration) error { return nil }
		s.Jitter = func() float64 { return 0.5 }
	}
	t.Cleanup(func() { time.Local, followNow, followStream = savedLocal, savedNow, savedStream })
}

// TestFollowSSEGolden runs deedglass actions list --follow against a scripted stream: S15's lines,
// a resume that missed nothing, a resync mid-stream and one after a reconnect (each refreshing the
// list), and the end of the session (exit 3).
func TestFollowSSEGolden(t *testing.T) {
	followSSESetup(t)
	at := func(s string) ledger.Time {
		tm, _ := time.Parse(time.RFC3339, "2026-10-05T"+s+"Z")
		return ledger.Time(tm)
	}
	sp := func(s string) *string { return &s }
	i64 := func(n int64) *int64 { return &n }
	pending := ledger.Action{Object: "action", ID: "act_01k6aaaa5m7p9r1t3v5x7z9b1d", CreatedAt: at("10:14:03"), Tool: "orders.execute_sql",
		State: "pending_approval", Status: ledger.StatusPendingApproval, Policy: ledger.ActionPolicy{RuleID: sp("orders-writes-need-approval")},
		Approval: &ledger.ActionApproval{ID: "apr_01k6aaaa5m7p9r1t3v5x7z9b1f"}}
	executed := ledger.Action{Object: "action", ID: "act_01k6bbbb5m7p9r1t3v5x7z9b1d", CreatedAt: at("10:14:09"), Tool: "postgres.execute",
		State: "sealed", Status: ledger.StatusExecuted, Execution: &ledger.ActionExecution{LatencyMS: i64(41)},
		Witness: &ledger.ActionWitness{Level: "L2", Verdict: "VERIFIED"}}
	denied := ledger.Action{Object: "action", ID: "act_01k6cccc5m7p9r1t3v5x7z9b1d", CreatedAt: at("10:14:21"), Tool: "postgres.execute",
		State: "sealed", Status: ledger.StatusDenied, Policy: ledger.ActionPolicy{RuleID: sp("block-destructive-sql")}, Deed: sp("deed_01k6cccc5m7p9r1t3v5x7z9b1e")}
	missed := ledger.Action{Object: "action", ID: "act_01k6dddd5m7p9r1t3v5x7z9b1d", CreatedAt: at("10:14:37"), Tool: "everything.echo",
		State: "sealed", Status: ledger.StatusFailed, Execution: &ledger.ActionExecution{ErrorCode: sp("connector_unavailable")}}
	old := ledger.Action{Object: "action", ID: "act_01k6eeee5m7p9r1t3v5x7z9b1d", CreatedAt: at("09:00:00"), Tool: "orders.execute_sql",
		State: "sealed", Status: ledger.StatusExecuted}
	fs := &fakeStreamServer{actions: map[string]ledger.Action{}}
	for _, a := range []ledger.Action{pending, executed, denied, missed, old} {
		fs.actions[a.ID] = a
	}
	deed := func(act string) map[string]any {
		return map[string]any{"object": "deed_summary", "id": "deed_x", "seq": 9, "kind": "action", "action": act, "issued_at": "2026-10-05T10:14:10.000Z"}
	}
	fs.conns = []func(http.ResponseWriter, string){
		func(w http.ResponseWriter, _ string) {
			fmt.Fprint(w, "retry: 3000\n\n")
			sse(w, "f1.0", "hello", map[string]any{"object": "stream", "instance": "f1", "resumed": false, "heartbeat_s": 15, "server_time": "x"})
			sse(w, "f1.1", "action.created", streamEnvelope("action.created", false, map[string]any{"object": "action", "id": pending.ID, "state": "received"}))
			sse(w, "f1.2", "action.decided", streamEnvelope("action.decided", true, pending))
			fmt.Fprint(w, ": ping\n\n")
			sse(w, "f1.3", "deed.sealed", streamEnvelope("deed.sealed", true, deed(executed.ID)))
			sse(w, "f1.4", "deed.sealed", streamEnvelope("deed.sealed", true, deed(old.ID))) // created before the follow started
			sse(w, "f1.5", "deed.sealed", streamEnvelope("deed.sealed", true, map[string]any{"object": "deed_summary", "id": "deed_y", "kind": "admin", "action": nil}))
		},
		func(w http.ResponseWriter, _ string) {
			sse(w, "f1.5", "hello", map[string]any{"object": "stream", "instance": "f1", "resumed": true, "heartbeat_s": 15})
			sse(w, "f1.6", "deed.sealed", streamEnvelope("deed.sealed", true, deed(denied.ID)))
			fs.list = []string{missed.ID, denied.ID, executed.ID, pending.ID}
			sse(w, "", "resync", map[string]any{"object": "resync", "reason": "lagged"})
		},
		func(w http.ResponseWriter, _ string) {
			sse(w, "g2.40", "hello", map[string]any{"object": "stream", "instance": "g2", "resumed": false, "heartbeat_s": 15})
			sse(w, "g2.40", "resync", map[string]any{"object": "resync", "reason": "other_instance"})
			sse(w, "", "session_ended", map[string]any{"code": "api_key_revoked", "message": "This API key was revoked. Create a new key in Console › Agents."})
		},
	}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	r := cli(t, map[string]string{"DEEDGLASS_API_KEY": "dg_test_x"}, false, "actions", "list", "--follow", "--server", srv.URL)
	got := fmt.Sprintf("%s--- stderr\n%s--- exit %d\n", r.stdout, r.stderr, r.code)
	p := filepath.Join("..", "..", "testdata", "cli", "actions-follow-sse.txt")
	if *updateFollow {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(want, []byte(got)) {
		t.Fatalf("differs from %s (-update-follow):\n%s", p, got)
	}
	if fs.seen[0] != "" || fs.seen[1] != "f1.5" || fs.seen[2] != "f1.6" {
		t.Fatalf("Last-Event-ID per connection: %q", fs.seen)
	}
}

// Refusals: a key without actions:read is exit 3 with the sentence; a server without the stream
// falls back to polling the Ledger (here: no database configured, so its config error).
func TestFollowSSERefusals(t *testing.T) {
	followSSESetup(t)
	fs := &fakeStreamServer{status: 403, code: "insufficient_scope"}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	key, _, err := identity.NewAPIKey(identity.ModeTest)
	if err != nil {
		t.Fatal(err)
	}
	r := cli(t, map[string]string{"DEEDGLASS_API_KEY": key}, false, "actions", "list", "--follow", "--server", srv.URL)
	if r.code != 3 || !strings.Contains(r.stderr, "✕ This key can't read events. Create a key with actions:read in Console › Agents.") {
		t.Fatalf("no scope: %d %q", r.code, r.stderr)
	}
	fs.status, fs.code = 404, "not_found"
	r = cli(t, map[string]string{"DEEDGLASS_API_KEY": key}, false, "actions", "list", "--follow", "--server", srv.URL)
	if r.code == 0 || !strings.Contains(r.stderr+r.stdout, "DEEDGLASS_DATABASE_URL") || strings.Contains(r.stdout, "live") {
		t.Fatalf("404 falls back to polling: %d %q %q", r.code, r.stdout, r.stderr)
	}
}

func TestBackoffSchedule(t *testing.T) {
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		lo, hi := deedglass.Backoff(deedglass.DefaultBackoff, i, 0), deedglass.Backoff(deedglass.DefaultBackoff, i, 0.999999)
		if lo != want*8/10 || hi < want*119/100 || hi > want*12/10 {
			t.Errorf("attempt %d: %v..%v around %v", i, lo, hi, want)
		}
	}
}
