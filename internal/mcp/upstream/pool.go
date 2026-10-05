// Package upstream is the MCP gateway's client side (S15): one official-SDK client session per
// configured remote MCP server (mcp.upstreams in deedglass.yaml), era-negotiating (server/discover
// first, initialize as the fallback). The Pool is the Gate's executor for those connectors
// (gateway mode), a cached tool list for the front, and an optional passthrough for resources and
// prompts. S16 moves upstreams into connector rows of type mcp_http with credentials in the vault;
// Config stays the shape the loader fills.
package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mcp/front"
	"github.com/deedglass/deedglass/internal/mcp/toolscan"
)

// ConnectorType is the type an upstream MCP connector carries in policy facts and deeds.
const ConnectorType = "mcp_http"

// MaxResponse caps an upstream response body (4 MiB); a larger answer is an unknown outcome.
const MaxResponse = 4 << 20

// MaxResultDepth bounds a tools/call result's nesting (S34; the same 128 as a request body).
const MaxResultDepth = 128

// jsonDepth is the deepest nesting of objects and arrays in b (strings skipped).
func jsonDepth(b []byte) int {
	depth, deepest, inStr, esc := 0, 0, false, false
	for _, c := range b {
		switch {
		case inStr && esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case inStr && c == '"':
			inStr = false
		case inStr:
		case c == '"':
			inStr = true
		case c == '{' || c == '[':
			depth++
			deepest = max(deepest, depth)
		case c == '}' || c == ']':
			depth--
		}
	}
	return deepest
}

// maxPages bounds one tools/list walk.
const maxPages = 20

// Config is one upstream (mcp.upstreams.<slug>).
type Config struct {
	Slug           string
	URL            *url.URL
	HeadersFromEnv map[string]string // header → env var name; read once at start, never logged or returned
	Timeout        time.Duration     // per HTTP request to the upstream
	ToolsTTL       time.Duration     // tools/list cache
}

// Options configure a Pool.
type Options struct {
	Upstreams    []Config
	AllowPrivate bool           // DEEDGLASS_ALLOW_PRIVATE_NETWORKS (dev): http:// and private addresses
	Egress       *egress.Policy // the one door (S16); nil: built from AllowPrivate (dev with the override, else prod)
	Version      string
	Getenv       func(string) string // nil: os.Getenv is not used; the caller resolves (tests pass a map)
	Log          *slog.Logger
	Clock        func() time.Time
}

// Pool holds the upstreams: the file-configured ones (mcp.upstreams, by slug) and the sessions of
// mcp_http connector rows (S18), keyed by connector id, config hash and a keyed MAC of the
// credential, so a replaced credential opens a new session.
type Pool struct {
	log     *slog.Logger
	now     func() time.Time
	ups     map[string]*up
	rows    sync.Map // rowKey → *up
	version string
	stash   stash

	// S34: the tool screen. pins holds each tool's first definition digest (scope + "\x00" + name);
	// reported dedupes the admin deeds (one per scope, tool, digest and reason per process).
	pins     sync.Map
	reported sync.Map
	onHidden HiddenRecorder
}

// HiddenTool is a tool the screen hid (S34, attack cases Q01 and Q02).
type HiddenTool struct {
	Org         ids.OrgID // zero for a file upstream (mcp.upstreams, dev only)
	ConnectorID string    // "" for a file upstream
	Connector   string    // the connector's name or the file upstream's slug
	Tool        string
	Reason      string // toolscan.ReasonDescription | toolscan.ReasonChanged
	Kind        string // the finding's kind ("instruction", "tag", "hidden_characters") or "changed"
	Field       string
	Excerpt     string // ≤ 40 characters, hidden characters escaped
	Digest      string // the definition's digest now
	Pinned      string // the first digest (definition_changed only)
	Sentence    string // the admin's line (toolscan.Sentence)
}

// HiddenRecorder records a hidden tool (serve seals connector.tool_hidden through it). It runs on
// its own goroutine with its own deadline; a failure is logged by the recorder.
type HiddenRecorder func(ctx context.Context, h HiddenTool)

// SetHiddenRecorder installs the recorder (serve calls it once, before the pool serves).
func (p *Pool) SetHiddenRecorder(r HiddenRecorder) { p.onHidden = r }

type up struct {
	cfg     Config
	pol     *egress.Policy
	client  *mcp.Client
	httpc   *http.Client
	mu      sync.Mutex // guards session and the cache
	session *mcp.ClientSession
	tools   []*mcp.Tool
	fresh   time.Time       // the cache is good until then
	down    bool            // the last connect or list failed
	hidden  map[string]bool // S34: tools the screen hid in the last listing (flagged or changed)

	// S34: who this upstream is, for the pin and the admin deed. scope keys the pins ("file:<slug>"
	// or "row:<con_ id>"), so a new credential or config keeps its connector's pins.
	scope       string
	org         ids.OrgID
	connectorID string
}

// ErrDown is returned when an upstream can't be reached (front.ErrUpstreamDown wraps it).
var ErrDown = errors.New("upstream: the MCP server can't be reached")

// New returns a Pool. Configs are validated by internal/config; New checks only what it needs.
func New(o Options) (*Pool, error) {
	p := &Pool{log: o.Log, now: o.Clock, ups: map[string]*up{}, version: o.Version}
	if p.log == nil {
		p.log = slog.New(slog.DiscardHandler)
	}
	if p.now == nil {
		p.now = time.Now
	}
	pol := o.Egress
	if pol == nil {
		pol = &egress.Policy{Env: egress.EnvProd}
		if o.AllowPrivate {
			pol = &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}
		}
	}
	for _, c := range o.Upstreams {
		if c.URL == nil || c.Slug == "" {
			return nil, fmt.Errorf("upstream: %q needs a URL", c.Slug)
		}
		if c.URL.Scheme != "https" && !(c.URL.Scheme == "http" && o.AllowPrivate) {
			return nil, fmt.Errorf("upstream %s: only https URLs are allowed (http needs DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1 in dev)", c.Slug)
		}
		if c.Timeout <= 0 {
			c.Timeout = 30 * time.Second
		}
		if c.ToolsTTL <= 0 {
			c.ToolsTTL = 30 * time.Second
		}
		hdr := http.Header{}
		for h, env := range c.HeadersFromEnv {
			v := ""
			if o.Getenv != nil {
				v = o.Getenv(env)
			}
			if v == "" {
				return nil, fmt.Errorf("upstream %s: header %s reads %s, which is empty", c.Slug, h, env)
			}
			hdr.Set(h, v)
		}
		// The URL's shape, and its address when it is one (or resolves now) must pass egress;
		// a name that doesn't resolve yet is checked again at every connect.
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := pol.CheckURL(cctx, c.URL.String())
		cancel()
		if _, blocked := egress.IsBlocked(err); blocked {
			return nil, fmt.Errorf("upstream %s: %w", c.Slug, err)
		}
		u := newUp(c, hdr, pol, o.Version, p.log)
		u.scope = "file:" + c.Slug
		p.ups[c.Slug] = u
	}
	return p, nil
}

func newUp(c Config, hdr http.Header, pol *egress.Policy, version string, log *slog.Logger) *up {
	u := &up{cfg: c, pol: pol, httpc: newHTTPClient(c.Timeout, hdr, pol, version)}
	u.client = mcp.NewClient(&mcp.Implementation{Name: "deedglass-gate", Title: "Deedglass Gate", Version: version}, nil)
	u.client.AddReceivingMiddleware(serverRequestGuard(log, c.Slug))
	return u
}

