package policy

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// The policy test runner (S12 §3.8): cases run like unit tests against S11's Evaluate, with rule
// coverage. It is pure: no database, explain on, local usage filled (FillLocalUsage). The CLI
// (`deedglass policy test`) and POST /v1/policies/test return the same TestResult.
//
//	policy: postgres-safe.yaml          # optional; resolved relative to the cases file
//	defaults:                           # merged into every case's input
//	  agent: { name: support-bot, labels: { env: prod } }
//	cases:
//	  - name: reads run freely
//	    input: { tool: postgres.query, sql: "SELECT 1" }
//	    expect: { decision: allow, rule: allow-reads }

// Limits of a cases file.
const (
	MaxCases      = 1000
	MaxCasesBytes = 1 << 20
	MaxCaseName   = 200
)

// CasesFile is a parsed cases file.
type CasesFile struct {
	File     string // as given, for messages
	Policy   string // the policy: key ("" when absent)
	Cases    []Case
	defaults any
}

// Case is one test case.
type Case struct {
	Name   string
	Line   int // the case's line in the file (0 for JSON input)
	Input  any // the merged case input, for BuildInput
	Expect Expect
	path   string // cases[3]
}

// Expect is what a case expects. Only Decision is required; every other field is checked when set.
type Expect struct {
	Decision       Outcome       `json:"decision"`
	Rule           *string       `json:"rule,omitempty"` // a rule id, "default", "budget:<id>" or "stop"
	Code           *string       `json:"code,omitempty"`
	ReasonContains *string       `json:"reason_contains,omitempty"`
	Message        *string       `json:"message,omitempty"`
	Verify         *string       `json:"verify,omitempty"`
	Approval       *ExpectApprov `json:"approval,omitempty"`
	Notify         []string      `json:"notify,omitempty"`
	notifySet      bool
}

// ExpectApprov is the approval part of an expectation.
type ExpectApprov struct {
	Required  *int     `json:"required,omitempty"`
	Approvers []string `json:"approvers,omitempty"`
	ExpiresIn *int64   `json:"expires_in,omitempty"` // seconds
}

var (
	casesTopKeys   = []string{"policy", "defaults", "cases"}
	caseKeys       = []string{"name", "input", "expect"}
	expectKeys     = []string{"decision", "rule", "code", "reason_contains", "message", "verify", "approval", "notify"}
	expectApprKeys = []string{"required", "approvers", "expires_in"}
	ruleExpectRE   = regexp.MustCompile(`^(default|stop|budget:[a-z0-9._-]{1,80}|[a-z0-9][a-z0-9._-]{0,79})$`)
)

// ParseCasesYAML reads a cases file with S10's safe YAML reader (no anchors, aliases, tags or
// duplicate keys; the same limits) and checks every key. Diagnostics use S10's codes and style.
func ParseCasesYAML(raw []byte, file string) (*CasesFile, []Diagnostic) {
	d := &diags{file: file}
	if len(raw) > MaxCasesBytes {
		d.errorf("file_too_large", span{}, "", "", "Split the cases into several files.",
			"This cases file is %s KiB; the limit is %s KiB.", thousands((len(raw)+1023)/1024), thousands(MaxCasesBytes/1024))
		return nil, d.list
	}
	src := newSource(raw)
	root := readYAML(src, d)
	if root == nil {
		return nil, d.list
	}
	pos := map[string]Pos{}
	v, err := nodeValue(root, "", pos)
	if err != nil {
		d.errorf("invalid_value", span{}, "", "", "", "%s", err.Error())
		return nil, d.list
	}
	cf := parseCasesValue(v, file, pos, d)
	return cf, d.list
}

// ParseCasesValue reads cases given as a generic value (JSON: an object with cases, or a list of
// cases). Diagnostics carry the path and no line.
func ParseCasesValue(v any, file string) (*CasesFile, []Diagnostic) {
	d := &diags{file: file}
	if list, ok := v.([]any); ok {
		v = map[string]any{"cases": list}
	}
	cf := parseCasesValue(NormalizeJSONValue(v), file, map[string]Pos{}, d)
	return cf, d.list
}

