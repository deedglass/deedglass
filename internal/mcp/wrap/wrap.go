package wrap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mcp/gateclient"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
	"github.com/deedglass/deedglass/internal/mcp/present"
)

// Methods the wrapper treats specially. Everything else passes through with only its id rewritten.
const (
	mInitialize   = "initialize"
	mInitialized  = "notifications/initialized"
	mDiscover     = "server/discover"
	mToolsList    = "tools/list"
	mToolsCall    = "tools/call"
	mCancelled    = "notifications/cancelled"
	mListChanged  = "notifications/tools/list_changed"
	transportName = "mcp_stdio"
)

type kind int

const (
	kPassthrough kind = iota
	kInit
	kDiscover
	kList
	kCall
	kInternal
)

// pending is one request the wrapper sent upstream and waits on.
type pending struct {
	upID     int64
	clientID json.RawMessage // nil for the wrapper's own requests
	method   string
	modern   bool
	kind     kind
	gen      int // the child it was written to; 0 while buffered
	call     *callState
	internal func(jsonrpc.Envelope) // kInternal: receives the response (nil envelope: the child died)
	params   json.RawMessage        // kList: the request's params (cursor)
	timer    *time.Timer
}

// inflight is a client request the wrapper hasn't answered yet (for cancellation).
type inflight struct {
	mu        sync.Mutex
	p         *pending // set once forwarded
	call      *callState
	cancelled bool
}

// Wrapper is one running `deedglass mcp wrap`.
type Wrapper struct {
	cfg     Config
	gc      *gateclient.Client
	log     *logger
	out     *jsonrpc.Writer
	up      *upstream
	cat     *catalog
	session string

	nextID   atomic.Int64
	mu       sync.Mutex
	pend     map[int64]*pending
	inflight map[string]*inflight

	hmu         sync.Mutex
	initReq     jsonrpc.Envelope // the client's legacy initialize, for replay after a restart
	initialized []byte
	clientName  string
	clientVer   string
	modernMeta  map[string]json.RawMessage // the latest modern request's _meta, for the wrapper's own requests

	amu       sync.Mutex
	approvals map[string]*approvalEntry

	reports  sync.WaitGroup
	ctx      context.Context // the wrapper's lifetime (Gate calls)
	stop     context.CancelFunc
	closing  atomic.Bool
	unreachW atomic.Int64 // last unreachable warning (unix ns), to keep the log calm
}

// New builds a wrapper. stderr gets the log lines; it is never the client's stdout.
func New(cfg Config, stderr io.Writer) (*Wrapper, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	l, err := newLogger(stderr, cfg.LogFile)
	if err != nil {
		return nil, err
	}
	session := "mcp:" + cfg.Connector + ":" + strings.TrimPrefix(ids.New[ids.SessionKind]().String(), "ses_")
	ua := "deedglass-mcp-wrap/" + cfg.Version
	w := &Wrapper{cfg: cfg, log: l, session: session, pend: map[int64]*pending{}, inflight: map[string]*inflight{},
		approvals: map[string]*approvalEntry{}}
	w.gc = gateclient.New(gateclient.Options{Server: strings.TrimSuffix(cfg.Server.String(), "/"), APIKey: cfg.APIKey,
		Session: session, OnBehalfOf: cfg.OnBehalfOf, UserAgent: ua})
	w.cat = newCatalog(w)
	return w, nil
}

// Run relays between the client (in, out) and the upstream server until the client closes its
// side or ctx ends. It returns nil after a clean shutdown.
func (w *Wrapper) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	w.ctx, w.stop = context.WithCancel(context.WithoutCancel(ctx))
	defer w.stop()
	defer w.log.close()
	w.out = jsonrpc.NewWriter(out)
	srv := w.cfg.Server.Redacted()
	w.log.line("deedglass mcp wrap", "connector "+w.cfg.Connector, "server "+srv)
	if w.cfg.FailOpen {
		w.log.sym(symDenied, "fail-open is on: calls run without a decision or a deed when Deedglass is unreachable (development only)")
	}
	go w.hello()
	w.up = newUpstream(w)
	w.up.start()

	lines := make(chan struct{})
	go func() {
		defer close(lines)
		r := jsonrpc.NewReader(in)
		r.MaxLine = w.cfg.MaxMessage
		for {
			e, line, err := r.Next()
			if err != nil {
				if isFraming(err) {
					code, msg := jsonrpc.Reply(err)
					_ = w.out.SendEnvelope(jsonrpc.NewError(nil, code, msg))
					continue
				}
				return
			}
			w.fromClient(e, line)
		}
	}()
	select {
	case <-lines:
	case <-ctx.Done():
	}
	w.shutdown()
	return nil
}

