package identity

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

// MCP authorization readiness (S09 §3.10, docs/MCP_AUTHORIZATION.md). Under the MCP 2026-07-28
// revision, authorization is optional for HTTP servers; one that implements it serves RFC 9728
// protected-resource metadata and answers with RFC 6750 challenges that point at it. Deedglass v1
// authenticates MCP clients with pre-issued API keys as bearer tokens and has no authorization
// server of its own, so the metadata omits authorization_servers on purpose. v2 lists the org's
// identity provider there (docs/IDENTITY.md).

// MCP endpoint and metadata paths.
const (
	MetadataPath     = "/.well-known/oauth-protected-resource"
	MCPDocsURL       = "https://docs.deedglass.com/mcp/authorization"
	mcpOriginMessage = "Deedglass refuses browser requests from this origin, to protect MCP endpoints from DNS rebinding. Call the Gate from your MCP client, not from a web page."
	mcpStubMessage   = "The MCP gateway isn't enabled on this server."
)

// ProtectedResource is an RFC 9728 protected-resource metadata document. AuthorizationServers is
// declared only to keep it out: v1 has no authorization server to list (a test pins its absence).
type ProtectedResource struct {
	Resource               string   `json:"resource"`
	ResourceName           string   `json:"resource_name"`
	AuthorizationServers   []string `json:"authorization_servers,omitempty"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ScopesSupported        []string `json:"scopes_supported"`
	ResourceDocumentation  string   `json:"resource_documentation"`
}

// CanonicalResource is base as an RFC 9728 / RFC 8707 resource identifier: lower-case scheme and
// host, no default port, no query or fragment, and no trailing slash.
func CanonicalResource(base *url.URL) string {
	if base == nil {
		return ""
	}
	scheme := strings.ToLower(base.Scheme)
	host := strings.ToLower(base.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // IPv6 literal
	}
	if p := base.Port(); p != "" && !(scheme == "https" && p == "443") && !(scheme == "http" && p == "80") {
		host = net.JoinHostPort(strings.Trim(host, "[]"), p)
	}
	return scheme + "://" + host + strings.TrimRight(base.EscapedPath(), "/")
}

// canonicalOrigin is the origin (RFC 6454) of a URL, in the same canonical form.
func canonicalOrigin(u *url.URL) string {
	o := *u
	o.Path, o.RawPath = "", ""
	return CanonicalResource(&o)
}

// MCPAuth is the middleware on /mcp/* (httpx.RouteGuard). It refuses browser requests from other
// origins, and shapes the bearer challenges of S08's key pipeline so MCP clients can discover the
// protected-resource metadata. S15 mounts the gateway behind it, and must call
// StripInboundCredentials before forwarding anything upstream.
type MCPAuth struct {
	base     *url.URL
	resource string // CanonicalResource(base)
	origin   string
}

// NewMCPAuth returns the middleware for the server at base (DEEDGLASS_BASE_URL).
func NewMCPAuth(base *url.URL) *MCPAuth {
	return &MCPAuth{base: base, resource: CanonicalResource(base), origin: canonicalOrigin(base)}
}

var _ httpx.RouteGuard = (*MCPAuth)(nil)

// Admit implements httpx.RouteGuard: an Origin header that isn't the base URL's origin is refused
// with 403 and a JSON-RPC error without an id (MCP Streamable HTTP: DNS-rebinding protection).
// Requests without Origin (every non-browser MCP client) pass.
func (m *MCPAuth) Admit(w http.ResponseWriter, r *http.Request) bool {
	o := r.Header.Values("Origin")
	if len(o) == 0 {
		return true
	}
	if len(o) == 1 {
		if u, err := url.Parse(o[0]); err == nil && u.Scheme != "" && u.Host != "" && u.Path == "" && canonicalOrigin(u) == m.origin {
			return true
		}
	}
	writeRPCError(w, http.StatusForbidden, nil, -32600, mcpOriginMessage)
	return false
}

// Challenge implements httpx.RouteGuard: every bearer challenge on an MCP endpoint names its
// metadata (resource_metadata) and the scope it needs, as MCP 2026-07-28 asks.
func (m *MCPAuth) Challenge(r *http.Request, v string) string {
	if !strings.HasPrefix(v, "Bearer ") {
		return v
	}
	if !strings.Contains(v, "resource_metadata=") {
		v += `, resource_metadata="` + m.MetadataURL(r.URL.Path) + `"`
	}
	if !strings.Contains(v, `, scope="`) {
		v += `, scope="` + httpx.ScopeActionsExecute + `"`
	}
	return v
}

// MetadataURL is the path-inserted metadata URL (RFC 9728 §3.1) for an MCP endpoint path. A path
// that isn't a well-formed MCP endpoint gets the root document's URL.
func (m *MCPAuth) MetadataURL(endpoint string) string {
	origin := m.origin
	basePath := strings.TrimRight(m.base.EscapedPath(), "/")
	if !validMCPPath(endpoint) {
		return origin + MetadataPath + basePath
	}
	return origin + MetadataPath + basePath + endpoint
}

