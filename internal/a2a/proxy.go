package a2a

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mcp/gateclient"
)

// JSON-RPC methods (A2A §5.3).
const (
	MethodSendMessage          = "SendMessage"
	MethodSendStreamingMessage = "SendStreamingMessage"
	MethodGetTask              = "GetTask"
	MethodListTasks            = "ListTasks"
	MethodCancelTask           = "CancelTask"
	MethodSubscribeToTask      = "SubscribeToTask"
	MethodCreatePushConfig     = "CreateTaskPushNotificationConfig"
	MethodGetPushConfig        = "GetTaskPushNotificationConfig"
	MethodListPushConfigs      = "ListTaskPushNotificationConfigs"
	MethodDeletePushConfig     = "DeleteTaskPushNotificationConfig"
	MethodGetExtendedCard      = "GetExtendedAgentCard"
)

// Transport is the action context's transport for calls through the proxy.
const Transport = "a2a_jsonrpc"

// ConnectorType is the connector type policies match for these calls.
const ConnectorType = "a2a"

// acting are the methods that ask the peer to do something: each is an action.
var acting = map[string]bool{MethodSendMessage: true, MethodSendStreamingMessage: true, MethodCancelTask: true,
	MethodCreatePushConfig: true, MethodDeletePushConfig: true}

// reading pass through unrecorded.
var reading = map[string]bool{MethodGetTask: true, MethodListTasks: true, MethodSubscribeToTask: true,
	MethodGetPushConfig: true, MethodListPushConfigs: true, MethodGetExtendedCard: true}

// Deedglass's own JSON-RPC error codes: application-defined, outside the range JSON-RPC 2.0 and
// A2A reserve (-32768 to -32000).
const (
	CodeDenied      = -31001 // the Gate refused and recorded it
	CodePending     = -31002 // waiting for a person's approval
	CodeUnavailable = -31003 // no decision: Deedglass unreachable, a bad key, or a refusal it didn't record
)

// Limits.
const (
	MaxRequestBytes = 4 << 20
	MaxAnswerBytes  = 8 << 20
	MaxResultBytes  = 256 << 10 // a larger answer is reported by its digest alone
)

// Gate is the part of gateclient.Client the proxy uses.
type Gate interface {
	Submit(ctx context.Context, r gateclient.SubmitRequest) gateclient.Decision
	Report(ctx context.Context, actionID string, r gate.EdgeResult) error
	GetActionWait(ctx context.Context, id string, wait time.Duration) (*ledger.Action, error)
	Whoami(ctx context.Context) (*gateclient.Whoami, error)
	Server() string
}

// Config configures a Proxy.
type Config struct {
	Connector    string        // tools are <connector>.<Method>
	Peer         *url.URL      // the peer's JSON-RPC endpoint
	CardURL      *url.URL      // the peer's Agent Card (default: the peer's origin + /.well-known/agent-card.json)
	PublicURL    string        // where callers reach this proxy; the card's JSONRPC url is rewritten to it
	PeerHeaders  http.Header   // set on every request to the peer (the operator's credentials for it)
	OnBehalfOf   string        // the person, when the caller's chain names none
	ResultMode   string        // full (default) or digest
	CallTimeout  time.Duration // per call to the peer; default 5 min
	ApprovalWait time.Duration // how long one call waits for a person's decision before answering CodePending
	HTTP         *http.Client  // to the peer: an egress-door client
	Version      string        // the User-Agent's version
	Now          func() time.Time
}

// Proxy is the HTTP handler.
type Proxy struct {
	cfg  Config
	gate Gate
	log  *slog.Logger

	mu      sync.Mutex
	self    *Hop
	card    json.RawMessage // the peer's card as fetched
	name    string          // the peer's card name
	pending map[string]*pendingCall

	reports sync.WaitGroup
}

type pendingCall struct {
	actionID string
	approval string
}