// serverRequestGuard (S34, attack case Q06): the Gate's client declares no sampling, elicitation or
// roots capability, so the SDK answers those server-to-client requests with an error and nothing
// reaches an MCP client or a model. The guard logs each one, so an admin sees an upstream asking.
func serverRequestGuard(log *slog.Logger, slug string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch {
			case method == "ping", strings.HasPrefix(method, "notifications/"):
			default:
				if log != nil {
					log.Warn("mcp: an upstream sent a server-to-client request; refused, nothing was relayed", "upstream", slug, "method", clipMethod(method))
				}
			}
			return next(ctx, method, req)
		}
	}
}

func clipMethod(m string) string {
	if len(m) > 64 {
		m = m[:64]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, m)
}

// Slugs lists the upstreams, sorted.
func (p *Pool) Slugs() []string {
	out := make([]string, 0, len(p.ups))
	for s := range p.ups {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Has reports whether slug is an upstream.
func (p *Pool) Has(slug string) bool { _, ok := p.ups[slug]; return ok }

// Close ends every session.
func (p *Pool) Close() error {
	closeUp := func(u *up) {
		u.mu.Lock()
		u.dropLocked()
		u.mu.Unlock()
	}
	for _, u := range p.ups {
		closeUp(u)
	}
	p.rows.Range(func(k, v any) bool {
		closeUp(v.(*up))
		p.rows.Delete(k)
		return true
	})
	return nil
}

// sessionLocked connects when there is no session. u.mu is held.
func (u *up) sessionLocked(ctx context.Context) (*mcp.ClientSession, error) {
	if u.session != nil {
		return u.session, nil
	}
	t := &mcp.StreamableClientTransport{Endpoint: u.cfg.URL.String(), HTTPClient: u.httpc, MaxRetries: -1, DisableStandaloneSSE: true}
	cctx, cancel := context.WithTimeout(ctx, u.cfg.Timeout)
	defer cancel()
	s, err := u.client.Connect(cctx, t, nil)
	if err != nil {
		u.down = true
		return nil, fmt.Errorf("%w: %v", ErrDown, err)
	}
	u.session, u.down = s, false
	return s, nil
}

func (u *up) dropLocked() {
	if u.session != nil {
		_ = u.session.Close()
		u.session = nil
	}
}

// ListTools returns the upstream's tools, from the cache when fresh (min of tools_ttl and the
// upstream's ttlMs). A lost legacy session is reconnected once and the list retried; tools/call is
// never retried.
func (p *Pool) ListTools(ctx context.Context, slug string) ([]*mcp.Tool, error) {
	u := p.ups[slug]
	if u == nil {
		return nil, connector.ErrToolUnknown
	}
	return p.listOn(ctx, u)
}

func (p *Pool) listOn(ctx context.Context, u *up) ([]*mcp.Tool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.tools != nil && p.now().Before(u.fresh) {
		return u.tools, nil
	}
	tools, ttl, err := u.listLocked(ctx)
	if err != nil && !errors.Is(err, ErrDown) {
		u.dropLocked()
		tools, ttl, err = u.listLocked(ctx)
	}
	if err != nil {
		u.dropLocked()
		u.down = true
		return nil, fmt.Errorf("%w: %v", ErrDown, err)
	}
	visible, events := p.screen(u, tools)
	u.tools, u.fresh = visible, p.now().Add(ttl)
	for _, ev := range events {
		p.report(ev)
	}
	return visible, nil
}

// screen (S34) hides the tools a client must not see: a definition whose text carries
// instructions aimed at the model or hidden characters (toolscan, Q01), and a definition that
// changed after it was first listed (the pin, Q02; hidden until Deedglass restarts). u.mu is held.
func (p *Pool) screen(u *up, tools []*mcp.Tool) ([]*mcp.Tool, []HiddenTool) {
	out := make([]*mcp.Tool, 0, len(tools))
	hidden := map[string]bool{}
	var events []HiddenTool
	for _, t := range tools {
		if t == nil {
			continue
		}
		def, err := json.Marshal(t)
		if err != nil {
			continue
		}
		digest := front.DefDigest(def)
		ev := HiddenTool{Org: u.org, ConnectorID: u.connectorID, Connector: u.cfg.Slug, Tool: t.Name, Digest: digest}
		if f, bad := toolscan.Flagged(def); bad {
			ev.Reason, ev.Kind, ev.Field, ev.Excerpt = toolscan.ReasonDescription, f.Kind, f.Field, f.Excerpt
			ev.Sentence = toolscan.Sentence(t.Name, u.cfg.Slug, f)
			hidden[t.Name] = true
			events = append(events, ev)
			continue
		}
		key := u.scope + "\x00" + t.Name
		if was, loaded := p.pins.LoadOrStore(key, digest); loaded && was.(string) != digest {
			ev.Reason, ev.Kind, ev.Pinned = toolscan.ReasonChanged, "changed", was.(string)
			ev.Sentence = toolscan.Sentence(t.Name, u.cfg.Slug, toolscan.Finding{Kind: "changed"})
			hidden[t.Name] = true
			events = append(events, ev)
			continue
		}
		out = append(out, t)
	}
	u.hidden = hidden
	return out, events
}

// report logs a hidden tool and records it once per scope, tool, digest and reason.
func (p *Pool) report(ev HiddenTool) {
	k := ev.Org.String() + "\x00" + ev.Connector + "\x00" + ev.Tool + "\x00" + ev.Digest + "\x00" + ev.Reason
	if _, seen := p.reported.LoadOrStore(k, true); seen {
		return
	}
	p.log.Warn(ev.Sentence, "upstream", ev.Connector, "tool", clipMethod(ev.Tool), "reason", ev.Reason, "alert", "tool_hidden")
	if p.onHidden == nil || ev.ConnectorID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		p.onHidden(ctx, ev)
	}()
}

// isHidden reports whether the last listing hid name.
func (u *up) isHidden(name string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hidden[name]
}

func (u *up) listLocked(ctx context.Context) ([]*mcp.Tool, time.Duration, error) {
	s, err := u.sessionLocked(ctx)
	if err != nil {
		return nil, 0, err
	}
	ttl := u.cfg.ToolsTTL
	var all []*mcp.Tool
	cursor := ""
	for page := 0; page < maxPages; page++ {
		lctx, cancel := context.WithTimeout(ctx, u.cfg.Timeout)
		res, err := s.ListTools(lctx, &mcp.ListToolsParams{Cursor: cursor})
		cancel()
		if err != nil {
			return nil, 0, err
		}
		if res.TTLMs > 0 && time.Duration(res.TTLMs)*time.Millisecond < ttl {
			ttl = time.Duration(res.TTLMs) * time.Millisecond
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return all, ttl, nil
}

// cachedTool returns the cached definition of one tool, if the cache has it.
func (p *Pool) cachedTool(slug, name string) *mcp.Tool {
	u := p.ups[slug]
	if u == nil {
		return nil
	}
	return u.cached(name)
}

func (u *up) cached(name string) *mcp.Tool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, t := range u.tools {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// --- gate.Executor / gate.Resolver ---

// Resolve implements the Gate's Resolver for upstream tools: any tool name of a configured slug
// resolves (the upstream decides whether it exists), so a call made while the upstream is down is
// still recorded, and fails as connector_unavailable.
func (p *Pool) Resolve(_ context.Context, _ ids.OrgID, qualified string) (gate.Executor, connector.ToolSpec, string, error) {
	slug, name, ok := strings.Cut(qualified, ".")
	if !ok || !p.Has(slug) || name == "" {
		return nil, connector.ToolSpec{}, "", connector.ErrToolUnknown
	}
	if p.ups[slug].isHidden(name) {
		return nil, connector.ToolSpec{}, "", connector.ErrToolUnknown // S34: flagged or changed (Q01, Q02)
	}
	spec := connector.ToolSpec{Name: name, SideEffect: connector.SideEffectUnknown}
	if t := p.cachedTool(slug, name); t != nil {
		spec.Description = t.Description
		if b, err := json.Marshal(t.InputSchema); err == nil {
			spec.InputSchema = b
		}
	}
	return executor{p: p, slug: slug}, spec, ConnectorType, nil
}

type executor struct {
	p    *Pool
	slug string
}

// Execute implements gate.Executor (connector.Executor).
func (x executor) Execute(ctx context.Context, call connector.Call) (connector.Result, error) {
	return x.p.Execute(ctx, x.slug, call)
}

// Execute runs one tools/call on the upstream (S15 §3.4's classification):
//   - the context ended before anything was sent → failed, client_cancelled (nothing ran)
//   - no session, or the connection was refused → failed, connector_unavailable (nothing ran)
//   - the context's deadline → unknown, timeout
//   - a JSON-RPC error → failed, upstream_error (the server refused the call)
//   - isError: true → failed, tool_error
//   - a response over 4 MiB → unknown, too_large
//   - anything else in doubt → unknown
//
// It never retries: the call may have applied.
func (p *Pool) Execute(ctx context.Context, slug string, call connector.Call) (connector.Result, error) {
	u := p.ups[slug]
	if u == nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindConfig, Code: "connector_unavailable",
			AgentMessage: gate.MCPUpstreamDownText(slug)}
	}
	return p.execOn(ctx, u, slug, call)
}

// execOn runs one tools/call on u; slug names it in messages.
func (p *Pool) execOn(ctx context.Context, u *up, slug string, call connector.Call) (connector.Result, error) {
	if ctx.Err() != nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindCancelled, Code: gate.ReasonClientCancelled,
			AgentMessage: "The MCP client went away before the call was sent. Nothing was run.", Err: context.Cause(ctx)}
	}
	u.mu.Lock()
	s, err := u.sessionLocked(ctx)
	u.mu.Unlock()
	if err != nil {
		// The SDK flattens transport errors into text, so ask the egress policy directly whether
		// the destination is the reason (destination_blocked rather than unavailable).
		if _, cerr := u.pol.CheckURL(ctx, u.cfg.URL.String()); cerr != nil {
			if be, ok := egress.IsBlocked(cerr); ok {
				return connector.Result{}, connector.ErrDestinationBlocked(be)
			}
		}
		return connector.Result{}, unavailable(slug, err)
	}
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: call.Tool.Name, Arguments: json.RawMessage(call.Args)})
	if err != nil {
		return p.callError(ctx, u, slug, call, err)
	}
	raw, merr := json.Marshal(res)
	if merr != nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindUpstream, Code: "invalid_result",
			AgentMessage: "The MCP server's answer couldn't be read. It may have run.", Err: merr}
	}
	if len(raw) > MaxResponse {
		return connector.Result{}, &connector.Error{Kind: connector.KindUpstream, Code: "too_large", AgentMessage: gate.MCPTooLarge}
	}
	if jsonDepth(raw) > MaxResultDepth {
		// S34 (Q04): a result nested deeper than any client should walk. It may have run.
		return connector.Result{}, &connector.Error{Kind: connector.KindUpstream, Code: "upstream_error",
			AgentMessage: "The MCP server's answer is nested too deeply to pass on. It may have run."}
	}
	p.stash.put(call.ActionID.String(), raw, nil, p.now())
	if res.IsError {
		return connector.Result{Output: raw}, &connector.Error{Kind: connector.KindToolError, Code: "tool_error",
			AgentMessage: "The tool reported an error."}
	}
	return connector.Result{Output: raw}, nil
}

