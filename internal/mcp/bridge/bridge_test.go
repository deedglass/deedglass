package bridge_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/mcp/bridge"
	"github.com/deedglass/deedglass/internal/mcp/front"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// run starts a bridge on pipes and returns a line sender, a line reader and the exit channel.
func run(t *testing.T, server string) (func(string), func() jsonrpc.Envelope, chan error, *io.PipeWriter) {
	t.Helper()
	u, _ := url.Parse(server)
	return runConfig(t, bridge.Config{Server: u, APIKey: "dg_test_key", Version: "test"})
}

// runConfig is run with a whole Config.
func runConfig(t *testing.T, cfg bridge.Config) (func(string), func() jsonrpc.Envelope, chan error, *io.PipeWriter) {
	t.Helper()
	b, err := bridge.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background(), inR, outW); outW.Close() }()
	lines := make(chan jsonrpc.Envelope, 16)
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			e, err := jsonrpc.Parse(sc.Bytes())
			if err != nil {
				t.Errorf("stdout line isn't JSON-RPC: %q", sc.Text())
				continue
			}
			lines <- e
		}
		close(lines)
	}()
	send := func(s string) { fmt.Fprintln(inW, s) }
	recv := func() jsonrpc.Envelope {
		select {
		case e := <-lines:
			return e
		case <-time.After(10 * time.Second):
			t.Fatal("no answer")
			return nil
		}
	}
	return send, recv, done, inW
}

func TestServerDown(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	send, recv, done, in := run(t, "http://"+addr)
	send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fake.echo","arguments":{}}}`)
	e := recv()
	var r struct {
		Content []struct{ Text string }
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(e["result"], &r)
	if string(e.ID()) != "1" || !r.IsError || !strings.HasPrefix(r.Content[0].Text, "Deedglass could not be reached at http://"+addr+", so this was not run.") {
		t.Fatalf("%s", e.Marshal())
	}
	send(`{"jsonrpc":"2.0","id":"p","method":"ping"}`)
	if e := recv(); string(e.ID()) != `"p"` || !strings.Contains(string(e["error"]), "-32603") {
		t.Fatalf("%s", e.Marshal())
	}
	send(`not json`)
	if e := recv(); !strings.Contains(string(e["error"]), "-32700") {
		t.Fatalf("%s", e.Marshal())
	}
	in.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no exit on stdin EOF")
	}
}

