// Package front is the sessionless, dual-era MCP server core behind POST /mcp/{connector} and
// POST /mcp/native, and behind deedglass mcp serve through them (S15, ADR-037). It answers one
// JSON-RPC message at a time, with no session state: initialize (legacy) and server/discover
// (2026-07-28), tools/list filtered for the calling agent, and tools/call through the Gate in
// gateway mode. Transports (internal/mcp/httpgw) validate headers and framing before calling it.
package front

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
	"github.com/deedglass/deedglass/internal/mcp/present"
)

// Protocol versions (MCP). Modern requests carry their version in _meta; legacy clients open with
// initialize.
var (
	ModernVersions = []string{"2026-07-28"}
	LegacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}
)

// AllVersions is every version the front serves, newest first (the -32022 data.supported list).
func AllVersions() []string { return append(append([]string{}, ModernVersions...), LegacyVersions...) }

// DefaultLegacy is the version answered to an initialize that asks for one the front doesn't serve.
const DefaultLegacy = "2025-11-25"

// PageSize is tools/list's page size.
const PageSize = 100

// Cache hints (MCP 2026-07-28 caching).
const (
	DiscoverTTL = 3_600_000 // ms; server/discover is the same for everyone
	ListTTL     = 30_000    // ms; tools/list is filtered per caller, so private
)

// Methods.
const (
	MInitialize  = "initialize"
	MDiscover    = "server/discover"
	MPing        = "ping"
	MToolsList   = "tools/list"
	MToolsCall   = "tools/call"
	MInitialized = "notifications/initialized"
)

// Tool is one tool a source offers.
type Tool struct {
	Name   string          // the name the client sees: "echo" upstream, "fake.echo" native
	Gate   string          // the qualified name the Gate decides on: "everything.echo"
	Def    json.RawMessage // the definition as tools/list shows it (name already set to Name)
	Schema json.RawMessage // inputSchema, for Mcp-Param-* (x-mcp-header)
	Digest string          // sha256 of the definition (tool_digest), "" when unknown
	Hidden bool            // the active policy always denies it to this agent
}

// ToolSource is where a front's tools come from.
type ToolSource interface {
	// List returns every tool with Hidden set for pr, in a deterministic order.
	List(ctx context.Context, pr identity.Principal) ([]Tool, error)
	// Lookup finds one tool by its wire name. ok=false means the source doesn't offer it. When the
	// source can't list right now (an upstream is down) it may still return a Tool with an empty
	// Digest, so the call reaches the Gate and is recorded.
	Lookup(ctx context.Context, pr identity.Principal, name string) (t Tool, ok bool, err error)
	// Label names the source in messages ("everything", "native").
	Label() string
}

// Results is an optional tap on what a connector returned for an action, so a result larger
// than gate.max_result_bytes (stored only as a digest) still reaches the client. The upstream
// pool implements it.
type Results interface {
	// Take returns and forgets the raw MCP result (or JSON-RPC error object) for an action.
	Take(actionID string) (result json.RawMessage, rpcErr json.RawMessage, ok bool)
}

// Passthrough forwards a non-tool method to the upstream (mcp.passthrough). Recorded in the log,
// not in deeds.
type Passthrough interface {
	Forward(ctx context.Context, method string, params json.RawMessage) (result json.RawMessage, rpcErr *jsonrpc.ErrorObject, err error)
}

// Gate is the subset of *gate.Pipeline the front calls.
type Gate interface {
	Submit(ctx context.Context, pr identity.Principal, req gate.ActionRequest) (*gate.Action, error)
	Get(ctx context.Context, pr identity.Principal, id string, wait time.Duration) (*gate.Action, error)
	ByApproval(ctx context.Context, pr identity.Principal, approval string, wait time.Duration) (*gate.Action, error)
}

// Front answers MCP messages for one endpoint.
type Front struct {
	Version     string // serverInfo.version
	Tools       ToolSource
	Gate        Gate
	Results     Results         // optional
	Passthrough Passthrough     // optional
	PassMethods map[string]bool // method families forwarded: "resources", "prompts", "completion"
	// CallWait is how long a tools/call waits for the Gate's answer: gate.execute_timeout + 2 s.
	CallWait time.Duration
	// ExecuteTimeout is gate.execute_timeout, for the timeout sentence.
	ExecuteTimeout time.Duration
	Log            *slog.Logger
}

