package sqlguard

import (
	"fmt"
	"slices"
	"strings"
)

// The tokenizer follows the lexical structure of PostgreSQL 18, "4.1. Lexical Structure":
// https://www.postgresql.org/docs/18/sql-syntax-lexical.html (and src/backend/parser/scan.l).
// It is lossless: concatenating Token.Text over the result gives back the input exactly, for any
// input, including invalid ones. Problems are recorded, never repaired.

// Kind is a token kind.
type Kind uint8

// Token kinds.
const (
	Space         Kind = iota // spaces, tabs, newlines (§4.1: space, tab, newline, CR, FF, VT)
	LineComment               // -- to the end of the line
	BlockComment              // /* … */, nesting (§4.1.5)
	Ident                     // an unquoted identifier or keyword (Norm is lowercased)
	QuotedIdent               // "…" with "" as the escape (Norm is unquoted, case kept)
	UnicodeIdent              // U&"…" (Norm is the raw body; escapes are not decoded)
	String                    // '…' with '' as the escape, and N'…'
	EString                   // E'…' with backslash escapes (§4.1.2.2)
	UnicodeString             // U&'…' (§4.1.2.3)
	BitString                 // B'…' and X'…' (§4.1.2.5)
	DollarString              // $tag$…$tag$ (§4.1.2.4)
	Number                    // numeric constants, including 0x1F, 1_000 and 1.5e-3
	Param                     // $1
	Op                        // operators: runs of + - * / < > = ~ ! @ # % ^ & | ` ? and :
	Punct                     // ( ) [ ] , ; .
	Invalid                   // a byte no rule covers (a backslash, a control character)
)

var kindNames = [...]string{"Space", "LineComment", "BlockComment", "Ident", "QuotedIdent", "UnicodeIdent",
	"String", "EString", "UnicodeString", "BitString", "DollarString", "Number", "Param", "Op", "Punct", "Invalid"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return fmt.Sprintf("Kind(%d)", k)
}

// Token is one lexical token. Text is the exact source slice; Start and End are byte offsets.
type Token struct {
	Kind       Kind
	Text       string // the exact source slice
	Norm       string // Ident lowercased; QuotedIdent unquoted with "" → "; otherwise Text
	Start, End int
	Depth      int // parenthesis depth: a "(" carries the outer depth, tokens inside it one more
}

// trivia reports whether a token carries no meaning for classification.
func (t *Token) trivia() bool {
	return t.Kind == Space || t.Kind == LineComment || t.Kind == BlockComment
}

// LexError is the first problem Tokenize met. The tokens are still returned, losslessly.
type LexError struct {
	Reason string // one of the Reason* constants
	Offset int
}

func (e *LexError) Error() string { return fmt.Sprintf("sqlguard: %s at byte %d", e.Reason, e.Offset) }

// Tokenize splits sql into tokens. Concatenating Token.Text gives back sql. The error, when not
// nil, is a *LexError naming the first problem; the tokens are complete either way.
func Tokenize(sql string) ([]Token, error) {
	var lx lexer
	lx.run(sql)
	if lx.firstReason != "" {
		return lx.toks, &LexError{Reason: lx.firstReason, Offset: lx.firstOffset}
	}
	return lx.toks, nil
}

// reasonSet is a small set of ambiguity reasons.
type reasonSet uint32

func (r *reasonSet) add(reason string) {
	for i, s := range AllReasons {
		if s == reason {
			*r |= 1 << i
			return
		}
	}
}

func (r reasonSet) has(reason string) bool {
	for i, s := range AllReasons {
		if s == reason {
			return r&(1<<i) != 0
		}
	}
	return false
}

// list returns the reasons in sorted order.
func (r reasonSet) list() []string {
	out := make([]string, 0, 2)
	for i, s := range AllReasons {
		if r&(1<<i) != 0 {
			out = append(out, s)
		}
	}
	sortStrings(out)
	return out
}

type lexer struct {
	src         string
	toks        []Token
	depth       int
	maxDepth    int
	unbalanced  bool
	reasons     reasonSet
	firstReason string
	firstOffset int
}

func (lx *lexer) flag(reason string, at int) {
	lx.reasons.add(reason)
	if lx.firstReason == "" {
		lx.firstReason, lx.firstOffset = reason, at
	}
}

func (lx *lexer) emit(k Kind, start, end int, norm string) {
	text := lx.src[start:end]
	if norm == "" {
		norm = text
	}
	lx.toks = append(lx.toks, Token{Kind: k, Text: text, Norm: norm, Start: start, End: end, Depth: lx.depth})
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// identStart and identCont follow scan.l: letters, underscore and every byte ≥ 0x80.
func identStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= 0x80
}

