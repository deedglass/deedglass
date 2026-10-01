package policy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// source is a policy file prepared for reading and for quoting in diagnostics.
type source struct {
	raw   []byte   // as given
	text  []byte   // BOM removed
	lines []string // text split on \n, without \r
}

func newSource(raw []byte) *source {
	text := bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	lines := strings.Split(string(text), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return &source{raw: raw, text: text, lines: lines}
}

// line returns 1-based line n, or "".
func (s *source) line(n int) string {
	if n < 1 || n > len(s.lines) {
		return ""
	}
	return s.lines[n-1]
}

var coreTags = map[string]bool{"!!str": true, "!!int": true, "!!float": true, "!!bool": true,
	"!!null": true, "!!map": true, "!!seq": true}

var yamlLineRE = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)

// readYAML runs the size check and the YAML refusals, and returns the root node (a mapping) when
// the file can be read as exactly one safe document. Any problem here stops the later stages.
func readYAML(src *source, d *diags) *yaml.Node {
	if n := len(src.raw); n > MaxSourceBytes {
		d.errorf("file_too_large", span{}, "", "", "Split it into smaller policies, or replace repeated rules with globs.",
			"This policy is %s KiB; the limit is %s KiB.", thousands((n+1023)/1024), thousands(MaxSourceBytes/1024))
		return nil
	}
	if !utf8.Valid(src.text) {
		ln := 1 + bytes.Count(src.text[:firstInvalidUTF8(src.text)], []byte("\n"))
		d.errorf("yaml_syntax", span{Start: Pos{ln, 1}}, "", "", "Save the file as UTF-8.",
			"Line %d is not valid UTF-8.", ln)
		return nil
	}
	for i, l := range src.lines {
		for j, r := range []rune(l) {
			if r == '\t' || r == '\r' {
				continue
			}
			if r < 0x20 || r == 0x7f {
				d.errorf("control_character", span{Start: Pos{i + 1, j + 1}, End: Pos{i + 1, j + 2}}, "", "",
					"Remove it. It can make text display differently from what it says.",
					"Line %d contains a control character (U+%04X).", i+1, r)
				return nil
			}
		}
	}
	if len(bytes.TrimSpace(stripComments(src.text))) == 0 {
		d.errorf("missing_marker", span{}, "", "", "Start the file with `deedglass: policy/v1`, then add `name` and `defaults`.",
			"This file is empty.")
		return nil
	}
	if tabs := tabIndentLines(src); len(tabs) > 0 {
		for _, ln := range tabs {
			d.errorf("yaml_tab_indent", span{Start: Pos{ln, 1}, End: Pos{ln, 2}}, "", "", "Use spaces; YAML does not allow tabs for indentation.",
				"Line %d is indented with a tab.", ln)
		}
		return nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(src.text))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		yamlError(src, d, err)
		return nil
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		ln := secondDocumentLine(src)
		d.errorf("yaml_multi_document", span{Start: Pos{ln, 1}, End: Pos{ln, 4}}, "", "",
			"Keep one policy per file, and remove the `---` line.", "This file holds more than one YAML document.")
		return nil
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	w := walker{d: d}
	w.walk(root, 1, "")
	if w.stopped || w.hard {
		return nil
	}
	if root.Kind != yaml.MappingNode {
		d.errorf("wrong_type", spanOf(src, root), "", "", "Start the file with `deedglass: policy/v1` on its own line.",
			"A policy must be a mapping of keys; found %s.", kindName(root))
		return nil
	}
	return root
}

// walker refuses anchors, aliases, merge keys, non-core tags and duplicate keys, and counts nodes
// and depth. It never follows an alias.
type walker struct {
	d       *diags
	nodes   int
	stopped bool // stop walking: nothing more can be read safely
	hard    bool // finish walking, then stop: later stages would only repeat the problem
}