// Request is one message for the front.
type Request struct {
	Env       jsonrpc.Envelope
	Principal identity.Principal
	// Version is the protocol version in force: the _meta version (modern), else the header or
	// 2025-03-26 (legacy). The transport has already checked it.
	Version   string
	Modern    bool
	Transport string // gate.TransportMCPHTTP
	RequestID string
	// OnBehalfOf and Session are the request's Deedglass-On-Behalf-Of and Deedglass-Session.
	OnBehalfOf, Session string
	// CancelOn ends a running tool call when it ends (the transport's request context, watched for
	// Stop). Nil: the context Handle was given.
	CancelOn context.Context
}

func (r Request) cancelOn(ctx context.Context) context.Context {
	if r.CancelOn != nil {
		return r.CancelOn
	}
	return ctx
}

// Response is the front's answer. Env is nil for notifications (HTTP 202).
type Response struct {
	Env        jsonrpc.Envelope
	HTTPStatus int
}

// Handle answers one message. It never panics on input, and always returns either a JSON-RPC
// response for a request or (202, nil) for a notification.
func (f *Front) Handle(ctx context.Context, r Request) Response {
	switch r.Env.Kind() {
	case jsonrpc.Notification:
		return Response{HTTPStatus: 202}
	case jsonrpc.Request:
	default:
		return Response{Env: jsonrpc.NewError(nil, jsonrpc.CodeInvalidRequest, gate.MCPOneMessage), HTTPStatus: 400}
	}
	id := r.Env.ID()
	method := r.Env.Method()
	switch method {
	case MInitialize:
		return f.ok(id, f.initialize(r))
	case MDiscover:
		return f.ok(id, f.discover())
	case MPing:
		return f.ok(id, f.complete(r, map[string]json.RawMessage{}))
	case MToolsList:
		return f.list(ctx, r)
	case MToolsCall:
		return f.call(ctx, r)
	}
	if fam, _, _ := strings.Cut(method, "/"); f.Passthrough != nil && f.PassMethods[fam] && passable(method) {
		return f.forward(ctx, r, method)
	}
	st := 404
	if !r.Modern {
		st = 200 // legacy servers answer an unknown method in a 200 body
	}
	return Response{Env: jsonrpc.NewError(id, jsonrpc.CodeMethodNotFound, gate.MCPMethodNotFoundText(method)), HTTPStatus: st}
}

// passable are the methods mcp.passthrough may forward.
func passable(m string) bool {
	switch m {
	case "resources/list", "resources/templates/list", "resources/read", "prompts/list", "prompts/get", "completion/complete":
		return true
	}
	return false
}

func (f *Front) ok(id, result json.RawMessage) Response {
	return Response{Env: jsonrpc.NewResponse(id, result), HTTPStatus: 200}
}

func errResp(id json.RawMessage, code int, msg string, status int) Response {
	return Response{Env: jsonrpc.NewError(id, code, msg), HTTPStatus: status}
}

// complete adds resultType "complete" to a modern result.
func (f *Front) complete(r Request, m map[string]json.RawMessage) json.RawMessage {
	if r.Modern {
		m["resultType"] = jsonrpc.String("complete")
	}
	return jsonrpc.MarshalObject(m)
}

type serverInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

func (f *Front) info() json.RawMessage {
	return jsonrpc.Value(serverInfo{Name: "deedglass-gate", Title: "Deedglass Gate", Version: f.Version})
}

var toolsCap = json.RawMessage(`{"tools":{"listChanged":false}}`)

// initialize negotiates a legacy version: the requested one when served, else 2025-11-25. No
// session id is ever minted.
func (f *Front) initialize(r Request) json.RawMessage {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(r.Env["params"], &p)
	v := DefaultLegacy
	if slices.Contains(LegacyVersions, p.ProtocolVersion) {
		v = p.ProtocolVersion
	}
	return jsonrpc.MarshalObject(map[string]json.RawMessage{
		"protocolVersion": jsonrpc.String(v),
		"capabilities":    toolsCap,
		"serverInfo":      f.info(),
		"instructions":    jsonrpc.String(gate.MCPInstructions),
	})
}

func (f *Front) discover() json.RawMessage {
	return jsonrpc.MarshalObject(map[string]json.RawMessage{
		"resultType":        jsonrpc.String("complete"),
		"supportedVersions": jsonrpc.Value(ModernVersions),
		"capabilities":      toolsCap,
		"_meta":             jsonrpc.MarshalObject(map[string]json.RawMessage{"io.modelcontextprotocol/serverInfo": f.info()}),
		"instructions":      jsonrpc.String(gate.MCPInstructions),
		"ttlMs":             jsonrpc.Int(DiscoverTTL),
		"cacheScope":        jsonrpc.String("public"),
	})
}

