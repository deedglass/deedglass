// Package httpgw is the Streamable HTTP transport of the MCP gateway (S15): POST /mcp/{connector}
// and POST /mcp/native. httpx runs first (identity.MCPAuth's Origin check, the 4 MiB body limit,
// Content-Type, the agent key and its scope); this package checks Accept, the single JSON-RPC
// message, the protocol era and the mirrored headers (MCP-Protocol-Version, Mcp-Method, Mcp-Name,
// Mcp-Param-*), limits each agent to 32 requests in flight, and hands the message to the front.
// It is sessionless: Mcp-Session-Id and Last-Event-ID are ignored, and GET and DELETE answer 405
// (the route table has only POST).
package httpgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/mcp/front"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
)

// MaxInFlight is the per-agent limit of MCP requests being served at once.
const MaxInFlight = 32

// MaxBody is the request body limit of the MCP routes.
const MaxBody = 4 << 20

// Header names (MCP 2026-07-28 Streamable HTTP).
const (
	HProtocolVersion = "MCP-Protocol-Version"
	HMethod          = "Mcp-Method"
	HName            = "Mcp-Name"
	HParamPrefix     = "Mcp-Param-"
)

// Gateway serves the MCP routes.
type Gateway struct {
	Native    *front.Front            // /mcp/native; nil answers "no connector"
	Upstreams map[string]*front.Front // /mcp/{slug}: the file upstreams (mcp.upstreams)
	// Rows returns the front of the caller's org's mcp_http connector named name (S18), or nil
	// when the org has none. nil: only file upstreams are served.
	Rows  func(ctx context.Context, p identity.Principal, name string) (*front.Front, error)
	Guard identity.Guard // nil: a Stop doesn't cancel a running call (tests)
	Log   *slog.Logger

	mu       sync.Mutex
	inflight map[string]int
}

// Connector is the handler of POST /mcp/{connector}.
func (g *Gateway) Connector(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("connector")
	f := g.Upstreams[name]
	if f == nil && g.Rows != nil {
		if p, ok := identity.PrincipalFrom(r.Context()); ok && p.Kind == httpx.KindAgentKey {
			var err error
			if f, err = g.Rows(r.Context(), *p, name); err != nil {
				return err
			}
		}
	}
	if f == nil {
		writeRPC(w, http.StatusNotFound, jsonrpc.NewError(nil, jsonrpc.CodeInvalidRequest, gate.MCPUnknownConnectorText(name)), true)
		return nil
	}
	return g.serve(w, r, f)
}

// NativeHandler is the handler of POST /mcp/native.
func (g *Gateway) NativeHandler(w http.ResponseWriter, r *http.Request) error {
	if g.Native == nil {
		writeRPC(w, http.StatusNotFound, jsonrpc.NewError(nil, jsonrpc.CodeInvalidRequest, gate.MCPUnknownConnectorText("native")), true)
		return nil
	}
	return g.serve(w, r, g.Native)
}