func parseCasesValue(v any, file string, pos map[string]Pos, d *diags) *CasesFile {
	at := func(path string) span {
		p := pos[path]
		return span{Start: p}
	}
	problem := func(prefix string, p *InputProblem) {
		path := prefix
		if p.Path != "" {
			path = prefix + "." + p.Path
		}
		code := "invalid_value"
		switch {
		case strings.HasPrefix(p.Message, "Unknown key"):
			code = "unknown_key"
		case strings.Contains(p.Message, " must be ") && strings.Contains(p.Message, "; found "):
			code = "wrong_type"
		}
		sp := at(path + "#key")
		if sp.Start.Line == 0 {
			sp = at(path)
		}
		if sp.Start.Line == 0 {
			sp = at(prefix)
		}
		d.add(SevError, code, sp, path, "", p.Message, p.Hint)
	}
	top, ok := v.(map[string]any)
	if !ok {
		d.errorf("wrong_type", span{}, "", "", "Start the file with `cases:`, a list of cases.", "A cases file must be a mapping of keys; found %s.", typeWord(v))
		return nil
	}
	if p := checkKeysIn("", "the cases file", top, casesTopKeys); p != nil {
		problem("", p)
		return nil
	}
	cf := &CasesFile{File: file}
	if p, set := top["policy"]; set {
		s, ok := p.(string)
		if !ok || s == "" {
			d.errorf("wrong_type", at("policy"), "policy", "", "Name the policy file, like postgres-safe.yaml.", "policy must be a file name.")
			return nil
		}
		cf.Policy = s
	}
	if def, set := top["defaults"]; set {
		dm, ok := def.(map[string]any)
		if !ok {
			d.errorf("wrong_type", at("defaults"), "defaults", "", "", "defaults must be a mapping of input keys; found %s.", typeWord(def))
			return nil
		}
		probe := MergeCaseInput(dm, map[string]any{"tool": "probe.defaults"})
		if _, p := BuildInput(probe); p != nil {
			problem("defaults", p)
			return nil
		}
		cf.defaults = dm
	}
	list, ok := top["cases"].([]any)
	if !ok {
		if _, set := top["cases"]; !set {
			d.errorf("missing_key", span{}, "", "", "Add `cases:` with at least one case.", "This cases file has no cases.")
		} else {
			d.errorf("wrong_type", at("cases"), "cases", "", "", "cases must be a list; found %s.", typeWord(top["cases"]))
		}
		return nil
	}
	if len(list) == 0 {
		d.errorf("missing_key", at("cases"), "cases", "", "Add at least one case.", "This cases file has no cases.")
		return nil
	}
	if len(list) > MaxCases {
		d.errorf("too_many_values", at("cases"), "cases", "", "Split the cases into several files.",
			"This file has %s cases; the limit is %s.", thousands(len(list)), thousands(MaxCases))
		return nil
	}
	names := map[string]int{}
	for i, item := range list {
		path := fmt.Sprintf("cases[%d]", i)
		cm, ok := item.(map[string]any)
		if !ok {
			d.errorf("wrong_type", at(path), path, "", "", "Each case must be a mapping with name, input and expect; found %s.", typeWord(item))
			continue
		}
		if p := checkKeysIn("", "this case", cm, caseKeys); p != nil {
			problem(path, p)
			continue
		}
		c := Case{Line: pos[path].Line, path: path}
		name, _ := cm["name"].(string)
		switch {
		case strings.TrimSpace(name) == "":
			d.errorf("missing_key", at(path), path+".name", "", "Name it after what it checks, like \"reads run freely\".", "Case %d has no name.", i+1)
			continue
		case len([]rune(name)) > MaxCaseName:
			d.errorf("value_too_long", at(path+".name"), path+".name", "", "Shorten it.", "This case name is longer than %d characters.", MaxCaseName)
			continue
		}
		if first, dup := names[name]; dup {
			d.errorf("duplicate_id", at(path+".name"), path+".name", "", "Give each case its own name.", "Case %q appears twice (first on line %d).", clipN(name, 60), first)
			continue
		}
		names[name] = c.Line
		c.Name = name
		in, set := cm["input"]
		if !set {
			d.errorf("missing_key", at(path), path+".input", "", "Add input: with at least a tool.", "Case %q has no input.", clipN(name, 60))
			continue
		}
		if cf.defaults != nil {
			in = MergeCaseInput(cf.defaults, in)
		}
		if _, p := BuildInput(in); p != nil {
			problem(path+".input", p)
			continue
		}
		c.Input = in
		e, set := cm["expect"]
		if !set {
			d.errorf("missing_key", at(path), path+".expect", "", "Add expect: with at least a decision.", "Case %q has no expect.", clipN(name, 60))
			continue
		}
		exp, p := parseExpect(e)
		if p != nil {
			problem(path+".expect", p)
			continue
		}
		c.Expect = exp
		cf.Cases = append(cf.Cases, c)
	}
	if d.hasErrors() {
		return nil
	}
	return cf
}

