package ledger

import (
	"encoding/json"
	"fmt"
	"time"

	gate "github.com/deedglass/deedglass/internal/gate/lifecycle"
)

// Patch is what one transition records beyond the state and its timestamp: the columns that edge
// owns (S13). A nil field leaves the column as it is. Each group of fields belongs to the edges
// into certain states, and Transition refuses a patch that writes outside them, so a bug can't
// rewrite a decision while recording an execution.
//
// JSON fields are raw JSON objects (or arrays for Reservations); payloads (Result, PolicyInput,
// WitnessDetail) are pruned by retention, digests stay.
type Patch struct {
	// Decision: the edge into evaluated, and into denied (a Stop found when a queued action
	// resumes, or a budget refused at reservation).
	PolicyInput       json.RawMessage
	PolicyInputDigest *string
	Decision          *string // allow | deny | require_approval
	DecisionCode      *string
	DecisionSource    *string // stop | budget | rule | default | builtin
	DecisionRuleID    *string
	DecisionReason    *string
	DecisionMessage   *string
	DecisionDetail    json.RawMessage
	PolicyID          *string
	PolicyVersionID   *string
	PolicyVersionHash *string
	PolicyMode        *string
	ShadowDecision    *string
	ShadowRuleID      *string
	ShadowVersionID   *string
	ShadowDetail      json.RawMessage
	Reservations      json.RawMessage
	WitnessRequired   *string // L0 | L1 | L2: the level the decision's verify: asks for (S19)

	// Approval: the edge into pending_approval.
	ApprovalID *string

	// Executing: the edge into executing (the gateway's or the edge's deadline).
	ExecutingDeadline *time.Time

	// Execution: the edges into executed and failed.
	ExecutionStatus *string // succeeded | failed | unknown
	Result          json.RawMessage
	ResultDigest    *string
	ErrorCode       *string
	ErrorMessage    *string
	CostAmount      *int64
	CostCurrency    *string
	LatencyMS       *int64
	StartedAt       *time.Time
	FinishedAt      *time.Time
	// ExecutionEvidence is the connector's evidence of where its write landed (Postgres: xid8,
	// marker_lsn, sql_digest, command_tag; ADR-044), for the Witness only. A JSON object ≤ 1 KiB.
	ExecutionEvidence json.RawMessage

	// Witness: the edges into verified, mismatch and unverifiable.
	WitnessLevel        *string
	WitnessVerdict      *string
	WitnessMethod       *string
	WitnessDetail       json.RawMessage
	WitnessDetailDigest *string
}

type patchGroup uint8

const (
	groupDecision patchGroup = 1 << iota
	groupApproval
	groupExecuting
	groupExecution
	groupWitness
)

// owners says which states each group may be written on the way into.
var owners = map[gate.State]patchGroup{
	gate.Evaluated:       groupDecision,
	gate.Denied:          groupDecision,
	gate.PendingApproval: groupApproval,
	gate.Executing:       groupExecuting,
	gate.Executed:        groupExecution,
	gate.Failed:          groupExecution,
	gate.Verified:        groupWitness,
	gate.Mismatch:        groupWitness,
	gate.Unverifiable:    groupWitness,
}

func (p *Patch) groups() patchGroup {
	var g patchGroup
	set := func(grp patchGroup, any ...bool) {
		for _, b := range any {
			if b {
				g |= grp
				return
			}
		}
	}
	set(groupDecision, p.PolicyInput != nil, p.PolicyInputDigest != nil, p.Decision != nil, p.DecisionCode != nil,
		p.DecisionSource != nil, p.DecisionRuleID != nil, p.DecisionReason != nil, p.DecisionMessage != nil,
		p.DecisionDetail != nil, p.PolicyID != nil, p.PolicyVersionID != nil, p.PolicyVersionHash != nil,
		p.PolicyMode != nil, p.ShadowDecision != nil, p.ShadowRuleID != nil, p.ShadowVersionID != nil,
		p.ShadowDetail != nil, p.Reservations != nil, p.WitnessRequired != nil)
	set(groupApproval, p.ApprovalID != nil)
	set(groupExecuting, p.ExecutingDeadline != nil)
	set(groupExecution, p.ExecutionStatus != nil, p.Result != nil, p.ResultDigest != nil, p.ErrorCode != nil,
		p.ErrorMessage != nil, p.CostAmount != nil, p.CostCurrency != nil, p.LatencyMS != nil, p.StartedAt != nil,
		p.FinishedAt != nil, p.ExecutionEvidence != nil)
	set(groupWitness, p.WitnessLevel != nil, p.WitnessVerdict != nil, p.WitnessMethod != nil, p.WitnessDetail != nil,
		p.WitnessDetailDigest != nil)
	return g
}

