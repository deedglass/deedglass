// Package a2atest is a fake A2A v1.0 peer for tests: a JSON-RPC endpoint at /rpc and an Agent Card
// at /.well-known/agent-card.json, on httptest (loopback, no network). What it answers depends on
// the first text part of the message:
//
//	"complete …"  a task in TASK_STATE_COMPLETED with one artifact
//	"fail …"      a task in TASK_STATE_FAILED
//	"work …"      a task in TASK_STATE_WORKING
//	"reply …"     a message, no task
//	"refuse …"    the JSON-RPC error UnsupportedOperationError (-32004)
//
// SendStreamingMessage answers SSE: a working task, a status update, then the final state. OnSend,
// when set, runs before the answer (the two-agent test delegates from there).
package a2atest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Received is one request the peer saw.
type Received struct {
	Method        string
	Version       string // A2A-Version
	Extensions    string // A2A-Extensions
	Authorization string
	Body          json.RawMessage
}

// Peer is the fake agent.
type Peer struct {
	Name   string
	Server *httptest.Server
	OnSend func(params json.RawMessage) // optional

	mu  sync.Mutex
	got []Received
}

// New starts a peer named name.
func New(name string) *Peer {
	p := &Peer{Name: name}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	return p
}

// URL is the JSON-RPC endpoint.
func (p *Peer) URL() string { return p.Server.URL + "/rpc" }

// Close stops the peer.
func (p *Peer) Close() { p.Server.Close() }

// Got returns every request so far.
func (p *Peer) Got() []Received {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Received(nil), p.got...)
}

func (p *Peer) card() string {
	return fmt.Sprintf(`{"name":%q,"description":"A fake agent for tests.","version":"1.0.0",`+
		`"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"},`+
		`{"url":%q,"protocolBinding":"HTTP+JSON","protocolVersion":"1.0"}],`+
		`"capabilities":{"streaming":true},"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],`+
		`"skills":[{"id":"research","name":"Research","description":"Looks things up.","tags":["research"]}],`+
		`"signatures":[{"protected":"eyJhbGciOiJFUzI1NiJ9","signature":"c2lnbmF0dXJl"}]}`,
		p.Name, p.URL(), p.Server.URL+"/rest")
}

type rpc struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (p *Peer) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/.well-known/agent-card.json" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, p.card())
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/rpc" {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var q rpc
	_ = json.Unmarshal(body, &q)
	p.mu.Lock()
	p.got = append(p.got, Received{Method: q.Method, Version: r.Header.Get("A2A-Version"), Extensions: r.Header.Get("A2A-Extensions"),
		Authorization: r.Header.Get("Authorization"), Body: body})
	p.mu.Unlock()
	answer := func(result string) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, q.ID, result)
	}
	switch q.Method {
	case "GetTask":
		answer(task("task-1", "TASK_STATE_COMPLETED", ""))
	case "CancelTask":
		answer(task("task-1", "TASK_STATE_CANCELED", ""))
	case "SendMessage", "SendStreamingMessage":
		if p.OnSend != nil {
			p.OnSend(q.Params)
		}
		text := firstText(q.Params)
		verb, _, _ := strings.Cut(text, " ")
		if q.Method == "SendStreamingMessage" {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ev := range []string{
				`{"task":` + task("task-2", "TASK_STATE_WORKING", "") + `}`,
				`{"statusUpdate":{"taskId":"task-2","contextId":"ctx-1","status":{"state":"TASK_STATE_WORKING"}}}`,
				`{"task":` + task("task-2", "TASK_STATE_COMPLETED", "done") + `}`,
			} {
				fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":%s}\n\n", q.ID, ev)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			return
		}
		switch verb {
		case "fail":
			answer(`{"task":` + task("task-1", "TASK_STATE_FAILED", "") + `}`)
		case "work":
			answer(`{"task":` + task("task-1", "TASK_STATE_WORKING", "") + `}`)
		case "reply":
			answer(`{"message":{"messageId":"m-reply","contextId":"ctx-1","role":"ROLE_AGENT","parts":[{"text":"Here you go."}]}}`)
		case "refuse":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32004,"message":"This operation is not supported"}}`, q.ID)
		default:
			answer(`{"task":` + task("task-1", "TASK_STATE_COMPLETED", "The answer is 42.") + `}`)
		}
	default:
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found"}}`, q.ID)
	}
}

func task(id, state, artifact string) string {
	s := fmt.Sprintf(`{"id":%q,"contextId":"ctx-1","status":{"state":%q,"timestamp":"2026-10-20T10:00:00.000Z"}`, id, state)
	if artifact != "" {
		s += fmt.Sprintf(`,"artifacts":[{"artifactId":"art-1","name":"Answer","parts":[{"text":%q}]}]`, artifact)
	}
	return s + "}"
}

func firstText(params json.RawMessage) string {
	var p struct {
		Message struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"message"`
	}
	_ = json.Unmarshal(params, &p)
	for _, part := range p.Message.Parts {
		if part.Text != "" {
			return part.Text
		}
	}
	return ""
}
