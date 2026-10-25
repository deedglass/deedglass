// Package lockdown is lockdown mode's decision overlay (ADR-117, docs/LOCKDOWN.md): one switch for
// the strictest setup. The Gate evaluates the org's policy as always, then, when lockdown is on for
// the org (lockdown: true in deedglass.yaml, or the org's settings.lockdown), Apply tightens the
// decision. It never loosens one: a deny stays a deny, an approval keeps its approvers, and a
// verify level is only raised.
//
// What Apply does, in order (the first refusal wins):
//
//  1. An agent's call to a connector other than the one its `connector` label names is refused,
//     so each agent, and every key it holds, reaches one connector.
//  2. A call to a FHIR connector while the org's PHI mode is off is refused.
//  3. Native Postgres SQL: unclear or several statements, DROP or TRUNCATE, schema and permission
//     changes (ALTER, CREATE, GRANT…), server-side code, and UPDATE or DELETE without a WHERE are
//     refused, whatever the policy says.
//  4. A write while the agent has a Can't check result nobody has acknowledged is refused.
//  5. Every other write waits for a person: an allow becomes require_approval; a high-risk write
//     needs two different people (four-eyes); every approval expires within ApprovalExpiry; and the
//     write is checked at the strongest level its connector reaches (Proven where it can).
//
// Reads pass through to the policy unchanged (after 1 and 2). The package is pure: no I/O.
package lockdown

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/sqlguard"
)

// ApprovalExpiry is the longest any approval waits while lockdown is on. Timeout means no.
const ApprovalExpiry = 15 * time.Minute

// LabelConnector is the agent label that names the one connector an agent may call in lockdown.
const LabelConnector = "connector"

// Rule ids: the decision's rule_id when lockdown, not the policy, decided. The record names them.
const (
	RuleOneConnector     = "lockdown.one-connector"
	RulePHIMode          = "lockdown.phi-mode"
	RuleUnclearSQL       = "lockdown.unclear-sql"
	RuleNoDropTruncate   = "lockdown.no-drop-truncate"
	RuleNoSchemaChange   = "lockdown.no-schema-change"
	RuleNoServerCode     = "lockdown.no-server-code"
	RuleNoUnboundedWrite = "lockdown.no-unbounded-write"
	RuleCantCheckHold    = "lockdown.cant-check-hold"
	RuleWriteNeedsPerson = "lockdown.write-needs-person"
)

// Flags recorded in the action's decision detail (lockdown.flags).
const (
	FlagApprovalAdded     = "approval_added"     // the policy allowed a write; lockdown made it wait
	FlagFourEyes          = "four_eyes"          // a high-risk write needs two different people
	FlagExpiryShortened   = "expiry_shortened"   // the policy's approval waited longer than ApprovalExpiry
	FlagProvenRequired    = "proven_required"    // the connector reaches Proven, so the write is checked there
	FlagProvenUnavailable = "proven_unavailable" // the connector can't reach Proven: Checked at best
	FlagClaimedOnly       = "claimed_only"       // nothing can check this tool's result: Claimed only
	FlagVerifyRaised      = "verify_raised"      // the policy asked for a lower level
)

// Sources of the switch.
const (
	SourceConfig = "config" // lockdown: true in deedglass.yaml (or DEEDGLASS_LOCKDOWN)
	SourceOrg    = "org"    // the org's settings.lockdown (deedglass lockdown on)
)

// State is whether lockdown is on for one org, and why.
type State struct {
	Config bool // the server's config pins it on for every org
	Org    bool // the org turned it on
}

// On reports whether lockdown applies.
func (s State) On() bool { return s.Config || s.Org }

// Source is "config", "org", "config+org" or "".
func (s State) Source() string {
	switch {
	case s.Config && s.Org:
		return SourceConfig + "+" + SourceOrg
	case s.Config:
		return SourceConfig
	case s.Org:
		return SourceOrg
	}
	return ""
}

// Facts are what the Gate knows beyond the policy input.
type Facts struct {
	// Person is a person's own action from the Console (S23's undo): the connector label and the
	// Can't check hold are about agents, so they don't apply; approvals and SQL rules do.
	Person bool
	// ProvenAvailable: the connector reaches Proven (L2) for this call (connector.L2Witnessing gave
	// a target).
	ProvenAvailable bool
	// ToolLevels are the levels the tool reaches without Proven (connector.ToolLevels: L0, maybe L1).
	ToolLevels []string
	// PHIMode is the org's settings.phi_mode.
	PHIMode bool
	// HeldBy is the act_… id of this agent's earliest Can't check result admitted in lockdown that no
	// person has acknowledged; "" when there is none.
	HeldBy string
}

// Detail is actions.decision_detail.lockdown: what lockdown changed, for the record and the CLI.
type Detail struct {
	Source string `json:"source"`
	// Strongest is the strongest level the tool reaches (L2, L1 or L0); empty for reads.
	Strongest policy.Level `json:"strongest_level,omitempty"`
	Flags     []string     `json:"flags,omitempty"`
	HeldBy    string       `json:"held_by,omitempty"`
}