// allowed reports ErrIllegalTransition when p writes a group the edge into to doesn't own.
func (p *Patch) allowed(to gate.State) error {
	g := p.groups()
	if g&^owners[to] != 0 {
		return fmt.Errorf("%w: this patch writes columns the edge into %s doesn't own", ErrIllegalTransition, to)
	}
	return nil
}

func rawArg(b json.RawMessage) any {
	if b == nil {
		return nil
	}
	return string(b)
}

// args are $8… of patchSet, in order.
func (p *Patch) args() []any {
	return []any{
		rawArg(p.PolicyInput), p.PolicyInputDigest, p.Decision, p.DecisionCode, p.DecisionSource, p.DecisionRuleID,
		p.DecisionReason, p.DecisionMessage, rawArg(p.DecisionDetail), p.PolicyID, p.PolicyVersionID, p.PolicyVersionHash,
		p.PolicyMode, p.ShadowDecision, p.ShadowRuleID, p.ShadowVersionID, rawArg(p.ShadowDetail), rawArg(p.Reservations),
		p.ApprovalID, p.ExecutingDeadline,
		p.ExecutionStatus, rawArg(p.Result), p.ResultDigest, p.ErrorCode, p.ErrorMessage, p.CostAmount, p.CostCurrency,
		p.LatencyMS, p.StartedAt, p.FinishedAt,
		p.WitnessLevel, p.WitnessVerdict, p.WitnessMethod, rawArg(p.WitnessDetail), p.WitnessDetailDigest,
		p.WitnessRequired, rawArg(p.ExecutionEvidence),
	}
}

// merge folds q into p. Two steps of one Advance may not write the same column.
func (p *Patch) merge(q *Patch) error {
	if q == nil {
		return nil
	}
	pa, qa := p.args(), q.args()
	for i := range pa {
		if !isNil(qa[i]) && !isNil(pa[i]) {
			return fmt.Errorf("%w: two steps write the same column", ErrIllegalTransition)
		}
	}
	var m Patch
	// Field by field: the args order is the field order (patchSet).
	pick := func(a, b any) any {
		if !isNil(b) {
			return b
		}
		return a
	}
	out := make([]any, len(pa))
	for i := range pa {
		out[i] = pick(pa[i], qa[i])
	}
	m.fromArgs(out)
	*p = m
	return nil
}

func isNil(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case *string:
		return x == nil
	case *int64:
		return x == nil
	case *time.Time:
		return x == nil
	}
	return false
}

// fromArgs is the inverse of args.
func (p *Patch) fromArgs(a []any) {
	raw := func(v any) json.RawMessage {
		if v == nil {
			return nil
		}
		return json.RawMessage(v.(string))
	}
	s := func(v any) *string { x, _ := v.(*string); return x }
	i := func(v any) *int64 { x, _ := v.(*int64); return x }
	t := func(v any) *time.Time { x, _ := v.(*time.Time); return x }
	*p = Patch{
		PolicyInput: raw(a[0]), PolicyInputDigest: s(a[1]), Decision: s(a[2]), DecisionCode: s(a[3]), DecisionSource: s(a[4]),
		DecisionRuleID: s(a[5]), DecisionReason: s(a[6]), DecisionMessage: s(a[7]), DecisionDetail: raw(a[8]), PolicyID: s(a[9]),
		PolicyVersionID: s(a[10]), PolicyVersionHash: s(a[11]), PolicyMode: s(a[12]), ShadowDecision: s(a[13]), ShadowRuleID: s(a[14]),
		ShadowVersionID: s(a[15]), ShadowDetail: raw(a[16]), Reservations: raw(a[17]),
		ApprovalID: s(a[18]), ExecutingDeadline: t(a[19]),
		ExecutionStatus: s(a[20]), Result: raw(a[21]), ResultDigest: s(a[22]), ErrorCode: s(a[23]), ErrorMessage: s(a[24]),
		CostAmount: i(a[25]), CostCurrency: s(a[26]), LatencyMS: i(a[27]), StartedAt: t(a[28]), FinishedAt: t(a[29]),
		WitnessLevel: s(a[30]), WitnessVerdict: s(a[31]), WitnessMethod: s(a[32]), WitnessDetail: raw(a[33]), WitnessDetailDigest: s(a[34]),
		WitnessRequired: s(a[35]), ExecutionEvidence: raw(a[36]),
	}
}
