// Package bridge is `deedglass mcp serve` (S15): a thin stdio ⇄ Streamable HTTP bridge, so a
// stdio-only MCP client (Claude Desktop's local servers) can use the Gate's HTTP endpoints. Each
// JSON-RPC line from stdin becomes one POST to /mcp/native (or /mcp/<connector>) with the headers
// a conforming HTTP client sends; each answer becomes one line on stdout. It holds no upstream
// credentials and spawns nothing; the agent's key travels only in the Authorization header.
package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/mcp/httpgw"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
)

// MaxInFlight is how many requests the bridge sends at once.
const MaxInFlight = 32

// maxBody caps an HTTP answer read by the bridge.
const maxBody = 4<<20 + 64<<10

// Config is one bridge.
type Config struct {
	Server    *url.URL // the Deedglass server
	Connector string   // "native" or an upstream's name
	APIKey    string
	Version   string
	Client    *http.Client // nil: a default with a 5 s dial timeout and no overall timeout
}

// Bridge relays one stdio session.
type Bridge struct {
	cfg      Config
	endpoint string
	client   *http.Client

	mu       sync.Mutex
	legacy   string                        // the version initialize negotiated (legacy clients)
	inflight map[string]context.CancelFunc // request id → its POST's cancel
}

// New returns a Bridge.
func New(cfg Config) (*Bridge, error) {
	if cfg.Server == nil || cfg.Server.Host == "" {
		return nil, errors.New("the Deedglass server URL is missing")
	}
	if cfg.Connector == "" {
		cfg.Connector = "native"
	}
	c := cfg.Client
	if c == nil {
		d := &net.Dialer{Timeout: 5 * time.Second}
		c = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: d.DialContext,
			MaxIdleConnsPerHost: MaxInFlight, IdleConnTimeout: 90 * time.Second},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	ep := strings.TrimSuffix(cfg.Server.String(), "/") + "/mcp/" + cfg.Connector
	return &Bridge{cfg: cfg, endpoint: ep, client: c, inflight: map[string]context.CancelFunc{}}, nil
}

// Run relays until stdin ends (or ctx is done), then cancels whatever is in flight and returns
// nil.
func (b *Bridge) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	w := jsonrpc.NewWriter(out)
	defer w.Close()
	r := jsonrpc.NewReader(in)
	sem := make(chan struct{}, MaxInFlight)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancelAll()
	for {
		e, _, err := r.Next()
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil && isFraming(err):
			code, msg := jsonrpc.Reply(err)
			_ = w.SendEnvelope(jsonrpc.NewError(nil, code, msg))
			continue
		case err != nil:
			return nil // stdin closed or broken: the client went away
		}
		switch e.Kind() {
		case jsonrpc.Response:
			continue // the gateway never asks the client anything (sessionless, no server requests)
		case jsonrpc.Notification:
			if e.Method() == "notifications/cancelled" {
				b.cancel(e)
				continue
			}
			wg.Add(1)
			go func() { defer wg.Done(); b.post(ctx, e, nil) }()
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		rctx, cancel := context.WithCancel(ctx)
		key := string(e.ID())
		b.mu.Lock()
		b.inflight[key] = cancel
		b.mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				b.mu.Lock()
				delete(b.inflight, key)
				b.mu.Unlock()
				cancel()
			}()
			b.post(rctx, e, w)
		}()
	}
}

func isFraming(err error) bool {
	for _, f := range []error{jsonrpc.ErrTooLarge, jsonrpc.ErrParse, jsonrpc.ErrBatch, jsonrpc.ErrTooDeep, jsonrpc.ErrNotUTF8, jsonrpc.ErrShape} {
		if errors.Is(err, f) {
			return true
		}
	}
	return false
}

// cancel is notifications/cancelled: it closes the matching POST (on Streamable HTTP, closing the
// request is the cancellation).
func (b *Bridge) cancel(e jsonrpc.Envelope) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(e["params"], &p) != nil {
		return
	}
	b.mu.Lock()
	c := b.inflight[string(p.RequestID)]
	b.mu.Unlock()
	if c != nil {
		c()
	}
}