func (g *Gateway) serve(w http.ResponseWriter, r *http.Request, f *front.Front) error {
	p, ok := identity.PrincipalFrom(r.Context())
	if !ok || p.Kind != httpx.KindAgentKey {
		return httpx.ErrInsufficientScope(httpx.ScopeActionsExecute)
	}
	if !acceptsJSON(r.Header.Values("Accept")) {
		writeRPC(w, http.StatusNotAcceptable, jsonrpc.NewError(nil, jsonrpc.CodeInvalidRequest, gate.MCPNotAcceptable), true)
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return httpx.E(httpx.CodeRequestTooLarge, httpx.Msg("The request body is larger than 4 MiB. Send a smaller body."))
		}
		return err
	}
	env, err := jsonrpc.Parse(body)
	if err != nil {
		code, msg := jsonrpc.Reply(err)
		if errors.Is(err, jsonrpc.ErrBatch) {
			msg = gate.MCPOneMessage
		}
		writeRPC(w, http.StatusBadRequest, jsonrpc.NewError(nil, code, msg), true)
		return nil
	}
	if env.Kind() == jsonrpc.Response {
		writeRPC(w, http.StatusBadRequest, jsonrpc.NewError(nil, jsonrpc.CodeInvalidRequest, gate.MCPOneMessage), true)
		return nil
	}
	v := Validate(r.Header, env, schemaOf(r.Context(), f, *p))
	if v.Err != nil {
		writeRPC(w, v.Status, v.Err, env.Kind() == jsonrpc.Notification)
		return nil
	}
	agent := p.AgentID.String()
	if !g.acquire(agent) {
		w.Header().Set("Retry-After", "1")
		writeRPC(w, http.StatusTooManyRequests, jsonrpc.NewError(env.ID(), jsonrpc.CodeInternal, gate.MCPTooManyInFlight), false)
		return nil
	}
	defer g.release(agent)
	ctx := r.Context()
	cancelOn := ctx
	if g.Guard != nil && env.Method() == front.MToolsCall {
		// A Stop cancels a running call; the Gate records a stopped agent's new call itself, so
		// admission keeps the unwatched context.
		var cancel context.CancelFunc
		cancelOn, cancel = g.Guard.Watch(ctx, p.OrgID, p.AgentID)
		defer cancel()
	}
	obo, ses := r.Header.Get("Deedglass-On-Behalf-Of"), r.Header.Get("Deedglass-Session")
	res := f.Handle(ctx, front.Request{Env: env, Principal: *p, Version: v.Version, Modern: v.Modern,
		Transport: gate.TransportMCPHTTP, RequestID: httpx.RequestID(r.Context()), OnBehalfOf: obo, Session: ses, CancelOn: cancelOn})
	if res.Env == nil {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusAccepted)
		return nil
	}
	writeRPC(w, res.HTTPStatus, res.Env, false)
	return nil
}

// schemaOf returns a lookup of a tool's inputSchema for Mcp-Param-* checks.
func schemaOf(ctx context.Context, f *front.Front, p identity.Principal) func(string) json.RawMessage {
	return func(name string) json.RawMessage {
		t, ok, err := f.Tools.Lookup(ctx, p, name)
		if err != nil || !ok {
			return nil
		}
		return t.Schema
	}
}

func (g *Gateway) acquire(agent string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inflight == nil {
		g.inflight = map[string]int{}
	}
	if g.inflight[agent] >= MaxInFlight {
		return false
	}
	g.inflight[agent]++
	return true
}

func (g *Gateway) release(agent string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inflight[agent]--; g.inflight[agent] <= 0 {
		delete(g.inflight, agent)
	}
}

// acceptsJSON: an absent Accept is fine; a present one must allow application/json.
func acceptsJSON(vals []string) bool {
	if len(vals) == 0 {
		return true
	}
	for _, v := range vals {
		for _, part := range strings.Split(v, ",") {
			mt, _, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil {
				continue
			}
			if mt == "application/json" || mt == "application/*" || mt == "*/*" {
				return true
			}
		}
	}
	return false
}