func (w *walker) walk(n *yaml.Node, depth int, key string) {
	if w.stopped || n == nil {
		return
	}
	w.nodes++
	if w.nodes > MaxYAMLNodes {
		w.d.errorf("yaml_too_complex", span{Start: Pos{n.Line, n.Column}}, "", "", "Split it into smaller policies, or replace repeated rules with globs.",
			"This policy has more than %s YAML nodes; the limit is %s.", thousands(MaxYAMLNodes), thousands(MaxYAMLNodes))
		w.stopped = true
		return
	}
	if depth > MaxYAMLDepth {
		w.d.errorf("yaml_too_complex", span{Start: Pos{n.Line, n.Column}}, "", "", "Flatten the structure; a policy needs only a few levels.",
			"This policy nests more than %d levels deep.", MaxYAMLDepth)
		w.stopped = true
		return
	}
	at := span{Start: Pos{n.Line, n.Column}, End: Pos{n.Line, n.Column + 1}}
	if n.Kind == yaml.AliasNode {
		w.d.errorf("yaml_alias", at, "", "", "Repeat the value, so each rule reads on its own.",
			"Anchors and aliases (& and *) are not allowed in policies.")
		w.stopped = true
		return
	}
	if n.Anchor != "" {
		w.d.errorf("yaml_alias", span{Start: at.Start, End: Pos{n.Line, n.Column + 1 + runeLen(n.Anchor)}}, "", "",
			"Repeat the value, so each rule reads on its own.", "Anchors and aliases (& and *) are not allowed in policies.")
		w.stopped = true
		return
	}
	if n.Style&yaml.TaggedStyle != 0 && !coreTags[n.Tag] {
		tag := n.Tag
		if strings.HasPrefix(tag, "tag:yaml.org,2002:") {
			tag = "!!" + strings.TrimPrefix(tag, "tag:yaml.org,2002:")
		}
		end := Pos{n.Line, n.Column + runeLen(tag)}
		if n.Kind == yaml.ScalarNode && n.Value != "" {
			end.Col += 1 + runeLen(n.Value)
		}
		expr := strings.TrimSpace(tag + " " + n.Value)
		if strings.HasPrefix(tag, "!") && !strings.HasPrefix(tag, "!!") && n.Kind == yaml.ScalarNode && (key == "when" || key == "measure") {
			w.d.errorf("yaml_tag", span{Start: at.Start, End: end}, "", "", fmt.Sprintf("Quote the expression: `when: '%s'`.", clip(expr)),
				"`%s` is read by YAML as a tag, not as CEL.", clip(expr))
		} else {
			hint := "Remove the tag; policies use plain YAML values."
			if strings.HasPrefix(tag, "!") && !strings.HasPrefix(tag, "!!") && n.Kind == yaml.ScalarNode {
				hint = fmt.Sprintf("Quote the value if the %s is part of it: `'%s'`.", "`!`", clip(expr))
			}
			w.d.errorf("yaml_tag", span{Start: at.Start, End: end}, "", "", hint,
				"`%s` is a YAML tag, and policies don't use tags.", clip(tag))
		}
		return
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]*yaml.Node{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.ScalarNode && (k.Tag == "!!merge" || (k.Value == "<<" && k.Style == 0)) {
				w.d.errorf("yaml_merge_key", span{Start: Pos{k.Line, k.Column}, End: Pos{k.Line, k.Column + 2}}, "", "",
					"Repeat the keys, so each rule reads on its own.", "Merge keys (<<) are not allowed in policies.")
				w.stopped = true
				return
			}
			if k.Kind == yaml.ScalarNode {
				if first, ok := seen[k.Value]; ok {
					w.d.errorf("yaml_duplicate_key", span{Start: Pos{k.Line, k.Column}, End: Pos{k.Line, k.Column + runeLen(k.Value)}}, "", "",
						"Keep one of them; YAML would silently use only one.",
						"Key %q appears twice in this mapping (first on line %d).", clip(k.Value), first.Line)
					w.hard = true
				} else {
					seen[k.Value] = k
				}
			}
		}
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			w.walk(n.Content[i], depth+1, "")
			w.walk(n.Content[i+1], depth+1, n.Content[i].Value)
		}
		return
	}
	for _, c := range n.Content {
		w.walk(c, depth+1, "")
	}
}

// yamlError turns a parser error into one diagnostic, with the specific advice for CEL's ` : `.
func yamlError(src *source, d *diags, err error) {
	msg := err.Error()
	ln, text := 0, strings.TrimPrefix(msg, "yaml: ")
	if m := yamlLineRE.FindStringSubmatch(msg); m != nil {
		ln, _ = strconv.Atoi(m[1])
		text = m[2]
	}
	text = strings.TrimSuffix(text, " in this context")
	if strings.Contains(text, "anchor") {
		d.errorf("yaml_alias", span{Start: Pos{ln, 1}}, "", "", "Repeat the value, so each rule reads on its own.",
			"Anchors and aliases (& and *) are not allowed in policies.")
		return
	}
	hint := "Check the indentation and the punctuation on this line; quote any value that contains `: ` or starts with a special character."
	sp := span{Start: Pos{ln, 1}}
	if ln > 0 {
		line := src.line(ln)
		trim := strings.TrimLeft(line, " ")
		trim = strings.TrimPrefix(trim, "- ")
		if strings.Contains(text, "mapping values are not allowed") {
			// Point at the second ": " on the line, the one YAML stumbled on.
			if i := strings.Index(line, ": "); i >= 0 {
				if j := strings.Index(line[i+2:], ":"); j >= 0 {
					c := runeLen(line[:i+2+j]) + 1
					sp = span{Start: Pos{ln, c}, End: Pos{ln, c + 1}}
				}
			}
			if strings.HasPrefix(trim, "when:") || strings.HasPrefix(trim, "measure:") {
				hint = "Quote the expression or write it as a block (`when: >-`), because YAML reads a colon followed by a space as a new key."
			}
		}
	}
	d.errorf("yaml_syntax", sp, "", "", hint, "The YAML could not be read: %s.", strings.TrimSuffix(text, "."))
}

// tabIndentLines lists lines whose leading whitespace contains a tab (outside block scalars it is
// always an error in YAML; inside them we refuse it too, for clarity).
func tabIndentLines(src *source) []int {
	var out []int
	for i, l := range src.lines {
		for _, r := range l {
			if r == '\t' {
				out = append(out, i+1)
				break
			}
			if r != ' ' {
				break
			}
		}
	}
	return out
}

func secondDocumentLine(src *source) int {
	seenContent := false
	for i, l := range src.lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(l, "---") || strings.HasPrefix(l, "...") {
			if seenContent {
				return i + 1
			}
			continue
		}
		seenContent = true
	}
	return len(src.lines)
}

func stripComments(b []byte) []byte {
	var out []byte
	for _, l := range bytes.Split(b, []byte("\n")) {
		t := bytes.TrimSpace(l)
		if len(t) == 0 || t[0] == '#' || string(t) == "---" {
			continue
		}
		out = append(out, l...)
		out = append(out, '\n')
	}
	return out
}

func firstInvalidUTF8(b []byte) int {
	for i := 0; i < len(b); {
		r, w := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && w == 1 {
			return i
		}
		i += w
	}
	return len(b)
}

// kindName names a node's type for wrong_type messages.
func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!null":
			return "nothing (null)"
		case "!!int":
			return "a number"
		case "!!float":
			return "a decimal number"
		case "!!bool":
			return "true or false"
		}
		return "a string"
	}
	return "something else"
}
