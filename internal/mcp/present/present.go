// Package present shapes the Gate's MCP-facing sentences (internal/gate/present.go) into MCP
// results (S14): a text content block, isError, the com.deedglass/* _meta keys, and resultType
// for 2026-07-28-era requests only.
package present

import (
	"encoding/json"
	"time"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
)

// _meta keys Deedglass adds to tools/call results (CONTRACT CHANGE, additive).
const (
	MetaAction   = "com.deedglass/action"
	MetaDeed     = "com.deedglass/deed"
	MetaDecision = "com.deedglass/decision"
	MetaRule     = "com.deedglass/rule"
	MetaApproval = "com.deedglass/approval"
	// MetaWitness (S19) is {"level":"L1","verdict":"MISMATCH"} on a witnessed tools/call result.
	MetaWitness = "com.deedglass/witness"
)

// WitnessMeta is the com.deedglass/witness value for an action's verdict, or nil before one.
func WitnessMeta(a *ledger.Action) json.RawMessage {
	if a == nil || a.Witness == nil {
		return nil
	}
	return jsonrpc.Value(map[string]string{"level": a.Witness.Level, "verdict": a.Witness.Verdict})
}

// CheckApprovalName is the wrapper's own tool.
const CheckApprovalName = "deedglass.check_approval"

// Meta is the _meta of a synthetic result.
type Meta map[string]string

// ActionMeta names the action, its deed, the decision and the rule.
func ActionMeta(a *ledger.Action) Meta {
	m := Meta{}
	if a == nil {
		return m
	}
	m[MetaAction] = a.ID
	if a.Deed != nil {
		m[MetaDeed] = *a.Deed
	}
	if a.Decision != nil {
		if a.Decision.Outcome != "" {
			m[MetaDecision] = a.Decision.Outcome
		}
		if a.Decision.RuleID != nil {
			m[MetaRule] = *a.Decision.RuleID
		}
	}
	if a.Approval != nil {
		m[MetaApproval] = a.Approval.ID
	}
	return m
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type result struct {
	Content           []textContent `json:"content"`
	StructuredContent any           `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError"`
	Meta              Meta          `json:"_meta,omitempty"`
	ResultType        string        `json:"resultType,omitempty"`
}

// Text is a tool result with one text block. modern adds resultType "complete" (2026-07-28).
func Text(text string, isError, modern bool, meta Meta, structured any) json.RawMessage {
	r := result{Content: []textContent{{Type: "text", Text: text}}, IsError: isError, StructuredContent: structured}
	if len(meta) > 0 {
		r.Meta = meta
	}
	if modern {
		r.ResultType = "complete"
	}
	return jsonrpc.Value(r)
}

// Deny is a recorded refusal.
func Deny(a *ledger.Action, fallback string, modern bool) json.RawMessage {
	return Text(gate.MCPDenyText(a, fallback), true, modern, ActionMeta(a), nil)
}

// Pending is a pending approval.
func Pending(a *ledger.Action, modern bool) json.RawMessage {
	var id string
	var exp time.Time
	if a.Approval != nil {
		id, exp = a.Approval.ID, time.Time(a.Approval.ExpiresAt)
	}
	return Text(gate.MCPPendingText(a, id, exp), true, modern, ActionMeta(a), nil)
}

// StillPending answers deedglass.check_approval while nobody has decided (not an error).
func StillPending(a *ledger.Action, modern bool) json.RawMessage {
	var id string
	var exp time.Time
	if a.Approval != nil {
		id, exp = a.Approval.ID, time.Time(a.Approval.ExpiresAt)
	}
	sc := map[string]string{"status": "pending_approval", "approval_id": id, "expires_at": exp.UTC().Format(time.RFC3339)}
	return Text(gate.MCPStillPendingText(id, exp), false, modern, ActionMeta(a), sc)
}

// Plain is a synthetic error result with no action behind it.
func Plain(text string, modern bool) json.RawMessage { return Text(text, true, modern, nil, nil) }

// WithAction is Plain naming an action (crash, timeout).
func WithAction(text, actionID string, modern bool) json.RawMessage {
	return Text(text, true, modern, Meta{MetaAction: actionID}, nil)
}

// CheckApprovalTool is the tool definition appended to tools/list.
func CheckApprovalTool() json.RawMessage {
	return json.RawMessage(`{"name":"deedglass.check_approval","title":` + string(jsonrpc.String(gate.MCPCheckApprovalTitle)) +
		`,"description":` + string(jsonrpc.String(gate.MCPCheckApprovalDescription)) +
		`,"inputSchema":{"type":"object","additionalProperties":false,"required":["approval_id"],"properties":{"approval_id":{"type":"string","pattern":"^apr_[0-9a-hjkmnp-tv-z]{26}$","description":"The approval id, for example apr_01j9zs4k7m2n3p5q6r7s8t9v0w"}}},` +
		`"annotations":{"readOnlyHint":false,"idempotentHint":true,"openWorldHint":false}}`)
}
