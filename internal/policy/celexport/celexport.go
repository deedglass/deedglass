// Package celexport writes a compiled policy's rules as plain CEL strings (S47).
//
// The YAML compiler stays in Go. The browser playground on deedglass.com/play receives only these
// strings and evaluates them with cel-js; a parity test runs the same cases through policy.Evaluate
// and cel-js and must agree 40 of 40. Budgets, approvals and verify levels are not exported: the
// playground has no clock, no counters and no approvers.
package celexport

import (
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/policy"
)

// Version is the export format.
const Version = 1

// Policy is one exported policy.
type Policy struct {
	Object      string    `json:"object"` // "play_policy"
	V           int       `json:"v"`
	Name        string    `json:"name"`
	VersionHash string    `json:"version_hash"`
	Default     Default   `json:"default"`
	Rules       []CELRule `json:"rules"`
}

// Default decides when no rule matches.
type Default struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// CELRule is one rule as two CEL expressions: match_cel (the rule's `match`, translated) and
// when_cel (the rule's `when` as written; "true" when omitted). A rule decides when both are true.
type CELRule struct {
	ID       string `json:"id"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
	MatchCEL string `json:"match_cel"`
	WhenCEL  string `json:"when_cel"`
}

// Export translates a compiled policy.
func Export(c *policy.Compiled) Policy {
	p := Policy{Object: "play_policy", V: Version, Name: c.Name, VersionHash: c.VersionHash,
		Default: Default{Decision: string(c.Defaults.Decision), Reason: c.Defaults.Reason}, Rules: []CELRule{}}
	for i := range c.Rules {
		r := &c.Rules[i]
		when := strings.TrimSpace(r.WhenSource)
		if when == "" {
			when = "true"
		}
		p.Rules = append(p.Rules, CELRule{ID: r.ID, Decision: string(r.Decision), Reason: r.Reason,
			MatchCEL: MatchCEL(&r.Match), WhenCEL: when})
	}
	return p
}

// MatchCEL is the CEL form of a matcher, using the same glob() the policy language registers.
// An empty matcher is "true".
func MatchCEL(m *policy.Matcher) string {
	var parts []string
	if len(m.Tool) > 0 {
		parts = append(parts, globs("tool.name", m.Tool))
	}
	if len(m.ConnectorType) > 0 {
		parts = append(parts, globs("tool.connector_type", m.ConnectorType))
	}
	if len(m.SideEffect) > 0 {
		parts = append(parts, "tool.side_effect in "+list(m.SideEffect))
	}
	if len(m.AgentIDs) > 0 || len(m.AgentNames) > 0 {
		var any []string
		if len(m.AgentIDs) > 0 {
			any = append(any, "agent.id in "+list(m.AgentIDs))
		}
		if len(m.AgentNames) > 0 {
			any = append(any, globs("agent.name", m.AgentNames))
		}
		parts = append(parts, group(any))
	}
	labels := append([]policy.LabelPair(nil), m.Labels...)
	sort.Slice(labels, func(i, j int) bool { return labels[i].Key < labels[j].Key })
	for _, l := range labels {
		parts = append(parts, "("+Quote(l.Key)+" in agent.labels && agent.labels["+Quote(l.Key)+"] == "+Quote(l.Value)+")")
	}
	if len(parts) == 0 {
		return "true"
	}
	return strings.Join(parts, " && ")
}

func globs(field string, gs []policy.Glob) string {
	var any []string
	for _, g := range gs {
		any = append(any, "glob("+field+", "+Quote(g.String())+")")
	}
	return group(any)
}

func group(any []string) string {
	if len(any) == 1 {
		return any[0]
	}
	return "(" + strings.Join(any, " || ") + ")"
}

func list(vs []string) string {
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = Quote(v)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// Quote writes a CEL double-quoted string literal.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
