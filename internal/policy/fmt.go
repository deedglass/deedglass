package policy

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// ErrNotFormatted is returned by Format for a file that doesn't compile: fmt never changes meaning,
// so it refuses anything it can't check.
var ErrNotFormatted = errors.New("policy: the file has errors, so it was not formatted")

const (
	fmtFlowWidth = 100 // match, approval and short lists go on one line when they fit
	fmtWhenWidth = 80  // a longer `when` becomes a folded block
)

var keyOrder = map[string][]string{
	"":         topKeys,
	"defaults": defaultsKeys,
	"budget":   budgetKeys,
	"rule":     ruleKeys,
	"match":    matchKeys,
	"approval": approvalKeys,
}

// Format rewrites a valid policy into the canonical layout (docs/POLICY_LANGUAGE.md §11), keeping
// comments. The result compiles to the same version_hash, or Format returns an error.
//
// Format is idempotent: it repeats the one-pass layout until the text stops changing (at most
// fmtPasses times). One pass is not always a fixed point: the YAML emitter can drop a comment that
// is glued to a flow collection (`approvers: []#0`, FuzzCompile seed c7b361a4d00f9c49), and the
// next pass then lays the same mapping out in flow style.
func Format(src []byte) ([]byte, error) {
	out, err := formatOnce(src)
	for i := 1; err == nil && i < fmtPasses; i++ {
		again, err2 := formatOnce(out)
		if err2 != nil || bytes.Equal(again, out) {
			return out, err2
		}
		out = again
	}
	return out, err
}

// fmtPasses bounds Format's passes; real files settle in one, the comment edge case in two.
const fmtPasses = 4

func formatOnce(src []byte) ([]byte, error) {
	c, _ := Compile(src, Options{})
	if c == nil {
		return nil, ErrNotFormatted
	}
	s := newSource(src)
	var doc yaml.Node
	if err := yaml.Unmarshal(s.text, &doc); err != nil {
		return nil, ErrNotFormatted
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	f := formatter{}
	f.mapping(root, "", 0)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("policy: fmt: %w", err)
	}
	_ = enc.Close()
	out := spaceOut(wrapWhens(buf.Bytes()))
	c2, diags := Compile(out, Options{})
	if c2 == nil || c2.VersionHash != c.VersionHash {
		return nil, fmt.Errorf("policy: fmt changed the policy's meaning (an internal error; %d diagnostics)", len(diags))
	}
	return out, nil
}

type formatter struct{}

// mapping reorders keys and sets styles. kind names the mapping's role for key order.
func (f formatter) mapping(n *yaml.Node, kind string, indent int) {
	if n.Kind != yaml.MappingNode {
		return
	}
	n.Style &^= yaml.FlowStyle
	if order, ok := keyOrder[kind]; ok {
		// The comment above a file's first key is the file's own comment: it stays on top.
		var top string
		if kind == "" && len(n.Content) > 0 {
			top, n.Content[0].HeadComment = n.Content[0].HeadComment, ""
		}
		reorder(n, order)
		if top != "" && len(n.Content) > 0 {
			n.Content[0].HeadComment = strings.TrimSpace(top + "\n" + n.Content[0].HeadComment)
		}
	}
	for _, kv := range pairs(n) {
		k, v := kv.Key, kv.Value
		if v.Kind != yaml.ScalarNode && v.LineComment != "" {
			// A comment after a collection belongs to its key; left on the value, the emitter moves it.
			k.LineComment = strings.TrimSpace(strings.TrimSpace(k.LineComment) + " " + v.LineComment)
			v.LineComment = ""
		}
		switch {
		case kind == "" && (k.Value == "budgets" || k.Value == "rules") && v.Kind == yaml.SequenceNode:
			v.Style &^= yaml.FlowStyle
			for _, item := range v.Content {
				role := "rule"
				if k.Value == "budgets" {
					role = "budget"
				}
				f.mapping(item, role, indent+4)
			}
		case kind == "" && k.Value == "defaults":
			f.mapping(v, "defaults", indent+2)
		case k.Value == "match" || k.Value == "approval":
			f.mapping(v, k.Value, indent+2)
			for _, c := range v.Content {
				if c.Kind == yaml.SequenceNode {
					c.Style |= yaml.FlowStyle
				}
				plainScalars(c)
			}
			if fitsFlow(v, indent+len(k.Value)+2) {
				v.Style |= yaml.FlowStyle
			}
		case v.Kind == yaml.SequenceNode && (k.Value == "notify" || k.Value == "approvers"):
			plainScalars(v)
			if fitsFlow(v, indent+len(k.Value)+2) {
				v.Style |= yaml.FlowStyle
			}
		case k.Value == "when" || k.Value == "measure":
			whenStyle(v)
		case k.Value == "max_spend" && v.Kind == yaml.ScalarNode:
			if m, prob := ParseMoney(v.Value); prob == nil {
				v.Value = lowerCurrency(v.Value, m)
				v.Style = yaml.DoubleQuotedStyle
				v.Tag = "!!str"
			}
		}
	}
}

// plainScalars lets the emitter choose quoting for string values (plain when YAML allows it), so
// equal values are always written the same way. Mapping keys and non-string scalars are kept.
func plainScalars(n *yaml.Node) {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.ShortTag() == "!!str" && n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 && !hasComments(n) {
			n.Style, n.Tag = 0, "!!str"
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			plainScalars(c)
		}
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			plainScalars(n.Content[i])
		}
	}
}

func lowerCurrency(s string, m Money) string {
	if i := strings.LastIndexByte(s, ' '); i >= 0 {
		return s[:i+1] + m.Currency
	}
	return s
}