func TestHeadersAndIDs(t *testing.T) {
	seen := make(chan http.Header, 8)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		var e map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&e)
		w.Header().Set("Content-Type", "application/json")
		var m string
		_ = json.Unmarshal(e["method"], &m)
		switch m {
		case "notifications/initialized":
			w.WriteHeader(202)
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, e["id"])
		case "boom":
			w.WriteHeader(401)
			fmt.Fprint(w, "{\n  \"jsonrpc\": \"2.0\",\n  \"error\": {\"code\": -32600, \"message\": \"Deedglass needs an agent API key.\"}\n}")
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, e["id"])
		}
	}))
	defer ts.Close()
	send, recv, _, in := run(t, ts.URL)
	defer in.Close()
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}`
	send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"größe",` + meta + `}}`)
	recv()
	h := <-seen
	if h.Get("MCP-Protocol-Version") != "2026-07-28" || h.Get("Mcp-Method") != "tools/call" || h.Get("Mcp-Name") != "=?base64?Z3LDtsOfZQ==?=" ||
		h.Get("Authorization") != "Bearer dg_test_key" || !strings.Contains(h.Get("Accept"), "text/event-stream") {
		t.Fatalf("%v", h)
	}
	send(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	recv()
	if h := <-seen; h.Get("MCP-Protocol-Version") != "" {
		t.Fatalf("initialize carried %q", h.Get("MCP-Protocol-Version"))
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	<-seen
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	recv()
	if h := <-seen; h.Get("MCP-Protocol-Version") != "2025-06-18" {
		t.Fatalf("legacy version header %q", h.Get("MCP-Protocol-Version"))
	}
	send(`{"jsonrpc":"2.0","id":4,"method":"boom"}`)
	if e := recv(); string(e.ID()) != "4" || !strings.Contains(string(e["error"]), "agent API key") {
		t.Fatalf("%s", e.Marshal())
	}
	<-seen
}

func TestSSEAndCancel(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&e)
		var m string
		_ = json.Unmarshal(e["method"], &m)
		switch m {
		case "slow":
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"ok\":true}}\n\n", e["id"])
		}
	}))
	defer ts.Close()
	defer close(release)
	send, recv, _, in := run(t, ts.URL)
	defer in.Close()
	send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if e := recv(); e.Method() != "notifications/progress" {
		t.Fatalf("%s", e.Marshal())
	}
	if e := recv(); string(e.ID()) != "1" || !strings.Contains(string(e["result"]), "ok") {
		t.Fatalf("%s", e.Marshal())
	}
	// A cancelled request gets no answer; the next one still does.
	send(`{"jsonrpc":"2.0","id":2,"method":"slow"}`)
	time.Sleep(100 * time.Millisecond)
	send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2}}`)
	send(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	for {
		e := recv()
		if string(e.ID()) == "2" {
			t.Fatalf("answered a cancelled request: %s", e.Marshal())
		}
		if string(e.ID()) == "3" {
			break
		}
	}
	if _, err := bridge.New(bridge.Config{}); err == nil {
		t.Fatal("no server accepted")
	}
}

// noNetwork fails the test on any request the bridge tries to send.
type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.t.Errorf("offline bridge sent %s %s", r.Method, r.URL)
	return nil, fmt.Errorf("no network in this test")
}

// TestOffline (PRD-03): with Offline set the bridge answers initialize, server/discover, ping and
// tools/list from the catalog with the run-init notice, refuses every tool call with it, sends
// nothing, and returns nil at EOF.
func TestOffline(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:1")
	ns := &front.NativeSource{Rows: func(context.Context, identity.Principal) ([]connector.RowTools, error) {
		return []connector.RowTools{{Name: "postgres", Type: "postgres", Tools: []connector.ToolSpec{
			{Name: "query", Description: "Run one read-only SQL statement.", SideEffect: connector.SideEffectRead, InputSchema: json.RawMessage(`{"type":"object"}`)}}}}, nil
	}}
	send, recv, done, in := runConfig(t, bridge.Config{Server: u, Version: "test", Client: &http.Client{Transport: noNetwork{t}},
		Offline: &front.Front{Version: "test", Tools: ns}})
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	e := recv()
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		Instructions    string `json:"instructions"`
	}
	if json.Unmarshal(e["result"], &init) != nil || init.ProtocolVersion != "2025-06-18" || !strings.HasPrefix(init.Instructions, gate.MCPNoKey+" ") {
		t.Fatalf("%s", e.Marshal())
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	e = recv()
	var list struct {
		Tools []struct{ Name string } `json:"tools"`
		Meta  map[string]string       `json:"_meta"`
	}
	if json.Unmarshal(e["result"], &list) != nil || len(list.Tools) != 2 || list.Tools[0].Name != "postgres.query" ||
		list.Tools[1].Name != "deedglass.check_approval" || list.Meta["com.deedglass/notice"] != gate.MCPNoKey {
		t.Fatalf("%s", e.Marshal())
	}
	for i, call := range []string{"postgres.query", "deedglass.check_approval", "nope"} {
		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, 10+i, call))
		e = recv()
		var r struct {
			Content []struct{ Text string }
			IsError bool `json:"isError"`
		}
		if json.Unmarshal(e["result"], &r) != nil || !r.IsError || len(r.Content) != 1 || r.Content[0].Text != gate.MCPNoKey || string(e.ID()) != fmt.Sprint(10+i) {
			t.Fatalf("%s: %s", call, e.Marshal())
		}
	}
	modern := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}`
	send(`{"jsonrpc":"2.0","id":20,"method":"server/discover","params":{` + modern + `}}`)
	if e = recv(); !strings.Contains(string(e["result"]), `"instructions":"No Deedglass key found`) || !strings.Contains(string(e["result"]), `"2026-07-28"`) {
		t.Fatalf("%s", e.Marshal())
	}
	send(`{"jsonrpc":"2.0","id":21,"method":"tools/list","params":{` + modern + `}}`)
	if e = recv(); !strings.Contains(string(e["result"]), `"resultType":"complete"`) || !strings.Contains(string(e["result"]), "postgres.query") {
		t.Fatalf("%s", e.Marshal())
	}
	send(`{"jsonrpc":"2.0","id":22,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1999-01-01"}}}`)
	if e = recv(); !strings.Contains(string(e["error"]), "-32022") {
		t.Fatalf("%s", e.Marshal())
	}
	send(`{"jsonrpc":"2.0","id":23,"method":"ping"}`)
	if e = recv(); string(e.ID()) != "23" || string(e["result"]) != "{}" {
		t.Fatalf("%s", e.Marshal())
	}
	send(`{"jsonrpc":"2.0","id":24,"method":"resources/list"}`)
	if e = recv(); !strings.Contains(string(e["error"]), "-32601") {
		t.Fatalf("%s", e.Marshal())
	}
	in.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no exit on stdin EOF")
	}
}