func unavailable(slug string, err error) error {
	if be, ok := egress.IsBlocked(err); ok {
		return connector.ErrDestinationBlocked(be)
	}
	return &connector.Error{Kind: connector.KindConfig, Code: "connector_unavailable", AgentMessage: gate.MCPUpstreamDownText(slug), Err: err}
}

// callError classifies a failed tools/call.
func (p *Pool) callError(ctx context.Context, u *up, slug string, call connector.Call, err error) (connector.Result, error) {
	var we *mcpWireError
	switch {
	case refused(err):
		u.mu.Lock()
		u.dropLocked()
		u.mu.Unlock()
		return connector.Result{}, unavailable(slug, err)
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded), timedOut(err):
		// S34 (Q05): the upstream's own deadline (timeout_ms, the HTTP client's header timeout) is
		// a timeout too, not a lost answer.
		return connector.Result{}, &connector.Error{Kind: connector.KindTimeout, Code: "timeout",
			AgentMessage: "The MCP server didn't answer in time. It may have run.", Err: err}
	case ctx.Err() != nil:
		return connector.Result{}, &connector.Error{Kind: connector.KindUpstream, Code: gate.ReasonClientCancelled,
			AgentMessage: "The call was cancelled while it ran. It may have run.", Err: err}
	case errors.As(err, &we):
		raw, _ := json.Marshal(struct {
			Code    int64           `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data,omitempty"`
		}{we.Code, we.Message, we.Data})
		p.stash.put(call.ActionID.String(), nil, raw, p.now())
		return connector.Result{}, &connector.Error{Kind: connector.KindToolError, Code: "upstream_error",
			AgentMessage: "The MCP server refused the call.", Err: err}
	case errors.Is(err, errTooDeep) || strings.Contains(err.Error(), errTooDeep.Error()):
		return connector.Result{}, &connector.Error{Kind: connector.KindUpstream, Code: "upstream_error",
			AgentMessage: "The MCP server's answer is nested too deeply to pass on. It may have run.", Err: err}
	case errors.Is(err, errTooLarge) || strings.Contains(err.Error(), errTooLarge.Error()):
		return connector.Result{}, &connector.Error{Kind: connector.KindUpstream, Code: "too_large", AgentMessage: gate.MCPTooLarge, Err: err}
	}
	// Lost mid-call: the session may be broken; the next call reconnects.
	u.mu.Lock()
	u.dropLocked()
	u.mu.Unlock()
	return connector.Result{}, &connector.Error{Kind: connector.KindUpstream, Code: "upstream_error",
		AgentMessage: "The MCP server's answer was lost. It may have run.", Err: err}
}

