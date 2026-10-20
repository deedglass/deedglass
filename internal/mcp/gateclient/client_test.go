package gateclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/gate"
)

const actID = "act_01k6w4a3d5f7h9k1n3q5s7v9x1"

func action(status string, extra string) string {
	return `{"object":"action","id":"` + actID + `","status":"` + status + `","state":"x"` + extra + `}`
}

// TestSubmitStatusTable maps every row of S13's status table (as the wrapper sees it) to a kind.
func TestSubmitStatusTable(t *testing.T) {
	denied := `{"error":{"type":"policy_error","code":"policy_denied","message":"Blocked by policy \"x\": no.","action":` + action("denied", `,"deed":"deed_01k6w4a3d5f7h9k1n3q5s7v9x1"`) + `}}`
	cases := []struct {
		name   string
		status int
		body   string
		want   Kind
		calls  int32
	}{
		{"authorized", 200, action("authorized", `,"next_action":{"type":"execute_at_edge"}`), Authorized, 1},
		{"pending", 202, action("pending_approval", `,"approval":{"id":"apr_01k6w4a3d5f7h9k1n3q5s7v9x1","status":"pending","expires_at":"2026-10-05T10:42:00.000Z"}`), Pending, 1},
		{"denied 403", 403, denied, Denied, 1},
		{"budget 429 with action", 429, strings.Replace(denied, "policy_denied", "budget_exceeded", 1), Denied, 1},
		{"stopped 403 with action", 403, strings.Replace(denied, "policy_denied", "agent_stopped", 1), Denied, 1},
		{"tool_unknown 404 with action", 404, strings.Replace(denied, "policy_denied", "tool_unknown", 1), Denied, 1},
		{"401", 401, `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"x"}}`, BadKey, 1},
		{"400 validation", 400, `{"error":{"type":"invalid_request_error","code":"validation_failed","message":"args has a key that appears twice in one object. Send each key once."}}`, Rejected, 1},
		{"403 intent_required", 403, `{"error":{"type":"permission_error","code":"intent_required","message":"Sign it."}}`, Rejected, 1},
		{"500 retried twice", 500, `{"error":{"type":"api_error","code":"internal_error","message":"x"}}`, Unreachable, 3},
		{"503 retried twice", 503, ``, Unreachable, 3},
		{"200 with an unexpected body", 200, `{"hello":"world"}`, Unreachable, 1},
		{"200 executed (gateway answer)", 200, action("executed", ""), Unreachable, 1},
		{"200 authorized without next_action", 200, action("authorized", ""), Unreachable, 1},
		{"202 processing", 202, action("processing", ""), Unreachable, 1},
		{"200 not JSON", 200, `<html>`, Unreachable, 1},
		{"403 with an unreadable action", 403, `{"error":{"code":"policy_denied","message":"m","action":{"id":"nope"}}}`, Unreachable, 1},
		{"302", 302, ``, Unreachable, 1},
	}
	for _, k := range cases {
		t.Run(k.name, func(t *testing.T) {
			var calls atomic.Int32
			var keys []string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				if r.Header.Get("Deedglass-Session") != "mcp:scratch:x" || r.Header.Get("Authorization") != "Bearer dg_test_k" {
					t.Error("headers")
				}
				var b map[string]any
				_ = json.NewDecoder(r.Body).Decode(&b)
				if b["mode"] != "proxy" || b["tool"] != "scratch.echo" {
					t.Errorf("body %v", b)
				}
				w.Header().Set("Content-Type", "application/json")
				if k.status == 302 {
					w.Header().Set("Location", "/elsewhere")
				}
				w.WriteHeader(k.status)
				_, _ = io.WriteString(w, k.body)
			}))
			defer ts.Close()
			c := New(Options{Server: ts.URL, APIKey: "dg_test_k", Session: "mcp:scratch:x", Sleep: func(time.Duration) {},
				HTTP: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
			d := c.Submit(context.Background(), SubmitRequest{Tool: "scratch.echo", Args: json.RawMessage(`{"m":1}`), Transport: "mcp_stdio"})
			if d.Kind != k.want {
				t.Fatalf("kind %v, want %v (%v)", d.Kind, k.want, d.Err)
			}
			if calls.Load() != k.calls {
				t.Fatalf("%d calls", calls.Load())
			}
			for _, key := range keys {
				if key != keys[0] || !strings.HasPrefix(key, "mcpw_") {
					t.Fatalf("keys %v", keys)
				}
			}
			if k.want == Denied && (d.Action == nil || d.Action.Deed == nil) {
				t.Fatal("denied without its action")
			}
		})
	}
}

func TestSubmitUnreachable(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	c := New(Options{Server: url, APIKey: "k", Sleep: func(time.Duration) {}})
	if d := c.Submit(context.Background(), SubmitRequest{Tool: "a.b"}); d.Kind != Unreachable || d.Err == nil || strings.Contains(d.Err.Error(), "dg_") {
		t.Fatal(d)
	}
}

func TestSubmitTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer ts.Close()
	c := New(Options{Server: ts.URL, APIKey: "k", SubmitTimeout: 50 * time.Millisecond, Sleep: func(time.Duration) {}})
	if d := c.Submit(context.Background(), SubmitRequest{Tool: "a.b"}); d.Kind != Unreachable {
		t.Fatal(d.Kind)
	}
}

