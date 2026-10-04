package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Patch edits a policy's text the way a careful person would (S28 §3.3): it splices whole lines
// into the original bytes at positions read from the YAML tree, so every comment, blank line and
// style choice outside the edit survives byte for byte, and a diff shows exactly what changed.
// It never re-emits the file. The result must still compile with every base rule intact
// (CheckPatch); callers drop a proposal whose patch fails either step.
//
// Line endings: a file written with CRLF throughout keeps CRLF on the spliced lines (the rest is
// untouched); a file that mixes LF and CRLF is refused, because a splice would change which one a
// reader sees.

// PatchOp is one edit.
type PatchOp interface{ isPatchOp() }

// InsertRuleBefore adds Rule just above the rule RuleID (above its leading comments), so the new
// rule is checked first.
type InsertRuleBefore struct {
	RuleID string
	Rule   RuleSpec
}

// AppendRule adds Rule after the last rule.
type AppendRule struct{ Rule RuleSpec }

// SetRuleField sets one key of rule RuleID. Only verify is supported today (S28's
// tighten.verify_level).
type SetRuleField struct {
	RuleID, Key string
	Value       any
}

// SetBudgetLimit changes a budget's max_calls or max_spend (exactly one is set).
type SetBudgetLimit struct {
	BudgetID string
	MaxCalls *int64
	MaxSpend *Money
}

func (InsertRuleBefore) isPatchOp() {}
func (AppendRule) isPatchOp()       {}
func (SetRuleField) isPatchOp()     {}
func (SetBudgetLimit) isPatchOp()   {}

// RuleSpec is a rule to write. It renders in S10's canonical key order (id, description, match,
// when, decision, reason, approval, notify, verify) with match on one line in flow style.
type RuleSpec struct {
	Comment     []string // comment lines written above the rule, without "# "
	ID          string
	Description string
	Match       MatchSpec
	When        string // CEL, written single-quoted; "" for none
	Decision    Outcome
	Reason      string
	Approval    *ApprovalYAML
	Notify      []string
	Verify      Level
}

// MatchSpec is a rule's match block as written.
type MatchSpec struct {
	Tool          []string
	ConnectorType []string
	SideEffect    []string
	Agent         []string
}

// ApprovalYAML is an approval block as written (expires_in as a duration string, like "4h").
type ApprovalYAML struct {
	Approvers []string
	Required  int
	ExpiresIn string
}

// PatchError says why a patch was refused.
type PatchError struct{ Reason string }

func (e *PatchError) Error() string { return "policy: patch: " + e.Reason }

func patchErr(format string, a ...any) error { return &PatchError{Reason: fmt.Sprintf(format, a...)} }

// ErrPatchMixedEOL is a file that mixes LF and CRLF line endings.
var ErrPatchMixedEOL = &PatchError{Reason: "the file mixes LF and CRLF line endings; run deedglass policy fmt and push it again"}

// Patch applies ops in order and returns the new source.
func Patch(src []byte, ops ...PatchOp) ([]byte, error) {
	if len(src) > MaxSourceBytes {
		return nil, patchErr("the file is larger than %d bytes", MaxSourceBytes)
	}
	out := src
	for _, op := range ops {
		var err error
		if out, err = patchOne(out, op); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// doc is a source split for splicing.
type doc struct {
	bom   bool
	eol   string   // "\n" or "\r\n"
	lines []string // without line endings
	final bool     // the text ended with a line ending
	root  *yaml.Node
}

func parseDoc(src []byte) (*doc, error) {
	text := bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))
	d := &doc{bom: len(text) != len(src), eol: "\n"}
	n, crlf := bytes.Count(text, []byte("\n")), bytes.Count(text, []byte("\r\n"))
	switch {
	case crlf > 0 && crlf == n:
		d.eol = "\r\n"
	case crlf > 0:
		return nil, ErrPatchMixedEOL
	}
	s := string(text)
	d.final = strings.HasSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\n")
	if s != "" || d.final {
		d.lines = strings.Split(s, "\n")
	}
	for i, l := range d.lines {
		d.lines[i] = strings.TrimSuffix(l, "\r")
	}
	var root yaml.Node
	if err := yaml.Unmarshal(text, &root); err != nil {
		return nil, patchErr("the file isn't valid YAML")
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, patchErr("the file isn't one YAML mapping")
	}
	d.root = root.Content[0]
	return d, nil
}

