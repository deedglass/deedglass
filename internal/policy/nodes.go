package policy

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// spanOf is the source range of a node's token: a scalar's full token on its first line (quotes
// and tag included), or the first character of a collection.
func spanOf(src *source, n *yaml.Node) span {
	if n == nil {
		return span{}
	}
	start := Pos{n.Line, n.Column}
	end := Pos{n.Line, n.Column + 1}
	if n.Kind == yaml.ScalarNode {
		line := []rune(src.line(n.Line))
		c := n.Column - 1
		if c < 0 || c > len(line) {
			return span{Start: start, End: end}
		}
		w := tokenWidth(line[c:], n)
		if w > 0 {
			end.Col = n.Column + w
		}
	}
	return span{Start: start, End: end}
}

// tokenWidth measures a scalar token on its first line, in runes.
func tokenWidth(line []rune, n *yaml.Node) int {
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		for i := 1; i < len(line); i++ {
			if line[i] == '\\' {
				i++
				continue
			}
			if line[i] == '"' {
				return i + 1
			}
		}
		return len(line)
	case n.Style&yaml.SingleQuotedStyle != 0:
		for i := 1; i < len(line); i++ {
			if line[i] == '\'' {
				if i+1 < len(line) && line[i+1] == '\'' {
					i++
					continue
				}
				return i + 1
			}
		}
		return len(line)
	case n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return 1
	}
	v := []rune(n.Value)
	if i := strings.IndexByte(n.Value, '\n'); i >= 0 {
		v = []rune(n.Value[:i])
	}
	if len(v) > len(line) {
		return len(line)
	}
	return len(v)
}

// keyValue is one entry of a mapping.
type keyValue struct {
	Key   *yaml.Node
	Value *yaml.Node
}

func pairs(m *yaml.Node) []keyValue {
	var out []keyValue
	for i := 0; i+1 < len(m.Content); i += 2 {
		out = append(out, keyValue{m.Content[i], m.Content[i+1]})
	}
	return out
}

func isNull(n *yaml.Node) bool {
	return n == nil || (n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" && n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) == 0)
}

// isText reports a scalar that reads as text: a quoted or block scalar, or any plain scalar other
// than null. `env: true` and `tier: 1` read as "true" and "1".
func isText(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.ScalarNode && !isNull(n)
}