func identCont(c byte) bool { return identStart(c) || c >= '0' && c <= '9' || c == '$' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// opChar lists the characters an operator can be made of (§4.1.3), plus ':' for casts.
func opChar(c byte) bool {
	switch c {
	case '+', '-', '*', '/', '<', '>', '=', '~', '!', '@', '#', '%', '^', '&', '|', '`', '?', ':':
		return true
	}
	return false
}

func (lx *lexer) run(src string) {
	lx.src = src
	lx.toks = make([]Token, 0, len(src)/2+8)
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == 0:
			lx.flag(ReasonNULByte, i)
			lx.emit(Invalid, i, i+1, "")
			i++
		case isSpace(c):
			j := i + 1
			for j < len(src) && isSpace(src[j]) {
				j++
			}
			lx.emit(Space, i, j, "")
			i = j
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			j := i + 2
			for j < len(src) && src[j] != '\n' && src[j] != '\r' {
				if src[j] == 0 {
					lx.flag(ReasonNULByte, j)
				}
				j++
			}
			lx.emit(LineComment, i, j, "")
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = lx.blockComment(i)
		case c == '\'':
			i = lx.quoted(i, i, String, false)
		case (c == 'e' || c == 'E') && i+1 < len(src) && src[i+1] == '\'':
			i = lx.quoted(i, i+1, EString, true)
		case (c == 'b' || c == 'B' || c == 'x' || c == 'X') && i+1 < len(src) && src[i+1] == '\'':
			i = lx.quoted(i, i+1, BitString, false)
		case (c == 'n' || c == 'N') && i+1 < len(src) && src[i+1] == '\'':
			i = lx.quoted(i, i+1, String, false)
		case (c == 'u' || c == 'U') && i+2 < len(src) && src[i+1] == '&' && src[i+2] == '\'':
			i = lx.quoted(i, i+2, UnicodeString, false)
		case (c == 'u' || c == 'U') && i+2 < len(src) && src[i+1] == '&' && src[i+2] == '"':
			i = lx.quotedIdent(i, i+2, UnicodeIdent)
		case c == '"':
			i = lx.quotedIdent(i, i, QuotedIdent)
		case c == '$':
			i = lx.dollar(i)
		case identStart(c):
			j := i + 1
			for j < len(src) && identCont(src[j]) {
				j++
			}
			lx.ident(i, j)
			i = j
		case isDigit(c) || c == '.' && i+1 < len(src) && isDigit(src[i+1]):
			i = lx.number(i)
		case c == '(':
			lx.emit(Punct, i, i+1, "")
			lx.depth++
			if lx.depth > lx.maxDepth {
				lx.maxDepth = lx.depth
			}
			i++
		case c == ')':
			if lx.depth == 0 {
				lx.unbalanced = true
			} else {
				lx.depth--
			}
			lx.emit(Punct, i, i+1, "")
			i++
		case c == '[' || c == ']' || c == ',' || c == ';' || c == '.':
			lx.emit(Punct, i, i+1, "")
			i++
		case opChar(c):
			j := i + 1
			for j < len(src) && opChar(src[j]) {
				// An operator never contains -- or /* (§4.1.3): a comment starts there.
				if (src[j] == '-' && j+1 < len(src) && src[j+1] == '-') || (src[j] == '/' && j+1 < len(src) && src[j+1] == '*') {
					break
				}
				j++
			}
			lx.emit(Op, i, j, "")
			i = j
		case c == '\\':
			lx.flag(ReasonBackslashCommand, i)
			lx.emit(Invalid, i, i+1, "")
			i++
		default:
			// A control character, '{', '}' or another byte no rule covers.
			lx.flag(ReasonUnknownStatement, i)
			lx.emit(Invalid, i, i+1, "")
			i++
		}
	}
	if lx.unbalanced || lx.depth != 0 {
		lx.flag(ReasonUnknownStatement, len(src))
	}
}

func (lx *lexer) ident(i, j int) {
	text := lx.src[i:j]
	norm := text
	ascii := true
	upper := false
	for k := 0; k < len(text); k++ {
		c := text[k]
		if c >= 0x80 {
			ascii = false
		} else if c >= 'A' && c <= 'Z' {
			upper = true
		}
	}
	if !ascii {
		// Postgres reads these bytes as identifier characters, so "DRОP" (Cyrillic О) is an
		// identifier, not DROP. Deedglass can't read such SQL with confidence.
		lx.flag(ReasonNonASCII, i)
	}
	if upper {
		norm = strings.ToLower(text)
	}
	lx.emit(Ident, i, j, norm)
}