func parseExpect(v any) (Expect, *InputProblem) {
	var e Expect
	m, ok := v.(map[string]any)
	if !ok {
		return e, wrongType("expect", "a mapping", v)
	}
	if p := checkKeysIn("", "expect", m, expectKeys); p != nil {
		return e, p
	}
	dec, _ := m["decision"].(string)
	switch Outcome(dec) {
	case Allow, Deny, RequireApproval:
		e.Decision = Outcome(dec)
	default:
		p := &InputProblem{Path: "decision", Message: "expect needs a decision: allow, deny or require_approval."}
		if dec != "" {
			p.Message = fmt.Sprintf("%q isn't a decision. Use allow, deny or require_approval.", clipN(dec, 40))
			if s := suggest(dec, Outcomes); s != "" {
				p.Hint = fmt.Sprintf("Did you mean %q?", s)
			}
		}
		return e, p
	}
	opt := func(k string) (*string, *InputProblem) {
		x, set := m[k]
		if !set {
			return nil, nil
		}
		s, ok := x.(string)
		if !ok {
			return nil, wrongType(k, "a string", x)
		}
		return &s, nil
	}
	var p *InputProblem
	if e.Rule, p = opt("rule"); p != nil {
		return e, p
	}
	if e.Rule != nil && !ruleExpectRE.MatchString(*e.Rule) {
		return e, &InputProblem{Path: "rule", Message: fmt.Sprintf("%q isn't a rule id, default, stop or budget:<id>.", clipN(*e.Rule, 60))}
	}
	if e.Code, p = opt("code"); p != nil {
		return e, p
	}
	if e.ReasonContains, p = opt("reason_contains"); p != nil {
		return e, p
	}
	if e.Message, p = opt("message"); p != nil {
		return e, p
	}
	if e.Verify, p = opt("verify"); p != nil {
		return e, p
	}
	if e.Verify != nil {
		switch Level(*e.Verify) {
		case L0, L1, L2:
		default:
			return e, &InputProblem{Path: "verify", Message: "verify must be L0, L1 or L2."}
		}
	}
	if n, set := m["notify"]; set {
		list, ok := n.([]any)
		if !ok {
			return e, wrongType("notify", "a list", n)
		}
		e.notifySet = true
		e.Notify = []string{}
		for _, x := range list {
			s, ok := x.(string)
			if !ok {
				return e, wrongType("notify", "a list of channels", n)
			}
			e.Notify = append(e.Notify, s)
		}
	}
	if a, set := m["approval"]; set {
		am, ok := a.(map[string]any)
		if !ok {
			return e, wrongType("approval", "a mapping", a)
		}
		if p := checkKeysIn("approval", "expect.approval", am, expectApprKeys); p != nil {
			return e, p
		}
		ea := &ExpectApprov{}
		if x, set := am["required"]; set {
			n, p := wholeNumber("approval.required", x)
			if p != nil {
				return e, p
			}
			r := int(n)
			ea.Required = &r
		}
		if x, set := am["approvers"]; set {
			list, ok := x.([]any)
			if !ok {
				return e, wrongType("approval.approvers", "a list", x)
			}
			for _, y := range list {
				s, ok := y.(string)
				if !ok {
					return e, wrongType("approval.approvers", "a list of approvers", x)
				}
				ea.Approvers = append(ea.Approvers, s)
			}
			sort.Strings(ea.Approvers)
		}
		if x, set := am["expires_in"]; set {
			var secs int64
			switch y := x.(type) {
			case string:
				s, ok := parseDuration(y)
				if !ok {
					return e, &InputProblem{Path: "approval.expires_in", Message: fmt.Sprintf("%q isn't a duration.", clipN(y, 20)), Hint: "Write it like 30m, 2h or 24h."}
				}
				secs = s
			default:
				n, p := wholeNumber("approval.expires_in", x)
				if p != nil {
					return e, p
				}
				secs = n
			}
			ea.ExpiresIn = &secs
		}
		e.Approval = ea
	}
	return e, nil
}

