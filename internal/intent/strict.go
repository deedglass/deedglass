package intent

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxSafeInteger is 2^53-1, the largest integer every JSON implementation
// represents exactly. JCS (RFC 8785) serializes numbers as IEEE-754 doubles,
// so a larger integer would be silently rounded (observed in spike S4:
// 9007199254740993 became 9007199254740992). The strict parser rejects it
// before canonicalization.
const MaxSafeInteger int64 = 1<<53 - 1

// maxDepth bounds nesting so hostile input cannot exhaust the stack.
// deedglass.transfer/1 needs depth 2.
const maxDepth = 16

// ParseStrict parses one JSON value under the deedglass strict profile and returns
// it as Go values: map[string]any, []any, string, int64, bool or nil.
//
// Compared with RFC 8259 it additionally rejects: invalid UTF-8, duplicate
// object keys (compared after unescaping), numbers with a fraction or
// exponent, integers whose magnitude exceeds 2^53-1, lone UTF-16 surrogates
// in \u escapes, nesting deeper than maxDepth, and trailing data.
// It does not check canonical form; see CheckCanonical.
func ParseStrict(b []byte) (any, error) { return parseStrict(b, profile{maxDepth: maxDepth}) }

// ParseStrictNonNegative is ParseStrict with one more rule, for deed bodies (contract §7): every
// number must be in 0..2^53-1, so negative integers and "-0" are refused as well
// (CodeNegativeNumber). Signed intents keep ParseStrict.
func ParseStrictNonNegative(b []byte) (any, error) {
	return parseStrict(b, profile{nonNeg: true, maxDepth: maxDepth})
}

// profile selects the strict rules a parse applies.
type profile struct {
	nonNeg   bool // refuse a leading minus sign (deed bodies)
	floats   bool // accept fractions and exponents (tool arguments, S09)
	maxDepth int
}

func parseStrict(b []byte, pr profile) (any, error) {
	if !utf8.Valid(b) {
		off := 0
		for off < len(b) {
			r, n := utf8.DecodeRune(b[off:])
			if r == utf8.RuneError && n <= 1 {
				break
			}
			off += n
		}
		return nil, errAt(CodeInvalidUTF8, "", off, "input is not valid UTF-8")
	}
	p := &parser{b: b, profile: pr}
	p.ws()
	v, err := p.value("", 0)
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.b) {
		return nil, errAt(CodeSyntax, "", p.i, "trailing data after the top-level value")
	}
	return v, nil
}

type parser struct {
	b []byte
	i int
	profile
}

func (p *parser) ws() {
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) value(path string, depth int) (any, error) {
	if p.i >= len(p.b) {
		return nil, errAt(CodeSyntax, path, p.i, "unexpected end of input")
	}
	switch c := p.b[p.i]; {
	case c == '{':
		return p.object(path, depth+1)
	case c == '[':
		return p.array(path, depth+1)
	case c == '"':
		return p.str(path)
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number(path)
	case c == 't':
		return true, p.literal(path, "true")
	case c == 'f':
		return false, p.literal(path, "false")
	case c == 'n':
		return nil, p.literal(path, "null")
	default:
		return nil, errAt(CodeSyntax, path, p.i, fmt.Sprintf("unexpected character %q", c))
	}
}

func (p *parser) literal(path, lit string) error {
	if len(p.b)-p.i < len(lit) || string(p.b[p.i:p.i+len(lit)]) != lit {
		return errAt(CodeSyntax, path, p.i, "invalid literal")
	}
	p.i += len(lit)
	return nil
}