func (d *doc) bytes() []byte {
	var b bytes.Buffer
	if d.bom {
		b.WriteString("\xef\xbb\xbf")
	}
	for i, l := range d.lines {
		b.WriteString(l)
		if i < len(d.lines)-1 || d.final {
			b.WriteString(d.eol)
		}
	}
	return b.Bytes()
}

// insert puts block before 0-based line index at.
func (d *doc) insert(at int, block []string) {
	d.lines = slices.Insert(d.lines, at, block...)
}

func mapValue(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// seq is a block sequence under a top-level key, with its items' line spans.
type seq struct {
	node   *yaml.Node
	indent int // the column of each item's "-" (0-based)
	items  []item
}

type item struct {
	node        *yaml.Node
	id          string
	first, last int // 0-based lines: the "-" line, the last content line
	head        int // 0-based line where the item's own leading comments start (≤ first)
}

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

func isBlank(l string) bool { return strings.TrimSpace(l) == "" }

func isComment(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "#") }

// blockSeq reads the block sequence under top-level key (rules or budgets).
func (d *doc) blockSeq(key string) (*seq, error) {
	_, v := mapValue(d.root, key)
	if v == nil {
		return nil, patchErr("the file has no %s list", key)
	}
	if v.Kind == yaml.ScalarNode && (v.Tag == "!!null" || v.Value == "") {
		return nil, patchErr("the %s list is empty; write it as a block list first", key)
	}
	if v.Kind != yaml.SequenceNode {
		return nil, patchErr("%s isn't a list", key)
	}
	if v.Style&yaml.FlowStyle != 0 {
		return nil, patchErr("%s is written in flow style ([…]); run deedglass policy fmt so each entry is on its own lines", key)
	}
	s := &seq{node: v, indent: -1}
	for _, it := range v.Content {
		if it.Kind != yaml.MappingNode {
			return nil, patchErr("an entry of %s isn't a mapping", key)
		}
		first := it.Line - 1
		if first < 0 || first >= len(d.lines) {
			return nil, patchErr("an entry of %s has no position", key)
		}
		line := d.lines[first]
		dash := strings.LastIndex(line[:min(len(line), it.Column-1)], "-")
		if dash < 0 || strings.TrimSpace(line[:dash]) != "" {
			return nil, patchErr("an entry of %s doesn't start its own line; run deedglass policy fmt", key)
		}
		if s.indent >= 0 && dash != s.indent {
			return nil, patchErr("the entries of %s aren't aligned", key)
		}
		s.indent = dash
		_, idn := mapValue(it, "id")
		x := item{node: it, first: first, last: first}
		if idn != nil {
			x.id = idn.Value
		}
		s.items = append(s.items, x)
	}
	for i := range s.items {
		it := &s.items[i]
		stop := len(d.lines)
		if i+1 < len(s.items) {
			stop = s.items[i+1].first
		}
		for j := it.first + 1; j < stop; j++ {
			l := d.lines[j]
			if isBlank(l) {
				continue
			}
			if indentOf(l) <= s.indent {
				break
			}
			it.last = j
		}
		// Leading comments: the unbroken run of comment lines at the item's own indent just above it.
		it.head = it.first
		lo := 0
		if i > 0 {
			lo = s.items[i-1].last + 1
		}
		for j := it.first - 1; j >= lo; j-- {
			l := d.lines[j]
			if !isComment(l) || indentOf(l) != s.indent {
				break
			}
			it.head = j
		}
	}
	return s, nil
}

// paragraphs reports whether the list separates its entries with blank lines.
func (d *doc) paragraphs(s *seq) bool {
	for i := 1; i < len(s.items); i++ {
		if h := s.items[i].head; h > 0 && isBlank(d.lines[h-1]) {
			return true
		}
	}
	return false
}

func (s *seq) find(id string) (*item, bool) {
	for i := range s.items {
		if s.items[i].id == id {
			return &s.items[i], true
		}
	}
	return nil, false
}

