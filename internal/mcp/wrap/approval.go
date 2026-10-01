package wrap

import (
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
	"github.com/deedglass/deedglass/internal/mcp/present"
)

// deedglass.check_approval (S14 §3.7). Only the process that created a paused action runs it, once,
// under a per-approval mutex; after a restart the agent is asked to make the call again. No double
// execution, ever.

var reApproval = regexp.MustCompile(`^apr_[0-9a-hjkmnp-tv-z]{26}$`)

type approvalEntry struct {
	mu        sync.Mutex
	id        string
	actionID  string
	name      string
	qualified string
	params    map[string]json.RawMessage // the original tools/call params, as decided
	ran       bool
}

// unrun clears ran when the approved call could not be written to the server at all.
func (a *approvalEntry) unrun() {
	a.mu.Lock()
	a.ran = false
	a.mu.Unlock()
}

func (w *Wrapper) rememberApproval(a *ledger.Action, cp callParams, pm map[string]json.RawMessage, qualified string, _ bool) {
	if a.Approval == nil {
		return
	}
	w.amu.Lock()
	defer w.amu.Unlock()
	w.approvals[a.Approval.ID] = &approvalEntry{id: a.Approval.ID, actionID: a.ID, name: cp.Name, qualified: qualified, params: pm}
}

func (w *Wrapper) checkApproval(e jsonrpc.Envelope, modern bool, cp callParams) {
	id := e.ID()
	var args struct {
		ApprovalID string `json:"approval_id"`
	}
	if json.Unmarshal(cp.Arguments, &args) != nil || !reApproval.MatchString(args.ApprovalID) {
		w.respond(id, present.Plain(gate.MCPApprovalIDBad, modern))
		return
	}
	w.amu.Lock()
	ent := w.approvals[args.ApprovalID]
	w.amu.Unlock()
	if ent == nil {
		// GET /v1/approvals/{id} arrives with S22; until then an approval from another process
		// (or from before a restart) is never run here.
		w.respond(id, present.Plain(gate.MCPApprovalNotHereText(args.ApprovalID), modern))
		return
	}
	ent.mu.Lock()
	if ent.ran {
		ent.mu.Unlock()
		ref := ent.actionID
		if a, err := w.gc.GetAction(w.ctx, ent.actionID); err == nil && a.Deed != nil {
			ref = *a.Deed
		}
		w.respond(id, present.Text(gate.MCPAlreadyRanText(ref), false, modern, present.Meta{present.MetaAction: ent.actionID}, nil))
		return
	}
	a, err := w.gc.GetAction(w.ctx, ent.actionID)
	if err != nil {
		ent.mu.Unlock()
		w.unreachableErr(err)
		w.respond(id, present.Plain(gate.UnreachableMessage(w.cfg.Server.Redacted()), modern))
		return
	}
	switch a.Status {
	case ledger.StatusPendingApproval:
		ent.mu.Unlock()
		w.respond(id, present.StillPending(a, modern))
	case ledger.StatusAuthorized:
		ent.ran = true
		ent.mu.Unlock()
		w.log.sym(symOK, ent.qualified, "approved by a person; running it", ent.actionID)
		cs := &callState{name: ent.name, qualified: ent.qualified, actionID: ent.actionID, approval: ent}
		f := w.track(id)
		up := jsonrpc.Envelope{"jsonrpc": e["jsonrpc"], "id": id, "method": e["method"]}
		w.forwardCall(up, ent.params, modern, cs, f)
	case ledger.StatusDenied:
		ent.mu.Unlock()
		w.respond(id, present.Text(gate.MCPApprovalDenied, true, modern, present.ActionMeta(a), nil))
	case ledger.StatusExpired:
		ent.mu.Unlock()
		w.respond(id, present.Text(gate.MCPApprovalExpiredText(args.ApprovalID), true, modern, present.ActionMeta(a), nil))
	case ledger.StatusCancelled:
		ent.mu.Unlock()
		w.respond(id, present.Text(gate.MCPApprovalCancelledText(args.ApprovalID), true, modern, present.ActionMeta(a), nil))
	default:
		ent.mu.Unlock()
		w.respond(id, present.Text(gate.MCPApprovalFinishedText(args.ApprovalID, a.Status), true, modern, present.ActionMeta(a), nil))
	}
}

func (w *Wrapper) unreachableErr(err error) {
	now := time.Now().UnixNano()
	last := w.unreachW.Load()
	if now-last < int64(10*time.Second) || !w.unreachW.CompareAndSwap(last, now) {
		return
	}
	w.log.sym(symDenied, "Gate unreachable at "+w.cfg.Server.Redacted()+" ("+shortErr(err)+"). Calls are blocked until it is back.")
}