func (p *parser) object(path string, depth int) (any, error) {
	if depth > p.maxDepth {
		return nil, errAt(CodeTooDeep, path, p.i, "nesting too deep")
	}
	p.i++ // '{'
	m := map[string]any{}
	p.ws()
	if p.i < len(p.b) && p.b[p.i] == '}' {
		p.i++
		return m, nil
	}
	for {
		p.ws()
		if p.i >= len(p.b) || p.b[p.i] != '"' {
			return nil, errAt(CodeSyntax, path, p.i, "expected object key")
		}
		keyOff := p.i
		k, err := p.str(path)
		if err != nil {
			return nil, err
		}
		kp := joinPath(path, k.(string))
		if _, dup := m[k.(string)]; dup {
			return nil, errAt(CodeDuplicateKey, kp, keyOff, "duplicate object key")
		}
		p.ws()
		if p.i >= len(p.b) || p.b[p.i] != ':' {
			return nil, errAt(CodeSyntax, kp, p.i, "expected ':'")
		}
		p.i++
		p.ws()
		v, err := p.value(kp, depth)
		if err != nil {
			return nil, err
		}
		m[k.(string)] = v
		p.ws()
		if p.i >= len(p.b) {
			return nil, errAt(CodeSyntax, path, p.i, "unterminated object")
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return m, nil
		default:
			return nil, errAt(CodeSyntax, path, p.i, "expected ',' or '}'")
		}
	}
}

func (p *parser) array(path string, depth int) (any, error) {
	if depth > p.maxDepth {
		return nil, errAt(CodeTooDeep, path, p.i, "nesting too deep")
	}
	p.i++ // '['
	a := []any{}
	p.ws()
	if p.i < len(p.b) && p.b[p.i] == ']' {
		p.i++
		return a, nil
	}
	for {
		p.ws()
		v, err := p.value(fmt.Sprintf("%s[%d]", path, len(a)), depth)
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		p.ws()
		if p.i >= len(p.b) {
			return nil, errAt(CodeSyntax, path, p.i, "unterminated array")
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return a, nil
		default:
			return nil, errAt(CodeSyntax, path, p.i, "expected ',' or ']'")
		}
	}
}

// str parses a JSON string starting at the opening quote and returns the
// decoded text.
func (p *parser) str(path string) (any, error) {
	start := p.i
	// Fast path: no escapes and no control characters, so the text is the bytes between the
	// quotes (the input is valid UTF-8, checked up front).
	for j := start + 1; j < len(p.b); j++ {
		c := p.b[j]
		if c == '"' {
			p.i = j + 1
			return string(p.b[start+1 : j]), nil
		}
		if c == '\\' || c < 0x20 {
			break
		}
	}
	p.i++ // opening quote
	var sb strings.Builder
	for {
		if p.i >= len(p.b) {
			return nil, errAt(CodeSyntax, path, start, "unterminated string")
		}
		c := p.b[p.i]
		switch {
		case c == '"':
			p.i++
			return sb.String(), nil
		case c < 0x20:
			return nil, errAt(CodeSyntax, path, p.i, "unescaped control character in string")
		case c == '\\':
			if err := p.escape(path, &sb); err != nil {
				return nil, err
			}
		default:
			// Input is valid UTF-8 (checked up front), so copy the whole rune.
			_, n := utf8.DecodeRune(p.b[p.i:])
			sb.Write(p.b[p.i : p.i+n])
			p.i += n
		}
	}
}

func (p *parser) escape(path string, sb *strings.Builder) error {
	off := p.i
	if p.i+1 >= len(p.b) {
		return errAt(CodeSyntax, path, off, "truncated escape")
	}
	e := p.b[p.i+1]
	p.i += 2
	switch e {
	case '"':
		sb.WriteByte('"')
	case '\\':
		sb.WriteByte('\\')
	case '/':
		sb.WriteByte('/')
	case 'b':
		sb.WriteByte('\b')
	case 'f':
		sb.WriteByte('\f')
	case 'n':
		sb.WriteByte('\n')
	case 'r':
		sb.WriteByte('\r')
	case 't':
		sb.WriteByte('\t')
	case 'u':
		r, ok := p.hex4()
		if !ok {
			return errAt(CodeSyntax, path, off, "invalid \\u escape")
		}
		switch {
		case utf16.IsSurrogate(r) && r < 0xDC00: // high surrogate: a low one must follow
			if p.i+1 >= len(p.b) || p.b[p.i] != '\\' || p.b[p.i+1] != 'u' {
				return errAt(CodeLoneSurrogate, path, off, "high surrogate not followed by a low surrogate")
			}
			p.i += 2
			r2, ok := p.hex4()
			if !ok {
				return errAt(CodeSyntax, path, off, "invalid \\u escape")
			}
			if r2 < 0xDC00 || r2 > 0xDFFF {
				return errAt(CodeLoneSurrogate, path, off, "high surrogate not followed by a low surrogate")
			}
			sb.WriteRune(utf16.DecodeRune(r, r2))
		case utf16.IsSurrogate(r): // low surrogate without a preceding high one
			return errAt(CodeLoneSurrogate, path, off, "lone low surrogate")
		default:
			sb.WriteRune(r)
		}
	default:
		return errAt(CodeSyntax, path, off, fmt.Sprintf("invalid escape \\%c", e))
	}
	return nil
}

