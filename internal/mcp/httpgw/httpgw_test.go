package httpgw_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mcp/front"
	"github.com/deedglass/deedglass/internal/mcp/httpgw"
)

var (
	orgID   = ids.New[ids.OrgKind]()
	agentID = ids.New[ids.AgentKind]()
)

// fakeKeys authenticates "agent", "stopped" and "revoked".
type fakeKeys struct{}

func (fakeKeys) AuthenticateKey(_ context.Context, bearer string) (httpx.Principal, error) {
	p := httpx.Principal{Kind: httpx.KindAgentKey, OrgID: orgID, AgentID: agentID, AgentName: "claude-desktop", AgentStatus: "active",
		Scopes: httpx.DefaultAgentScopes}
	switch bearer {
	case "agent":
		return p, nil
	case "stopped":
		p.AgentStatus = "stopped"
		return p, nil
	case "admin":
		return httpx.Principal{Kind: httpx.KindAdminKey, OrgID: orgID, Scopes: []string{httpx.ScopeAdmin}, Role: httpx.RoleAdmin}, nil
	}
	return httpx.Principal{}, httpx.E(httpx.CodeAPIKeyRevoked)
}

// fakeTools offers echo, delete_all, region (x-mcp-header Region and Count) and hidden.
type fakeTools struct{}

var regionSchema = json.RawMessage(`{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"},` +
	`"count":{"type":"integer","x-mcp-header":"Count"},"opts":{"type":"object","properties":{"dry":{"type":"boolean","x-mcp-header":"Dry"}}},"query":{"type":"string"}}}`)

func (fakeTools) tools() []front.Tool {
	mk := func(name string, schema json.RawMessage, hidden bool) front.Tool {
		def := json.RawMessage(fmt.Sprintf(`{"name":%q,"inputSchema":%s}`, name, schema))
		return front.Tool{Name: name, Gate: "everything." + name, Def: def, Schema: schema, Digest: front.DefDigest(def), Hidden: hidden}
	}
	obj := json.RawMessage(`{"type":"object"}`)
	return []front.Tool{mk("delete_all", obj, false), mk("echo", obj, false), mk("hidden", obj, true), mk("region", regionSchema, false), mk("slow", obj, false)}
}
func (f fakeTools) List(context.Context, identity.Principal) ([]front.Tool, error) {
	return f.tools(), nil
}
func (f fakeTools) Lookup(_ context.Context, _ identity.Principal, name string) (front.Tool, bool, error) {
	for _, t := range f.tools() {
		if t.Name == name {
			return t, true, nil
		}
	}
	return front.Tool{}, false, nil
}
func (fakeTools) Label() string { return "everything" }

// fakeGate executes echo, denies delete_all, and blocks slow until released.
type fakeGate struct {
	calls   atomic.Int64
	release chan struct{}
	started chan struct{}
}

func act(status, state string) *gate.Action {
	a := &gate.Action{}
	a.Action = ledger.Action{Object: "action", ID: ids.New[ids.ActionKind]().String(), Status: status, State: state, Tool: "everything.echo"}
	d := "deed_" + a.ID[4:]
	a.Deed = &d
	return a
}

func (g *fakeGate) Submit(ctx context.Context, _ identity.Principal, req gate.ActionRequest) (*gate.Action, error) {
	g.calls.Add(1)
	switch req.Tool {
	case "everything.delete_all":
		a := act(ledger.StatusDenied, "sealed")
		msg, src, rule := `Blocked by policy "no-deletes": Agents never delete everything.`, "rule", "no-deletes"
		a.Decision = &ledger.ActionDecision{Outcome: "deny", Message: &msg, Source: &src, RuleID: &rule}
		return a, httpx.E(httpx.CodePolicyDenied)
	case "everything.slow":
		g.started <- struct{}{}
		select {
		case <-g.release:
		case <-ctx.Done():
		}
	}
	a := act(ledger.StatusExecuted, "sealed")
	st := "succeeded"
	a.Execution = &ledger.ActionExecution{Status: &st, Result: json.RawMessage(`{"content":[{"type":"text","text":"Echo: hello"}],"isError":false}`)}
	return a, nil
}
func (g *fakeGate) Get(context.Context, identity.Principal, string, time.Duration) (*gate.Action, error) {
	return nil, httpx.E(httpx.CodeNotFound)
}
func (g *fakeGate) ByApproval(context.Context, identity.Principal, string, time.Duration) (*gate.Action, error) {
	return nil, httpx.E(httpx.CodeNotFound)
}