type mcpWireError = sdkjsonrpc.Error

// timedOut reports a transport timeout: a net.Error that says so, or the text the HTTP client and
// the SDK use when they flatten it.
func timedOut(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	m := err.Error()
	return strings.Contains(m, "timeout awaiting response headers") || strings.Contains(m, "Client.Timeout exceeded") ||
		strings.Contains(m, "context deadline exceeded")
}

// refused: the connection was refused or never dialed, so the request never reached the server.
func refused(err error) bool {
	var op *net.OpError
	if errors.Is(err, syscall.ECONNREFUSED) || errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	m := err.Error()
	return strings.Contains(m, "connect: connection refused") || strings.Contains(m, "dial tcp")
}

// Take implements front.Results: the raw result or JSON-RPC error of an action this process ran.
func (p *Pool) Take(actionID string) (json.RawMessage, json.RawMessage, bool) {
	return p.stash.take(actionID, p.now())
}

// --- the HTTP client ---

var errRedirect = errors.New("upstream: redirects are not followed")
var errTooLarge = errors.New("upstream: response larger than 4 MiB")

func newHTTPClient(timeout time.Duration, hdr http.Header, pol *egress.Policy, version string) *http.Client {
	// The egress door (S16): resolution, classification and the connect-time check. Its User-Agent
	// and body cap are replaced by the pool's own round tripper below.
	c := pol.Client(egress.ClientOptions{HeaderTimeout: timeout, UserAgent: "deedglass/" + version})
	c.Transport = &roundTripper{next: c.Transport, hdr: hdr, ua: "deedglass/" + version}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errRedirect }
	return c
}