// nodeValue turns a YAML node into a generic value and records each path's position.
func nodeValue(n *yaml.Node, path string, pos map[string]Pos) (any, error) {
	pos[path] = Pos{Line: n.Line, Col: n.Column}
	switch n.Kind {
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			kp := k
			if path != "" {
				kp = path + "." + k
			}
			v, err := nodeValue(n.Content[i+1], kp, pos)
			if err != nil {
				return nil, err
			}
			if n.Content[i+1].Kind == yaml.ScalarNode {
				// Point at the key: "Unknown key" and "must be" read best there.
				pos[kp] = Pos{Line: n.Content[i].Line, Col: n.Content[i].Column}
			} else {
				pos[kp+"#key"] = Pos{Line: n.Content[i].Line, Col: n.Content[i].Column}
			}
			m[k] = v
		}
		return m, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for i, c := range n.Content {
			v, err := nodeValue(c, fmt.Sprintf("%s[%d]", path, i), pos)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!null":
			return nil, nil
		case "!!bool":
			var b bool
			if err := n.Decode(&b); err != nil {
				return nil, fmt.Errorf("line %d: %q isn't true or false", n.Line, n.Value)
			}
			return b, nil
		case "!!int":
			var i int64
			if err := n.Decode(&i); err != nil {
				return nil, fmt.Errorf("line %d: %q is out of range", n.Line, n.Value)
			}
			return i, nil
		case "!!float":
			var f float64
			if err := n.Decode(&f); err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
				return nil, fmt.Errorf("line %d: %q isn't a usable number", n.Line, n.Value)
			}
			return f, nil
		}
		return n.Value, nil
	}
	return nil, fmt.Errorf("line %d: unsupported YAML", n.Line)
}

// TestOptions tune RunCases.
type TestOptions struct {
	Run      *regexp.Regexp // only cases whose name matches
	Deadline time.Duration  // per case; default 1 s (local and interactive, as policy evaluate)
}

// TestResult is one policy's run: `deedglass policy test --json` and POST /v1/policies/test.
type TestResult struct {
	Object      string       `json:"object"` // "policy_test_result"
	Policy      string       `json:"policy"` // the policy's name
	File        string       `json:"file,omitempty"`
	CasesFile   string       `json:"cases_file,omitempty"`
	VersionHash string       `json:"version_hash"`
	OK          bool         `json:"ok"` // every case passed (coverage is judged by the caller's options)
	Passed      int          `json:"passed"`
	Failed      int          `json:"failed"`
	Skipped     int          `json:"skipped"` // filtered out by Run
	Cases       []CaseResult `json:"cases"`
	Coverage    Coverage     `json:"coverage"`
	ElapsedUS   int64        `json:"elapsed_us"`
}

// CaseResult is one case.
type CaseResult struct {
	Name     string   `json:"name"`
	Line     int      `json:"line,omitzero"`
	Pass     bool     `json:"pass"`
	Expected Expect   `json:"expected"`
	Got      Got      `json:"got"`
	Problems []string `json:"problems,omitempty"` // "rule: expected block-destructive-sql, got allow-reads"
}

