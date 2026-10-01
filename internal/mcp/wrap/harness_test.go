package wrap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
	"github.com/deedglass/deedglass/internal/mcp/mcptest"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("FAKE_MCP_MODE"); mode != "" {
		if strings.HasPrefix(mode, "sdk-") {
			runSDKServer(mode)
		} else {
			mcptest.FakeServer(mode)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// --- a fake Gate: S13's status table, on httptest ---

type fakeAction struct {
	ID, Tool, Status, Approval string
	Args                       json.RawMessage
}

type fakeGate struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	n        int
	actions  map[string]*fakeAction
	reports  map[string]gate.EdgeResult
	submits  []string
	resolves int
	mode     string // "" | "401" | "garbage" | "500"
}

func newFakeGate(t *testing.T) *fakeGate {
	g := &fakeGate{t: t, actions: map[string]*fakeAction{}, reports: map[string]gate.EdgeResult{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGate) id(prefix string) string {
	g.n++
	return fmt.Sprintf("%s_01k6w4a3d5f7h9k1n3q5s7v%03d", prefix, g.n)
}

const denyMsg = `Blocked by policy "scratch-writes-only-in-allowed": Agents may only write inside /tmp/deedglass-demo/allowed.`

func (g *fakeGate) actionJSON(a *fakeAction, extra string) string {
	s := `{"object":"action","id":"` + a.ID + `","tool":"` + a.Tool + `","mode":"proxy","state":"x","status":"` + a.Status + `"`
	if a.Approval != "" {
		s += `,"approval":{"id":"` + a.Approval + `","status":"pending","expires_at":"2026-10-05T10:42:00.000Z"}`
		s += `,"decision":{"outcome":"require_approval","rule_id":"writes-need-approval","source":"rule","message":"Waiting for approval under policy \"writes-need-approval\": Writes wait for a human."}`
	}
	return s + extra + `}`
}

func (g *fakeGate) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer dg_test_wrapkey" || g.mode == "401" {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"The API key isn't valid."}}`)
		return
	}
	if g.mode == "500" {
		w.WriteHeader(500)
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/whoami":
		io.WriteString(w, `{"object":"whoami","org":{"id":"org_x","name":"Acme"},"agent":{"id":"agt_01k6w4a3d5f7h9k1n3q5s7v9x1","name":"claude-desktop","status":"active"},"key":{"mode":"test"}}`)
	case r.Method == "POST" && r.URL.Path == "/v1/tools/resolve":
		g.resolves++
		var b gate.ResolveRequest
		_ = json.NewDecoder(r.Body).Decode(&b)
		var data []string
		for _, t := range b.Tools {
			if !strings.HasPrefix(t.Digest, "sha256:") {
				g.t.Errorf("resolve without a digest: %v", t)
			}
			if t.Name == "move_file" {
				data = append(data, `{"object":"tool_visibility","name":"move_file","tool":"scratch.move_file","visibility":"hidden","rule_id":null,"reason":"No rule allows this tool, and the policy default is deny."}`)
			} else {
				data = append(data, `{"object":"tool_visibility","name":"`+t.Name+`","tool":"scratch.`+t.Name+`","visibility":"visible","rule_id":null,"reason":null}`)
			}
		}
		io.WriteString(w, `{"object":"list","data":[`+strings.Join(data, ",")+`],"has_more":false,"next_cursor":null,"policy_version":"pv_01k6w4a3d5f7h9k1n3q5s7v9x1"}`)
	case r.Method == "POST" && r.URL.Path == "/v1/actions":
		if g.mode == "garbage" {
			io.WriteString(w, `{"ok":true}`)
			return
		}
		var b struct {
			Tool          string              `json:"tool"`
			Mode          string              `json:"mode"`
			Args          json.RawMessage     `json:"args"`
			ConnectorType string              `json:"connector_type"`
			ToolDigest    string              `json:"tool_digest"`
			Context       gate.RequestContext `json:"context"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &b)
		if b.Mode != "proxy" || b.Context.Transport != "mcp_stdio" || !strings.HasPrefix(r.Header.Get("Idempotency-Key"), "mcpw_") ||
			!strings.HasPrefix(r.Header.Get("Deedglass-Session"), "mcp:scratch:") {
			g.t.Errorf("submit shape: %s %v", raw, r.Header)
		}
		g.submits = append(g.submits, b.Tool)
		a := &fakeAction{ID: g.id("act"), Tool: b.Tool, Args: b.Args}
		g.actions[a.ID] = a
		var args map[string]any
		_ = json.Unmarshal(b.Args, &args)
		path, _ := args["path"].(string)
		switch {
		case b.Tool == "scratch.write_file" && !strings.HasPrefix(path, "/tmp/deedglass-demo/allowed/"):
			a.Status = "denied"
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"type":"policy_error","code":"policy_denied","message":`+string(jsonrpc.String(denyMsg))+`,"action":`+
				g.actionJSON(a, `,"deed":"deed_01k6w4a3d5f7h9k1n3q5s7v9x1","decision":{"outcome":"deny","source":"rule","code":"policy_denied","rule_id":"scratch-writes-only-in-allowed","message":`+string(jsonrpc.String(denyMsg))+`}`)+`}}`)
		case b.Tool == "scratch.move_file":
			a.Status = "denied"
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"type":"policy_error","code":"policy_denied","message":"Blocked by policy \"acme-demo\": No rule allows this action.","action":`+
				g.actionJSON(a, `,"deed":"deed_01k6w4a3d5f7h9k1n3q5s7v9x2","decision":{"outcome":"deny","source":"default","code":"policy_denied","message":"Blocked by policy \"acme-demo\": No rule allows this action."}`)+`}}`)
		case args["approve"] == true:
			a.Status, a.Approval = "pending_approval", g.id("apr")
			w.WriteHeader(202)
			io.WriteString(w, g.actionJSON(a, `,"next_action":{"type":"await_approval"}`))
		default:
			a.Status = "authorized"
			io.WriteString(w, g.actionJSON(a, `,"next_action":{"type":"execute_at_edge","report_url":"/v1/actions/`+a.ID+`/result"}`))
		}
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/actions/"):
		a := g.actions[strings.TrimPrefix(r.URL.Path, "/v1/actions/")]
		if a == nil {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":"not_found","message":"No such action."}}`)
			return
		}
		extra := ""
		if a.Status == "executed" {
			extra = `,"deed":"deed_01k6w4a3d5f7h9k1n3q5s7v9x9"`
		}
		io.WriteString(w, g.actionJSON(a, extra))
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/result"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/actions/"), "/result")
		var er gate.EdgeResult
		_ = json.NewDecoder(r.Body).Decode(&er)
		a := g.actions[id]
		if a == nil || a.Status != "authorized" {
			w.WriteHeader(409)
			io.WriteString(w, `{"error":{"code":"conflict","message":"A result was already reported for this action."}}`)
			return
		}
		g.reports[id] = er
		a.Status = "executed"
		io.WriteString(w, g.actionJSON(a, ""))
	default:
		w.WriteHeader(404)
	}
}