func patchOne(src []byte, op PatchOp) ([]byte, error) {
	d, err := parseDoc(src)
	if err != nil {
		return nil, err
	}
	switch o := op.(type) {
	case InsertRuleBefore:
		s, err := d.blockSeq("rules")
		if err != nil {
			return nil, err
		}
		it, ok := s.find(o.RuleID)
		if !ok {
			return nil, patchErr("no rule %q", o.RuleID)
		}
		block, err := renderRule(o.Rule, s.indent)
		if err != nil {
			return nil, err
		}
		// Keep the file's rhythm: in a list whose rules are separated by blank lines, the new
		// rule is its own paragraph too.
		if d.paragraphs(s) {
			block = append(block, "")
		}
		d.insert(it.head, block)
	case AppendRule:
		s, err := d.blockSeq("rules")
		if err != nil {
			return nil, err
		}
		last := s.items[len(s.items)-1]
		block, err := renderRule(o.Rule, s.indent)
		if err != nil {
			return nil, err
		}
		if d.paragraphs(s) {
			block = append([]string{""}, block...)
		}
		d.insert(last.last+1, block)
	case SetRuleField:
		if o.Key != "verify" {
			return nil, patchErr("only verify can be set on a rule, not %q", o.Key)
		}
		lv, ok := o.Value.(Level)
		if !ok {
			if str, ok2 := o.Value.(string); ok2 {
				lv = Level(str)
			}
		}
		if lv != L0 && lv != L1 && lv != L2 {
			return nil, patchErr("verify must be L0, L1 or L2")
		}
		s, err := d.blockSeq("rules")
		if err != nil {
			return nil, err
		}
		it, ok := s.find(o.RuleID)
		if !ok {
			return nil, patchErr("no rule %q", o.RuleID)
		}
		if err := d.setKey(it, "verify", string(lv)); err != nil {
			return nil, err
		}
	case SetBudgetLimit:
		if (o.MaxCalls == nil) == (o.MaxSpend == nil) {
			return nil, patchErr("set exactly one of max_calls and max_spend")
		}
		s, err := d.blockSeq("budgets")
		if err != nil {
			return nil, err
		}
		it, ok := s.find(o.BudgetID)
		if !ok {
			return nil, patchErr("no budget %q", o.BudgetID)
		}
		if o.MaxCalls != nil {
			if *o.MaxCalls < 1 {
				return nil, patchErr("max_calls must be at least 1")
			}
			err = d.setKey(it, "max_calls", strconv.FormatInt(*o.MaxCalls, 10))
		} else {
			if o.MaxSpend.Amount < 1 {
				return nil, patchErr("max_spend must be more than zero")
			}
			err = d.setKey(it, "max_spend", `"`+o.MaxSpend.String()+`"`)
		}
		if err != nil {
			return nil, err
		}
	default:
		return nil, patchErr("unknown edit %T", op)
	}
	return d.bytes(), nil
}

// setKey replaces the value of key in a block-mapping item, or adds `key: value` as the item's last
// line (verify and the budget limits sit last in canonical order, or near it).
func (d *doc) setKey(it *item, key, value string) error {
	if it.node.Style&yaml.FlowStyle != 0 {
		return patchErr("entry %q is written in flow style ({…}); run deedglass policy fmt", it.id)
	}
	k, v := mapValue(it.node, key)
	if k == nil {
		if len(it.node.Content) == 0 {
			return patchErr("entry %q is empty", it.id)
		}
		col := it.node.Content[0].Column - 1 // the column of the item's keys
		d.insert(it.last+1, []string{strings.Repeat(" ", col) + key + ": " + value})
		return nil
	}
	if v.Kind != yaml.ScalarNode || v.Line-1 >= len(d.lines) {
		return patchErr("%s of %q isn't a plain value", key, it.id)
	}
	i := v.Line - 1
	l := d.lines[i]
	start := v.Column - 1
	if start > len(l) {
		return patchErr("%s of %q has no position", key, it.id)
	}
	end := start
	switch {
	case v.Style&yaml.DoubleQuotedStyle != 0 || v.Style&yaml.SingleQuotedStyle != 0:
		q := l[start]
		end = start + 1
		for end < len(l) {
			if l[end] == q {
				if q == '\'' && end+1 < len(l) && l[end+1] == '\'' {
					end += 2
					continue
				}
				if q == '"' && l[end-1] == '\\' {
					end++
					continue
				}
				end++
				break
			}
			end++
		}
	case v.Style == 0 || v.Style&yaml.TaggedStyle != 0:
		rest := l[start:]
		if c := strings.Index(rest, " #"); c >= 0 {
			rest = rest[:c]
		}
		end = start + len(strings.TrimRight(rest, " \t"))
	default:
		return patchErr("%s of %q is a block scalar; write it on one line", key, it.id)
	}
	d.lines[i] = l[:start] + value + l[end:]
	return nil
}