// Got is what the evaluation decided, in the expectation's terms.
type Got struct {
	Decision Outcome       `json:"decision"`
	Rule     string        `json:"rule"`
	Code     string        `json:"code,omitempty"`
	Reason   string        `json:"reason,omitempty"`
	Message  string        `json:"message"`
	Verify   string        `json:"verify,omitempty"`
	Approval *ApprovalSpec `json:"approval,omitempty"`
	Notify   []string      `json:"notify,omitempty"`
}

// Coverage is which rules decided at least one case, plus the default and every budget.
type Coverage struct {
	Rules     []RuleCoverage `json:"rules"`
	Covered   int            `json:"covered"` // rules that decided at least one case
	Total     int            `json:"total"`   // rules
	Uncovered []string       `json:"uncovered"`
	Default   int            `json:"default"` // cases the default decided
	Budgets   []RuleCoverage `json:"budgets"`
}

// RuleCoverage is how many cases a rule or budget decided.
type RuleCoverage struct {
	ID    string `json:"id"`
	Line  int    `json:"line,omitzero"`
	Cases int    `json:"cases"`
}

// GotRule names who decided, the way expect.rule does: a rule id, "default", "budget:<id>" or
// "stop" (and the builtin rule id for the builtin decisions).
func GotRule(d Decision) string {
	switch d.Source {
	case SourceDefault:
		return "default"
	case SourceStop:
		return "stop"
	case SourceBudget:
		if strings.HasPrefix(d.RuleID, "budget:") {
			return d.RuleID
		}
		return "budget:" + d.RuleID
	}
	return d.RuleID
}

// RunCases runs every case of cf against c.
func RunCases(ctx context.Context, c *Compiled, cf *CasesFile, o TestOptions) *TestResult {
	start := time.Now()
	if o.Deadline <= 0 {
		o.Deadline = time.Second
	}
	r := &TestResult{Object: "policy_test_result", Policy: c.Name, VersionHash: c.VersionHash, CasesFile: cf.File, Cases: []CaseResult{}}
	ruleCases := map[string]int{}
	budgetCases := map[string]int{}
	for _, cs := range cf.Cases {
		if o.Run != nil && !o.Run.MatchString(cs.Name) {
			r.Skipped++
			continue
		}
		in, p := BuildInput(cs.Input)
		cr := CaseResult{Name: cs.Name, Line: cs.Line, Expected: cs.Expect}
		if p != nil { // checked when parsing; kept for callers that build cases by hand
			cr.Problems = []string{"input: " + p.Message}
			r.Failed++
			r.Cases = append(r.Cases, cr)
			continue
		}
		in.Usage = FillLocalUsage(c, in.Usage)
		d := Evaluate(ctx, c, in, EvalOptions{Explain: true, Deadline: o.Deadline})
		g := Got{Decision: d.Outcome, Rule: GotRule(d), Code: string(d.Code), Reason: d.Reason, Message: d.Message,
			Verify: string(d.Verify), Approval: d.Approval, Notify: d.Notify}
		cr.Got = g
		cr.Problems = compare(cs.Expect, g)
		cr.Pass = len(cr.Problems) == 0
		if cr.Pass {
			r.Passed++
		} else {
			r.Failed++
		}
		switch d.Source {
		case SourceRule:
			ruleCases[d.RuleID]++
		case SourceDefault:
			r.Coverage.Default++
		case SourceBudget:
			budgetCases[strings.TrimPrefix(d.RuleID, "budget:")]++
		}
		r.Cases = append(r.Cases, cr)
	}
	for _, rule := range c.Rules {
		n := ruleCases[rule.ID]
		r.Coverage.Rules = append(r.Coverage.Rules, RuleCoverage{ID: rule.ID, Line: rule.Pos.Line, Cases: n})
		if n > 0 {
			r.Coverage.Covered++
		} else {
			r.Coverage.Uncovered = append(r.Coverage.Uncovered, rule.ID)
		}
	}
	r.Coverage.Total = len(c.Rules)
	if r.Coverage.Rules == nil {
		r.Coverage.Rules = []RuleCoverage{}
	}
	if r.Coverage.Uncovered == nil {
		r.Coverage.Uncovered = []string{}
	}
	r.Coverage.Budgets = []RuleCoverage{}
	for _, b := range c.Budgets {
		r.Coverage.Budgets = append(r.Coverage.Budgets, RuleCoverage{ID: b.ID, Line: b.Pos.Line, Cases: budgetCases[b.ID]})
	}
	for _, id := range []string{AgentCallsPerMinute, AgentCallsPerDay, AgentSpendPerDay} {
		if n := budgetCases[id]; n > 0 {
			r.Coverage.Budgets = append(r.Coverage.Budgets, RuleCoverage{ID: id, Cases: n})
		}
	}
	r.OK = r.Failed == 0
	r.ElapsedUS = time.Since(start).Microseconds()
	return r
}