// New returns a Proxy.
func New(cfg Config, g Gate, log *slog.Logger) (*Proxy, error) {
	if cfg.Peer == nil || (cfg.Peer.Scheme != "https" && cfg.Peer.Scheme != "http") || cfg.Peer.Host == "" {
		return nil, errors.New("the peer must be an http(s) URL")
	}
	if cfg.HTTP == nil {
		return nil, errors.New("a2a: no HTTP client for the peer")
	}
	if cfg.CardURL == nil {
		cfg.CardURL = &url.URL{Scheme: cfg.Peer.Scheme, Host: cfg.Peer.Host, Path: "/.well-known/agent-card.json"}
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = 5 * time.Minute
	}
	if cfg.ResultMode == "" {
		cfg.ResultMode = "full"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Proxy{cfg: cfg, gate: g, log: log, pending: map[string]*pendingCall{}}, nil
}

// Wait returns once every result report has been sent (or given up).
func (p *Proxy) Wait() { p.reports.Wait() }

// LoadCard fetches the peer's Agent Card once. A peer without one still works: the chain then
// names it by URL.
func (p *Proxy) LoadCard(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.CardURL.String(), nil)
	if err != nil {
		return err
	}
	p.peerHeaders(req.Header)
	res, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK || len(body) > 1<<20 {
		return fmt.Errorf("the Agent Card answered HTTP %d", res.StatusCode)
	}
	var c struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return errors.New("the Agent Card isn't JSON")
	}
	p.mu.Lock()
	p.card, p.name = body, clip(strings.TrimSpace(c.Name), 64)
	p.mu.Unlock()
	return nil
}

func (p *Proxy) peerHeaders(h http.Header) {
	for k, vs := range p.cfg.PeerHeaders {
		h[k] = append([]string(nil), vs...)
	}
	h.Set("User-Agent", "deedglass/"+p.cfg.Version)
}

// ServeHTTP answers the card at /.well-known/agent-card.json and JSON-RPC at /.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/.well-known/agent-card.json" && r.Method == http.MethodGet:
		p.serveCard(w, r)
	case r.URL.Path == "/" && r.Method == http.MethodPost:
		p.serveRPC(w, r)
	case r.URL.Path == "/":
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST JSON-RPC here.", http.StatusMethodNotAllowed)
	default:
		http.NotFound(w, r)
	}
}