func (lx *lexer) blockComment(i int) int {
	src := lx.src
	j := i + 2
	nest := 1
	for j < len(src) && nest > 0 {
		switch {
		case src[j] == '/' && j+1 < len(src) && src[j+1] == '*':
			nest++
			j += 2
		case src[j] == '*' && j+1 < len(src) && src[j+1] == '/':
			nest--
			j += 2
		default:
			if src[j] == 0 {
				lx.flag(ReasonNULByte, j)
			}
			j++
		}
	}
	if nest > 0 {
		lx.flag(ReasonUnterminatedComment, i)
	}
	lx.emit(BlockComment, i, j, "")
	return j
}

// quoted scans a '…' literal whose opening quote is at q (start is where the token begins, so a
// prefix such as E, B, X, N or U& is part of it). backslash: E-string escapes.
func (lx *lexer) quoted(start, q int, k Kind, backslash bool) int {
	src := lx.src
	j := q + 1
	sawBackslash := false
	for {
		if j >= len(src) {
			lx.flag(ReasonUnterminatedString, start)
			lx.emit(k, start, len(src), "")
			return len(src)
		}
		c := src[j]
		switch {
		case c == 0:
			lx.flag(ReasonNULByte, j)
			j++
		case backslash && c == '\\':
			j += 2
		case c == '\\':
			sawBackslash = true
			j++
		case c == '\'':
			if j+1 < len(src) && src[j+1] == '\'' {
				j += 2
				continue
			}
			j++
			if sawBackslash && k == String {
				// With standard_conforming_strings off, \' would end the string somewhere else.
				lx.flag(ReasonBackslashInString, start)
			}
			lx.emit(k, start, j, "")
			return j
		default:
			j++
		}
	}
}

func (lx *lexer) quotedIdent(start, q int, k Kind) int {
	src := lx.src
	j := q + 1
	esc := false
	for {
		if j >= len(src) {
			lx.flag(ReasonUnterminatedIdentifier, start)
			lx.emit(k, start, len(src), "")
			return len(src)
		}
		c := src[j]
		if c == 0 {
			lx.flag(ReasonNULByte, j)
		}
		if c == '"' {
			if j+1 < len(src) && src[j+1] == '"' {
				esc = true
				j += 2
				continue
			}
			norm := src[q+1 : j]
			if esc {
				norm = strings.ReplaceAll(norm, `""`, `"`)
			}
			if norm == "" {
				// A zero-length quoted identifier is an error in Postgres.
				lx.flag(ReasonUnknownStatement, start)
				norm = `""`
			}
			j++
			lx.emit(k, start, j, norm)
			return j
		}
		j++
	}
}

// dollar scans $1 parameters and $tag$ … $tag$ quotes (§4.1.2.4, §4.1.2.8).
func (lx *lexer) dollar(i int) int {
	src := lx.src
	j := i + 1
	if j < len(src) && isDigit(src[j]) {
		for j < len(src) && isDigit(src[j]) {
			j++
		}
		lx.emit(Param, i, j, "")
		return j
	}
	// The tag is an identifier without $; it can't start with a digit.
	if j < len(src) && identStart(src[j]) {
		for j < len(src) && identCont(src[j]) && src[j] != '$' {
			j++
		}
	}
	if j >= len(src) || src[j] != '$' {
		// A lone $ (or $tag without its closing $) is not valid SQL.
		lx.flag(ReasonUnknownStatement, i)
		lx.emit(Invalid, i, i+1, "")
		return i + 1
	}
	delim := src[i : j+1]
	body := j + 1
	end := strings.Index(src[body:], delim)
	if end < 0 {
		lx.flag(ReasonUnterminatedDollar, i)
		lx.emit(DollarString, i, len(src), "")
		return len(src)
	}
	stop := body + end + len(delim)
	if strings.IndexByte(src[body:body+end], 0) >= 0 {
		lx.flag(ReasonNULByte, body)
	}
	lx.emit(DollarString, i, stop, "")
	return stop
}

func (lx *lexer) number(i int) int {
	src := lx.src
	j := i
	for j < len(src) {
		c := src[j]
		switch {
		case isDigit(c) || c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			if (c == 'e' || c == 'E') && j+1 < len(src) && (src[j+1] == '+' || src[j+1] == '-') && j+2 < len(src) && isDigit(src[j+2]) {
				j += 2
				continue
			}
			j++
		case c == '.':
			// 1..10 is two numbers around a range operator in some dialects; Postgres reads "1."
			if j+1 < len(src) && src[j+1] == '.' {
				lx.emit(Number, i, j, "")
				return j
			}
			j++
		default:
			lx.emit(Number, i, j, "")
			return j
		}
	}
	lx.emit(Number, i, j, "")
	return j
}

// sortStrings sorts in place: insertion sort for the usual short lists, slices.Sort otherwise.
func sortStrings(s []string) {
	if len(s) > 12 {
		slices.Sort(s)
		return
	}
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
