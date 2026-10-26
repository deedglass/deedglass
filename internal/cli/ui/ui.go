// Package ui is the deedglass CLI's visual language (brand §13.3, docs/design/CLI_STYLE.md):
// symbols, colour, spacing and alignment, shared by every command and by deedglass play.
// Colour is used only on a terminal, never when NO_COLOR is set, TERM is dumb or --no-color is
// given, and it only ever repeats what the symbol and text already say.
package ui

import (
	"os"
	"strings"
)

// Symbols (CLI_STYLE.md › Glyphs). Each has an ASCII stand-in (ASCIIWriter).
const (
	OK      = "✓" // verified, valid, done (success)
	Fail    = "✕" // mismatch, denied, error (error)
	Pending = "◐" // waiting for a person (warn)
	Unknown = "○" // can't tell, a notice (warn)
	Bullet  = "•" // a neutral list item or note (dim)
	Arrow   = "→" // a change: paid → shipped
	More    = "…" // elided: an id's middle, the rest of a list
	Sep     = "·" // joins two or three short facts in dim meta text, never more
	None    = "—" // not set
)

// Style renders text with or without ANSI colour (16-colour palette).
type Style struct{ Color bool }

// ColorAllowed applies the rules: stdout is a terminal, NO_COLOR is unset, TERM isn't dumb and
// --no-color is absent.
func ColorAllowed(isTTY bool, getenv func(string) string, noColorFlag bool) bool {
	if !isTTY || noColorFlag {
		return false
	}
	if _, set := lookup(getenv, "NO_COLOR"); set {
		return false
	}
	return getenv("TERM") != "dumb"
}

func lookup(getenv func(string) string, k string) (string, bool) {
	v := getenv(k)
	return v, v != ""
}

// IsTerminal reports whether f is a terminal: a character device other than the null device.
// (Stdlib only: no ioctl. /dev/null is a character device too, and must not get colour.)
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, null) {
		return false
	}
	return true
}

func (s Style) wrap(code, text string) string {
	if !s.Color || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// The five roles (CLI_STYLE.md › Palette). Only the ANSI-16 base colours, so the person's own
// terminal theme decides the exact shade on a light or a dark background; faint, not grey, for
// secondary text, because faint follows the foreground colour on both.
func (s Style) Accent(t string) string  { return s.wrap("36", t) } // the brand teal: ✓ and the one value to read
func (s Style) Success(t string) string { return s.wrap("36", t) } // the accent: verified is the brand colour
func (s Style) Warn(t string) string    { return s.wrap("33", t) } // ◐ ○: a person or a look is needed
func (s Style) Error(t string) string   { return s.wrap("31", t) } // ✕
func (s Style) Dim(t string) string     { return s.wrap("2", t) }  // keys, meta, provenance, hints
func (s Style) Bold(t string) string    { return s.wrap("1", t) }  // titles and the primary noun

// The brand names, kept for the call sites that read better with them.
func (s Style) Teal(t string) string   { return s.Success(t) }
func (s Style) Red(t string) string    { return s.Error(t) }
func (s Style) Amber(t string) string  { return s.Warn(t) }
func (s Style) Violet(t string) string { return s.Warn(t) } // pending shares the warn colour: one palette

// Symbol lines.
func (s Style) OKLine(t string) string      { return s.Teal(OK) + " " + t }
func (s Style) FailLine(t string) string    { return s.Red(Fail) + " " + t }
func (s Style) UnknownLine(t string) string { return s.Amber(Unknown) + " " + t }

// Pad right-pads t to width w, counting runes, not bytes.
func Pad(t string, w int) string {
	n := Width(t)
	if n >= w {
		return t
	}
	return t + strings.Repeat(" ", w-n)
}

// Width is the display width of t in terminal columns: East Asian wide and fullwidth runes take
// two (S32 §3.11), combining marks none.
func Width(t string) int {
	n := 0
	for _, r := range t {
		switch {
		case r < 0x300:
			n++
		case r >= 0x300 && r <= 0x36F, r == 0x200B, r == 0x200D, r >= 0xFE00 && r <= 0xFE0F:
		case wide(r):
			n += 2
		default:
			n++
		}
	}
	return n
}

// wide reports East Asian Wide (W) and Fullwidth (F) runes, by block.
func wide(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) || (r >= 0x2E80 && r <= 0x303E) || (r >= 0x3041 && r <= 0x33FF) ||
		(r >= 0x3400 && r <= 0x4DBF) || (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0xA000 && r <= 0xA4CF) ||
		(r >= 0xAC00 && r <= 0xD7A3) || (r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFE30 && r <= 0xFE4F) ||
		(r >= 0xFF00 && r <= 0xFF60) || (r >= 0xFFE0 && r <= 0xFFE6) || (r >= 0x1F300 && r <= 0x1F64F) ||
		(r >= 0x1F900 && r <= 0x1F9FF) || (r >= 0x20000 && r <= 0x3FFFD)
}

// Wrap breaks t into lines at most width columns wide, at spaces, keeping t's leading spaces on the
// first line and indenting the lines after it by hang more columns than that. A word longer than a
// line breaks after a "|" when it has one (a flag's value list); otherwise it gets a line of its own
// rather than being cut (a URL). width < 20 means no wrapping. ANSI codes take no width.
func Wrap(t string, width, hang int) []string {
	lead := len(t) - len(strings.TrimLeft(t, " "))
	if width < 20 || Width(stripANSI(t)) <= width {
		return []string{t}
	}
	type tok struct {
		s    string
		glue bool // no space before it
	}
	var toks []tok
	for _, w := range strings.Fields(t) {
		// A separator stays at the end of its line, never the start of the next.
		if (w == Sep || w == Arrow) && len(toks) > 0 {
			toks[len(toks)-1].s += " " + w
			continue
		}
		if Width(stripANSI(w)) <= width-lead-hang || !strings.Contains(w, "|") {
			toks = append(toks, tok{s: w})
			continue
		}
		parts := strings.SplitAfter(w, "|")
		for i, p := range parts {
			if p != "" {
				toks = append(toks, tok{s: p, glue: i > 0})
			}
		}
	}
	var out []string
	cur, curW := strings.Repeat(" ", lead), lead
	indent := strings.Repeat(" ", lead+hang)
	start := true
	for _, tk := range toks {
		sep := 1
		if tk.glue {
			sep = 0
		}
		ww := Width(stripANSI(tk.s))
		if !start && curW+sep+ww > width {
			out = append(out, cur)
			cur, curW, start = indent, lead+hang, true
		}
		if !start && sep == 1 {
			cur += " "
			curW++
		}
		cur += tk.s
		curW += ww
		start = false
	}
	return append(out, cur)
}
