package gate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/intent"
	"github.com/deedglass/deedglass/internal/policy"
)

// Limits of a request (S13 §3.6). gate.max_args_bytes may lower MaxArgsBytes, never raise it.
const (
	MaxToolBytes    = 200
	MaxArgsBytes    = intent.MaxArgsBytes // 256 KiB: S11's policy input limit
	MaxArgsDepth    = intent.MaxArgsDepth // 32
	MaxContextBytes = 4096
)

// Tool-name grammar (aligned with S16's naming rules). A name is "<connector>.<tool>", split at
// the first dot. Native tools are lowercase; MCP-sourced tools (proxy mode) use the MCP charset.
var (
	reConnector  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	reNativeTool = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	reMCPTool    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	reDigest     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reTransport  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	reErrorCode  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// Normalized is a request the Gate can record: the tool split, the arguments checked, and the
// digests computed. Nothing in it does I/O.
type Normalized struct {
	Tool          string          // as sent
	Connector     string          // the slug before the first dot; "" when the name isn't a tool name
	ToolName      string          // after the first dot
	Unknown       bool            // the name fails the grammar in a way worth recording (a look-alike): tool_unknown
	Args          json.RawMessage // exactly as sent (a JSON object)
	ArgsBytes     int64           //
	Mode          Mode            //
	ToolDigest    *string         //
	ConnectorType string          // proxy: the edge's declared type (default mcp); gateway: set from the registry
	Digest        string          // request_digest
	Fingerprint   string          // request_digest + on-behalf-of + session (the Idempotency-Key comparison)
	Context       json.RawMessage // what the action row stores as context: the client, the transport, the connector type
	Transport     string          //
	// HTTP is the request a REST connector will send (S16's request.http), filled by the
	// connector's enrichment before the decision; empty otherwise.
	HTTP policy.HTTPFacts
	// GitHub and Slack are those connectors' facts (S29: request.github, request.slack).
	GitHub policy.GitHubFacts
	Slack  policy.SlackFacts
	// Stripe and Email are S30's request.stripe and request.email.
	Stripe policy.StripeFacts
	Email  policy.EmailFacts
	// DefaultSchema and RaisedSideEffect come from the Postgres connector's enrichment (S17): the
	// schema unqualified names resolve to, and a per-call side effect above the tool's declared one.
	DefaultSchema    string
	RaisedSideEffect string
}

// digestBody is what request_digest covers (S13 §3.6): JCS of these four members.
type digestBody struct {
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args"`
	Mode       Mode            `json:"mode"`
	ToolDigest *string         `json:"tool_digest"`
}

// RequestDigest is "sha256:" + hex(sha256(JCS({tool, args, mode, tool_digest}))).
func RequestDigest(tool string, args json.RawMessage, mode Mode, toolDigest *string) (string, error) {
	b, err := json.Marshal(digestBody{Tool: tool, Args: args, Mode: mode, ToolDigest: toolDigest})
	if err != nil {
		return "", err
	}
	return deed.DigestJSON(b)
}

// Fingerprint is what an Idempotency-Key compares: the request digest plus the on-behalf-of and
// session headers, so the same key with another user or session is a different request.
func Fingerprint(requestDigest, onBehalfOf, session string) (string, error) {
	type fp struct {
		RequestDigest string  `json:"request_digest"`
		OnBehalfOf    *string `json:"on_behalf_of"`
		Session       *string `json:"session"`
	}
	v := fp{RequestDigest: requestDigest}
	if onBehalfOf != "" {
		v.OnBehalfOf = &onBehalfOf
	}
	if session != "" {
		v.Session = &session
	}
	return deed.DigestValue(v)
}

// Normalize checks req and computes its digests (S13 §3.6). A malformed request is a 400
// validation_failed (not recorded); a name that looks like a tool but isn't one (non-ASCII, a
// look-alike) comes back with Unknown set, so the Gate records and seals the attempt.
func Normalize(req ActionRequest, maxArgs int) (*Normalized, error) {
	if maxArgs <= 0 || maxArgs > MaxArgsBytes {
		maxArgs = MaxArgsBytes
	}
	n := &Normalized{Tool: req.Tool, Mode: req.Mode, Args: req.Args}
	if n.Mode == "" {
		n.Mode = ModeGateway
	}
	if n.Mode != ModeGateway && n.Mode != ModeProxy {
		return nil, httpx.ErrValidation("mode", msgMode(string(req.Mode)))
	}
	mcpNames := n.Mode == ModeProxy || req.Context != nil && isMCP(req.Context.Transport)
	if err := n.checkTool(mcpNames); err != nil {
		return nil, err
	}
	// Arguments: a JSON object, at most maxArgs bytes and 32 levels, integers within ±(2^53−1).
	n.ArgsBytes = int64(len(req.Args))
	if len(req.Args) == 0 {
		return nil, httpx.ErrValidation("args", msgArgsMissing)
	}
	if len(req.Args) > maxArgs {
		return nil, httpx.ErrValidation("args", msgArgsTooLarge(len(req.Args), maxArgs))
	}
	v, err := intent.ParseStrictArgs(req.Args)
	if err != nil {
		return nil, httpx.ErrValidation("args", msgArgsInvalid(err))
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, httpx.ErrValidation("args", msgArgsNotObject)
	}
	// Proxy mode: the edge declares its connector type (S10's enum); gateway takes it from the
	// registry, so a client may not claim one.
	switch {
	case n.Mode == ModeGateway && req.ConnectorType != "":
		return nil, httpx.ErrValidation("connector_type", msgConnectorTypeGateway)
	case n.Mode == ModeProxy && req.ConnectorType == "":
		n.ConnectorType = "mcp"
	case n.Mode == ModeProxy:
		if !policy.KnownConnectorTypes[req.ConnectorType] {
			return nil, httpx.ErrValidation("connector_type", msgConnectorTypeUnknown(req.ConnectorType))
		}
		n.ConnectorType = req.ConnectorType
	}
	if req.ToolDigest != "" {
		if !reDigest.MatchString(req.ToolDigest) {
			return nil, httpx.ErrValidation("tool_digest", msgToolDigest)
		}
		d := req.ToolDigest
		n.ToolDigest = &d
	}
	cx, transport, err := contextJSON(req.Context, n.Mode, n.ConnectorType)
	if err != nil {
		return nil, err
	}
	n.Context, n.Transport = cx, transport
	if n.Digest, err = RequestDigest(n.Tool, n.Args, n.Mode, n.ToolDigest); err != nil {
		return nil, httpx.ErrValidation("args", msgArgsInvalid(err))
	}
	if n.Fingerprint, err = Fingerprint(n.Digest, req.OnBehalfOf, req.Session); err != nil {
		return nil, err
	}
	return n, nil
}

// checkTool applies the tool grammar. ASCII mistakes ("Fake.echo", "fake", ".echo") are 400s with
// the grammar; a non-ASCII name is recorded as tool_unknown, because a look-alike ("fаke.echo"
// with a Cyrillic а) is an attempt worth a deed.
//
// mcpNames applies the MCP tool-name charset after the connector: proxy mode (S14), and gateway
// calls that arrive over an MCP transport, whose upstream servers name tools in camelCase (S15).
func (n *Normalized) checkTool(mcpNames bool) error {
	t := n.Tool
	switch {
	case t == "":
		return httpx.ErrValidation("tool", msgToolEmpty)
	case len(t) > MaxToolBytes:
		return httpx.ErrValidation("tool", msgToolTooLong)
	case !utf8.ValidString(t):
		return httpx.ErrValidation("tool", msgToolNotUTF8)
	case strings.ContainsFunc(t, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return httpx.ErrValidation("tool", msgToolGrammar(t))
	}
	conn, name, ok := strings.Cut(t, ".")
	if !isASCII(t) {
		if ok && reConnector.MatchString(conn) {
			n.Connector = conn
		}
		n.ToolName, n.Unknown = name, true
		return nil
	}
	if !ok || !reConnector.MatchString(conn) {
		return httpx.ErrValidation("tool", msgToolGrammar(t))
	}
	re := reNativeTool
	if mcpNames {
		re = reMCPTool
	}
	if !re.MatchString(name) {
		return httpx.ErrValidation("tool", msgToolGrammar(t))
	}
	n.Connector, n.ToolName = conn, name
	return nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// storedContext is the action's context column.
type storedContext struct {
	Client        *ClientInfo `json:"client,omitempty"`
	Transport     string      `json:"transport,omitempty"`
	ConnectorType string      `json:"connector_type,omitempty"` // proxy: the edge's declared type
}

func contextJSON(c *RequestContext, mode Mode, ctype string) (json.RawMessage, string, error) {
	sc := storedContext{}
	if mode == ModeProxy {
		sc.ConnectorType = ctype
	}
	if c != nil {
		if c.Transport != "" && !reTransport.MatchString(c.Transport) {
			return nil, "", httpx.ErrValidation("context.transport", msgTransport)
		}
		if c.Client != nil {
			if len(c.Client.Name) > 64 || len(c.Client.Version) > 64 || !utf8.ValidString(c.Client.Name) || !utf8.ValidString(c.Client.Version) {
				return nil, "", httpx.ErrValidation("context.client", msgClient)
			}
			if c.Client.Name != "" || c.Client.Version != "" {
				cl := *c.Client
				sc.Client = &cl
			}
		}
		sc.Transport = c.Transport
	}
	if sc.Client == nil && sc.Transport == "" && sc.ConnectorType == "" {
		return nil, "", nil
	}
	b, err := json.Marshal(sc)
	if err != nil {
		return nil, "", err
	}
	if len(b) > MaxContextBytes {
		return nil, "", httpx.ErrValidation("context", fmt.Sprintf("context is %d bytes; the limit is %d. Send the client name, version and transport only.", len(b), MaxContextBytes))
	}
	return b, sc.Transport, nil
}

// isMCP reports whether the call came through an MCP surface (S14, S15).
func isMCP(transport string) bool {
	return transport == TransportMCPStdio || transport == TransportMCPHTTP
}

// safeErrorCode keeps a connector's error code when it fits the column's grammar.
func safeErrorCode(code, fallback string) string {
	if reErrorCode.MatchString(code) {
		return code
	}
	return fallback
}
