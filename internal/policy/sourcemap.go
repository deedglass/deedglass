package policy

import (
	"go.yaml.in/yaml/v3"
)

// rawRune is one rune of the file with its position.
type rawRune struct {
	r   rune
	pos Pos
}

// sourceMap maps each rune of a scalar's value (and one past the end) back to its file position,
// for every YAML style: plain, single-quoted (” escapes), double-quoted (backslash escapes and
// line continuations), literal `|` and folded `>`. It returns nil when the value can't be aligned
// with the source; callers then fall back to the scalar's start. It never panics on odd input.
func sourceMap(src *source, n *yaml.Node) (out []Pos) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	if n == nil || n.Kind != yaml.ScalarNode || n.Line < 1 {
		return nil
	}
	quoted := n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) != 0
	block := n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0
	// The raw runes from the scalar's start, read one line at a time as alignment needs them
	// (reading to the end of the file for every expression would be quadratic).
	var raw []rawRune
	next := n.Line
	more := func() bool {
		if next > len(src.lines) {
			return false
		}
		ln := next
		next++
		col := 1
		for i, r := range []rune(src.line(ln)) {
			if ln == n.Line && i < n.Column-1 {
				col++
				continue
			}
			raw = append(raw, rawRune{r, Pos{ln, col}})
			col++
		}
		raw = append(raw, rawRune{'\n', Pos{ln, col}})
		return true
	}
	// need makes raw[k] available when the file has it.
	need := func(k int) bool {
		for k >= len(raw) {
			if !more() {
				return false
			}
		}
		return true
	}
	i := 0
	// Skip the tag, the opening quote, or the block header line.
	if n.Style&yaml.TaggedStyle != 0 {
		for need(i) && raw[i].r != ' ' && raw[i].r != '\n' {
			i++
		}
		for need(i) && raw[i].r == ' ' {
			i++
		}
	}
	switch {
	case quoted:
		i++
	case block:
		for need(i) && raw[i].r != '\n' {
			i++
		}
		i++
	}
	indent := -1
	if block {
		// The block indent is the indentation of the first non-empty content line.
		for j := i; need(j); {
			k := j
			for need(k) && raw[k].r == ' ' {
				k++
			}
			if need(k) && raw[k].r != '\n' {
				indent = k - j
				break
			}
			j = k + 1
		}
		if indent < 0 {
			return nil
		}
	}
	atLineStart := block
	double := n.Style&yaml.DoubleQuotedStyle != 0
	single := n.Style&yaml.SingleQuotedStyle != 0
	val := []rune(n.Value)
	out = make([]Pos, 0, len(val)+1)
	for vi := 0; vi < len(val); {
		if !need(i+1) && !need(i) {
			return nil
		}
		if atLineStart {
			// Skip the block indentation (at most indent spaces).
			for k := 0; k < indent && need(i) && raw[i].r == ' '; k++ {
				i++
			}
			atLineStart = false
			continue
		}
		v := val[vi]
		r := raw[i].r
		switch {
		case double && r == '\\':
			if need(i+1) && raw[i+1].r == '\n' {
				// A line continuation: no value rune; skip it and the next line's indentation.
				i += 2
				for need(i) && (raw[i].r == ' ' || raw[i].r == '\t') {
					i++
				}
				continue
			}
			out = append(out, raw[i].pos)
			need(i + 10)
			i += escapeWidth(raw, i)
			vi++
		case single && r == '\'' && need(i+1) && raw[i+1].r == '\'' && v == '\'':
			out = append(out, raw[i].pos)
			i += 2
			vi++
		case r == v:
			out = append(out, raw[i].pos)
			i++
			vi++
			if r == '\n' && block {
				atLineStart = true
			}
		case (v == ' ' || v == '\n') && (r == '\n' || r == ' '):
			// Line folding: a break (and the indentation after it) reads as one space or newline.
			out = append(out, raw[i].pos)
			for need(i) && (raw[i].r == ' ' || raw[i].r == '\n') {
				if raw[i].r == '\n' && block {
					i++
					for k := 0; k < indent && need(i) && raw[i].r == ' '; k++ {
						i++
					}
					if need(i) && raw[i].r != ' ' && raw[i].r != '\n' {
						break
					}
					continue
				}
				i++
			}
			vi++
		case r == '\n' || r == ' ':
			// Extra whitespace in the source that the value doesn't have (a blank line folded away).
			i++
		default:
			return nil
		}
	}
	end := Pos{}
	if len(out) > 0 {
		last := out[len(out)-1]
		end = Pos{last.Line, last.Col + 1}
	} else if need(i) {
		end = raw[i].pos
	}
	return append(out, end)
}

// escapeWidth is the number of raw runes a double-quoted escape takes.
func escapeWidth(raw []rawRune, i int) int {
	if i+1 >= len(raw) {
		return 1
	}
	switch raw[i+1].r {
	case 'x':
		return 4
	case 'u':
		return 6
	case 'U':
		return 10
	}
	return 2
}