type harness struct {
	ts *httptest.Server
	g  *fakeGate
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	g := &fakeGate{release: make(chan struct{}), started: make(chan struct{}, 64)}
	f := &front.Front{Version: "0.1.0-test", Tools: fakeTools{}, Gate: g, CallWait: 5 * time.Second}
	gw := &httpgw.Gateway{Native: f, Upstreams: map[string]*front.Front{"everything": f}}
	base, _ := url.Parse("http://127.0.0.1:8080")
	srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Keys: fakeKeys{}, BaseURL: base})
	auth := identity.NewMCPAuth(base).WithAllowedOrigins([]string{"https://console.example.test"})
	srv.Mount(auth.GatewayRoutes(identity.MCPGateway{Connector: gw.Connector, Native: gw.NativeHandler, Timeout: 20 * time.Second})...)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &harness{ts: ts, g: g}
}

const modernMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`

func modern(method, params string) string {
	if params != "" {
		params += ","
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":%q,"params":{%s%s}}`, method, params, modernMeta)
}

func modernHeaders(method, name string) []string {
	h := []string{"MCP-Protocol-Version", "2026-07-28", "Mcp-Method", method, "Accept", "application/json, text/event-stream"}
	if name != "" {
		h = append(h, "Mcp-Name", name)
	}
	return h
}

type resp struct {
	status int
	header http.Header
	body   string
}

func (h *harness) do(t *testing.T, method, path, key, body string, hdr ...string) resp {
	t.Helper()
	req, _ := http.NewRequest(method, h.ts.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "<del>" {
			req.Header.Del(hdr[i])
			continue
		}
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, res.Header, string(b)}
}

type rpcOut struct {
	ID     json.RawMessage `json:"id"`
	Result map[string]json.RawMessage
	Error  *struct {
		Code    int
		Message string
		Data    json.RawMessage
	}
}

func parse(t *testing.T, r resp) rpcOut {
	t.Helper()
	var o rpcOut
	if err := json.Unmarshal([]byte(r.body), &o); err != nil {
		t.Fatalf("not JSON-RPC: %d %q", r.status, r.body)
	}
	return o
}

