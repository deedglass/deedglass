package policy

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// LintResult is the outcome of linting one file. `deedglass policy lint --json` prints it, and
// S12's POST /v1/policies/lint returns the same object, so the CLI and the Console agree.
type LintResult struct {
	Object      string       `json:"object"` // "policy_lint_result"
	File        string       `json:"file"`
	Valid       bool         `json:"valid"`
	Name        *string      `json:"name"`
	VersionHash *string      `json:"version_hash"`
	Rules       int          `json:"rules"`
	Budgets     int          `json:"budgets"`
	Errors      []Diagnostic `json:"errors"`
	Warnings    []Diagnostic `json:"warnings"`

	compiled *Compiled
}

// Compiled returns the accepted policy, or nil.
func (r *LintResult) Compiled() *Compiled { return r.compiled }

// Lint compiles src and summarizes the result.
func Lint(src []byte, opts Options) *LintResult {
	c, list, p := compile(src, opts)
	r := &LintResult{Object: "policy_lint_result", File: opts.File, compiled: c}
	r.Errors, r.Warnings = split(list)
	if p != nil {
		if p.name != "" {
			n := p.name
			r.Name = &n
		}
		r.Rules, r.Budgets = len(p.rules), len(p.budgets)
	}
	if c != nil {
		r.Valid = true
		h := c.VersionHash
		r.VersionHash = &h
	}
	return r
}

// JSON renders the result with two-space indentation and no HTML escaping.
func (r *LintResult) JSON() []byte {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(r)
	return []byte(b.String())
}

// Painter colours text; the CLI passes its terminal style, tests pass Plain.
type Painter interface {
	Red(string) string
	Amber(string) string
	Teal(string) string
	Dim(string) string
}

// Plain is a Painter without colour.
type Plain struct{}

func (Plain) Red(s string) string   { return s }
func (Plain) Amber(s string) string { return s }
func (Plain) Teal(s string) string  { return s }
func (Plain) Dim(s string) string   { return s }

// ShortHash renders "sha256:3f9a…c21e" (brand §12).
func ShortHash(h string) string {
	hex := strings.TrimPrefix(h, "sha256:")
	if len(hex) < 8 {
		return h
	}
	return "sha256:" + hex[:4] + "…" + hex[len(hex)-4:]
}

// PlainText turns a message for a terminal: code spans lose their backticks, and a final period
// right after a code span is dropped so it isn't read as part of the code.
func PlainText(s string) string {
	if strings.HasSuffix(s, "`.") {
		body := strings.TrimSuffix(s, "`.")
		if i := strings.LastIndex(body, "`"); i >= 0 && strings.ContainsAny(body[i+1:], " '\"=<>!(") {
			s = strings.TrimSuffix(s, ".")
		}
	}
	return strings.ReplaceAll(s, "`", "")
}

// keyCodes get one line of context above the excerpt: the mapping the key sits in.
var keyCodes = map[string]bool{"unknown_key": true, "yaml_duplicate_key": true, "invalid_label": true}

// RenderText writes the diagnostics of one file with source excerpts and carets, then a summary
// line. display is the file name to show.
func RenderText(w io.Writer, r *LintResult, src []byte, display string, p Painter) {
	RenderDiagnostics(w, ordered(r), src, display, p)
	fmt.Fprintln(w, Summary(r, display, p))
}

// RenderDiagnostics writes diagnostics with source excerpts and carets, without a summary line
// (the test runner shows cases-file problems this way).
func RenderDiagnostics(w io.Writer, list []Diagnostic, src []byte, display string, p Painter) {
	s := newSource(src)
	for _, d := range list {
		sym := p.Red("✕")
		if d.Severity == SevWarning {
			sym = p.Amber("○")
		}
		loc := display
		if d.Line > 0 {
			loc = fmt.Sprintf("%s:%d:%d", display, d.Line, d.Col)
		}
		fmt.Fprintf(w, "%s %s · %s\n", sym, loc, d.Code)
		fmt.Fprintf(w, "  %s\n", PlainText(d.Message))
		if d.Line > 0 && d.Line <= len(s.lines) {
			fmt.Fprintln(w)
			excerpt(w, s, d, p)
		}
		if d.Hint != "" {
			fmt.Fprintf(w, "  %s %s\n", p.Dim("hint:"), PlainText(d.Hint))
		}
		fmt.Fprintln(w)
	}
}

