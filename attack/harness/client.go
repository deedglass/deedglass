//go:build attack

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/connector/egress"
)

// BodyLog keeps every response body the harness saw, for the sentinel sweep.
type BodyLog struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (b *BodyLog) add(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bodies = append(b.bodies, append([]byte(nil), p...))
}

// Snapshot returns the bodies so far.
func (b *BodyLog) Snapshot() [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([][]byte(nil), b.bodies...)
}

// Client talks HTTP to the Deedglass server like a real agent or admin: one bearer key (or a
// session cookie), JSON bodies, every response body logged for the sweep. It uses the egress
// door's first-party client (the server is a destination the operator named).
type Client struct {
	Base    string
	Key     string
	Headers map[string]string
	hc      *http.Client
	log     *BodyLog
	csrf    func() string
}

// Response is one answer.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body into v.
func (r Response) JSON(v any) error { return json.Unmarshal(r.Body, v) }

// Map decodes the body as a JSON object.
func (r Response) Map() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.Body, &m)
	return m
}

// ErrorCode is error.code of an error envelope, or "".
func (r Response) ErrorCode() string { return Str(r.Map(), "error", "code") }

func newClient(base, key string, log *BodyLog) *Client {
	return &Client{Base: base, Key: key, log: log, hc: egress.FirstParty(egress.FirstPartyOptions{Timeout: 30 * time.Second, MaxIdle: 64})}
}

// WithKey returns a client for another key.
func (c *Client) WithKey(key string) *Client {
	n := *c
	n.Key, n.Headers = key, nil
	return &n
}

// WithSession returns a client that sends cookies (a person's session) and the CSRF header.
func (c *Client) WithSession(jar *cookiejar.Jar, csrf func() string) *Client {
	n := *c
	n.Key = ""
	hc := *c.hc
	hc.Jar = jar
	n.hc, n.csrf = &hc, csrf
	return &n
}

// Do sends one request. body may be nil, a []byte, a string or a value to encode as JSON.
func (c *Client) Do(method, path string, body any, hdr ...string) Response {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			panic(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.Base+path, rd)
	if err != nil {
		panic(err)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	if c.csrf != nil && method != "GET" && method != "HEAD" {
		req.Header.Set("Deedglass-CSRF", c.csrf())
	}
	if method == "POST" && !hasHeader(hdr, "Idempotency-Key") {
		req.Header.Set("Idempotency-Key", newIdemKey())
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := c.hc.Do(req)
	if err != nil {
		panic(fmt.Sprintf("%s %s: %v", method, path, err))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if c.log != nil {
		c.log.add(raw)
		var hb strings.Builder
		for k, vs := range res.Header {
			hb.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
		}
		c.log.add([]byte(hb.String()))
	}
	return Response{Status: res.StatusCode, Header: res.Header, Body: raw}
}

// Peek sends GET path and reads the answer for at most d: for a stream (GET /v1/events) that
// never ends on its own. The body is what arrived in that time; it is logged like any other.
func (c *Client) Peek(path string, d time.Duration) Response {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+path, nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	res, err := c.hc.Do(req)
	if err != nil {
		panic(fmt.Sprintf("GET %s: %v", path, err))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20)) // ends at the deadline
	if c.log != nil {
		c.log.add(raw)
	}
	return Response{Status: res.StatusCode, Header: res.Header, Body: raw}
}

func hasHeader(hdr []string, name string) bool {
	for i := 0; i+1 < len(hdr); i += 2 {
		if strings.EqualFold(hdr[i], name) {
			return true
		}
	}
	return false
}

// Str walks a decoded JSON object and returns the string at the path, or "".
func Str(m any, path ...string) string {
	v := Get(m, path...)
	s, _ := v.(string)
	return s
}

// Get walks a decoded JSON object.
func Get(m any, path ...string) any {
	cur := m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

// MCP sends one JSON-RPC request to /mcp/<endpoint> (Streamable HTTP, the 2025-06-18 legacy era
// with initialize first, which every client still speaks) and returns the decoded response.
func (c *Client) MCP(endpoint, method string, id int, params any) (Response, map[string]any) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	r := c.Do("POST", "/mcp/"+endpoint, msg, "Accept", "application/json, text/event-stream",
		"MCP-Protocol-Version", "2025-06-18")
	return r, r.Map()
}

// MCPInit runs initialize on the endpoint.
func (c *Client) MCPInit(endpoint string) Response {
	r, _ := c.MCP(endpoint, "initialize", 0, map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "attack-harness", "version": "1"}})
	return r
}