func TestReport(t *testing.T) {
	var calls atomic.Int32
	var bodies []map[string]any
	var keys []string
	mode := "ok"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		switch {
		case r.Method == "GET":
			_, _ = io.WriteString(w, action("executed", `,"result_digest":"`+bodies[0]["result_digest"].(string)+`"`))
		case mode == "flaky" && n < 3:
			w.WriteHeader(502)
		case mode == "toolarge" && n == 1:
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"code":"validation_failed","message":"result is larger than the limit."}}`)
		case mode == "conflict":
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"error":{"code":"conflict","message":"A result was already reported for this action."}}`)
		default:
			_, _ = io.WriteString(w, action("executed", ""))
		}
	}))
	defer ts.Close()
	var slept []time.Duration
	c := New(Options{Server: ts.URL, APIKey: "k", Sleep: func(d time.Duration) { slept = append(slept, d) }})
	r := gate.EdgeResult{Status: gate.ExecSucceeded, StartedAt: time.Now(), FinishedAt: time.Now(), Result: json.RawMessage(`{"content":[]}`)}
	for _, m := range []string{"ok", "flaky", "toolarge", "conflict"} {
		calls.Store(0)
		bodies, keys, slept, mode = nil, nil, nil, m
		err := c.Report(context.Background(), actID, r)
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		switch m {
		case "flaky":
			if calls.Load() != 3 || len(slept) != 2 || slept[0] != 200*time.Millisecond {
				t.Fatalf("flaky: %d %v", calls.Load(), slept)
			}
		case "toolarge":
			if bodies[1]["result"] != nil || bodies[1]["result_digest"] == nil || keys[0] == keys[1] {
				t.Fatalf("digest retry: %v %v", bodies[1], keys)
			}
		}
	}
}

func TestReadsAndResolve(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/whoami":
			io.WriteString(w, `{"object":"whoami","org":{"id":"org_x","name":"Acme"},"agent":{"id":"agt_x","name":"claude-desktop","status":"active"},"key":{"mode":"test"}}`)
		case "/v1/actions/" + actID:
			io.WriteString(w, action("executed", `,"deed":"deed_x"`))
		case "/v1/tools/resolve":
			var b gate.ResolveRequest
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b.Connector != "scratch" || b.ConnectorType != "postgres" || len(b.Tools) != 1 {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, `{"object":"list","data":[{"object":"tool_visibility","name":"q","tool":"scratch.q","visibility":"hidden","rule_id":null,"reason":"No."}],"has_more":false,"next_cursor":null,"policy_version":null}`)
		case "/v1/tools/resolve-short":
		default:
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"code":"invalid_api_key","message":"x"}}`)
		}
	}))
	defer ts.Close()
	c := New(Options{Server: ts.URL, APIKey: "k", OnBehalfOf: "usr:priya"})
	if c.Server() != ts.URL {
		t.Fatal("server")
	}
	me, err := c.Whoami(context.Background())
	if err != nil || me.Agent.Name != "claude-desktop" || me.Key.Mode != "test" {
		t.Fatalf("%v %+v", err, me)
	}
	a, err := c.GetAction(context.Background(), actID)
	if err != nil || a.Status != "executed" || *a.Deed != "deed_x" {
		t.Fatalf("%v %+v", err, a)
	}
	v, err := c.Resolve(context.Background(), "scratch", "postgres", []gate.ToolRef{{Name: "q"}})
	if err != nil || v.Data[0].Visibility != "hidden" {
		t.Fatalf("%v %+v", err, v)
	}
	if _, err := c.Resolve(context.Background(), "scratch", "postgres", []gate.ToolRef{{Name: "q"}, {Name: "r"}}); err == nil {
		t.Fatal("mismatched resolve")
	}
	_, err = c.GetAction(context.Background(), "act_unknown")
	if !IsAuthError(err) || IsAuthError(nil) {
		t.Fatal(err)
	}
	for k, want := range map[Kind]string{Unreachable: "unreachable", Authorized: "authorized", Denied: "denied", Pending: "pending_approval", BadKey: "bad_key", Rejected: "rejected"} {
		if k.String() != want {
			t.Fatal(k)
		}
	}
	if clip("éé", 3) != "é" || clip("ab", 5) != "ab" {
		t.Fatal(clip("éé", 3))
	}
	if _, err := encodeSubmit(submitBody{Args: json.RawMessage(`{bad`)}); err == nil {
		t.Fatal("bad args")
	}
	if !strings.HasPrefix(NewIdempotencyKey(), "mcpw_") || len(NewIdempotencyKey()) != 31 {
		t.Fatal(NewIdempotencyKey())
	}
}

// TestSubmitOnBehalfOf (TY-07): a call's own person replaces the client's default; without one the
// default is sent.
func TestSubmitOnBehalfOf(t *testing.T) {
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Deedglass-On-Behalf-Of"))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, action("authorized", `,"next_action":{"type":"execute_at_edge"}`))
	}))
	defer ts.Close()
	c := New(Options{Server: ts.URL, APIKey: "dg_test_x", OnBehalfOf: "u_default", HTTP: ts.Client()})
	for _, obo := range []string{"u_5780", ""} {
		if d := c.Submit(context.Background(), SubmitRequest{Tool: "peer.SendMessage", Args: json.RawMessage(`{}`), OnBehalfOf: obo}); d.Kind != Authorized {
			t.Fatalf("%v %v", d.Kind, d.Err)
		}
	}
	if strings.Join(got, ",") != "u_5780,u_default" {
		t.Errorf("Deedglass-On-Behalf-Of sent: %q", got)
	}
}