// TestConformance is S15 §6's HTTP conformance table.
func TestConformance(t *testing.T) {
	h := newHarness(t)
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"x":"` + strings.Repeat("a", 4<<20) + `"}}`
	cases := []struct {
		name, method, path, key, body string
		hdr                           []string
		status, code                  int
		check                         func(t *testing.T, r resp, o rpcOut)
	}{
		{name: "modern tools/list", method: "POST", path: "/mcp/everything", key: "agent", body: modern("tools/list", ""), hdr: modernHeaders("tools/list", ""),
			status: 200, check: func(t *testing.T, r resp, o rpcOut) {
				var tools []struct{ Name string }
				_ = json.Unmarshal(o.Result["tools"], &tools)
				names := []string{}
				for _, x := range tools {
					names = append(names, x.Name)
				}
				if strings.Join(names, ",") != "delete_all,echo,region,slow,deedglass.check_approval" {
					t.Fatalf("tools %v", names)
				}
				if string(o.Result["cacheScope"]) != `"private"` || string(o.Result["ttlMs"]) != "30000" || string(o.Result["resultType"]) != `"complete"` {
					t.Fatalf("%s", r.body)
				}
				if r.header.Get("Mcp-Session-Id") != "" || r.header.Get("Cache-Control") != "no-store" || r.header.Get("Access-Control-Allow-Origin") != "" {
					t.Fatalf("%v", r.header)
				}
			}},
		{name: "modern server/discover", method: "POST", path: "/mcp/everything", key: "agent", body: modern("server/discover", ""), hdr: modernHeaders("server/discover", ""),
			status: 200, check: func(t *testing.T, r resp, o rpcOut) {
				if string(o.Result["supportedVersions"]) != `["2026-07-28"]` || string(o.Result["resultType"]) != `"complete"` || string(o.Result["cacheScope"]) != `"public"` {
					t.Fatalf("%s", r.body)
				}
			}},
		{name: "missing Mcp-Method", method: "POST", path: "/mcp/everything", key: "agent", body: modern("tools/list", ""),
			hdr: []string{"MCP-Protocol-Version", "2026-07-28"}, status: 400, code: -32020},
		{name: "Mcp-Name differs from the body", method: "POST", path: "/mcp/everything", key: "agent",
			body: modern("tools/call", `"name":"delete_all","arguments":{}`), hdr: modernHeaders("tools/call", "echo"), status: 400, code: -32020,
			check: func(t *testing.T, r resp, o rpcOut) {
				if o.Error.Message != `Header mismatch: Mcp-Name is "echo" but the body names "delete_all". Send the same tool name in both.` {
					t.Fatalf("%q", o.Error.Message)
				}
				if string(o.ID) != "3" {
					t.Fatalf("id %s", o.ID)
				}
			}},
		{name: "Mcp-Name base64 sentinel", method: "POST", path: "/mcp/everything", key: "agent",
			body: modern("tools/call", `"name":"echo","arguments":{"message":"hello"}`), hdr: modernHeaders("tools/call", "=?base64?ZWNobw==?="), status: 200,
			check: func(t *testing.T, r resp, o rpcOut) {
				var meta map[string]string
				_ = json.Unmarshal(o.Result["_meta"], &meta)
				if !strings.HasPrefix(meta["com.deedglass/action"], "act_") || !strings.HasPrefix(meta["com.deedglass/deed"], "deed_") {
					t.Fatalf("%s", r.body)
				}
			}},
		{name: "Mcp-Name invalid base64", method: "POST", path: "/mcp/everything", key: "agent",
			body: modern("tools/call", `"name":"echo","arguments":{}`), hdr: modernHeaders("tools/call", "=?base64?!!!?="), status: 400, code: -32020},
		{name: "header version differs from _meta", method: "POST", path: "/mcp/everything", key: "agent", body: modern("tools/list", ""),
			hdr: []string{"MCP-Protocol-Version", "2025-11-25", "Mcp-Method", "tools/list"}, status: 400, code: -32020},
		{name: "unsupported modern version", method: "POST", path: "/mcp/everything", key: "agent",
			body: strings.ReplaceAll(modern("tools/list", ""), "2026-07-28", "2099-01-01"), hdr: []string{"MCP-Protocol-Version", "2099-01-01", "Mcp-Method", "tools/list"},
			status: 400, code: -32022, check: func(t *testing.T, r resp, o rpcOut) {
				var d struct {
					Supported []string
					Requested string
				}
				_ = json.Unmarshal(o.Error.Data, &d)
				if len(d.Supported) != 4 || d.Requested != "2099-01-01" || o.Error.Message != gate.MCPUnsupportedVersion {
					t.Fatalf("%s", r.body)
				}
			}},
		{name: "x-mcp-header argument without its header", method: "POST", path: "/mcp/everything", key: "agent",
			body: modern("tools/call", `"name":"region","arguments":{"region":"us-west1","query":"x"}`), hdr: modernHeaders("tools/call", "region"), status: 400, code: -32020},
		{name: "x-mcp-header matching (integer numerically, nested boolean)", method: "POST", path: "/mcp/everything", key: "agent",
			body: modern("tools/call", `"name":"region","arguments":{"region":"us-west1","count":42,"opts":{"dry":true}}`),
			hdr:  append(modernHeaders("tools/call", "region"), "Mcp-Param-Region", "us-west1", "Mcp-Param-Count", "42.0", "Mcp-Param-Dry", "true"), status: 200},
		{name: "x-mcp-header header without its argument", method: "POST", path: "/mcp/everything", key: "agent",
			body: modern("tools/call", `"name":"region","arguments":{}`), hdr: append(modernHeaders("tools/call", "region"), "Mcp-Param-Region", "us-west1"), status: 400, code: -32020},
		{name: "legacy initialize 2025-06-18", method: "POST", path: "/mcp/everything", key: "agent",
			body:   `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`,
			status: 200, check: func(t *testing.T, r resp, o rpcOut) {
				if string(o.Result["protocolVersion"]) != `"2025-06-18"` || r.header.Get("Mcp-Session-Id") != "" || o.Result["resultType"] != nil {
					t.Fatalf("%v %s", r.header, r.body)
				}
				if !strings.Contains(string(o.Result["serverInfo"]), `"name":"deedglass-gate"`) || !strings.Contains(string(o.Result["instructions"]), "deedglass.check_approval") {
					t.Fatalf("%s", r.body)
				}
			}},
		{name: "legacy initialize 2024-11-05", method: "POST", path: "/mcp/everything", key: "agent",
			body:   `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`,
			status: 200, check: func(t *testing.T, r resp, o rpcOut) {
				if string(o.Result["protocolVersion"]) != `"2025-11-25"` {
					t.Fatalf("%s", r.body)
				}
			}},
		{name: "legacy request with Mcp-Session-Id", method: "POST", path: "/mcp/everything", key: "agent", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			hdr: []string{"Mcp-Session-Id", "abc", "MCP-Protocol-Version", "2025-11-25"}, status: 200},
		{name: "notifications/initialized", method: "POST", path: "/mcp/everything", key: "agent", body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			status: 202, check: func(t *testing.T, r resp, _ rpcOut) {}},
		{name: "GET", method: "GET", path: "/mcp/everything", key: "agent", status: 405},
		{name: "DELETE", method: "DELETE", path: "/mcp/native", key: "agent", status: 405},
		{name: "PUT", method: "PUT", path: "/mcp/native", key: "agent", body: "{}", status: 405},
		{name: "evil Origin", method: "POST", path: "/mcp/native", key: "agent", body: modern("tools/list", ""), hdr: append(modernHeaders("tools/list", ""), "Origin", "https://evil.example"),
			status: 403, check: func(t *testing.T, r resp, o rpcOut) {
				if o.Error == nil || o.Error.Message != identity.MCPOriginMessage || o.ID != nil {
					t.Fatalf("%s", r.body)
				}
			}},
		{name: "an allowed Origin", method: "POST", path: "/mcp/native", key: "agent", body: modern("tools/list", ""),
			hdr: append(modernHeaders("tools/list", ""), "Origin", "https://console.example.test"), status: 200},
		{name: "no Authorization", method: "POST", path: "/mcp/everything", body: modern("tools/list", ""), hdr: modernHeaders("tools/list", ""),
			status: 401, check: func(t *testing.T, r resp, o rpcOut) {
				ch := r.header.Get("WWW-Authenticate")
				if !strings.Contains(ch, `resource_metadata="http://127.0.0.1:8080/.well-known/oauth-protected-resource/mcp/everything"`) || !strings.Contains(ch, `scope="actions:execute"`) {
					t.Fatalf("%q", ch)
				}
				var d struct {
					Code   string `json:"code"`
					DocURL string `json:"doc_url"`
				}
				if o.Error != nil {
					_ = json.Unmarshal(o.Error.Data, &d)
				}
				if o.Error == nil || o.Error.Message != identity.MCPNeedKeyMessage || d.Code != "authentication_required" || !strings.HasSuffix(d.DocURL, "/authentication_required") {
					t.Fatalf("%s", r.body)
				}
			}},
		{name: "revoked key", method: "POST", path: "/mcp/everything", key: "revoked", body: modern("tools/list", ""), hdr: modernHeaders("tools/list", ""), status: 401},
		{name: "an admin key", method: "POST", path: "/mcp/everything", key: "admin", body: modern("tools/list", ""), hdr: modernHeaders("tools/list", ""), status: 403},
		{name: "JSON array body", method: "POST", path: "/mcp/everything", key: "agent", body: `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, status: 400, code: -32600},
		{name: "a response body", method: "POST", path: "/mcp/everything", key: "agent", body: `{"jsonrpc":"2.0","id":1,"result":{}}`, status: 400, code: -32600},
		{name: "not JSON", method: "POST", path: "/mcp/everything", key: "agent", body: `{"jsonrpc":`, status: 400, code: -32700},
		{name: "4 MiB + 1", method: "POST", path: "/mcp/everything", key: "agent", body: big, status: 413},
		{name: "text/plain", method: "POST", path: "/mcp/everything", key: "agent", body: `{}`, hdr: []string{"Content-Type", "text/plain"}, status: 415},
		{name: "Accept without JSON", method: "POST", path: "/mcp/everything", key: "agent", body: modern("tools/list", ""),
			hdr: append(modernHeaders("tools/list", ""), "Accept", "text/html"), status: 406},
		{name: "modern unknown method", method: "POST", path: "/mcp/everything", key: "agent", body: modern("tasks/list", ""), hdr: modernHeaders("tasks/list", ""), status: 404, code: -32601},
		{name: "legacy unknown method", method: "POST", path: "/mcp/everything", key: "agent", body: `{"jsonrpc":"2.0","id":1,"method":"tasks/list"}`, status: 200, code: -32601},
		{name: "unknown connector", method: "POST", path: "/mcp/evrything", key: "agent", body: modern("tools/list", ""), hdr: modernHeaders("tools/list", ""),
			status: 404, check: func(t *testing.T, r resp, o rpcOut) {
				if o.Error == nil || o.Error.Message != `No MCP connector named "evrything". Check the URL; configured connectors are listed in Console › Connectors.` {
					t.Fatalf("%s", r.body)
				}
			}},
		{name: "a stopped agent reaches the gateway (recorded by the Gate)", method: "POST", path: "/mcp/everything", key: "stopped", body: modern("tools/list", ""),
			hdr: modernHeaders("tools/list", ""), status: 200},
		{name: "denied call is a tool result", method: "POST", path: "/mcp/everything", key: "agent", body: modern("tools/call", `"name":"delete_all","arguments":{}`),
			hdr: modernHeaders("tools/call", "delete_all"), status: 200, check: func(t *testing.T, r resp, o rpcOut) {
				if string(o.Result["isError"]) != "true" || !strings.Contains(r.body, `Blocked by policy \"no-deletes\"`) || !strings.Contains(r.body, "Nothing was run.") {
					t.Fatalf("%s", r.body)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.do(t, c.method, c.path, c.key, c.body, c.hdr...)
			if r.status != c.status {
				t.Fatalf("status %d, want %d: %s", r.status, c.status, r.body)
			}
			if c.status == 405 {
				if r.header.Get("Allow") != "POST" {
					t.Fatalf("Allow %q", r.header.Get("Allow"))
				}
				return
			}
			if c.status == 202 {
				if r.body != "" {
					t.Fatalf("202 with a body: %q", r.body)
				}
				return
			}
			o := parse(t, r)
			if c.code != 0 && (o.Error == nil || o.Error.Code != c.code) {
				t.Fatalf("want code %d: %s", c.code, r.body)
			}
			if !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
				t.Fatalf("content type %q", r.header.Get("Content-Type"))
			}
			if c.check != nil {
				c.check(t, r, o)
			}
		})
	}
}

