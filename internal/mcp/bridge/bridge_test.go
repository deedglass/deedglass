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

	"github.com/deedglass/deedglass/internal/mcp/bridge"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
)

// run starts a bridge on pipes and returns a line sender, a line reader and the exit channel.
func run(t *testing.T, server string) (func(string), func() jsonrpc.Envelope, chan error, *io.PipeWriter) {
	t.Helper()
	u, _ := url.Parse(server)
	b, err := bridge.New(bridge.Config{Server: u, APIKey: "dg_test_key", Version: "test"})
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