func (g *fakeGate) set(f func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f()
}

func (g *fakeGate) reportFor(id string) (gate.EdgeResult, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r, ok := g.reports[id]
	return r, ok
}

func (g *fakeGate) waitReport(t *testing.T, id string) gate.EdgeResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := g.reportFor(id); ok {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no report for %s", id)
	return gate.EdgeResult{}
}

// --- the harness: a wrapper in-process, the fake server as a real child ---

type harness struct {
	t      *testing.T
	g      *fakeGate
	w      *Wrapper
	in     *io.PipeWriter
	msgs   chan jsonrpc.Envelope
	others []jsonrpc.Envelope
	stderr *syncBuf
	calls  string // the fake server's call log
	done   chan error
	modern bool
	nextID int
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type hopt func(*Config)

func start(t *testing.T, mode string, opts ...hopt) *harness {
	t.Helper()
	g := newFakeGate(t)
	return startWith(t, g, mode, opts...)
}

func startWith(t *testing.T, g *fakeGate, mode string, opts ...hopt) *harness {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.log")
	u, _ := url.Parse(g.srv.URL)
	cfg := Config{Connector: "scratch", Server: u, APIKey: "dg_test_wrapkey", HideDenied: true, Version: "test",
		Upstream: []string{os.Args[0]}, LogFile: filepath.Join(dir, "logs", "mcp-scratch.log"),
		Env:         append(os.Environ(), "FAKE_MCP_MODE="+mode, "FAKE_MCP_LOG="+calls, "DEEDGLASS_API_KEY=dg_test_wrapkey", "DEEDGLASS_SERVER=x"),
		RestartBase: 20 * time.Millisecond, ShutdownWait: 500 * time.Millisecond, KillWait: 200 * time.Millisecond}
	for _, o := range opts {
		o(&cfg)
	}
	stderr := &syncBuf{}
	w, err := New(cfg, stderr)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := &harness{t: t, g: g, w: w, in: inW, msgs: make(chan jsonrpc.Envelope, 1024), stderr: stderr, calls: calls,
		done: make(chan error, 1), modern: mode == "modern" || mode == "sdk-modern"}
	go func() {
		h.done <- w.Run(context.Background(), inR, outW)
		outW.Close()
	}()
	go func() {
		br := bufio.NewReaderSize(outR, 1<<20)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				e, perr := jsonrpc.Parse(bytes.TrimRight(line, "\n"))
				if perr != nil {
					t.Errorf("stdout line isn't JSON-RPC (%v): %.200q", perr, line)
					continue
				}
				h.msgs <- e
			}
			if err != nil {
				close(h.msgs)
				return
			}
		}
	}()
	t.Cleanup(func() { h.close() })
	return h
}

