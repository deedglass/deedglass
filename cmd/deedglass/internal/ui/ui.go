// Package ui is the deedglass CLI's visual language (brand §13.3): symbols, colour and alignment.
// Colour is used only on a terminal, never when NO_COLOR is set, TERM is dumb or --no-color is
// given, and it only ever repeats what the symbol and text already say.
package ui

import (
	"os"
	"strings"
	"unicode/utf8"
)

// Symbols.
const (
	OK      = "✓" // verified, valid (teal)
	Fail    = "✕" // mismatch, denied, error (red)
	Pending = "◐" // pending (violet)
	Unknown = "○" // unverifiable, notice (amber)
	Sep     = "·"
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

func (s Style) Teal(t string) string   { return s.wrap("36", t) } // cyan stands in for teal
func (s Style) Red(t string) string    { return s.wrap("31", t) }
func (s Style) Violet(t string) string { return s.wrap("35", t) } // magenta stands in for violet
func (s Style) Amber(t string) string  { return s.wrap("33", t) } // yellow stands in for amber
func (s Style) Dim(t string) string    { return s.wrap("2", t) }
func (s Style) Bold(t string) string   { return s.wrap("1", t) }

// Symbol lines.
func (s Style) OKLine(t string) string      { return s.Teal(OK) + " " + t }
func (s Style) FailLine(t string) string    { return s.Red(Fail) + " " + t }
func (s Style) UnknownLine(t string) string { return s.Amber(Unknown) + " " + t }

// Pad right-pads t to width w, counting runes, not bytes.
func Pad(t string, w int) string {
	n := utf8.RuneCountInString(t)
	if n >= w {
		return t
	}
	return t + strings.Repeat(" ", w-n)
}

// Width is the display width of t in runes.
func Width(t string) int { return utf8.RuneCountInString(t) }