// post sends one message and writes its answer (w is nil for notifications).
func (b *Bridge) post(ctx context.Context, e jsonrpc.Envelope, w *jsonrpc.Writer) {
	body := e.Marshal()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(body))
	if err != nil {
		b.fail(w, e)
		return
	}
	h := req.Header
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/event-stream")
	h.Set("User-Agent", "deedglass-mcp-serve/"+b.cfg.Version)
	if b.cfg.APIKey != "" {
		h.Set("Authorization", "Bearer "+b.cfg.APIKey)
	}
	b.headers(h, e)
	res, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return // cancelled by the client: no answer is expected
		}
		b.fail(w, e)
		return
	}
	defer res.Body.Close()
	if w == nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		if ctx.Err() == nil {
			b.fail(w, e)
		}
		return
	}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		b.relaySSE(data, e, w)
		return
	}
	if res.StatusCode == http.StatusAccepted || len(bytes.TrimSpace(data)) == 0 {
		if res.StatusCode >= 400 {
			b.fail(w, e)
		}
		return
	}
	b.answer(data, e, w)
}

// headers sets the MCP request headers: modern requests mirror their _meta version, method and
// name; legacy requests carry the version initialize negotiated.
func (b *Bridge) headers(h http.Header, e jsonrpc.Envelope) {
	method := e.Method()
	if v := e.ModernVersion(); v != "" {
		h.Set(httpgw.HProtocolVersion, v)
		h.Set(httpgw.HMethod, method)
		if field := nameField(method); field != "" {
			var p map[string]json.RawMessage
			var name string
			if json.Unmarshal(e["params"], &p) == nil && json.Unmarshal(p[field], &name) == nil {
				h.Set(httpgw.HName, httpgw.EncodeHeader(name))
			}
		}
		return
	}
	b.mu.Lock()
	v := b.legacy
	b.mu.Unlock()
	if v != "" && method != "initialize" {
		h.Set(httpgw.HProtocolVersion, v)
	}
}

func nameField(method string) string {
	switch method {
	case "tools/call", "prompts/get":
		return "name"
	case "resources/read":
		return "uri"
	}
	return ""
}

// answer writes one JSON-RPC body as a line, with the request's id when the server's error had
// none (errors raised before the gateway read the message).
func (b *Bridge) answer(data []byte, req jsonrpc.Envelope, w *jsonrpc.Writer) {
	var c bytes.Buffer
	if json.Compact(&c, data) != nil {
		b.fail(w, req)
		return
	}
	var resp jsonrpc.Envelope
	if json.Unmarshal(c.Bytes(), &resp) != nil || resp == nil {
		b.fail(w, req)
		return
	}
	if id := resp.ID(); id == nil || string(id) == "null" {
		resp["id"] = req.ID()
	}
	if resp.Kind() != jsonrpc.Response {
		b.fail(w, req)
		return
	}
	if req.Method() == "initialize" {
		var r struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(resp["result"], &r) == nil && r.ProtocolVersion != "" {
			b.mu.Lock()
			b.legacy = r.ProtocolVersion
			b.mu.Unlock()
		}
	}
	_ = w.SendEnvelope(resp)
}

// relaySSE writes each data: message of a request-scoped event stream (forward compatibility:
// the gateway answers application/json today).
func (b *Bridge) relaySSE(data []byte, req jsonrpc.Envelope, w *jsonrpc.Writer) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), maxBody)
	var ev strings.Builder
	flush := func() {
		if ev.Len() == 0 {
			return
		}
		data := []byte(ev.String())
		ev.Reset()
		// Request-scoped notifications (progress, log messages) go through as they are.
		if e, err := jsonrpc.Parse(data); err == nil && e.Kind() == jsonrpc.Notification {
			_ = w.SendEnvelope(e)
			return
		}
		b.answer(data, req, w)
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			ev.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
}

// fail answers a request the server didn't: a tool call gets the calm unreachable result, any
// other request a -32603 with the same sentence. Nothing ran.
func (b *Bridge) fail(w *jsonrpc.Writer, e jsonrpc.Envelope) {
	if w == nil || e.Kind() != jsonrpc.Request {
		return
	}
	msg := gate.UnreachableMessage(b.cfg.Server.Redacted())
	if e.Method() == "tools/call" {
		r := map[string]any{"content": []map[string]string{{"type": "text", "text": msg}}, "isError": true}
		if e.ModernVersion() != "" {
			r["resultType"] = "complete"
		}
		_ = w.SendEnvelope(jsonrpc.NewResponse(e.ID(), jsonrpc.Value(r)))
		return
	}
	_ = w.SendEnvelope(jsonrpc.NewError(e.ID(), jsonrpc.CodeInternal, msg))
}

// Endpoint is the URL the bridge posts to.
func (b *Bridge) Endpoint() string { return b.endpoint }

// String is for logs: never the key.
func (b *Bridge) String() string { return fmt.Sprintf("bridge to %s", b.endpoint) }