// reorder puts known keys in canonical order, keeping any others after them.
func reorder(n *yaml.Node, order []string) {
	rank := map[string]int{}
	for i, k := range order {
		rank[k] = i
	}
	ps := pairs(n)
	out := make([]*yaml.Node, 0, len(n.Content))
	for _, k := range order {
		for _, kv := range ps {
			if kv.Key.Value == k {
				out = append(out, kv.Key, kv.Value)
			}
		}
	}
	for _, kv := range ps {
		if _, ok := rank[kv.Key.Value]; !ok {
			out = append(out, kv.Key, kv.Value)
		}
	}
	n.Content = out
}

// fitsFlow reports whether a collection fits on one line in flow style, with no comments inside.
func fitsFlow(n *yaml.Node, col int) bool {
	if hasComments(n) {
		return false
	}
	cp := *n
	cp.Style |= yaml.FlowStyle
	b, err := yaml.Marshal(&cp)
	if err != nil {
		return false
	}
	line := strings.TrimRight(string(b), "\n")
	return !strings.Contains(line, "\n") && col+utf8.RuneCountInString(line) <= fmtFlowWidth
}

func hasComments(n *yaml.Node) bool {
	if n.HeadComment != "" || n.LineComment != "" || n.FootComment != "" {
		return true
	}
	for _, c := range n.Content {
		if hasComments(c) {
			return true
		}
	}
	return false
}

// whenStyle: plain when YAML allows it, single quotes when it doesn't, a folded block over 80
// characters. A value that already spans lines keeps its block style.
func whenStyle(n *yaml.Node) {
	if n.Kind != yaml.ScalarNode {
		return
	}
	if strings.Contains(n.Value, "\n") {
		// A folded block whose value has a more-indented line can't be re-emitted folded without
		// gaining a blank line each pass (FuzzCompile, S25): write it literal, which keeps the value
		// byte for byte.
		if n.Style&yaml.FoldedStyle != 0 && moreIndented(n.Value) {
			n.Style = yaml.LiteralStyle
		}
		return
	}
	n.Tag = "!!str"
	switch {
	case utf8.RuneCountInString(n.Value) > fmtWhenWidth:
		n.Style = yaml.FoldedStyle
	case plainSafe(n.Value):
		n.Style = 0
	default:
		n.Style = yaml.SingleQuotedStyle
	}
}

// moreIndented reports whether a line after the first starts with a space or a tab.
func moreIndented(v string) bool {
	for _, l := range strings.Split(v, "\n")[1:] {
		if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") {
			return true
		}
	}
	return false
}

// plainSafe reports whether YAML would write s as a plain scalar that reads back as the same string.
func plainSafe(s string) bool {
	b, err := yaml.Marshal(map[string]string{"k": s})
	if err != nil {
		return false
	}
	return string(b) == "k: "+s+"\n"
}

// wrapWhens breaks the one long line of each folded `when` at single spaces, so it reads in 80
// columns. Folding joins the lines with single spaces again, so the value is unchanged.
func wrapWhens(b []byte) []byte {
	lines := strings.Split(string(b), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		out = append(out, l)
		t := strings.TrimLeft(strings.TrimPrefix(strings.TrimLeft(l, " "), "- "), " ")
		if (t == "when: >-" || t == "measure: >-") && i+1 < len(lines) {
			body := lines[i+1]
			ind := len(body) - len(strings.TrimLeft(body, " "))
			out = append(out, wrapLine(body[ind:], strings.Repeat(" ", ind), fmtWhenWidth)...)
			i++
		}
	}
	return []byte(strings.Join(out, "\n"))
}

func wrapLine(s, indent string, width int) []string {
	var out []string
	r := []rune(s)
	for len(r) > 0 {
		if len(indent)+len(r) <= width {
			out = append(out, indent+string(r))
			break
		}
		cut := -1
		for i := min(len(r)-2, width-len(indent)); i > 0; i-- {
			if r[i] == ' ' && r[i-1] != ' ' && r[i+1] != ' ' {
				cut = i
				break
			}
		}
		if cut < 0 {
			for i := width - len(indent); i < len(r)-1; i++ {
				if r[i] == ' ' && r[i-1] != ' ' && r[i+1] != ' ' {
					cut = i
					break
				}
			}
		}
		if cut < 0 {
			out = append(out, indent+string(r))
			break
		}
		out = append(out, indent+string(r[:cut]))
		r = r[cut+1:]
	}
	return out
}

// spaceOut adds a blank line before `defaults`, `budgets` and `rules`, and between list items of
// budgets and rules (before any comment lines that belong to the item).
func spaceOut(b []byte) []byte {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	var out []string
	section := ""
	items := 0
	blankBefore := func() {
		// Insert the blank line before the run of comment lines just added.
		j := len(out)
		for j > 0 && strings.HasPrefix(strings.TrimLeft(out[j-1], " "), "#") {
			j--
		}
		if j > 0 && out[j-1] != "" {
			out = append(out[:j], append([]string{""}, out[j:]...)...)
		}
	}
	for _, l := range lines {
		top := len(l) > 0 && l[0] != ' ' && l[0] != '#' && l[0] != '-'
		if top {
			key := strings.SplitN(l, ":", 2)[0]
			section, items = key, 0
			out = append(out, l)
			if key == "defaults" || key == "budgets" || key == "rules" {
				last := out[len(out)-1]
				out = out[:len(out)-1]
				blankBefore()
				out = append(out, last)
			}
			continue
		}
		if (section == "budgets" || section == "rules") && strings.HasPrefix(l, "  - ") {
			items++
			if items > 1 {
				blankBefore()
			}
		}
		out = append(out, l)
	}
	return []byte(strings.Join(out, "\n") + "\n")
}