// ordered merges errors and warnings back into position order (the cap was applied by Compile;
// the "and N more" entry stays last).
func ordered(r *LintResult) []Diagnostic {
	all := append(append([]Diagnostic{}, r.Errors...), r.Warnings...)
	var more []Diagnostic
	out := all[:0]
	for _, d := range all {
		if d.Code == "more_diagnostics" {
			more = append(more, d)
			continue
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		if out[i].Col != out[j].Col {
			return out[i].Col < out[j].Col
		}
		return out[i].Severity == SevError && out[j].Severity != SevError
	})
	return append(out, more...)
}

// Summary is the one-line verdict for a file.
func Summary(r *LintResult, display string, p Painter) string {
	nw := len(r.Warnings)
	if !r.Valid {
		parts := []string{p.Red("✕") + " " + plural(len(r.Errors), "error", "errors")}
		if nw > 0 {
			parts = append(parts, p.Amber("○")+" "+plural(nw, "warning", "warnings"))
		}
		parts = append(parts, display+" was not accepted")
		return strings.Join(parts, " · ")
	}
	name := display
	if r.Name != nil {
		name = *r.Name
	}
	parts := []string{p.Teal("✓") + " " + name, plural(r.Rules, "rule", "rules"), plural(r.Budgets, "budget", "budgets"), ShortHash(*r.VersionHash)}
	if nw > 0 {
		parts = append(parts, p.Amber("○")+" "+plural(nw, "warning", "warnings"))
	}
	return strings.Join(parts, " · ")
}

// excerpt prints the source line (and one line of context for key diagnostics) with carets.
func excerpt(w io.Writer, s *source, d Diagnostic, p Painter) {
	first := d.Line
	if keyCodes[d.Code] && d.Line > 1 && strings.TrimSpace(s.line(d.Line-1)) != "" {
		first = d.Line - 1
	}
	width := max(3, len(fmt.Sprint(d.Line)))
	line := []rune(s.line(d.Line))
	startCol := max(d.Col, 1)
	endCol := startCol + 1
	if d.EndLine == d.Line && d.EndCol > startCol {
		endCol = d.EndCol
	} else if d.EndLine > d.Line {
		endCol = len(line) + 1
	}
	// Window long lines to maxSnippet runes around the caret.
	off := 0
	if len(line) > maxSnippet {
		off = max(0, min(startCol-1-maxSnippet/4, len(line)-maxSnippet))
	}
	for ln := first; ln <= d.Line; ln++ {
		text := []rune(s.line(ln))
		if ln == d.Line {
			text = line
		}
		fmt.Fprintf(w, "  %s %s %s\n", p.Dim(fmt.Sprintf("%*d", width, ln)), p.Dim("│"), window(text, off))
	}
	n := max(1, min(endCol, len(line)+1)-startCol)
	if startCol-1-off+n > maxSnippet+1 {
		n = max(1, maxSnippet+1-(startCol-1-off))
	}
	carets := strings.Repeat("^", n)
	if d.Severity == SevError {
		carets = p.Red(carets)
	} else {
		carets = p.Amber(carets)
	}
	pad := strings.Repeat(" ", max(0, startCol-1-off))
	if off > 0 {
		pad += " "
	}
	fmt.Fprintf(w, "  %s %s %s%s\n", strings.Repeat(" ", width), p.Dim("│"), pad, carets)
}

// window shows runes [off, off+maxSnippet) of a line with … at cut ends, and makes control and
// bidirectional characters visible so the terminal shows the line as it is.
func window(text []rune, off int) string {
	var b strings.Builder
	if off > 0 {
		b.WriteString("…")
	}
	end := len(text)
	if end-off > maxSnippet {
		end = off + maxSnippet
	}
	for _, r := range text[off:end] {
		switch {
		case r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || isBidiOrInvisible(r):
			b.WriteRune('�')
		default:
			b.WriteRune(r)
		}
	}
	if end < len(text) {
		b.WriteString("…")
	}
	return b.String()
}

// RenderGitHub writes GitHub Actions workflow commands, one per diagnostic.
func RenderGitHub(w io.Writer, r *LintResult, display string) {
	for _, d := range ordered(r) {
		kind := "error"
		if d.Severity == SevWarning {
			kind = "warning"
		}
		msg := d.Code + ": " + PlainText(d.Message)
		if d.Hint != "" {
			msg += " " + PlainText(d.Hint)
		}
		fmt.Fprintf(w, "::%s file=%s,line=%d,col=%d::%s\n", kind, ghProp(display), max(d.Line, 1), max(d.Col, 1), ghData(msg))
	}
}

func ghData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func ghProp(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}