// --- tools/list ---

// cursor is "o<offset>", base64url: opaque to clients, stable for a given list.
func encodeCursor(off int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("o" + strconv.Itoa(off)))
}

func decodeCursor(c string) (int, bool) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || len(b) < 2 || b[0] != 'o' {
		return 0, false
	}
	n, err := strconv.Atoi(string(b[1:]))
	return n, err == nil && n > 0 && n < 1<<20
}

func (f *Front) list(ctx context.Context, r Request) Response {
	id := r.Env.ID()
	var p struct {
		Cursor *string `json:"cursor"`
	}
	_ = json.Unmarshal(r.Env["params"], &p)
	off := 0
	if p.Cursor != nil && *p.Cursor != "" {
		n, ok := decodeCursor(*p.Cursor)
		if !ok {
			return errResp(id, jsonrpc.CodeInvalidParams, "The cursor isn't one this server gave out. Call tools/list without a cursor to start again.", 200)
		}
		off = n
	}
	tools, err := f.Tools.List(ctx, r.Principal)
	if err != nil {
		f.logf(ctx, "tools/list failed", "source", f.Tools.Label(), "err", err.Error())
		if errors.Is(err, ErrUpstreamDown) {
			return errResp(id, jsonrpc.CodeInternal, gate.MCPUpstreamDownText(f.Tools.Label()), 200)
		}
		return errResp(id, jsonrpc.CodeInternal, gate.MCPServerError, 200)
	}
	visible := make([]json.RawMessage, 0, len(tools)+1)
	for _, t := range tools {
		if !t.Hidden && !strings.HasPrefix(t.Name, "deedglass.") {
			visible = append(visible, t.Def)
		}
	}
	visible = append(visible, present.CheckApprovalTool())
	if off > len(visible) {
		off = len(visible)
	}
	end := min(off+PageSize, len(visible))
	m := map[string]json.RawMessage{"tools": jsonArray(visible[off:end])}
	if end < len(visible) {
		m["nextCursor"] = jsonrpc.String(encodeCursor(end))
	}
	if r.Modern {
		m["ttlMs"] = jsonrpc.Int(ListTTL)
		m["cacheScope"] = jsonrpc.String("private")
	}
	return f.ok(id, f.complete(r, m))
}

// ErrUpstreamDown is what a ToolSource returns when its upstream MCP server can't be reached.
var ErrUpstreamDown = errors.New("front: the upstream MCP server can't be reached")

func jsonArray(items []json.RawMessage) json.RawMessage {
	var b strings.Builder
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(it)
	}
	b.WriteByte(']')
	return json.RawMessage(b.String())
}

// --- tools/call ---

// CallParams are tools/call's params as the front reads them.
type CallParams struct {
	Name      string
	Arguments json.RawMessage
}