// BudgetsCovered counts the policy's budgets that decided at least one case.
func (r *TestResult) BudgetsCovered(c *Compiled) int {
	n := 0
	for _, b := range r.Coverage.Budgets {
		if b.Cases > 0 && slices.ContainsFunc(c.Budgets, func(x Budget) bool { return x.ID == b.ID }) {
			n++
		}
	}
	return n
}

func compare(e Expect, g Got) []string {
	var out []string
	if e.Decision != g.Decision {
		out = append(out, fmt.Sprintf("decision: expected %s, got %s", e.Decision, g.Decision))
	}
	if e.Rule != nil && *e.Rule != g.Rule {
		out = append(out, fmt.Sprintf("rule: expected %s, got %s", *e.Rule, orNone(g.Rule)))
	}
	if e.Code != nil && *e.Code != g.Code {
		out = append(out, fmt.Sprintf("code: expected %s, got %s", orNone(*e.Code), orNone(g.Code)))
	}
	if e.ReasonContains != nil && !strings.Contains(g.Reason, *e.ReasonContains) && !strings.Contains(g.Message, *e.ReasonContains) {
		out = append(out, fmt.Sprintf("reason: expected it to contain %q, got %q", *e.ReasonContains, g.Reason))
	}
	if e.Message != nil && *e.Message != g.Message {
		out = append(out, fmt.Sprintf("message: expected %q, got %q", *e.Message, g.Message))
	}
	if e.Verify != nil && *e.Verify != g.Verify {
		out = append(out, fmt.Sprintf("verify: expected %s, got %s", *e.Verify, orNone(g.Verify)))
	}
	if e.notifySet {
		want, got := slices.Clone(e.Notify), slices.Clone(g.Notify)
		sort.Strings(want)
		sort.Strings(got)
		if !slices.Equal(want, got) {
			out = append(out, fmt.Sprintf("notify: expected [%s], got [%s]", strings.Join(want, ", "), strings.Join(got, ", ")))
		}
	}
	if a := e.Approval; a != nil {
		if g.Approval == nil {
			out = append(out, "approval: expected one, got none")
		} else {
			if a.Required != nil && *a.Required != g.Approval.Required {
				out = append(out, fmt.Sprintf("approval.required: expected %d, got %d", *a.Required, g.Approval.Required))
			}
			if a.Approvers != nil && !slices.Equal(a.Approvers, g.Approval.Approvers) {
				out = append(out, fmt.Sprintf("approval.approvers: expected [%s], got [%s]", strings.Join(a.Approvers, ", "), strings.Join(g.Approval.Approvers, ", ")))
			}
			if a.ExpiresIn != nil && *a.ExpiresIn != g.Approval.ExpiresIn {
				out = append(out, fmt.Sprintf("approval.expires_in: expected %s, got %s", secondsWords(*a.ExpiresIn), secondsWords(g.Approval.ExpiresIn)))
			}
		}
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func secondsWords(s int64) string {
	switch {
	case s%3600 == 0 && s > 0:
		return strconv.FormatInt(s/3600, 10) + "h"
	case s%60 == 0 && s > 0:
		return strconv.FormatInt(s/60, 10) + "m"
	}
	return strconv.FormatInt(s, 10) + "s"
}