// roundTripper strips every inbound credential (identity.StripInboundCredentials: nothing a
// client sent to Deedglass ever travels upstream), then sets the operator's configured headers
// and the User-Agent, and caps the response body.
type roundTripper struct {
	next http.RoundTripper
	hdr  http.Header
	ua   string
}

func (t *roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	identity.StripInboundCredentials(r.Header)
	for k, vs := range t.hdr {
		r.Header[k] = append([]string(nil), vs...)
	}
	r.Header.Set("User-Agent", t.ua)
	res, err := t.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	res.Body = &capped{rc: res.Body, left: MaxResponse}
	return res, nil
}

type capped struct {
	rc   io.ReadCloser
	left int64
	// S34 (Q04): the JSON nesting seen so far, across reads (strings skipped), so a deeply nested
	// answer fails fast here instead of in the SDK's decoder, which drops it and waits out the timeout.
	depth      int
	inStr, esc bool
}

// MaxStreamDepth bounds the nesting of an upstream's answer as it is read (S34). A tools/call
// result is refused at MaxResultDepth after decoding; this catches what can't be decoded at all.
const MaxStreamDepth = 512

var errTooDeep = errors.New("upstream: response nested deeper than 512 levels")

func (c *capped) scan(b []byte) error {
	for _, x := range b {
		switch {
		case c.inStr && c.esc:
			c.esc = false
		case c.inStr && x == '\\':
			c.esc = true
		case c.inStr && x == '"':
			c.inStr = false
		case c.inStr:
		case x == '"':
			c.inStr = true
		case x == '{' || x == '[':
			c.depth++
			if c.depth > MaxStreamDepth {
				return errTooDeep
			}
		case x == '}' || x == ']':
			c.depth--
		}
	}
	return nil
}