func (h *harness) send(v string) {
	h.t.Helper()
	if _, err := io.WriteString(h.in, v+"\n"); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) meta() string {
	if !h.modern {
		return ""
	}
	return `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test-client","version":"1.0"},"io.modelcontextprotocol/clientCapabilities":{}}`
}

// request sends method with params (a JSON object body without braces) and returns the response.
func (h *harness) request(method, params string) jsonrpc.Envelope {
	h.t.Helper()
	h.nextID++
	id := h.nextID
	h.sendRequest(id, method, params)
	return h.wait(fmt.Sprint(id))
}

func (h *harness) sendRequest(id int, method, params string) {
	h.t.Helper()
	p := params
	if m := h.meta(); m != "" {
		if p == "" {
			p = m
		} else {
			p = p + "," + m
		}
	}
	h.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":{%s}}`, id, method, p))
}

func (h *harness) wait(id string) jsonrpc.Envelope {
	h.t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case e, ok := <-h.msgs:
			if !ok {
				h.t.Fatalf("stdout closed waiting for %s", id)
			}
			if e.Kind() == jsonrpc.Response && string(e.ID()) == id {
				return e
			}
			h.others = append(h.others, e)
		case <-timeout:
			h.t.Fatalf("no response to %s\nstderr:\n%s", id, h.stderr)
		}
	}
}

// expectNone waits d and fails if a response with id arrives.
func (h *harness) expectNone(id string, d time.Duration) {
	h.t.Helper()
	timeout := time.After(d)
	for {
		select {
		case e, ok := <-h.msgs:
			if !ok {
				return
			}
			if e.Kind() == jsonrpc.Response && string(e.ID()) == id {
				h.t.Fatalf("unexpected response to %s: %s", id, e.Marshal())
			}
			h.others = append(h.others, e)
		case <-timeout:
			return
		}
	}
}

func (h *harness) handshake() jsonrpc.Envelope {
	h.t.Helper()
	if h.modern {
		return h.request("server/discover", "")
	}
	e := h.request("initialize", `"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0"}`)
	h.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return e
}

func (h *harness) call(name, args string) jsonrpc.Envelope {
	h.t.Helper()
	return h.request("tools/call", fmt.Sprintf(`"name":%q,"arguments":%s`, name, args))
}

func (h *harness) serverCalls() []string {
	b, _ := os.ReadFile(h.calls)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(l, "call ") {
			out = append(out, strings.TrimPrefix(l, "call "))
		}
	}
	return out
}

func (h *harness) serverLog() string {
	b, _ := os.ReadFile(h.calls)
	return string(b)
}

func (h *harness) close() {
	if h.in == nil {
		return
	}
	h.in.Close()
	h.in = nil
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("Run: %v", err)
		}
	case <-time.After(20 * time.Second):
		h.t.Errorf("the wrapper didn't exit\n%s", h.stderr)
	}
	for range h.msgs {
	}
}

// --- result helpers ---

type toolResult struct {
	Content []struct {
		Type, Text string
	} `json:"content"`
	IsError    bool              `json:"isError"`
	Meta       map[string]string `json:"_meta"`
	ResultType string            `json:"resultType"`
	Structured map[string]string `json:"structuredContent"`
}

func result(t *testing.T, e jsonrpc.Envelope) toolResult {
	t.Helper()
	var r toolResult
	if _, ok := e["result"]; !ok {
		t.Fatalf("not a result: %s", e.Marshal())
	}
	if err := json.Unmarshal(e["result"], &r); err != nil {
		t.Fatalf("%v: %s", err, e["result"])
	}
	return r
}

func (r toolResult) text() string {
	var parts []string
	for _, c := range r.Content {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "\n")
}

func toolNames(t *testing.T, e jsonrpc.Envelope) []string {
	t.Helper()
	var r struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := json.Unmarshal(e["result"], &r); err != nil {
		t.Fatalf("%v: %s", err, e.Marshal())
	}
	var out []string
	for _, x := range r.Tools {
		out = append(out, x.Name)
	}
	return out
}
