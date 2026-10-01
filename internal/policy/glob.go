package policy

import "strings"

// Glob is a precompiled pattern: `*` is any run of characters (dots included), `?` is exactly one
// character, everything else is literal; case-sensitive and anchored at both ends. Matching
// allocates nothing.
type Glob struct {
	src  string
	kind globKind
	lit  string // exact, prefix or suffix literal
	segs []string
}

type globKind uint8

const (
	globExact globKind = iota
	globAny
	globPrefix // lit*
	globSuffix // *lit
	globGeneral
)

// globAlphabet is the set of characters a glob may contain.
func globChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		r == '_' || r == '.' || r == '*' || r == '?' || r == ':' || r == '-'
}

// CompileGlob checks and precompiles a pattern. The error is the first refused character.
func CompileGlob(p string) (Glob, bool) {
	for _, r := range p {
		if !globChar(r) {
			return Glob{}, false
		}
	}
	g := Glob{src: p}
	stars := strings.Count(p, "*")
	q := strings.Contains(p, "?")
	switch {
	case stars == 0 && !q:
		g.kind, g.lit = globExact, p
	case !q && strings.Trim(p, "*") == "":
		g.kind = globAny
	case !q && stars == 1 && strings.HasSuffix(p, "*"):
		g.kind, g.lit = globPrefix, p[:len(p)-1]
	case !q && stars == 1 && strings.HasPrefix(p, "*"):
		g.kind, g.lit = globSuffix, p[1:]
	default:
		g.kind = globGeneral
	}
	return g, true
}

// String returns the pattern as written.
func (g Glob) String() string { return g.src }

// Match reports whether s matches the whole pattern.
func (g Glob) Match(s string) bool {
	switch g.kind {
	case globExact:
		return s == g.lit
	case globAny:
		return true
	case globPrefix:
		return strings.HasPrefix(s, g.lit)
	case globSuffix:
		return strings.HasSuffix(s, g.lit)
	}
	return globMatch(g.src, s)
}

// globMatch is the iterative wildcard matcher with single-star backtracking: linear in practice
// and O(len(p)·len(s)) at worst, never exponential. Patterns are ASCII; `?` consumes one rune.
func globMatch(p, s string) bool {
	pi, si := 0, 0
	starP, starS := -1, 0
	for si < len(s) {
		if pi < len(p) && p[pi] == '?' {
			_, w := decodeRune(s[si:])
			pi++
			si += w
			continue
		}
		if pi < len(p) && p[pi] == '*' {
			starP, starS = pi, si
			pi++
			continue
		}
		if pi < len(p) && p[pi] == s[si] {
			pi++
			si++
			continue
		}
		if starP >= 0 {
			_, w := decodeRune(s[starS:])
			starS += w
			pi, si = starP+1, starS
			continue
		}
		return false
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

func decodeRune(s string) (rune, int) {
	for i, r := range s {
		_ = i
		return r, len(string(r))
	}
	return 0, 1
}

// covers reports whether every string matched by b is matched by a, conservatively: true only
// when a is all stars, a equals b, or a is `prefix*` and b is a literal or `prefix2*` starting
// with prefix. When unsure it says false (no false rule_unreachable warnings).
func (g Glob) covers(b Glob) bool {
	switch {
	case g.kind == globAny:
		return true
	case g.src == b.src:
		return true
	case g.kind == globPrefix && b.kind == globExact:
		return strings.HasPrefix(b.lit, g.lit)
	case g.kind == globPrefix && b.kind == globPrefix:
		return strings.HasPrefix(b.lit, g.lit)
	}
	return false
}