func (p *parser) hex4() (rune, bool) {
	if len(p.b)-p.i < 4 {
		return 0, false
	}
	var r rune
	for k := 0; k < 4; k++ {
		c := p.b[p.i+k]
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			return 0, false
		}
		r = r<<4 | rune(v)
	}
	p.i += 4
	return r, true
}

// number accepts only integers: -?(0|[1-9][0-9]*) with magnitude <= 2^53-1.
func (p *parser) number(path string) (any, error) {
	start := p.i
	neg := false
	if p.b[p.i] == '-' {
		if p.nonNeg {
			return nil, errAt(CodeNegativeNumber, path, start, "negative numbers and -0 are not allowed")
		}
		neg = true
		p.i++
	}
	if p.i >= len(p.b) || p.b[p.i] < '0' || p.b[p.i] > '9' {
		return nil, errAt(CodeSyntax, path, start, "invalid number")
	}
	digits := p.i
	if p.b[p.i] == '0' {
		p.i++
		if p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			return nil, errAt(CodeSyntax, path, start, "leading zero in number")
		}
	} else {
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
		}
	}
	if p.i < len(p.b) {
		switch p.b[p.i] {
		case '.', 'e', 'E':
			if p.floats {
				return p.fraction(path, start)
			}
			return nil, errAt(CodeNonInteger, path, start, "only integers are allowed (no fraction or exponent)")
		}
	}
	d := p.b[digits:p.i]
	if len(d) > 16 { // 2^53-1 has 16 digits
		return nil, errAt(CodeIntegerRange, path, start, "integer magnitude exceeds 2^53-1")
	}
	var n int64
	for _, c := range d {
		n = n*10 + int64(c-'0')
	}
	if n > MaxSafeInteger {
		return nil, errAt(CodeIntegerRange, path, start, "integer magnitude exceeds 2^53-1")
	}
	if neg {
		n = -n
	}
	return n, nil
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// fraction finishes a number with a fraction or exponent (the args profile only). The integer part
// is already consumed; it returns the value as a float64, refusing values outside the IEEE-754
// double range, which RFC 8785 can't serialize.
func (p *parser) fraction(path string, start int) (any, error) {
	if p.b[p.i] == '.' {
		p.i++
		if p.i >= len(p.b) || p.b[p.i] < '0' || p.b[p.i] > '9' {
			return nil, errAt(CodeSyntax, path, start, "invalid number")
		}
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
		}
	}
	if p.i < len(p.b) && (p.b[p.i] == 'e' || p.b[p.i] == 'E') {
		p.i++
		if p.i < len(p.b) && (p.b[p.i] == '+' || p.b[p.i] == '-') {
			p.i++
		}
		if p.i >= len(p.b) || p.b[p.i] < '0' || p.b[p.i] > '9' {
			return nil, errAt(CodeSyntax, path, start, "invalid number")
		}
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
		}
	}
	f, err := strconv.ParseFloat(string(p.b[start:p.i]), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, errAt(CodeNumberRange, path, start, "number is outside the IEEE-754 double range")
	}
	return f, nil
}
