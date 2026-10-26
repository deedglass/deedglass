package ui

import (
	"io"
	"runtime"
	"strings"
)

// asciiGlyphs are the stand-ins for the glyph set on a terminal that can't draw it (CLI_STYLE.md ›
// Glyphs). Each stand-in has the same width as its glyph where it can, so columns stay aligned.
var asciiGlyphs = strings.NewReplacer(
	OK, "+",
	Fail, "x",
	Pending, "~",
	Unknown, "o",
	Bullet, "-",
	Arrow, "->",
	More, "...",
	Sep, "-",
	None, "-",
	"│", "|",
	"─", "-",
	"⋮", ":",
)

// ASCIIOnly reports whether glyphs must be drawn in ASCII: on a terminal, when DEEDGLASS_ASCII is
// set, TERM is dumb, or the Windows console has no virtual-terminal support (no WT_SESSION and no
// TERM), the same rule the terminal mark uses. Off a terminal the bytes go to a file or a program,
// which reads UTF-8.
func ASCIIOnly(isTTY bool, getenv func(string) string) bool {
	if !isTTY {
		return false
	}
	if v := getenv("DEEDGLASS_ASCII"); v != "" && v != "0" && v != "false" {
		return true
	}
	term := getenv("TERM")
	if term == "dumb" {
		return true
	}
	return runtime.GOOS == "windows" && getenv("WT_SESSION") == "" && term == ""
}

// ASCII rewrites the glyph set in t as ASCII.
func ASCII(t string) string { return asciiGlyphs.Replace(t) }

// ASCIIWriter writes through w with the glyph set rewritten as ASCII. Each Write is rewritten on
// its own: the CLI writes whole lines, so a glyph is never split across two writes.
func ASCIIWriter(w io.Writer) io.Writer { return asciiWriter{w} }

type asciiWriter struct{ w io.Writer }

func (a asciiWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(a.w, ASCII(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}