func (c *capped) Read(b []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		if n, _ := c.rc.Read(one[:]); n > 0 {
			return 0, errTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(b)) > c.left {
		b = b[:c.left]
	}
	n, err := c.rc.Read(b)
	c.left -= int64(n)
	if serr := c.scan(b[:n]); serr != nil {
		return 0, serr
	}
	return n, err
}

func (c *capped) Close() error { return c.rc.Close() }

// --- the result stash ---

// stash keeps each action's raw upstream answer for a minute, for the front (front.Results).
type stash struct {
	mu sync.Mutex
	m  map[string]stashed
}

type stashed struct {
	result, rpcErr json.RawMessage
	at             time.Time
}

const stashTTL = time.Minute

func (s *stash) put(id string, result, rpcErr json.RawMessage, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]stashed{}
	}
	for k, v := range s.m { // bounded: entries live a minute
		if now.Sub(v.at) > stashTTL {
			delete(s.m, k)
		}
	}
	s.m[id] = stashed{result: result, rpcErr: rpcErr, at: now}
}

func (s *stash) take(id string, now time.Time) (json.RawMessage, json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[id]
	if !ok || now.Sub(v.at) > stashTTL {
		return nil, nil, false
	}
	delete(s.m, id)
	return v.result, v.rpcErr, true
}

// Resolver routes the Gate's Resolve: an upstream slug to the pool, anything else to the native
// registry.
type Resolver struct {
	Native gate.Resolver
	Pool   *Pool
}

// Resolve implements gate.Resolver.
func (r Resolver) Resolve(ctx context.Context, org ids.OrgID, qualified string) (gate.Executor, connector.ToolSpec, string, error) {
	if slug, _, _ := strings.Cut(qualified, "."); r.Pool != nil && r.Pool.Has(slug) {
		return r.Pool.Resolve(ctx, org, qualified)
	}
	return r.Native.Resolve(ctx, org, qualified)
}