// TestInFlightLimit: the 33rd concurrent request from one agent is refused.
func TestInFlightLimit(t *testing.T) {
	h := newHarness(t)
	var wg sync.WaitGroup
	for i := 0; i < httpgw.MaxInFlight; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.do(t, "POST", "/mcp/everything", "agent", modern("tools/call", `"name":"slow","arguments":{}`), modernHeaders("tools/call", "slow")...)
		}()
	}
	for i := 0; i < httpgw.MaxInFlight; i++ {
		select {
		case <-h.g.started:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d calls started", i)
		}
	}
	r := h.do(t, "POST", "/mcp/everything", "agent", modern("tools/list", ""), modernHeaders("tools/list", "")...)
	if r.status != 429 || !strings.Contains(r.body, gate.MCPTooManyInFlight) || r.header.Get("Retry-After") != "1" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	close(h.g.release)
	wg.Wait()
	if r := h.do(t, "POST", "/mcp/everything", "agent", modern("tools/list", ""), modernHeaders("tools/list", "")...); r.status != 200 {
		t.Fatalf("after release: %d", r.status)
	}
}

func TestHeaderCodec(t *testing.T) {
	for _, c := range []struct{ in, enc string }{
		{"us-west1", "us-west1"}, {"Hello, 世界", "=?base64?SGVsbG8sIOS4lueVjA==?="}, {" padded ", "=?base64?IHBhZGRlZCA=?="},
		{"line1\nline2", "=?base64?bGluZTEKbGluZTI=?="}, {"=?base64?literal?=", "=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?="},
	} {
		if got := httpgw.EncodeHeader(c.in); got != c.enc {
			t.Errorf("encode %q = %q, want %q", c.in, got, c.enc)
		}
		if got, ok := httpgw.DecodeHeader(c.enc); !ok || got != c.in {
			t.Errorf("decode %q = %q %v", c.enc, got, ok)
		}
	}
	for _, bad := range []string{"=?base64?!!?=", " lead", "tail ", "ctl\x01", "=?base64?/w==?="} {
		if _, ok := httpgw.DecodeHeader(bad); ok {
			t.Errorf("%q decoded", bad)
		}
	}
}

func FuzzHeaderDecode(f *testing.F) {
	for _, s := range []string{"echo", "=?base64?ZWNobw==?=", "=?base64??=", "", "\t", "=?base64?", "?="} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		got, ok := httpgw.DecodeHeader(v)
		if !ok {
			return
		}
		if again, ok2 := httpgw.DecodeHeader(httpgw.EncodeHeader(got)); !ok2 || again != got {
			t.Fatalf("round trip of %q: %q %v", got, again, ok2)
		}
	})
}
