package shellguard

import "strings"

// The shell reader (ADR-104). It is not a shell: it splits a command line the way POSIX sh and
// bash would, far enough to find every simple command in it, and never runs or expands anything.
//
//   - Operators split commands: ; & && || | |& newlines, ( … ) subshells and { …; } groups.
//   - Words lose their quoting: '…', "…", $'…', backslashes and line continuations.
//   - Command substitutions ($(…), `…`, <(…), >(…)) are read as commands of their own.
//   - VAR=value words before a command are kept apart from its arguments.
//   - Heredoc bodies and here-strings are kept as the command's stdin text; redirection targets
//     are dropped.
//   - Parameter expansions (${…}, $((…))) stay as written; a variable's value is never known.
//
// It is lenient: an unterminated quote or a stray ")" never fails, the rest is read as well as it
// can be, and Parse reports whether it had to give up early.

const maxDepth = 12 // nested substitutions, subshells and sh -c strings

// cmd is one simple command as written.
type cmd struct {
	env   []string // leading NAME=value words
	args  []string // words, quotes removed
	stdin []string // heredoc bodies and here-strings
	files []string // files redirected into stdin (< file)
	from  *cmd     // the command piped into this one
}

type heredoc struct {
	c     *cmd
	delim string
	strip bool // <<-
}

type parser struct {
	s       string
	i       int
	depth   int
	out     []*cmd
	pending []heredoc
	cut     bool // gave up somewhere: unterminated quote or heredoc, or nested too deep
	deep    bool // nested deeper than maxDepth: what is inside wasn't read
}

// parse reads s and returns its simple commands, substitutions first, in reading order, and
// whether it stopped reading because the nesting went deeper than maxDepth.
func parse(s string, depth int) ([]*cmd, bool) {
	p := &parser{s: s, depth: depth}
	if depth > maxDepth {
		return nil, true
	}
	p.list(false)
	return p.out, p.deep
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\r' }

func isMeta(c byte) bool {
	return isBlank(c) || c == '\n' || c == ';' || c == '&' || c == '|' || c == '(' || c == ')' || c == '<' || c == '>'
}

// reserved words that start or end a compound command; they are skipped, not run.
var reserved = map[string]bool{"{": true, "}": true, "!": true, "if": true, "then": true, "else": true, "elif": true,
	"fi": true, "do": true, "done": true, "while": true, "until": true, "case": true, "esac": true, "for": true,
	"select": true, "function": true, "in": true, "[[": true, "]]": true}

// list reads commands until the end of input or, when sub is true, the ")" that closes it.
func (p *parser) list(sub bool) {
	var cur, prev *cmd
	end := func(pipe bool) {
		if cur == nil {
			return // "a |\n b" keeps the pipe
		}
		if len(cur.args) > 0 || len(cur.env) > 0 || len(cur.stdin) > 0 {
			p.out = append(p.out, cur)
		}
		prev = nil
		if pipe {
			prev = cur
		}
		cur = nil
	}
	start := func() {
		if cur == nil {
			cur = &cmd{from: prev}
			prev = nil
		}
	}
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case isBlank(c):
			p.i++
		case c == '\\' && p.at(1) == '\n':
			p.i += 2
		case c == '\n':
			p.i++
			if cur != nil { // "a |\n b" keeps the pipe
				end(false)
				prev = nil
			}
			p.heredocs()
		case c == '#':
			for p.i < len(p.s) && p.s[p.i] != '\n' {
				p.i++
			}
		case c == ';':
			p.i++
			end(false)
			prev = nil
		case c == '&':
			if p.at(1) == '>' {
				start()
				p.redirect(cur)
				continue
			}
			p.i++
			if p.at(0) == '&' {
				p.i++
			}
			end(false)
			prev = nil
		case c == '|':
			p.i++
			if p.at(0) == '|' {
				p.i++
				end(false)
				prev = nil
				continue
			}
			if p.at(0) == '&' {
				p.i++
			}
			end(true)
		case c == '(':
			if cur != nil && len(cur.args) > 0 && p.at(1) == ')' { // name() { …; }: the name isn't run
				p.i += 2
				cur = nil
				continue
			}
			if p.at(1) == '(' { // (( arithmetic ))
				p.skipParens()
				continue
			}
			p.i++
			end(false)
			p.sub()
		case c == ')':
			p.i++
			end(false)
			if sub {
				return
			}
			prev = nil // a case pattern, or a stray parenthesis
		case c == '<' || c == '>':
			if p.at(1) == '(' { // process substitution
				p.i += 2
				p.sub()
				continue
			}
			start()
			p.redirect(cur)
		default:
			from := p.i
			w := p.word()
			if p.i < len(p.s) && (p.s[p.i] == '<' || p.s[p.i] == '>') && allDigits(p.s[from:p.i]) {
				continue // 2>&1: the digits name a file descriptor
			}
			if cur == nil || (len(cur.args) == 0 && len(cur.env) == 0) {
				if reserved[w] && p.s[from] != '"' && p.s[from] != '\'' {
					end(false)
					continue
				}
			}
			start()
			if len(cur.args) == 0 && isAssign(w) {
				cur.env = append(cur.env, w)
			} else {
				cur.args = append(cur.args, w)
			}
		}
	}
	end(false)
	if sub {
		p.cut = true // no closing ")"
	}
}

