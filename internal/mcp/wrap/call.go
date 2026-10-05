package wrap

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mcp/gateclient"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
	"github.com/deedglass/deedglass/internal/mcp/present"
)

// callState is one governed tools/call after the Gate authorized it.
type callState struct {
	name      string // the MCP tool name
	qualified string // "<connector>.<name>"
	actionID  string
	approval  *approvalEntry // set when this run follows a person's approval
	started   time.Time
	reported  atomic.Bool
	failOpen  bool // forwarded without a decision (dev --fail-open): nothing to report
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// handleCall is S14 §3.6: pre-checks, the Gate, then forward or a synthetic result.
func (w *Wrapper) handleCall(e jsonrpc.Envelope, modern bool, f *inflight) {
	id := e.ID()
	var pm map[string]json.RawMessage
	var cp callParams
	// One decode: name and arguments share pm's bytes (a 64 KiB call is copied as few times as possible).
	if json.Unmarshal(e["params"], &pm) != nil || json.Unmarshal(pm["name"], &cp.Name) != nil || cp.Name == "" {
		w.respondError(id, jsonrpc.CodeInvalidParams, "tools/call needs params.name, the tool to call.")
		return
	}
	cp.Arguments = pm["arguments"]
	delete(e, "params") // the members in pm are the request from here on
	if cp.Name == present.CheckApprovalName {
		w.checkApproval(e, modern, cp)
		return
	}
	qualified := w.cfg.Connector + "." + cp.Name
	if strings.HasPrefix(cp.Name, "deedglass.") {
		w.respondError(id, jsonrpc.CodeInvalidParams, gate.MCPUnknownToolText(cp.Name))
		return
	}
	entry, seen := w.cat.get(cp.Name)
	if entry == nil && !seen || entry == nil {
		w.refresh(w.ctx, modern)
		entry, _ = w.cat.get(cp.Name)
	}
	if entry == nil {
		w.respondError(id, jsonrpc.CodeInvalidParams, gate.MCPUnknownToolText(cp.Name))
		return
	}
	if entry.Visibility == visFlagged {
		// S34 (Q01): never tell the agent why; to it the tool doesn't exist.
		w.log.sym(symDenied, qualified, "refused: its description was flagged")
		w.respondError(id, jsonrpc.CodeInvalidParams, gate.MCPUnknownToolText(cp.Name))
		return
	}
	if entry.Visibility == visChanged {
		w.log.sym(symDenied, qualified, "refused: its definition changed after it was first listed")
		w.respond(id, present.Plain(gate.MCPToolChangedText(qualified), modern))
		return
	}
	// The Gate decides; a hidden tool still goes there, so the attempt is recorded and sealed.
	w.hmu.Lock()
	cname, cver := w.clientName, w.clientVer
	w.hmu.Unlock()
	d := w.gc.Submit(w.ctx, gateclient.SubmitRequest{Tool: qualified, Args: cp.Arguments, ConnectorType: w.cfg.ConnectorType,
		ToolDigest: entry.Digest, ClientName: cname, ClientVersion: cver, Transport: transportName,
		IdempotencyKey: gateclient.NewIdempotencyKey()})
	f.mu.Lock()
	cancelled := f.cancelled
	f.mu.Unlock()
	switch d.Kind {
	case gateclient.Authorized:
		cs := &callState{name: cp.Name, qualified: qualified, actionID: d.Action.ID}
		if cancelled {
			// The client gave up before the Gate answered: nothing ran.
			w.untrack(id)
			w.log.sym(symDenied, qualified, "cancelled by the client before it ran", d.Action.ID)
			w.report(cs, gate.ExecFailed, "client_cancelled", nil, nil)
			return
		}
		w.forwardCall(e, pm, modern, cs, f)
	case gateclient.Denied:
		w.untrack(id)
		if cancelled {
			return
		}
		a := d.Action
		w.log.sym(symDenied, qualified, "denied", ruleOrCode(a, d.Code), a.ID)
		w.respond(id, present.Deny(a, d.Message, modern))
	case gateclient.Pending:
		a := d.Action
		w.rememberApproval(a, cp, pm, qualified, modern)
		w.untrack(id)
		if cancelled {
			return
		}
		w.log.sym(symPending, qualified, "waiting for approval", a.Approval.ID)
		w.respond(id, present.Pending(a, modern))
	case gateclient.BadKey:
		w.log.sym(symDenied, qualified, "refused: Deedglass did not accept the API key")
		w.respond(id, present.Plain(gate.MCPBadKey, modern))
	case gateclient.Rejected:
		w.log.sym(symDenied, qualified, "refused by Deedglass", d.Code)
		w.respond(id, present.Plain(gate.MCPRejectedText(d.Message), modern))
	default:
		if w.cfg.FailOpen && !cancelled {
			w.log.sym(symDenied, "fail-open: Deedglass unreachable; "+qualified+" ran without a decision or a deed")
			w.forwardCall(e, pm, modern, &callState{name: cp.Name, qualified: qualified, failOpen: true}, f)
			return
		}
		w.unreachable(d)
		w.respond(id, present.Plain(gate.UnreachableMessage(w.cfg.Server.Redacted()), modern))
	}
}

func ruleOrCode(a *ledger.Action, code string) string {
	if a != nil && a.Decision != nil && a.Decision.RuleID != nil && *a.Decision.RuleID != "" {
		return *a.Decision.RuleID
	}
	if a != nil && a.Decision != nil && a.Decision.Code != nil {
		return *a.Decision.Code
	}
	return code
}

// unreachable logs the Gate being down, at most every 10 s.
func (w *Wrapper) unreachable(d gateclient.Decision) {
	now := time.Now().UnixNano()
	last := w.unreachW.Load()
	if now-last < int64(10*time.Second) || !w.unreachW.CompareAndSwap(last, now) {
		return
	}
	why := "no answer"
	if d.Err != nil {
		why = shortErr(d.Err)
	}
	w.log.sym(symDenied, "Gate unreachable at "+w.cfg.Server.Redacted()+" ("+why+"). Calls are blocked until it is back.")
}

// forwardCall sends an authorized call upstream. params are re-encoded from the parsed members, so
// the server reads exactly the name and arguments the Gate decided on.
func (w *Wrapper) forwardCall(e jsonrpc.Envelope, pm map[string]json.RawMessage, modern bool, cs *callState, f *inflight) {
	up := cloneEnvelope(e)
	up["params"] = jsonrpc.MarshalObject(pm)
	cs.started = time.Now()
	p := w.forward(up, kCall, modern, cs)
	if p == nil || w.cfg.CallTimeout <= 0 {
		return
	}
	upID := p.upID
	p.timer = time.AfterFunc(w.cfg.CallTimeout, func() {
		q := w.take(upID)
		if q == nil {
			return
		}
		_ = w.up.send(jsonrpc.NewNotification(mCancelled, jsonrpc.MarshalObject(map[string]json.RawMessage{
			"requestId": jsonrpc.Int(upID), "reason": jsonrpc.String("timeout")})).Marshal(), nil)
		w.log.sym(symUnknown, cs.qualified, "no answer within "+humanDur(w.cfg.CallTimeout), cs.actionID)
		w.report(cs, gate.ExecUnknown, "timeout", nil, nil)
		w.respond(q.clientID, present.WithAction(gate.MCPTimeoutText(cs.qualified, w.cfg.CallTimeout, cs.actionID), cs.actionID, modern))
	})
}

func humanDur(d time.Duration) string { return d.String() }

// finishCall answers the client with the server's result (plus _meta com.deedglass/action) and
// reports it in the background.
func (w *Wrapper) finishCall(p *pending, e jsonrpc.Envelope) {
	cs := p.call
	elapsed := time.Since(cs.started)
	if errObj, ok := e["error"]; ok {
		w.log.sym(symDenied, cs.qualified, "the MCP server refused the call", ms(elapsed), cs.actionID)
		w.report(cs, gate.ExecFailed, "upstream_error", errObj, nil)
		w.reply(p, e)
		return
	}
	raw := e["result"]
	var res map[string]json.RawMessage
	if json.Unmarshal(raw, &res) != nil {
		w.report(cs, gate.ExecUnknown, "invalid_result", raw, nil)
		w.reply(p, e)
		return
	}
	var isErr bool
	_ = json.Unmarshal(res["isError"], &isErr)
	var rtype string
	_ = json.Unmarshal(res["resultType"], &rtype)
	switch {
	case cs.failOpen:
	case rtype == "input_required":
		w.log.sym(symPending, cs.qualified, "the server asked for more input", ms(elapsed), cs.actionID)
		w.report(cs, gate.ExecUnknown, "input_required", raw, nil)
	case isErr:
		w.log.sym(symDenied, cs.qualified, "the tool reported an error", ms(elapsed), cs.actionID)
		w.report(cs, gate.ExecFailed, "tool_error", raw, nil)
	default:
		w.log.sym(symOK, cs.qualified, "allowed", ms(elapsed), cs.actionID)
		w.report(cs, gate.ExecSucceeded, "", raw, nil)
	}
	if cs.actionID != "" {
		var meta map[string]json.RawMessage
		if v, ok := res["_meta"]; ok {
			_ = json.Unmarshal(v, &meta)
		}
		if meta == nil {
			meta = map[string]json.RawMessage{}
		}
		meta[present.MetaAction] = jsonrpc.String(cs.actionID)
		res["_meta"] = jsonrpc.MarshalObject(meta)
	}
	if cs.approval != nil {
		res["content"] = prependText(res["content"], gate.MCPApprovedRanText(cs.qualified))
	}
	out := cloneEnvelope(e)
	out["result"] = jsonrpc.MarshalObject(res)
	w.reply(p, out)
}

func prependText(content json.RawMessage, text string) json.RawMessage {
	var items []json.RawMessage
	_ = json.Unmarshal(content, &items)
	head := jsonrpc.Value(map[string]string{"type": "text", "text": text})
	return jsonArray(append([]json.RawMessage{head}, items...))
}

// handleCancel is notifications/cancelled from the client (S14 §3.6 step 7).
func (w *Wrapper) handleCancel(e jsonrpc.Envelope) {
	var params map[string]json.RawMessage
	if json.Unmarshal(e["params"], &params) != nil || params["requestId"] == nil {
		return
	}
	w.mu.Lock()
	f := w.inflight[clientKey(params["requestId"])]
	w.mu.Unlock()
	if f == nil {
		return
	}
	f.mu.Lock()
	f.cancelled = true
	p := f.p
	f.mu.Unlock()
	if p == nil {
		return // not forwarded yet: handleCall sees the flag after the Gate answers
	}
	if w.take(p.upID) == nil {
		return // already answered
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	params["requestId"] = jsonrpc.Int(p.upID)
	_ = w.up.send(jsonrpc.NewNotification(mCancelled, jsonrpc.MarshalObject(params)).Marshal(), nil)
	if p.kind == kCall && p.call != nil {
		w.log.sym(symUnknown, p.call.qualified, "cancelled by the client after it was sent; it may have run", p.call.actionID)
		w.report(p.call, gate.ExecUnknown, "client_cancelled", nil, nil)
	}
}