// JSON is the detail as JSON (never fails for this shape).
func (d *Detail) JSON() json.RawMessage {
	b, _ := json.Marshal(d)
	return b
}

// Messages the agent and the person read. Each ends with a next step.
const (
	ReasonWrite = "Lockdown is on, so every write waits for a person. An admin can approve in Console › Approvals."
	ReasonFour  = "Lockdown is on, and this write is high risk, so two different people approve it in Console › Approvals."

	msgOneConnectorNoLabel = "Lockdown is on, so each agent reaches one connector, and this agent has no connector label. An admin sets its labels with PATCH /v1/agents/{id} (labels: connector=<name>), or creates the agent with --label connector=<name>."
	msgOneConnectorOther   = "Lockdown is on, so this agent reaches only the connector %q. Use an agent made for %q, or ask an admin to change its connector label."
	msgPHIMode             = "Lockdown is on, and PHI mode is off for this org, so FHIR calls don't run. An owner turns PHI mode on (docs/PHI_MODE.md) or runs deedglass lockdown on."
	msgUnclearSQL          = "Lockdown is on, and Deedglass couldn't read this SQL with confidence, or it holds several statements, so it didn't run. Send one plain statement."
	msgNoDropTruncate      = "Lockdown is on: DROP and TRUNCATE never run through agents. Ship them as a reviewed migration."
	msgNoSchemaChange      = "Lockdown is on: schema and permission changes (ALTER, CREATE, GRANT and the like) never run through agents. Ship them as a reviewed migration."
	msgNoServerCode        = "Lockdown is on: server-side code, COPY PROGRAM and file, network or session functions never run through agents."
	msgNoUnboundedWrite    = "Lockdown is on: UPDATE and DELETE need a WHERE clause that names the rows to change."
	msgCantCheckHold       = "Lockdown is on, and this agent's action %s ended Can't check, so its writes wait until a person looks at it. An admin acknowledges it: deedglass lockdown ack %s --as <email>."
)

// Apply tightens d for one action in lockdown. With lockdown off it returns d and nil.
func Apply(st State, d policy.Decision, in policy.Input, f Facts) (policy.Decision, *Detail) {
	if !st.On() {
		return d, nil
	}
	det := &Detail{Source: st.Source()}
	if d.Outcome == policy.Deny {
		return d, det
	}
	tool := in.Tool
	// 1. One connector per agent.
	if !f.Person {
		label := in.Agent.Labels[LabelConnector]
		switch {
		case label == "":
			return refuse(d, RuleOneConnector, msgOneConnectorNoLabel), det
		case !strings.EqualFold(label, tool.Connector):
			return refuse(d, RuleOneConnector, sprintf(msgOneConnectorOther, label, label)), det
		}
	}
	// 2. PHI mode for FHIR.
	if tool.ConnectorType == "fhir" && !f.PHIMode {
		return refuse(d, RulePHIMode, msgPHIMode), det
	}
	// 3. The native Postgres connector's SQL.
	sql := SQLFacts(d, in)
	if sql.Present {
		switch {
		case sql.Ambiguous || sql.Multi || sql.Category == sqlguard.CatUnknown:
			return refuse(d, RuleUnclearSQL, msgUnclearSQL), det
		case sql.Kind == "drop" || sql.Kind == "truncate" || slices.Contains(sql.Kinds, "drop") || slices.Contains(sql.Kinds, "truncate"):
			return refuse(d, RuleNoDropTruncate, msgNoDropTruncate), det
		case sql.Category == sqlguard.CatDDL || sql.Category == sqlguard.CatPrivilege:
			return refuse(d, RuleNoSchemaChange, msgNoSchemaChange), det
		case sql.Category == sqlguard.CatServerCode || len(sql.DangerousFunctions) > 0 || sql.CopyProgram:
			return refuse(d, RuleNoServerCode, msgNoServerCode), det
		case sql.UnboundedWrite:
			return refuse(d, RuleNoUnboundedWrite, msgNoUnboundedWrite), det
		}
	}
	if !IsWrite(in, sql) {
		return d, det
	}
	// 4. A Can't check result holds the agent's writes.
	if !f.Person && f.HeldBy != "" {
		det.HeldBy = f.HeldBy
		return refuse(d, RuleCantCheckHold, sprintf(msgCantCheckHold, f.HeldBy, f.HeldBy)), det
	}
	// 5. A person approves every write; two people for a high-risk one.
	need := 1
	if HighRisk(in, sql) {
		need = 2
	}
	switch d.Outcome {
	case policy.Allow:
		d.Outcome, d.Code, d.Source, d.RuleID, d.RuleLine = policy.RequireApproval, policy.CodeApprovalRequired, policy.SourceBuiltin, RuleWriteNeedsPerson, 0
		d.Reason = ReasonWrite
		if need == 2 {
			d.Reason = ReasonFour
		}
		d.Message = sprintf(`Waiting for approval under policy %q: %s`, "lockdown", d.Reason)
		d.Approval = &policy.ApprovalSpec{Approvers: []string{"role:admin"}, Required: need, ExpiresIn: int64(ApprovalExpiry / time.Second)}
		det.Flags = append(det.Flags, FlagApprovalAdded)
		if need == 2 {
			det.Flags = append(det.Flags, FlagFourEyes)
		}
	case policy.RequireApproval:
		a := policy.ApprovalSpec{Approvers: []string{"role:admin"}, Required: 1}
		if d.Approval != nil {
			a = *d.Approval
			a.Approvers = slices.Clone(a.Approvers)
		}
		if a.Required < need {
			a.Required = need
			det.Flags = append(det.Flags, FlagFourEyes)
		}
		if max := int64(ApprovalExpiry / time.Second); a.ExpiresIn <= 0 || a.ExpiresIn > max {
			a.ExpiresIn = max
			det.Flags = append(det.Flags, FlagExpiryShortened)
		}
		d.Approval = &a
	}
	// The strongest check the connector reaches.
	strongest := policy.L0
	switch {
	case f.ProvenAvailable:
		strongest = policy.L2
		det.Flags = append(det.Flags, FlagProvenRequired)
	case slices.Contains(f.ToolLevels, string(policy.L1)):
		strongest = policy.L1
		det.Flags = append(det.Flags, FlagProvenUnavailable)
	default:
		det.Flags = append(det.Flags, FlagProvenUnavailable, FlagClaimedOnly)
	}
	det.Strongest = strongest
	if rank(d.Verify) < rank(strongest) {
		d.Verify = strongest
		det.Flags = append(det.Flags, FlagVerifyRaised)
	}
	return d, det
}