// validMCPPath reports whether p is /mcp/native or /mcp/<connector id>.
func validMCPPath(p string) bool {
	rest, ok := strings.CutPrefix(p, "/mcp/")
	return ok && (rest == "native" || ids.Check(ids.Connector, rest) == nil)
}

// MCPRoutes are the protected-resource metadata documents and the /mcp/* endpoints, which answer
// "not enabled" until S15 mounts the gateway behind MCPAuth.
func (m *MCPAuth) MCPRoutes() []httpx.Route {
	return []httpx.Route{
		{Method: "GET", Pattern: MetadataPath, OperationID: "getProtectedResourceMetadata", Auth: httpx.Public,
			Handler: m.metadata(func(*http.Request) (string, bool) { return "", true })},
		{Method: "GET", Pattern: MetadataPath + "/mcp/native", OperationID: "getNativeMCPResourceMetadata", Auth: httpx.Public,
			Handler: m.metadata(func(*http.Request) (string, bool) { return "/mcp/native", true })},
		{Method: "GET", Pattern: MetadataPath + "/mcp/{connector}", OperationID: "getMCPResourceMetadata", Auth: httpx.Public,
			Handler: m.metadata(func(r *http.Request) (string, bool) {
				c := r.PathValue("connector")
				return "/mcp/" + c, ids.Check(ids.Connector, c) == nil
			})},
		{Method: "POST", Pattern: "/mcp/{connector}", OperationID: "mcpConnector", Auth: httpx.AgentKey, Scopes: []string{httpx.ScopeActionsExecute},
			Idempotency: httpx.IdemNever, MaxBody: 4 << 20, Guard: m, Handler: mcpStub},
		{Method: "POST", Pattern: "/mcp/native", OperationID: "mcpNative", Auth: httpx.AgentKey, Scopes: []string{httpx.ScopeActionsExecute},
			Idempotency: httpx.IdemNever, MaxBody: 4 << 20, Guard: m, Handler: mcpStub},
	}
}

// metadata serves one document. It never reads the database: every well-formed connector id gets
// the same document, so the endpoint can't be used to learn which connectors exist, and connector
// names (which may be sensitive) never appear.
func (m *MCPAuth) metadata(path func(*http.Request) (string, bool)) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		p, ok := path(r)
		if !ok {
			return httpx.E(httpx.CodeNotFound, httpx.Msg("There's no MCP endpoint with that id. Connector ids look like con_01k67c04kxevyex3pjyqe0t45d."))
		}
		doc := ProtectedResource{Resource: m.resource, ResourceName: "Deedglass", BearerMethodsSupported: []string{"header"},
			ScopesSupported: []string{httpx.ScopeActionsExecute, httpx.ScopeActionsRead, httpx.ScopeDeedsRead}, ResourceDocumentation: MCPDocsURL}
		if p != "" {
			doc.Resource = m.resource + p
			doc.ResourceName = "Deedglass Gate"
			doc.ScopesSupported = []string{httpx.ScopeActionsExecute}
		}
		h := w.Header()
		h.Set("Cache-Control", "public, max-age=3600")
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Cross-Origin-Resource-Policy", "cross-origin")
		return httpx.WriteJSON(w, http.StatusOK, doc)
	}
}

// mcpStub answers every authenticated MCP request until S15: 404 with JSON-RPC -32601, echoing the
// request's id when it has one.
func mcpStub(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		ID json.RawMessage `json:"id"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if json.Unmarshal(body, &req) != nil || !validRPCID(req.ID) {
		req.ID = nil
	}
	writeRPCError(w, http.StatusNotFound, req.ID, -32601, mcpStubMessage)
	return nil
}

// validRPCID accepts a JSON-RPC id: a string, a number or null.
func validRPCID(id json.RawMessage) bool {
	if len(id) == 0 {
		return false
	}
	switch id[0] {
	case '"', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 'n':
		return len(id) <= 256
	}
	return false
}

// writeRPCError writes a JSON-RPC 2.0 error. A nil id is omitted when status is 403 (the Origin
// refusal carries no id) and written as null otherwise.
func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, msg string) {
	type rpcErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	var body any
	if id == nil && status == http.StatusForbidden {
		body = struct {
			JSONRPC string `json:"jsonrpc"`
			Error   rpcErr `json:"error"`
		}{"2.0", rpcErr{code, msg}}
	} else {
		if id == nil {
			id = json.RawMessage("null")
		}
		body = struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Error   rpcErr          `json:"error"`
		}{"2.0", id, rpcErr{code, msg}}
	}
	_ = httpx.WriteJSON(w, status, body)
}

// StripInboundCredentials deletes everything a client sent to authenticate to Deedglass before a
// request is forwarded upstream: Authorization, Proxy-Authorization, Cookie and every Deedglass-*
// header. MCP forbids passing a client's token through to another resource; S15 must call this on
// every forwarded request.
func StripInboundCredentials(h http.Header) {
	for k := range h {
		ck := http.CanonicalHeaderKey(k)
		switch {
		case ck == "Authorization", ck == "Proxy-Authorization", ck == "Cookie", strings.HasPrefix(ck, "Deedglass-"):
			delete(h, k)
		}
	}
}