// hello checks the key and the server once at startup (in parallel with the relay).
func (w *Wrapper) hello() {
	me, err := w.gc.Whoami(w.ctx)
	switch {
	case gateclient.IsAuthError(err):
		w.log.sym(symDenied, "Deedglass did not accept the API key", "calls are refused until the key in your MCP client config is fixed")
	case err != nil:
		w.log.sym(symDenied, "Gate unreachable at "+w.cfg.Server.Redacted()+" ("+shortErr(err)+")", "calls are blocked until it is back")
	case me.Agent == nil:
		w.log.sym(symDenied, "this is an org admin key", "the wrapper needs an agent's key (deedglass agent keys create <agent>)")
	default:
		w.log.sym(symOK, "Gate reachable", "agent "+me.Agent.Name+" ("+me.Agent.ID+")", "key "+me.Key.Mode)
	}
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && i+2 < len(s) {
		s = s[i+2:]
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// shutdown: close the child's stdin, wait, signal the group; then flush pending reports (≤ 5 s).
func (w *Wrapper) shutdown() {
	w.closing.Store(true)
	w.up.close(w.ctx)
	done := make(chan struct{})
	go func() { w.reports.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		w.log.sym(symUnknown, "some results were not reported before exit; Deedglass seals them UNVERIFIABLE at their deadline")
	}
	_ = w.out.Close()
}

// --- client → upstream ---

func (w *Wrapper) fromClient(e jsonrpc.Envelope, line []byte) {
	switch e.Kind() {
	case jsonrpc.Request:
		method := e.Method()
		modern := e.ModernVersion() != ""
		if modern {
			w.hmu.Lock()
			w.modernMeta = e.Meta()
			w.hmu.Unlock()
			w.rememberClient(e.Meta()[jsonrpc.MetaClientInfo])
		}
		switch method {
		case mToolsCall:
			f := w.track(e.ID())
			go w.handleCall(e, modern, f)
		case mInitialize:
			w.hmu.Lock()
			w.initReq = cloneEnvelope(e)
			w.hmu.Unlock()
			var p struct {
				ClientInfo json.RawMessage `json:"clientInfo"`
			}
			_ = json.Unmarshal(e["params"], &p)
			w.rememberClient(p.ClientInfo)
			w.forward(e, kInit, modern, nil)
		case mDiscover:
			w.forward(e, kDiscover, modern, nil)
		case mToolsList:
			w.forward(e, kList, modern, nil)
		default:
			w.forward(e, kPassthrough, modern, nil)
		}
	case jsonrpc.Notification:
		switch e.Method() {
		case mCancelled:
			w.handleCancel(e)
			return
		case mInitialized:
			w.hmu.Lock()
			w.initialized = append([]byte(nil), line...)
			w.hmu.Unlock()
		}
		_ = w.up.send(append([]byte(nil), line...), nil)
	case jsonrpc.Response:
		// An answer to a server-to-client request (legacy sampling, elicitation, roots): those ids
		// belong to the server, so it goes back unchanged.
		_ = w.up.send(append([]byte(nil), line...), nil)
	}
}

func (w *Wrapper) rememberClient(raw json.RawMessage) {
	var ci struct{ Name, Version string }
	if len(raw) == 0 || json.Unmarshal(raw, &ci) != nil {
		return
	}
	w.hmu.Lock()
	w.clientName, w.clientVer = ci.Name, ci.Version
	w.hmu.Unlock()
}

func (w *Wrapper) legacyHandshake() (jsonrpc.Envelope, []byte) {
	w.hmu.Lock()
	defer w.hmu.Unlock()
	return w.initReq, w.initialized
}

func clientKey(id json.RawMessage) string { return string(id) }

// track registers a client request before the wrapper answers it.
func (w *Wrapper) track(id json.RawMessage) *inflight {
	f := &inflight{}
	w.mu.Lock()
	w.inflight[clientKey(id)] = f
	w.mu.Unlock()
	return f
}

func (w *Wrapper) untrack(id json.RawMessage) {
	if id == nil {
		return
	}
	w.mu.Lock()
	delete(w.inflight, clientKey(id))
	w.mu.Unlock()
}

// forward sends a client request upstream under a fresh id.
func (w *Wrapper) forward(e jsonrpc.Envelope, k kind, modern bool, cs *callState) *pending {
	p := &pending{upID: w.nextID.Add(1), clientID: e.ID(), method: e.Method(), modern: modern, kind: k, call: cs}
	if k == kList {
		p.params = e["params"]
	}
	f := (*inflight)(nil)
	w.mu.Lock()
	w.pend[p.upID] = p
	if p.clientID != nil {
		f = w.inflight[clientKey(p.clientID)]
		if f == nil {
			f = &inflight{}
			w.inflight[clientKey(p.clientID)] = f
		}
	}
	w.mu.Unlock()
	if f != nil {
		f.mu.Lock()
		f.p = p
		f.mu.Unlock()
	}
	up := cloneEnvelope(e)
	up["id"] = jsonrpc.Int(p.upID)
	if err := w.up.send(up.Marshal(), p); err != nil {
		w.mu.Lock()
		delete(w.pend, p.upID)
		w.mu.Unlock()
		w.failOne(p, err)
		return nil
	}
	return p
}

// internalPending registers one of the wrapper's own requests.
func (w *Wrapper) internalPending(method string, fn func(jsonrpc.Envelope)) *pending {
	p := &pending{upID: w.nextID.Add(1), method: method, kind: kInternal, internal: fn}
	w.mu.Lock()
	w.pend[p.upID] = p
	w.mu.Unlock()
	return p
}

// request sends one of the wrapper's own requests upstream and waits for its answer.
func (w *Wrapper) request(ctx context.Context, method string, params json.RawMessage) (jsonrpc.Envelope, error) {
	ch := make(chan jsonrpc.Envelope, 1)
	p := w.internalPending(method, func(e jsonrpc.Envelope) { ch <- e })
	if err := w.up.send(jsonrpc.NewRequest(p.upID, method, params).Marshal(), p); err != nil {
		w.take(p.upID)
		return nil, err
	}
	select {
	case e := <-ch:
		if e == nil {
			return nil, errors.New("the MCP server stopped")
		}
		return e, nil
	case <-ctx.Done():
		w.take(p.upID)
		return nil, ctx.Err()
	}
}

// take removes and returns the pending request with upstream id id.
func (w *Wrapper) take(id int64) *pending {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.pend[id]
	delete(w.pend, id)
	if p != nil && p.clientID != nil {
		delete(w.inflight, clientKey(p.clientID))
	}
	return p
}

// --- upstream → client ---

func (w *Wrapper) fromUpstream(gen int, e jsonrpc.Envelope, line []byte, err error, r *jsonrpc.Reader) {
	if err != nil {
		if errors.Is(err, jsonrpc.ErrTooLarge) {
			w.tooLarge(r)
			return
		}
		w.log.sym(symDenied, "the MCP server wrote a line that isn't JSON-RPC; dropped", jsonrpcErr(err))
		return
	}
	switch e.Kind() {
	case jsonrpc.Response:
		var id int64
		if json.Unmarshal(e.ID(), &id) != nil {
			w.log.sym(symDenied, "the MCP server answered an id the wrapper never sent; dropped")
			return
		}
		p := w.take(id)
		if p == nil {
			w.log.sym(symDenied, "the MCP server answered an id the wrapper never sent; dropped")
			return
		}
		if p.timer != nil {
			p.timer.Stop()
		}
		w.deliver(p, e)
	case jsonrpc.Notification:
		if e.Method() == mListChanged {
			w.cat.invalidate()
		}
		_ = w.out.Send(append([]byte(nil), line...))
	case jsonrpc.Request:
		// Legacy server-to-client requests (sampling, elicitation, roots): passed through
		// ungoverned in v1 (docs/MCP.md); the client shows its own confirmation.
		_ = w.out.Send(append([]byte(nil), line...))
	}
}

func jsonrpcErr(err error) string {
	_, msg := jsonrpc.Reply(err)
	return msg
}

// deliver answers the client for one upstream response.
func (w *Wrapper) deliver(p *pending, e jsonrpc.Envelope) {
	switch p.kind {
	case kInternal:
		if p.internal != nil {
			p.internal(e)
		}
	case kCall:
		go w.finishCall(p, e)
	case kList:
		go w.finishList(p, e)
	case kInit, kDiscover:
		w.reply(p, w.withInstructions(e))
	default:
		w.reply(p, e)
	}
}

// reply sends e to the client under the client's id.
func (w *Wrapper) reply(p *pending, e jsonrpc.Envelope) {
	out := cloneEnvelope(e)
	out["id"] = p.clientID
	_ = w.out.SendEnvelope(out)
}

// respond answers a client request with a result.
func (w *Wrapper) respond(id, result json.RawMessage) {
	w.untrack(id)
	_ = w.out.SendEnvelope(jsonrpc.NewResponse(id, result))
}

// respondError answers a client request with a JSON-RPC error.
func (w *Wrapper) respondError(id json.RawMessage, code int, msg string) {
	w.untrack(id)
	_ = w.out.SendEnvelope(jsonrpc.NewError(id, code, msg))
}

// withInstructions appends Deedglass's one line to the server's instructions and makes sure the
// client sees a tools capability (initialize and server/discover answers).
func (w *Wrapper) withInstructions(e jsonrpc.Envelope) jsonrpc.Envelope {
	raw, ok := e["result"]
	if !ok {
		return e
	}
	var res map[string]json.RawMessage
	if json.Unmarshal(raw, &res) != nil {
		return e
	}
	var ins string
	if v, ok := res["instructions"]; ok {
		_ = json.Unmarshal(v, &ins)
	}
	if ins == "" {
		ins = gate.MCPInstructions
	} else if !strings.Contains(ins, gate.MCPInstructions) {
		ins = strings.TrimRight(ins, "\n") + "\n\n" + gate.MCPInstructions
	}
	res["instructions"] = jsonrpc.String(ins)
	var caps map[string]json.RawMessage
	if v, ok := res["capabilities"]; ok {
		_ = json.Unmarshal(v, &caps)
	}
	if caps == nil {
		caps = map[string]json.RawMessage{}
	}
	if _, ok := caps["tools"]; !ok {
		caps["tools"] = json.RawMessage("{}")
		res["capabilities"] = jsonrpc.MarshalObject(caps)
	}
	out := cloneEnvelope(e)
	out["result"] = jsonrpc.MarshalObject(res)
	return out
}

// tooLarge answers the request an oversize response belongs to, when its id can be found.
func (w *Wrapper) tooLarge(r *jsonrpc.Reader) {
	id, ok := findID(r.Head, r.Tail)
	if !ok {
		w.log.sym(symDenied, "the MCP server sent a message larger than 4 MiB; dropped")
		return
	}
	p := w.take(id)
	if p == nil {
		return
	}
	w.log.sym(symDenied, "the MCP server's answer to "+p.method+" was larger than 4 MiB; refused")
	if p.kind == kCall && p.call != nil {
		w.report(p.call, gate.ExecUnknown, "result_too_large", nil, nil)
	}
	if p.kind == kInternal {
		if p.internal != nil {
			p.internal(nil)
		}
		return
	}
	w.respondError(p.clientID, jsonrpc.CodeInternal, gate.MCPTooLarge)
}

// findID finds the top-level integer id of a response from its first and last bytes.
func findID(head, tail []byte) (int64, bool) {
	if id, ok := topLevelID(head); ok {
		return id, true
	}
	t := strings.TrimSpace(string(tail))
	t = strings.TrimSuffix(t, "}")
	t = strings.TrimSpace(t)
	if i := strings.LastIndex(t, `"id"`); i >= 0 {
		rest := strings.TrimSpace(t[i+4:])
		if strings.HasPrefix(rest, ":") {
			n, err := strconv.ParseInt(strings.TrimSpace(rest[1:]), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// topLevelID scans b (the start of a JSON object) for "id" at depth 1.
func topLevelID(b []byte) (int64, bool) {
	d := 0
	in, esc := false, false
	start := -1
	for i := 0; i < len(b); i++ {
		c := b[i]
		if in {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				in = false
				if d == 1 && start >= 0 && string(b[start:i]) == "id" {
					j := i + 1
					for j < len(b) && (b[j] == ' ' || b[j] == ':') {
						j++
					}
					k := j
					for k < len(b) && (b[k] == '-' || (b[k] >= '0' && b[k] <= '9')) {
						k++
					}
					n, err := strconv.ParseInt(string(b[j:k]), 10, 64)
					return n, err == nil && k > j
				}
			}
			continue
		}
		switch c {
		case '"':
			in, start = true, i+1
		case '{', '[':
			d++
		case '}', ']':
			d--
		}
	}
	return 0, false
}

// failInFlight answers every request child gen held (called with the upstream lock held).
func (w *Wrapper) failInFlight(gen int) {
	if gen < 0 {
		return
	}
	w.mu.Lock()
	var dead []*pending
	for id, p := range w.pend {
		if p.gen == gen && gen > 0 {
			dead = append(dead, p)
			delete(w.pend, id)
			if p.clientID != nil {
				delete(w.inflight, clientKey(p.clientID))
			}
		}
	}
	w.mu.Unlock()
	for _, p := range dead {
		if p.timer != nil {
			p.timer.Stop()
		}
		w.failOne(p, errCrashed)
	}
}

var errCrashed = errors.New("crashed")

// failQueued answers buffered requests the child will never get.
func (w *Wrapper) failQueued(buf []queued, err error) {
	for _, q := range buf {
		if q.p == nil {
			continue
		}
		w.take(q.p.upID)
		w.failOne(q.p, err)
	}
}

// failOne answers one request that can't reach (or never came back from) the MCP server.
func (w *Wrapper) failOne(p *pending, err error) {
	if p.kind == kInternal {
		if p.internal != nil {
			p.internal(nil)
		}
		return
	}
	if p.kind == kCall && p.call != nil {
		cs := p.call
		switch {
		case errors.Is(err, errCrashed):
			w.log.sym(symUnknown, cs.qualified, "the MCP server stopped mid-call", cs.actionID)
			w.report(cs, gate.ExecUnknown, "upstream_crashed", nil, nil)
			w.respond(p.clientID, present.WithAction(gate.MCPCrashedText(cs.qualified, cs.actionID), cs.actionID, p.modern))
		default:
			// Never written to a child: nothing ran.
			if cs.approval != nil {
				cs.approval.unrun()
			}
			w.report(cs, gate.ExecFailed, "upstream_unavailable", nil, nil)
			msg := gate.MCPServerStopped
			if errors.Is(err, errDead) {
				msg = gate.MCPKeepsCrashing
			}
			w.respond(p.clientID, present.WithAction(msg, cs.actionID, p.modern))
		}
		return
	}
	msg := gate.MCPServerStopped
	if errors.Is(err, errDead) {
		msg = gate.MCPKeepsCrashing
	}
	w.respondError(p.clientID, jsonrpc.CodeInternal, msg)
}

func cloneEnvelope(e jsonrpc.Envelope) jsonrpc.Envelope {
	out := make(jsonrpc.Envelope, len(e))
	for k, v := range e {
		out[k] = v
	}
	return out
}