// sub reads a subshell or substitution body up to its ")".
func (p *parser) sub() {
	if p.depth >= maxDepth {
		p.cut, p.deep = true, true
		p.i = len(p.s)
		return
	}
	p.depth++
	p.list(true)
	p.depth--
}

func (p *parser) at(k int) byte {
	if p.i+k < len(p.s) {
		return p.s[p.i+k]
	}
	return 0
}

// word reads one word and returns it without quotes. Substitutions inside it are read as
// commands; their text stays in the word as written.
func (p *parser) word() string {
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case isMeta(c):
			return b.String()
		case c == '\\':
			if p.at(1) != '\n' && p.i+1 < len(p.s) {
				b.WriteByte(p.s[p.i+1])
			}
			p.i = min(p.i+2, len(p.s))
		case c == '\'':
			j := strings.IndexByte(p.s[p.i+1:], '\'')
			if j < 0 {
				b.WriteString(p.s[p.i+1:])
				p.i, p.cut = len(p.s), true
				return b.String()
			}
			b.WriteString(p.s[p.i+1 : p.i+1+j])
			p.i += j + 2
		case c == '"':
			p.i++
			p.dquote(&b)
		case c == '`':
			p.backtick(&b)
		case c == '$':
			p.dollar(&b)
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	return b.String()
}

// dquote reads the rest of a "…" string.
func (p *parser) dquote(b *strings.Builder) {
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch c {
		case '"':
			p.i++
			return
		case '\\':
			switch n := p.at(1); n {
			case '\n':
			case '"', '\\', '$', '`':
				b.WriteByte(n)
			default:
				b.WriteByte('\\')
				if n != 0 {
					b.WriteByte(n)
				}
			}
			p.i = min(p.i+2, len(p.s))
		case '`':
			p.backtick(b)
		case '$':
			p.dollar(b)
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	p.cut = true
}

// dollar reads what follows a "$": a substitution, an expansion kept as written, or $'…'.
func (p *parser) dollar(b *strings.Builder) {
	from := p.i
	switch p.at(1) {
	case '(':
		if p.at(2) == '(' {
			p.i++
			p.skipParens()
		} else {
			p.i += 2
			p.sub()
		}
	case '{': // ${…}: kept as written, but a substitution in it (${X:-$(cmd)}) is read
		depth := 0
		for p.i < len(p.s) {
			ch := p.s[p.i]
			p.i++
			if ch == '{' {
				depth++
			} else if ch == '}' {
				if depth--; depth == 0 {
					break
				}
			}
		}
		lo, hi := min(from+2, p.i), p.i
		if hi > lo && p.s[hi-1] == '}' {
			hi--
		}
		if body := p.s[lo:hi]; strings.Contains(body, "$(") || strings.ContainsRune(body, '`') {
			p.substitutions(body)
		}
	case '\'':
		p.i += 2
		for p.i < len(p.s) && p.s[p.i] != '\'' {
			if p.s[p.i] == '\\' && p.i+1 < len(p.s) {
				b.WriteByte(unescape(p.s[p.i+1]))
				p.i += 2
				continue
			}
			b.WriteByte(p.s[p.i])
			p.i++
		}
		p.i = min(p.i+1, len(p.s))
		return
	default:
		p.i++
		b.WriteByte('$')
		return
	}
	p.i = min(p.i, len(p.s))
	b.WriteString(p.s[from:p.i])
}

func unescape(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	}
	return c
}