// serveCard answers the peer's card with its JSONRPC interface pointing here. Other interfaces are
// dropped (only JSON-RPC goes through the Gate), and so are the card's signatures, which no longer
// match the rewritten card.
func (p *Proxy) serveCard(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	card := p.card
	p.mu.Unlock()
	if card == nil {
		if err := p.LoadCard(r.Context()); err != nil {
			p.log.Warn("a2a: the peer's Agent Card can't be read", "err", err)
			http.Error(w, "The peer's Agent Card can't be read. Check --card-url.", http.StatusBadGateway)
			return
		}
		p.mu.Lock()
		card = p.card
		p.mu.Unlock()
	}
	out, err := rewriteCard(card, p.cfg.PublicURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func rewriteCard(card json.RawMessage, public string) ([]byte, error) {
	var m map[string]json.RawMessage
	if json.Unmarshal(card, &m) != nil {
		return nil, errors.New("The peer's Agent Card isn't a JSON object.")
	}
	var ifs []map[string]json.RawMessage
	_ = json.Unmarshal(m["supportedInterfaces"], &ifs)
	var keep []map[string]json.RawMessage
	for _, i := range ifs {
		var binding, version string
		_ = json.Unmarshal(i["protocolBinding"], &binding)
		_ = json.Unmarshal(i["protocolVersion"], &version)
		if binding == "JSONRPC" && version == SpecVersion {
			u, _ := json.Marshal(public)
			i["url"] = u
			keep = append(keep, i)
			break
		}
	}
	if len(keep) == 0 {
		return nil, errors.New("The peer's Agent Card has no JSONRPC interface for A2A 1.0.")
	}
	m["supportedInterfaces"], _ = json.Marshal(keep)
	delete(m, "signatures")
	return json.Marshal(m)
}

// --- JSON-RPC ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type errorInfo struct {
	Type     string            `json:"@type"`
	Reason   string            `json:"reason"`
	Domain   string            `json:"domain"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string, info *errorInfo) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	e := map[string]any{"code": code, "message": msg}
	if info != nil {
		info.Type, info.Domain = "type.googleapis.com/google.rpc.ErrorInfo", "deedglass.com"
		e["data"] = []any{info}
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": e})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func (p *Proxy) serveRPC(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
	if err != nil || len(body) > MaxRequestBytes {
		writeRPCError(w, nil, -32600, "The request is larger than 4 MiB or couldn't be read.", nil)
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		writeRPCError(w, nil, -32600, "Batches aren't forwarded by Deedglass. Send one request at a time.", nil)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeRPCError(w, nil, -32700, "Invalid JSON payload", nil)
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" || len(req.ID) == 0 {
		writeRPCError(w, req.ID, -32600, "Request payload validation error: jsonrpc 2.0, an id and a method are needed.", nil)
		return
	}
	v := strings.TrimSpace(r.Header.Get("A2A-Version"))
	if v == "" {
		v = r.URL.Query().Get("A2A-Version")
	}
	if v != SpecVersion {
		if v == "" {
			v = "0.3 (no A2A-Version header)"
		}
		writeRPCError(w, req.ID, -32009, fmt.Sprintf("Deedglass forwards A2A %s only; this request is %s. Send A2A-Version: %s.", SpecVersion, clip(v, 40), SpecVersion), nil)
		return
	}
	switch {
	case acting[req.Method]:
		p.act(w, r, req)
	case reading[req.Method]:
		p.forward(w, r, req, trimmed, nil)
	default:
		writeRPCError(w, req.ID, -32601, fmt.Sprintf("Method not found: Deedglass doesn't forward %q.", clip(req.Method, 64)), nil)
	}
}

// selfHop is this agent, from whoami once.
func (p *Proxy) selfHop(ctx context.Context) (Hop, error) {
	p.mu.Lock()
	if p.self != nil {
		h := *p.self
		p.mu.Unlock()
		return h, nil
	}
	p.mu.Unlock()
	who, err := p.gate.Whoami(ctx)
	if err != nil {
		return Hop{}, err
	}
	if who.Agent == nil {
		return Hop{}, errors.New("the key isn't an agent's key")
	}
	h := Hop{Kind: HopAgent, Ref: who.Agent.ID, Name: clip(who.Agent.Name, 64)}
	p.mu.Lock()
	p.self = &h
	p.mu.Unlock()
	return h, nil
}

func (p *Proxy) peerHop() Hop {
	p.mu.Lock()
	name := p.name
	p.mu.Unlock()
	ref := p.cfg.Peer.String()
	if !reRef.MatchString(ref) {
		ref = "a2a:" + p.cfg.Connector
	}
	if name == "" {
		name = p.cfg.Connector
	}
	return Hop{Kind: HopAgent, Ref: ref, Name: name}
}

// act gates one acting call, forwards it when authorized, and reports what the peer said.
func (p *Proxy) act(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	ctx := r.Context()
	params := req.Params
	if len(params) == 0 || string(params) == "null" {
		params = json.RawMessage("{}")
	}
	var pm map[string]json.RawMessage
	if json.Unmarshal(params, &pm) != nil {
		writeRPCError(w, req.ID, -32602, "Invalid parameters: params must be an object.", nil)
		return
	}
	key := req.Method + "\x00" + digestHex(params)

	// The chain: the caller's, or the configured person; then this agent; then the peer.
	var incoming []Hop
	var msg map[string]json.RawMessage
	send := req.Method == MethodSendMessage || req.Method == MethodSendStreamingMessage
	if send {
		if json.Unmarshal(pm["message"], &msg) != nil || msg == nil {
			writeRPCError(w, req.ID, -32602, "Invalid parameters: params.message must be an object.", nil)
			return
		}
		c, err := ChainFromMetadata(msg["metadata"])
		if err != nil {
			writeRPCError(w, req.ID, -32602, "Invalid parameters: the Deedglass delegation chain in message.metadata: "+err.Error()+".", nil)
			return
		}
		incoming = c
	}
	person := Person(incoming)
	switch {
	case person != "" && p.cfg.OnBehalfOf != "" && person != p.cfg.OnBehalfOf:
		writeRPCError(w, req.ID, -32602, fmt.Sprintf("Invalid parameters: the delegation chain names %s, but this proxy acts on behalf of %s. Send one person.", person, p.cfg.OnBehalfOf), nil)
		return
	case person == "" && p.cfg.OnBehalfOf != "":
		person = p.cfg.OnBehalfOf
		incoming = append([]Hop{{Kind: HopPerson, Ref: person}}, incoming...)
	}
	self, err := p.selfHop(ctx)
	if err != nil {
		p.log.Warn("a2a: whoami failed", "err", err)
		writeRPCError(w, req.ID, CodeUnavailable, gate.UnreachableMessage(p.gate.Server()), &errorInfo{Reason: "DEEDGLASS_UNAVAILABLE"})
		return
	}
	sent := incoming
	if len(sent) == 0 || sent[len(sent)-1].Ref != self.Ref {
		sent = append(append([]Hop(nil), incoming...), self)
	}
	chain := append(append([]Hop(nil), sent...), p.peerHop())
	if err := CheckChain(chain); err != nil {
		writeRPCError(w, req.ID, -32602, "Invalid parameters: the delegation chain: "+err.Error()+".", nil)
		return
	}
	if send {
		if err := setChain(msg, sent); err != nil {
			writeRPCError(w, req.ID, -32602, "Invalid parameters: "+err.Error()+".", nil)
			return
		}
		pm["message"], _ = json.Marshal(msg)
		params, _ = json.Marshal(pm)
	}
	args, _ := json.Marshal(map[string]any{
		"peer":       Peer{URL: p.cfg.Peer.String(), Name: p.peerHop().Name},
		"delegation": chain,
		"request":    params,
	})
	fwd, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: req.ID, Method: req.Method, Params: params})

	// A retry of a call that waited for a person: the same action, run once.
	p.mu.Lock()
	pc := p.pending[key]
	p.mu.Unlock()
	var actionID string
	if pc != nil {
		a, err := p.gate.GetActionWait(ctx, pc.actionID, p.cfg.ApprovalWait)
		if err != nil {
			writeRPCError(w, req.ID, CodeUnavailable, gate.UnreachableMessage(p.gate.Server()), &errorInfo{Reason: "DEEDGLASS_UNAVAILABLE"})
			return
		}
		if !p.settle(w, req.ID, key, a) {
			return
		}
		actionID = a.ID
	} else {
		d := p.gate.Submit(ctx, gateclient.SubmitRequest{Tool: p.cfg.Connector + "." + req.Method, Args: args,
			ConnectorType: ConnectorType, Transport: Transport, ClientName: "a2a", OnBehalfOf: person,
			IdempotencyKey: "a2a_" + strings.TrimPrefix(gateclient.NewIdempotencyKey(), "mcpw_")})
		switch d.Kind {
		case gateclient.Authorized:
			actionID = d.Action.ID
		case gateclient.Denied:
			p.log.Info("a2a: denied", "method", req.Method, "action", d.Action.ID)
			writeRPCError(w, req.ID, CodeDenied, d.Message, deniedInfo(d.Action))
			return
		case gateclient.Pending:
			a := d.Action
			if p.cfg.ApprovalWait > 0 {
				if b, err := p.gate.GetActionWait(ctx, a.ID, p.cfg.ApprovalWait); err == nil {
					a = b
				}
			}
			p.mu.Lock()
			p.pending[key] = &pendingCall{actionID: a.ID, approval: d.Action.Approval.ID}
			p.mu.Unlock()
			if !p.settle(w, req.ID, key, a) {
				return
			}
			actionID = a.ID
		case gateclient.BadKey:
			writeRPCError(w, req.ID, CodeUnavailable, "Deedglass refused the API key: "+d.Message+" Set DEEDGLASS_API_KEY to an agent's key.", &errorInfo{Reason: "DEEDGLASS_BAD_KEY"})
			return
		case gateclient.Rejected:
			writeRPCError(w, req.ID, CodeUnavailable, d.Message, &errorInfo{Reason: "DEEDGLASS_REJECTED", Metadata: map[string]string{"code": d.Code}})
			return
		default:
			p.log.Warn("a2a: the Gate is unreachable; nothing was forwarded", "method", req.Method, "err", d.Err)
			writeRPCError(w, req.ID, CodeUnavailable, gate.UnreachableMessage(p.gate.Server()), &errorInfo{Reason: "DEEDGLASS_UNAVAILABLE"})
			return
		}
	}
	p.log.Info("a2a: authorized; forwarding", "method", req.Method, "action", actionID, "chain", FormatChain(chain))
	p.forward(w, r, req, fwd, &call{actionID: actionID, method: req.Method, started: p.cfg.Now()})
}

// settle answers a call whose action isn't authorized (and returns false), or clears the pending
// entry of one that is (and returns true: run it now, once).
func (p *Proxy) settle(w http.ResponseWriter, id json.RawMessage, key string, a *ledger.Action) bool {
	switch a.Status {
	case ledger.StatusAuthorized:
		p.mu.Lock()
		delete(p.pending, key)
		p.mu.Unlock()
		return true
	case ledger.StatusPendingApproval, ledger.StatusProcessing:
		msg := "Waiting for a person's approval. Send the same request again to run it once approved."
		if a.Decision != nil && a.Decision.Message != nil {
			msg = *a.Decision.Message + " Send the same request again to run it once approved."
		}
		info := &errorInfo{Reason: "DEEDGLASS_PENDING_APPROVAL", Metadata: map[string]string{"action": a.ID}}
		if a.Approval != nil {
			info.Metadata["approval"] = a.Approval.ID
		}
		writeRPCError(w, id, CodePending, msg, info)
		return false
	default:
		p.mu.Lock()
		delete(p.pending, key)
		p.mu.Unlock()
		msg := fmt.Sprintf("Not run: action %s is %s.", a.ID, a.Status)
		if a.Approval != nil && a.Approval.Status == "denied" {
			msg = "Not run: a person denied the approval."
			if a.Approval.DecisionReason != nil {
				msg += " Their reason: " + clip(*a.Approval.DecisionReason, 200)
			}
		}
		writeRPCError(w, id, CodeDenied, msg, deniedInfo(a))
		return false
	}
}

func deniedInfo(a *ledger.Action) *errorInfo {
	info := &errorInfo{Reason: "DEEDGLASS_DENIED", Metadata: map[string]string{}}
	if a == nil {
		return info
	}
	info.Metadata["action"] = a.ID
	if a.Decision != nil && a.Decision.RuleID != nil {
		info.Metadata["rule"] = *a.Decision.RuleID
	}
	if a.Deed != nil {
		info.Metadata["deed"] = *a.Deed
	}
	return info
}

// setChain writes the chain into message.metadata under ExtensionURI and lists the URI in
// message.extensions.
func setChain(msg map[string]json.RawMessage, c []Hop) error {
	meta := map[string]json.RawMessage{}
	if raw, ok := msg["metadata"]; ok && string(raw) != "null" {
		if json.Unmarshal(raw, &meta) != nil {
			return errors.New("message.metadata must be an object")
		}
	}
	meta[ExtensionURI], _ = json.Marshal(Delegation{Chain: c})
	msg["metadata"], _ = json.Marshal(meta)
	var ext []string
	if raw, ok := msg["extensions"]; ok && string(raw) != "null" {
		if json.Unmarshal(raw, &ext) != nil {
			return errors.New("message.extensions must be a list of strings")
		}
	}
	for _, e := range ext {
		if e == ExtensionURI {
			return nil
		}
	}
	msg["extensions"], _ = json.Marshal(append(ext, ExtensionURI))
	return nil
}

// --- forwarding ---

type call struct {
	actionID string
	method   string
	started  time.Time
}

// forward sends body to the peer and copies the answer back unchanged. With c set, what the peer
// said is reported to the Gate.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, req rpcRequest, body []byte, c *call) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), p.cfg.CallTimeout)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel) // the caller left: stop waiting, keep the report
	defer stop()
	up, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Peer.String(), bytes.NewReader(body))
	if err != nil {
		writeRPCError(w, req.ID, -32603, "Internal error", nil)
		return
	}
	up.Header.Set("Content-Type", "application/json")
	up.Header.Set("A2A-Version", SpecVersion)
	if a := r.Header.Get("Accept"); a != "" {
		up.Header.Set("Accept", a)
	}
	ext := r.Header.Get("A2A-Extensions")
	if c != nil && (req.Method == MethodSendMessage || req.Method == MethodSendStreamingMessage) && !strings.Contains(ext, ExtensionURI) {
		ext = strings.TrimPrefix(ext+","+ExtensionURI, ",")
	}
	if ext != "" {
		up.Header.Set("A2A-Extensions", ext)
	}
	identity.StripInboundCredentials(up.Header) // nothing the caller sent as a credential
	p.peerHeaders(up.Header)
	res, err := p.cfg.HTTP.Do(up)
	if err != nil {
		status, code := gate.ExecUnknown, "peer_no_answer"
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			status, code = gate.ExecFailed, "peer_unreachable" // never connected: nothing was sent
		} else if errors.Is(err, context.DeadlineExceeded) {
			code = "timeout"
		}
		p.report(c, Outcome{Status: status, Code: code})
		writeRPCError(w, req.ID, -32603, "The peer agent didn't answer ("+code+").", nil)
		return
	}
	defer res.Body.Close()
	ct := res.Header.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(ct), "text/event-stream") {
		p.relayStream(w, res, c)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxAnswerBytes+1))
	if err != nil || len(raw) > MaxAnswerBytes {
		p.report(c, Outcome{Status: gate.ExecUnknown, Code: "peer_answer_too_large"})
		writeRPCError(w, req.ID, -32603, "The peer's answer was larger than 8 MiB or broke off.", nil)
		return
	}
	if c != nil {
		o := Classify(c.method, raw)
		if res.StatusCode >= 300 && o.Code == "peer_answer_unreadable" {
			o.Code = "peer_http_error"
		}
		p.report(c, o)
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(raw)
}

// relayStream copies an SSE answer line by line, flushing each event, and reports the last event's
// outcome when the stream ends.
func (p *Proxy) relayStream(w http.ResponseWriter, res *http.Response, c *call) {
	w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(res.StatusCode)
	fl, _ := w.(http.Flusher)
	br := bufio.NewReaderSize(res.Body, 64<<10)
	var data []string
	var last []byte
	total := 0
	for {
		line, err := br.ReadString('\n')
		total += len(line)
		if total > MaxAnswerBytes {
			p.report(c, Outcome{Status: gate.ExecUnknown, Code: "peer_answer_too_large"})
			return
		}
		if line != "" {
			_, _ = io.WriteString(w, line)
		}
		t := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(t, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(t, "data:"), " "))
		case t == "" && len(data) > 0:
			last = []byte(strings.Join(data, "\n"))
			data = nil
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	if len(data) > 0 {
		last = []byte(strings.Join(data, "\n"))
	}
	if fl != nil {
		fl.Flush()
	}
	if c == nil {
		return
	}
	if last == nil {
		p.report(c, Outcome{Status: gate.ExecUnknown, Code: "stream_ended_early"})
		return
	}
	p.report(c, Classify(c.method, last))
}

// report sends one call's outcome in the background, once. A report that never lands leaves the
// action executing; the Gate's sweeper seals it UNVERIFIABLE (edge lost) at its deadline.
func (p *Proxy) report(c *call, o Outcome) {
	if c == nil {
		return
	}
	r := gate.EdgeResult{Status: o.Status, StartedAt: c.started.UTC(), FinishedAt: p.cfg.Now().UTC(), ErrorCode: o.Code, ErrorMessage: o.Message}
	if len(o.Recorded) > 0 {
		if d, err := deed.DigestJSON(o.Recorded); err == nil {
			r.ResultDigest = d
			if p.cfg.ResultMode == "full" && len(o.Recorded) <= MaxResultBytes {
				r.Result = o.Recorded
			}
		}
	}
	p.reports.Add(1)
	go func() {
		defer p.reports.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := p.gate.Report(ctx, c.actionID, r); err != nil {
			p.log.Warn("a2a: could not report the result; Deedglass seals it UNVERIFIABLE at its deadline", "action", c.actionID, "err", err)
		}
	}()
}

func digestHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