// --- rendering ------------------------------------------------------------------------------------

var patchPlain = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 .,()/_+$%€£₹'’-]*[A-Za-z0-9.)%]$`)

// yamlString writes s as a plain scalar when that reads back as the same string, else
// double-quoted (JSON escapes are valid YAML).
func yamlString(s string) string {
	low := strings.ToLower(s)
	switch low {
	case "true", "false", "yes", "no", "on", "off", "null", "y", "n", "~":
		return strconv.Quote(s)
	}
	if patchPlain.MatchString(s) && !strings.Contains(s, ": ") && !strings.Contains(s, " #") {
		return s
	}
	b, _ := json.Marshal(s)
	return string(b)
}

var idSafe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// flowItem writes one item of a flow list or a match value: plain when it is a simple token.
var flowSafe = regexp.MustCompile(`^[A-Za-z0-9_.*:@/-]+$`)

func flowItem(s string) string {
	if flowSafe.MatchString(s) && !strings.HasPrefix(s, "*") && !strings.HasPrefix(s, "@") {
		return s
	}
	b, _ := json.Marshal(s)
	return string(b)
}

func flowValue(xs []string) string {
	if len(xs) == 1 {
		return flowItem(xs[0])
	}
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = flowItem(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func flowList(xs []string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = flowItem(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// renderRule renders r as block lines with the "-" at column indent.
func renderRule(r RuleSpec, indent int) ([]string, error) {
	if !idSafe.MatchString(r.ID) || len(r.ID) > 64 {
		return nil, patchErr("rule id %q isn't a valid id", r.ID)
	}
	switch r.Decision {
	case Allow, Deny, RequireApproval:
	default:
		return nil, patchErr("rule %s has no decision", r.ID)
	}
	if strings.ContainsAny(r.When, "\n\r") || strings.ContainsAny(r.Description, "\n\r") || strings.ContainsAny(r.Reason, "\n\r") {
		return nil, patchErr("rule %s has a line break in a value", r.ID)
	}
	pad := strings.Repeat(" ", indent)
	key := pad + "  "
	var out []string
	for _, c := range r.Comment {
		if strings.ContainsAny(c, "\n\r") {
			return nil, patchErr("a comment has a line break")
		}
		out = append(out, pad+"# "+c)
	}
	out = append(out, pad+"- id: "+r.ID)
	if r.Description != "" {
		out = append(out, key+"description: "+yamlString(r.Description))
	}
	var m []string
	if len(r.Match.Tool) > 0 {
		m = append(m, "tool: "+flowValue(r.Match.Tool))
	}
	if len(r.Match.ConnectorType) > 0 {
		m = append(m, "connector_type: "+flowValue(r.Match.ConnectorType))
	}
	if len(r.Match.SideEffect) > 0 {
		m = append(m, "side_effect: "+flowValue(r.Match.SideEffect))
	}
	if len(r.Match.Agent) > 0 {
		m = append(m, "agent: "+flowList(r.Match.Agent))
	}
	if len(m) > 0 {
		out = append(out, key+"match: { "+strings.Join(m, ", ")+" }")
	}
	if r.When != "" {
		out = append(out, key+"when: '"+strings.ReplaceAll(r.When, "'", "''")+"'")
	}
	out = append(out, key+"decision: "+string(r.Decision))
	if r.Reason != "" {
		out = append(out, key+"reason: "+yamlString(r.Reason))
	}
	if r.Approval != nil {
		var a []string
		if len(r.Approval.Approvers) > 0 {
			a = append(a, "approvers: "+flowList(r.Approval.Approvers))
		}
		if r.Approval.Required > 0 {
			a = append(a, "required: "+strconv.Itoa(r.Approval.Required))
		}
		if r.Approval.ExpiresIn != "" {
			a = append(a, "expires_in: "+flowItem(r.Approval.ExpiresIn))
		}
		out = append(out, key+"approval: { "+strings.Join(a, ", ")+" }")
	}
	if len(r.Notify) > 0 {
		out = append(out, key+"notify: "+flowList(r.Notify))
	}
	if r.Verify != "" {
		out = append(out, key+"verify: "+string(r.Verify))
	}
	return out, nil
}

// --- the structural check -----------------------------------------------------------------------------

// ErrPatchChanged is a patch whose compiled result changed something it shouldn't have.
var ErrPatchChanged = errors.New("policy: the patched policy changed more than the edit")

// CheckPatch compares the compiled base and candidate: everything must be equal except what ops
// name. Inserted rules must appear exactly where they were put, every base rule must keep its
// order and its normalized form, and nothing else (name, defaults, other budgets) may move.
func CheckPatch(base, cand *Compiled, ops ...PatchOp) error {
	var b, c normDoc
	if base == nil || cand == nil || json.Unmarshal(base.Normalized, &b) != nil || json.Unmarshal(cand.Normalized, &c) != nil {
		return ErrPatchChanged
	}
	want := b
	want.Rules = slices.Clone(b.Rules)
	want.Budgets = slices.Clone(b.Budgets)
	inserted := map[string]bool{}
	for _, op := range ops {
		switch o := op.(type) {
		case InsertRuleBefore:
			i := slices.IndexFunc(want.Rules, func(r normRule) bool { return r.ID == o.RuleID })
			j := slices.IndexFunc(c.Rules, func(r normRule) bool { return r.ID == o.Rule.ID })
			if i < 0 || j < 0 {
				return ErrPatchChanged
			}
			want.Rules = slices.Insert(want.Rules, i, c.Rules[j])
			inserted[o.Rule.ID] = true
		case AppendRule:
			j := slices.IndexFunc(c.Rules, func(r normRule) bool { return r.ID == o.Rule.ID })
			if j < 0 {
				return ErrPatchChanged
			}
			want.Rules = append(want.Rules, c.Rules[j])
			inserted[o.Rule.ID] = true
		case SetRuleField:
			i := slices.IndexFunc(want.Rules, func(r normRule) bool { return r.ID == o.RuleID })
			if i < 0 {
				return ErrPatchChanged
			}
			lv, _ := o.Value.(Level)
			if s, ok := o.Value.(string); ok {
				lv = Level(s)
			}
			want.Rules[i].Verify = lv
		case SetBudgetLimit:
			i := slices.IndexFunc(want.Budgets, func(x normBudget) bool { return x.ID == o.BudgetID })
			if i < 0 {
				return ErrPatchChanged
			}
			nb := want.Budgets[i]
			if o.MaxCalls != nil {
				v := *o.MaxCalls
				nb.MaxCalls, nb.MaxSpend = &v, nil
			} else {
				m := *o.MaxSpend
				nb.MaxSpend, nb.MaxCalls = &m, nil
			}
			want.Budgets[i] = nb
		}
	}
	// An inserted rule's own content is the candidate's (checked by compiling it); everything
	// else must match the base exactly.
	if !reflect.DeepEqual(want, c) {
		return ErrPatchChanged
	}
	return nil
}

// RuleIDs lists a compiled policy's rule ids in order.
func RuleIDs(c *Compiled) []string {
	out := make([]string, len(c.Rules))
	for i, r := range c.Rules {
		out[i] = r.ID
	}
	return out
}

// UniqueRuleID returns base, or base-2, base-3, … when the id is taken in c.
func UniqueRuleID(c *Compiled, base string) string {
	taken := map[string]bool{}
	for _, r := range c.Rules {
		taken[r.ID] = true
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		id := base + "-" + strconv.Itoa(n)
		if !taken[id] {
			return id
		}
	}
}