// ParseCall reads tools/call params: name (required) and arguments (an object; {} when absent).
func ParseCall(params json.RawMessage) (CallParams, bool) {
	var pm map[string]json.RawMessage
	var cp CallParams
	if json.Unmarshal(params, &pm) != nil || json.Unmarshal(pm["name"], &cp.Name) != nil || cp.Name == "" {
		return cp, false
	}
	cp.Arguments = pm["arguments"]
	if len(cp.Arguments) == 0 || string(cp.Arguments) == "null" {
		cp.Arguments = json.RawMessage(`{}`)
	}
	return cp, true
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (r Request) client() *gate.ClientInfo {
	var ci clientInfo
	if raw, ok := r.Env.Meta()[jsonrpc.MetaClientInfo]; ok {
		_ = json.Unmarshal(raw, &ci)
	}
	if ci.Name == "" && ci.Version == "" {
		return nil
	}
	return &gate.ClientInfo{Name: clipTo(ci.Name, 64), Version: clipTo(ci.Version, 64)}
}

func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

func (f *Front) call(ctx context.Context, r Request) Response {
	id := r.Env.ID()
	cp, ok := ParseCall(r.Env["params"])
	if !ok {
		return errResp(id, jsonrpc.CodeInvalidParams, "tools/call needs params.name, the tool to call.", 200)
	}
	if cp.Name == present.CheckApprovalName {
		return f.checkApproval(ctx, r, cp)
	}
	if strings.HasPrefix(cp.Name, "deedglass.") {
		return errResp(id, jsonrpc.CodeInvalidParams, gate.MCPUnknownToolText(cp.Name), 200)
	}
	t, found, err := f.Tools.Lookup(ctx, r.Principal, cp.Name)
	if err != nil {
		f.logf(ctx, "tool lookup failed", "source", f.Tools.Label(), "err", err.Error())
		return f.result(id, present.Plain(gate.MCPServerError, r.Modern))
	}
	if !found {
		return errResp(id, jsonrpc.CodeInvalidParams, gate.MCPUnknownToolText(cp.Name), 200)
	}
	req := gate.ActionRequest{Tool: t.Gate, Args: cp.Arguments, Mode: gate.ModeGateway, ToolDigest: t.Digest,
		Context: &gate.RequestContext{Client: r.client(), Transport: r.Transport}, Wait: gate.MaxWait, WaitSet: true,
		OnBehalfOf: r.OnBehalfOf, Session: r.Session, RequestID: r.RequestID, CancelOn: r.cancelOn(ctx)}
	start := time.Now()
	a, err := f.Gate.Submit(ctx, r.Principal, req)
	if a == nil && err != nil {
		return f.rejected(ctx, r, err)
	}
	// Still processing after the Gate's longest wait: keep waiting, up to CallWait in total.
	for a.Status == ledger.StatusProcessing && ctx.Err() == nil {
		left := f.callWait() - time.Since(start)
		if left <= 0 {
			break
		}
		next, gerr := f.Gate.Get(ctx, r.Principal, a.ID, min(left, gate.MaxWait))
		if gerr != nil {
			break
		}
		a = next
	}
	return f.outcome(ctx, r, t, a, err, false)
}

func (f *Front) callWait() time.Duration {
	if f.CallWait > 0 {
		return f.CallWait
	}
	return 32 * time.Second
}

// rejected answers a refusal the Gate didn't record (validation, intents, server errors).
func (f *Front) rejected(ctx context.Context, r Request, err error) Response {
	id := r.Env.ID()
	e := httpx.AsError(err)
	if e != nil && e.Code == httpx.CodeConnectorUnavailable {
		return f.result(id, present.Plain(gate.MCPUpstreamDownText(f.Tools.Label()), r.Modern))
	}
	if e == nil || e.Status >= 500 {
		f.logf(ctx, "tools/call failed", "err", err.Error())
		return f.result(id, present.Plain(gate.MCPServerError, r.Modern))
	}
	return f.result(id, present.Plain(gate.MCPRejectedText(e.Message), r.Modern))
}

func (f *Front) result(id, res json.RawMessage) Response { return f.ok(id, res) }

// outcome turns a recorded action into the client's answer. approved marks a run that followed a
// person's approval (deedglass.check_approval).
func (f *Front) outcome(ctx context.Context, r Request, t Tool, a *gate.Action, err error, approved bool) Response {
	id := r.Env.ID()
	v := &a.Action
	switch {
	case a.Refusal() != nil || err != nil:
		msg := ""
		if e := httpx.AsError(err); e != nil {
			msg = e.Message
		}
		return f.result(id, present.Deny(v, msg, r.Modern))
	case v.Status == ledger.StatusPendingApproval:
		return f.result(id, present.Pending(v, r.Modern))
	case v.Status == ledger.StatusProcessing && v.State == string(gate.Witnessing):
		// S21: the tool ran; the verdict still waits for Postgres's change log.
		return f.result(id, present.WithAction(gate.MCPWitnessPendingText, v.ID, r.Modern))
	case v.Status == ledger.StatusProcessing:
		return f.result(id, present.WithAction(gate.MCPStillRunningText(v.ID), v.ID, r.Modern))
	case v.Status == ledger.StatusDenied:
		return f.result(id, present.Deny(v, "", r.Modern))
	case v.Status == ledger.StatusExpired:
		return f.result(id, present.Text(gate.MCPApprovalExpiredText(approvalID(v)), true, r.Modern, present.ActionMeta(v), nil))
	case v.Status == ledger.StatusCancelled:
		return f.result(id, present.Text(gate.MCPApprovalCancelledText(approvalID(v)), true, r.Modern, present.ActionMeta(v), nil))
	}
	raw, rpcErr, tapped := f.take(v.ID)
	code := ""
	if v.Execution != nil && v.Execution.ErrorCode != nil {
		code = *v.Execution.ErrorCode
	}
	status := ""
	if v.Execution != nil && v.Execution.Status != nil {
		status = *v.Execution.Status
	}
	switch {
	case status == string(gate.ExecFailed) && code == string(httpx.CodeConnectorUnavailable):
		return f.result(id, present.Text(gate.MCPUpstreamDownText(f.Tools.Label()), true, r.Modern, present.ActionMeta(v), nil))
	case len(rpcErr) > 0:
		// The upstream answered with a JSON-RPC error: relay it, as the stdio wrap does.
		var eo jsonrpc.ErrorObject
		if json.Unmarshal(rpcErr, &eo) == nil && eo.Code != 0 {
			return Response{Env: jsonrpc.Envelope{"jsonrpc": json.RawMessage(`"2.0"`), "id": idOrNull(id), "error": rpcErr}, HTTPStatus: 200}
		}
	case status == string(gate.ExecUnknown):
		msg := gate.UnknownOutcomeMessage(a, f.ExecuteTimeout)
		if code == "timeout" {
			msg = gate.MCPTimeoutText(v.Tool, f.ExecuteTimeout, v.ID)
		}
		return f.result(id, present.Text(msg, true, r.Modern, present.ActionMeta(v), nil))
	}
	if !tapped && v.Execution != nil {
		raw = v.Execution.Result
	}
	if len(raw) == 0 {
		if status == string(gate.ExecFailed) {
			return f.result(id, present.Text(gate.MCPToolFailedText(v.Tool, v.ID), true, r.Modern, present.ActionMeta(v), nil))
		}
		raw = json.RawMessage(`{}`)
	}
	return f.result(id, f.toolResult(r, t, v, raw, approved))
}

func idOrNull(id json.RawMessage) json.RawMessage {
	if id == nil {
		return jsonrpc.Null
	}
	return id
}

func approvalID(v *ledger.Action) string {
	if v.Approval != nil {
		return v.Approval.ID
	}
	return "unknown"
}

func (f *Front) take(actionID string) (json.RawMessage, json.RawMessage, bool) {
	if f.Results == nil {
		return nil, nil, false
	}
	return f.Results.Take(actionID)
}

// toolResult is the client's tools/call result: an MCP CallToolResult as the upstream returned
// it, or (native connectors) the connector's JSON as text plus structuredContent, with _meta
// naming the action and its deed.
func (f *Front) toolResult(r Request, t Tool, v *ledger.Action, raw json.RawMessage, approved bool) json.RawMessage {
	var res map[string]json.RawMessage
	isCTR := json.Unmarshal(raw, &res) == nil && res != nil && isCallToolResult(res)
	if !isCTR {
		// A native connector's output: one text block with the JSON, and the object as
		// structuredContent when it is one.
		res = map[string]json.RawMessage{
			"content": jsonArray([]json.RawMessage{jsonrpc.Value(map[string]string{"type": "text", "text": string(raw)})}),
			"isError": json.RawMessage("false"),
		}
		if len(raw) > 0 && raw[0] == '{' {
			res["structuredContent"] = raw
		}
	}
	if v.Execution != nil && v.Execution.Status != nil && *v.Execution.Status == string(gate.ExecFailed) {
		res["isError"] = json.RawMessage("true")
	}
	if approved {
		res["content"] = prependText(res["content"], gate.MCPApprovedRanText(v.Tool))
	}
	meta := map[string]json.RawMessage{}
	if m, ok := res["_meta"]; ok {
		_ = json.Unmarshal(m, &meta)
		if meta == nil {
			meta = map[string]json.RawMessage{}
		}
	}
	for k, val := range present.ActionMeta(v) {
		meta[k] = jsonrpc.String(val)
	}
	if wm := present.WitnessMeta(v); wm != nil {
		meta[present.MetaWitness] = wm
		// The tool ran, so isError stays as it is; a verdict other than VERIFIED gets one line (S19).
		if t := gate.MCPWitnessText(v); t != "" {
			res["content"] = appendText(res["content"], t)
		}
	}
	res["_meta"] = jsonrpc.MarshalObject(meta)
	if r.Modern {
		if _, ok := res["resultType"]; !ok {
			res["resultType"] = jsonrpc.String("complete")
		}
	} else {
		delete(res, "resultType")
	}
	return jsonrpc.MarshalObject(res)
}

func isCallToolResult(m map[string]json.RawMessage) bool {
	c, ok := m["content"]
	return ok && len(c) > 0 && c[0] == '['
}

func appendText(content json.RawMessage, text string) json.RawMessage {
	var items []json.RawMessage
	_ = json.Unmarshal(content, &items)
	return jsonArray(append(items, jsonrpc.Value(map[string]string{"type": "text", "text": text})))
}

func prependText(content json.RawMessage, text string) json.RawMessage {
	var items []json.RawMessage
	_ = json.Unmarshal(content, &items)
	head := jsonrpc.Value(map[string]string{"type": "text", "text": text})
	return jsonArray(append([]json.RawMessage{head}, items...))
}

// --- deedglass.check_approval ---

func (f *Front) checkApproval(ctx context.Context, r Request, cp CallParams) Response {
	id := r.Env.ID()
	var args struct {
		ApprovalID  string `json:"approval_id"`
		WaitSeconds *int   `json:"wait_seconds"`
	}
	if json.Unmarshal(cp.Arguments, &args) != nil || !reApproval(args.ApprovalID) {
		return f.result(id, present.Plain(gate.MCPApprovalIDBad, r.Modern))
	}
	a, err := f.Gate.ByApproval(ctx, r.Principal, args.ApprovalID, CheckWait(args.WaitSeconds))
	if err != nil {
		if e := httpx.AsError(err); e != nil && e.Status == 404 {
			return f.result(id, present.Plain(gate.MCPApprovalUnknownText(args.ApprovalID), r.Modern))
		}
		f.logf(ctx, "check_approval failed", "err", err.Error())
		return f.result(id, present.Plain(gate.MCPServerError, r.Modern))
	}
	// Approved and resumed: the Gate is running it. Wait a little for the outcome.
	if a.Status == ledger.StatusProcessing || a.Status == ledger.StatusAuthorized {
		if next, err := f.Gate.Get(ctx, r.Principal, a.ID, min(f.callWait(), gate.MaxWait)); err == nil {
			a = next
		}
	}
	v := &a.Action
	switch a.Status {
	case ledger.StatusPendingApproval:
		return f.result(id, present.StillPending(v, r.Modern))
	case ledger.StatusDenied:
		if v.Approval != nil && v.Approval.Status == "denied" {
			return f.result(id, present.Text(gate.MCPApprovalDeniedText(v), true, r.Modern, present.ActionMeta(v), nil))
		}
		if v.Approval != nil && v.Approval.Status == "approved" {
			return f.result(id, present.Text(gate.MCPApprovedStoppedText(v.Agent.Name), true, r.Modern, present.ActionMeta(v), nil))
		}
	}
	t := Tool{Name: v.Tool, Gate: v.Tool}
	return f.outcome(ctx, r, t, a, nil, a.Status == ledger.StatusExecuted || a.Status == ledger.StatusFailed)
}

// CheckWait is deedglass.check_approval's wait_seconds: 0 to 20, default 10 (S22 §3.8), so a model
// checks once instead of looping.
func CheckWait(s *int) time.Duration {
	if s == nil {
		return 10 * time.Second
	}
	return time.Duration(min(max(*s, 0), 20)) * time.Second
}

var reApprovalID = regexp.MustCompile(`^apr_[0-7][0-9a-hjkmnp-tv-z]{25}$`)

func reApproval(s string) bool { return reApprovalID.MatchString(s) }

// --- passthrough ---

func (f *Front) forward(ctx context.Context, r Request, method string) Response {
	id := r.Env.ID()
	params := r.Env["params"]
	res, rpcErr, err := f.Passthrough.Forward(ctx, method, params)
	switch {
	case errors.Is(err, ErrUpstreamDown):
		return errResp(id, jsonrpc.CodeInternal, gate.MCPUpstreamDownText(f.Tools.Label()), 200)
	case err != nil:
		f.logf(ctx, "passthrough failed", "method", method, "err", err.Error())
		return errResp(id, jsonrpc.CodeInternal, gate.MCPServerError, 200)
	case rpcErr != nil:
		return Response{Env: jsonrpc.NewError(id, rpcErr.Code, rpcErr.Message), HTTPStatus: 200}
	}
	f.logf(ctx, "passthrough", "method", method, "source", f.Tools.Label())
	return f.ok(id, res)
}

func (f *Front) logf(ctx context.Context, msg string, args ...any) {
	if f.Log != nil {
		f.Log.InfoContext(ctx, "mcp: "+msg, args...)
	}
}

// SortTools orders tools by wire name, the deterministic order tools/list promises.
func SortTools(ts []Tool) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
}

// SideEffectOf maps a connector's declared side effect to the policy fact.
func SideEffectOf(s connector.SideEffect) string {
	if s == "" {
		return string(connector.SideEffectUnknown)
	}
	return string(s)
}
