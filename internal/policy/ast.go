package policy

import (
	"strings"

	"cel.dev/cel-go/cel"
)

// Outcome is a policy decision (contract §6).
type Outcome string

const (
	Allow           Outcome = "allow"
	Deny            Outcome = "deny"
	RequireApproval Outcome = "require_approval"
)

// Outcomes in documentation order.
var Outcomes = []string{string(Allow), string(Deny), string(RequireApproval)}

// Level is a minimum witness level (contract §6).
type Level string

const (
	L0 Level = "L0"
	L1 Level = "L1"
	L2 Level = "L2"
)

// Compiled is an accepted policy. It is immutable after Compile and safe for concurrent use.
type Compiled struct {
	Name, Description string
	Lang              string // "deedglass.policy/v1"
	VersionHash       string // "sha256:<hex>" over JCS(Normalized)
	SourceHash        string // "sha256:<hex>" over the raw bytes
	Normalized        []byte // exactly the bytes the hash covers
	Defaults          Defaults
	Budgets           []Budget
	Rules             []Rule
	Warnings          []Diagnostic
}

// Defaults decide when no rule matches.
type Defaults struct {
	Decision Outcome
	Reason   string
	Verify   Level
	Approval ApprovalSpec // always filled; rules merge over it
	Notify   []string
}

// ApprovalSpec says who approves and for how long.
type ApprovalSpec struct {
	Approvers []string // role:owner, role:admin, user:<email>; sorted, unique
	Required  int
	ExpiresIn int64 // seconds
}

// Budget is one entry of `budgets`.
type Budget struct {
	ID, Description  string
	Pos              Pos
	Match            Matcher
	Per              string // agent | org
	Window           string // minute | hour | day
	MaxCalls         int64  // 0 when MaxSpend is set
	MaxSpend         *Money // nil when MaxCalls is set
	Measure          cel.Program
	MeasureSource    string
	MeasureCanonical string
	MeasureCostMax   uint64
}

// Rule is one entry of `rules`.
type Rule struct {
	ID, Description string
	Pos             Pos
	Match           Matcher
	When            cel.Program // nil when omitted (always true)
	WhenSource      string      // as written
	WhenCanonical   string      // cel.AstToString of the checked AST; "true" when omitted
	WhenCostMax     uint64
	Decision        Outcome
	Reason          string
	Approval        *ApprovalSpec // require_approval only, with defaults merged in
	Verify          Level         // allow and require_approval; "" for deny
	Notify          []string      // deny and require_approval
}

// Matcher is a precompiled `match`. An empty field matches everything; Match allocates nothing.
type Matcher struct {
	Tool          []Glob
	ConnectorType []Glob
	SideEffect    []string
	AgentNames    []Glob
	AgentIDs      []string
	Labels        []LabelPair // sorted by key
	hasAgent      bool
}

// LabelPair is one required label.
type LabelPair struct{ Key, Value string }

// Match reports whether the static part of a rule or budget covers this tool and agent.
func (m *Matcher) Match(t ToolFacts, a AgentFacts) bool {
	if len(m.Tool) > 0 && !anyGlob(m.Tool, t.Name) {
		return false
	}
	if len(m.ConnectorType) > 0 && !anyGlob(m.ConnectorType, t.ConnectorType) {
		return false
	}
	if len(m.SideEffect) > 0 {
		ok := false
		for _, s := range m.SideEffect {
			if s == t.SideEffect {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if m.hasAgent {
		ok := false
		for _, id := range m.AgentIDs {
			if id == a.ID {
				ok = true
				break
			}
		}
		if !ok && !anyGlob(m.AgentNames, a.Name) {
			return false
		}
	}
	for _, l := range m.Labels {
		if v, ok := a.Labels[l.Key]; !ok || v != l.Value {
			return false
		}
	}
	return true
}

func anyGlob(gs []Glob, s string) bool {
	for i := range gs {
		if gs[i].Match(s) {
			return true
		}
	}
	return false
}

// String renders a matcher for messages ("tool stripe.*").
func (m *Matcher) String() string {
	var parts []string
	for _, g := range m.Tool {
		parts = append(parts, g.src)
	}
	return strings.Join(parts, ", ")
}