// writeRPC writes one JSON-RPC message. noID drops the id member (errors raised before the
// message's id is known, and answers to notifications).
func writeRPC(w http.ResponseWriter, status int, e jsonrpc.Envelope, noID bool) {
	if noID {
		delete(e, "id")
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(e.Marshal())
}

// --- validation ---

// Validation is the outcome of Validate.
type Validation struct {
	Modern  bool
	Version string
	Status  int              // when Err is set
	Err     jsonrpc.Envelope // a ready error response, or nil
}

// Validate checks the protocol era and every mirrored header against the body (MCP 2026-07-28
// Streamable HTTP "Server Validation"): a missing, undecodable or different value is 400 with
// -32020 HeaderMismatch; an unsupported version is 400 with -32022 and the supported list. Legacy
// requests (no _meta version) may omit the headers, but any header they send must still match:
// a header that routes one way and a body that runs another is refused in both eras.
// schema returns a tool's inputSchema (nil when unknown), for Mcp-Param-*.
func Validate(h http.Header, env jsonrpc.Envelope, schema func(name string) json.RawMessage) Validation {
	id := env.ID()
	method := env.Method()
	notification := env.Kind() == jsonrpc.Notification
	mismatch := func(msg string) Validation {
		return Validation{Status: http.StatusBadRequest, Err: jsonrpc.NewError(id, jsonrpc.CodeHeaderMismatch, msg)}
	}
	unsupported := func(requested string) Validation {
		e := jsonrpc.ErrorObject{Code: jsonrpc.CodeUnsupportedVersion, Message: gate.MCPUnsupportedVersion,
			Data: jsonrpc.Value(map[string]any{"supported": front.AllVersions(), "requested": requested})}
		if id == nil {
			id = jsonrpc.Null
		}
		return Validation{Status: http.StatusBadRequest, Err: jsonrpc.Envelope{"jsonrpc": json.RawMessage(`"2.0"`), "id": id, "error": jsonrpc.Value(e)}}
	}
	hv, hasV := single(h, HProtocolVersion)
	modernV := env.ModernVersion()
	out := Validation{}
	if _, present := env.Meta()[jsonrpc.MetaProtocolVersion]; present {
		if !slices.Contains(front.ModernVersions, modernV) {
			return unsupported(modernV)
		}
		out.Modern, out.Version = true, modernV
		switch {
		case !hasV:
			return mismatch(gate.MCPHeaderMissingText(HProtocolVersion, modernV))
		case hv != modernV:
			return mismatch(gate.MCPHeaderMismatchText(HProtocolVersion, hv, "protocol version", modernV))
		}
	} else {
		switch {
		case !hasV:
			out.Version = "2025-03-26" // clients before 2025-06-18 sent no header
		case slices.Contains(front.ModernVersions, hv):
			return mismatch("Header mismatch: " + HProtocolVersion + " is " + quote(hv) + " but the body has no _meta protocol version. Send io.modelcontextprotocol/protocolVersion in params._meta.")
		case !slices.Contains(front.LegacyVersions, hv):
			if method == front.MInitialize {
				out.Version = hv // initialize negotiates; the front answers with a version it serves
				break
			}
			return unsupported(hv)
		default:
			out.Version = hv
		}
		if method == front.MInitialize {
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(env["params"], &p)
			out.Version = p.ProtocolVersion
		}
	}
	// Mcp-Method: required on every modern request; checked whenever sent.
	mv, hasM := single(h, HMethod)
	switch {
	case !hasM && out.Modern && !notification:
		return mismatch(gate.MCPHeaderMissingText(HMethod, method))
	case hasM && mv != method:
		return mismatch(gate.MCPHeaderMismatchText(HMethod, mv, "method", method))
	}
	// Mcp-Name: tools/call, resources/read and prompts/get.
	if field, ok := nameField(method); ok {
		bodyName := paramString(env["params"], field)
		raw, hasN := single(h, HName)
		switch {
		case !hasN && out.Modern:
			return mismatch(gate.MCPHeaderMissingText(HName, bodyName))
		case hasN:
			got, ok := DecodeHeader(raw)
			if !ok {
				return mismatch(gate.MCPHeaderInvalidText(HName))
			}
			if got != bodyName {
				what := "tool name"
				if field == "uri" {
					what = "resource URI"
				} else if method == "prompts/get" {
					what = "prompt name"
				}
				return mismatch(gate.MCPHeaderMismatchText(HName, got, what, bodyName))
			}
		}
	}
	if method == front.MToolsCall && schema != nil {
		if v := checkParams(h, env, schema); v != nil {
			return mismatch(*v)
		}
	}
	return out
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

// single returns a header's value when it is sent exactly once.
func single(h http.Header, name string) (string, bool) {
	vs := h.Values(name)
	if len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}

func nameField(method string) (string, bool) {
	switch method {
	case "tools/call", "prompts/get":
		return "name", true
	case "resources/read":
		return "uri", true
	}
	return "", false
}

func paramString(params json.RawMessage, field string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) != nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(m[field], &s)
	return s
}