// skipParens skips "((…))" (arithmetic), counting parentheses.
func (p *parser) skipParens() {
	depth := 0
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		p.i++
		if depth == 0 {
			return
		}
	}
}

// backtick reads `…`, whose body is read as a command line of its own.
func (p *parser) backtick(b *strings.Builder) {
	from := p.i
	var body strings.Builder
	p.i++
	for p.i < len(p.s) && p.s[p.i] != '`' {
		if p.s[p.i] == '\\' && p.i+1 < len(p.s) {
			p.i++
		}
		body.WriteByte(p.s[p.i])
		p.i++
	}
	if p.i >= len(p.s) {
		p.cut = true
	}
	p.i = min(p.i+1, len(p.s))
	p.nested(body.String())
	b.WriteString(p.s[from:p.i])
}

// substitutions reads s as the text of a word one level deeper (the inside of ${…}) and keeps
// the commands of the substitutions in it.
func (p *parser) substitutions(s string) {
	q := &parser{s: s, depth: p.depth + 1}
	if q.depth > maxDepth {
		p.deep = true
		return
	}
	for q.i < len(q.s) {
		if q.word(); q.i < len(q.s) {
			q.i++
		}
	}
	p.out = append(p.out, q.out...)
	p.deep = p.deep || q.deep
}

// nested reads s as a command line one level deeper and keeps its commands.
func (p *parser) nested(s string) {
	out, deep := parse(s, p.depth+1)
	p.out = append(p.out, out...)
	p.deep = p.deep || deep
}

// redirect reads a redirection at p.i and its target. A heredoc waits for the next newline; a
// here-string becomes stdin text now.
func (p *parser) redirect(c *cmd) {
	op := p.i
	for p.i < len(p.s) && p.i-op < 3 && strings.IndexByte("<>&|-", p.s[p.i]) >= 0 {
		p.i++
	}
	o := p.s[op:p.i]
	for p.i < len(p.s) && isBlank(p.s[p.i]) {
		p.i++
	}
	w := p.word()
	switch {
	case strings.HasPrefix(o, "<<<"):
		c.stdin = append(c.stdin, w)
	case strings.HasPrefix(o, "<<"):
		p.pending = append(p.pending, heredoc{c: c, delim: w, strip: strings.HasSuffix(o, "-")})
	case o == "<":
		c.files = append(c.files, w)
	}
}

// heredocs reads the bodies of the heredocs started on the line that just ended.
func (p *parser) heredocs() {
	for _, h := range p.pending {
		var body strings.Builder
		done := false
		for p.i < len(p.s) && !done {
			j := strings.IndexByte(p.s[p.i:], '\n')
			line := p.s[p.i:]
			if j >= 0 {
				line = p.s[p.i : p.i+j]
				p.i += j + 1
			} else {
				p.i = len(p.s)
			}
			if h.strip {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				done = true
				break
			}
			body.WriteString(line)
			body.WriteByte('\n')
		}
		if !done {
			p.cut = true
		}
		h.c.stdin = append(h.c.stdin, body.String())
	}
	p.pending = p.pending[:0]
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// isAssign reports whether w is NAME=value (or NAME+=value).
func isAssign(w string) bool {
	eq := strings.IndexByte(w, '=')
	if eq <= 0 {
		return false
	}
	name := strings.TrimSuffix(w[:eq], "+")
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return name != ""
}