// refuse is a builtin deny naming the lockdown rule.
func refuse(d policy.Decision, rule, msg string) policy.Decision {
	return policy.Decision{Outcome: policy.Deny, Code: policy.CodePolicyDenied, Source: policy.SourceBuiltin, RuleID: rule,
		Reason: msg, Message: msg, Facts: d.Facts, SQLGuard: d.SQLGuard, Elapsed: d.Elapsed, Trace: d.Trace}
}

// SQLFacts are the decision's SQL facts, or, when the policy didn't derive them (no policy, or an
// input recorded without them), the same analysis done here for the native Postgres connector.
func SQLFacts(d policy.Decision, in policy.Input) policy.SQLFacts {
	if d.Facts.SQL.Present {
		return d.Facts.SQL
	}
	if in.Tool.ConnectorType != "postgres" {
		return policy.SQLFacts{}
	}
	var args map[string]any
	if json.Unmarshal(in.Args, &args) != nil {
		return policy.SQLFacts{}
	}
	s, ok := args["sql"].(string)
	if !ok {
		return policy.SQLFacts{}
	}
	f := sqlguard.Analyze(s, sqlguard.Options{DefaultSchema: in.Tool.DefaultSchema})
	return policy.SQLFacts{Present: f.Present, Kind: f.Kind, Category: f.Category, Kinds: f.Kinds, Multi: f.Multi, Writes: f.Writes,
		WriteKinds: f.WriteKinds, HasWhere: f.HasWhere, UnboundedWrite: f.UnboundedWrite, CTEWrite: f.CTEWrite, CopyProgram: f.CopyProgram,
		Tables: f.Tables, DangerousFunctions: f.DangerousFunctions, Ambiguous: f.Ambiguous}
}

// IsWrite reports whether the action may change something: every side effect but read (unknown
// counts as a write: an MCP tool's annotations are the server's claim), and SQL that writes.
func IsWrite(in policy.Input, sql policy.SQLFacts) bool {
	if sql.Present && (sql.Writes || sql.CTEWrite || sql.Category == sqlguard.CatWrite) {
		return true
	}
	return in.Tool.SideEffect != "read"
}

// HighRisk reports whether a write needs two different people: a destructive tool, a delete (SQL,
// FHIR or HTTP), a database function call, an undo, or money movement (Stripe).
func HighRisk(in policy.Input, sql policy.SQLFacts) bool {
	t := in.Tool
	op := t.Op
	if op == "" {
		_, op, _ = strings.Cut(t.Name, ".")
	}
	switch {
	case t.SideEffect == "destructive":
		return true
	case sql.Present && (sql.Kind == "delete" || slices.Contains(sql.WriteKinds, "delete")):
		return true
	case t.ConnectorType == "postgres" && op == "call":
		return true
	case op == "undo" || op == "undo_session":
		return true
	case in.Request.FHIR.Interaction == "delete":
		return true
	case strings.EqualFold(in.Request.HTTP.Method, "DELETE"):
		return true
	case t.ConnectorType == "stripe":
		return true
	}
	return false
}

var sprintf = fmt.Sprintf

func rank(l policy.Level) int {
	switch l {
	case policy.L1:
		return 1
	case policy.L2:
		return 2
	}
	return 0
}