// TestOfflineSDK: a real MCP client (the Go SDK, as Claude Desktop-style stdio) connects in both
// eras, lists the catalog, sees the notice and gets it back from a call, with no network.
func TestOfflineSDK(t *testing.T) {
	ns := &front.NativeSource{Rows: func(context.Context, identity.Principal) ([]connector.RowTools, error) {
		return []connector.RowTools{{Name: "postgres", Type: "postgres", Tools: []connector.ToolSpec{
			{Name: "query", Description: "Run one read-only SQL statement.", SideEffect: connector.SideEffectRead, InputSchema: json.RawMessage(`{"type":"object"}`)}}}}, nil
	}}
	u, _ := url.Parse("http://127.0.0.1:1")
	for _, v := range []string{"2026-07-28", "2025-11-25", "2025-06-18"} {
		t.Run(v, func(t *testing.T) {
			ctx := context.Background()
			b, err := bridge.New(bridge.Config{Server: u, Version: "test", Client: &http.Client{Transport: noNetwork{t}},
				Offline: &front.Front{Version: "test", Tools: ns}})
			if err != nil {
				t.Fatal(err)
			}
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			done := make(chan error, 1)
			go func() { done <- b.Run(ctx, inR, outW); outW.Close() }()
			c := mcp.NewClient(&mcp.Implementation{Name: "sdk-stdio", Version: "1"}, nil)
			s, err := c.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, &mcp.ClientSessionOptions{ProtocolVersion: v})
			if err != nil {
				t.Fatal(err)
			}
			if ir := s.InitializeResult(); ir == nil || !strings.HasPrefix(ir.Instructions, gate.MCPNoKey) {
				t.Fatalf("%+v", ir)
			}
			lt, err := s.ListTools(ctx, nil)
			if err != nil || len(lt.Tools) != 2 || lt.Tools[0].Name != "postgres.query" {
				t.Fatalf("%v %+v", err, lt)
			}
			res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "postgres.query", Arguments: map[string]any{"sql": "SELECT 1"}})
			if err != nil || !res.IsError || len(res.Content) != 1 || res.Content[0].(*mcp.TextContent).Text != gate.MCPNoKey {
				t.Fatalf("%v %+v", err, res)
			}
			_ = s.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the bridge didn't exit on stdin EOF")
			}
		})
	}
}
