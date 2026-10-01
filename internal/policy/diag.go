package policy

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Severity is "error" or "warning".
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
)

// Diagnostic is one finding about a policy file. Codes are API (docs/POLICY_ERRORS.md): they are
// added, never renamed. S12 returns the same shape in the policy_invalid error.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`           // snake_case, stable
	Message  string   `json:"message"`        // one sentence, sentence case, ends with a period
	Hint     string   `json:"hint,omitempty"` // the next step
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line"`   // 1-based; 0 = the whole file
	Col      int      `json:"column"` // 1-based, in runes
	EndLine  int      `json:"end_line,omitempty"`
	EndCol   int      `json:"end_column,omitempty"` // exclusive
	RuleID   string   `json:"rule_id,omitempty"`
	Path     string   `json:"path,omitempty"` // YAML path, e.g. rules[3].when
	DocURL   string   `json:"doc_url"`
}

// Pos is a 1-based line and rune column in the source file.
type Pos struct {
	Line int `json:"line"`
	Col  int `json:"column"`
}

// span is a source range for a diagnostic; End is exclusive and on the same line or later.
type span struct {
	Start, End Pos
}

// diags collects diagnostics in one pass.
type diags struct {
	file string
	list []Diagnostic
}

func (d *diags) add(sev Severity, code string, sp span, path, ruleID, msg, hint string) {
	x := Diagnostic{Severity: sev, Code: code, Message: msg, Hint: hint, File: d.file,
		Line: sp.Start.Line, Col: sp.Start.Col, RuleID: ruleID, Path: path, DocURL: DocBase + code}
	if sp.End.Line > 0 && (sp.End.Line != sp.Start.Line || sp.End.Col > sp.Start.Col) {
		x.EndLine, x.EndCol = sp.End.Line, sp.End.Col
	}
	if x.Line > 0 && x.Col == 0 {
		x.Col = 1
	}
	d.list = append(d.list, x)
}

func (d *diags) errorf(code string, sp span, path, ruleID, hint, format string, a ...any) {
	d.add(SevError, code, sp, path, ruleID, fmt.Sprintf(format, a...), hint)
}

func (d *diags) warnf(code string, sp span, path, ruleID, hint, format string, a ...any) {
	d.add(SevWarning, code, sp, path, ruleID, fmt.Sprintf(format, a...), hint)
}

func (d *diags) hasErrors() bool {
	for _, x := range d.list {
		if x.Severity == SevError {
			return true
		}
	}
	return false
}

// finish sorts by position (errors before warnings at the same position), removes exact
// duplicates and caps the list at MaxDiagnostics with a final "and N more" entry.
func finish(list []Diagnostic) []Diagnostic {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Col != b.Col {
			return a.Col < b.Col
		}
		if a.Severity != b.Severity {
			return a.Severity == SevError
		}
		return a.Code < b.Code
	})
	out := list[:0]
	for i, x := range list {
		if i > 0 {
			p := list[i-1]
			if p.Line == x.Line && p.Col == x.Col && p.Code == x.Code && p.Message == x.Message {
				continue
			}
		}
		out = append(out, x)
	}
	if len(out) > MaxDiagnostics {
		rest := out[MaxDiagnostics:]
		ne, nw := 0, 0
		for _, x := range rest {
			if x.Severity == SevError {
				ne++
			} else {
				nw++
			}
		}
		sev, code := SevWarning, "more_diagnostics"
		if ne > 0 {
			sev = SevError
		}
		more := Diagnostic{Severity: sev, Code: code, File: out[0].File, DocURL: DocBase + code,
			Message: fmt.Sprintf("And %s more (%s, %s).", thousands(len(rest)), plural(ne, "error", "errors"), plural(nw, "warning", "warnings")),
			Hint:    "Fix the ones above and run lint again."}
		out = append(out[:MaxDiagnostics:MaxDiagnostics], more)
	}
	return out
}

// split separates errors and warnings, keeping order.
func split(list []Diagnostic) (errs, warns []Diagnostic) {
	errs, warns = []Diagnostic{}, []Diagnostic{}
	for _, x := range list {
		if x.Severity == SevError {
			errs = append(errs, x)
		} else {
			warns = append(warns, x)
		}
	}
	return errs, warns
}

// thousands formats n with comma separators: 2400000 → "2,400,000".
func thousands[T int | int64 | uint64](n T) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return thousands(n) + " " + many
}

// clip shortens the user's own text for quoting in a message (at most maxSnippet runes), and
// replaces control characters so a message never carries them.
func clip(s string) string { return clipN(s, maxSnippet) }

func clipN(s string, n int) string {
	var b strings.Builder
	i := 0
	for _, r := range s {
		if i == n {
			b.WriteString("…")
			break
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || isBidiOrInvisible(r) {
			if r == '\n' || r == '\t' {
				r = ' '
			} else {
				b.WriteString(fmt.Sprintf("U+%04X", r))
				i++
				continue
			}
		}
		b.WriteRune(r)
		i++
	}
	return b.String()
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }
