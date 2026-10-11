package mark

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Depth is how much colour the terminal gets.
type Depth int

const (
	DepthNone  Depth = iota // no colour: a monochrome orb in shades, no escape codes
	DepthASCII              // no colour and no Unicode art: the inline (|) mark only
	Depth16                 // the 16 ANSI colours
	Depth256                // the xterm-256 palette
	DepthTrue               // 24-bit colour
)

// Theme is the terminal's background.
type Theme int

const (
	ThemeDark Theme = iota
	ThemeLight
)

// Options says what to draw and how. Detect fills the terminal fields; callers fill the rest.
type Options struct {
	Depth   Depth
	Theme   Theme
	Width   int    // terminal columns; 0 means unknown, so treat as 80
	Version string // "0.1.0"
	API     string // "2026-10-01"
	Command string // for Inline: "doctor", "init", "help"
	Next    string // the banner's last line (lines split on \n; `backticks` mark a command); empty omits it
	Quiet   bool   // no banner and no inline mark: not a TTY, CI, DEEDGLASS_NO_BANNER, --json or -q
	Anim    bool   // Sealed may animate: a truecolor TTY without DEEDGLASS_NO_ANIM
}

// Copy (S05X §5). Written once here.
const (
	Tagline    = "See what your agents actually did."
	DocsHost   = "docs.deedglass.com"
	NextInit   = "Run `deedglass init` to connect your first agent."
	SealedLine = "Sealed. Your first record is in the ledger."
)

const (
	orbRows  = orbSize / 2
	textCol  = orbSize + 4 // the text block starts 4 columns right of the orb
	fullMin  = 60          // narrower than this: no orb
	lineMin  = 40          // narrower than this: the inline mark and one line
	asciiSep = "-"
)

// px is one generated pixel; see orb_gen.go.
type px struct{ r, g, b, cov, region, x256 uint8 }

// colour is a text colour with its precomputed xterm-256 index.
type colour struct{ r, g, b, x256 uint8 }

const (
	regionNone = iota
	regionLeft
	regionPane
	regionRight
)

func (o Options) width() int {
	if o.Width <= 0 {
		return 80
	}
	return o.Width
}

// Banner returns the orb and the welcome text block, or "" when o.Quiet.
func Banner(o Options) string {
	if o.Quiet {
		return ""
	}
	var b strings.Builder
	b.Grow(12 << 10)
	text := textLines(o)
	w := o.width()
	switch {
	case o.Depth == DepthASCII || w < lineMin || w < fullMin:
		writeInline(&b, o, "", true)
		b.WriteByte('\n')
		if w < lineMin {
			if o.Next != "" {
				writeSegs(&b, o, nextLines(o.Next)[0])
			} else {
				writeSegs(&b, o, text[1])
			}
			b.WriteByte('\n')
			return b.String()
		}
		for _, l := range text[1:] {
			writeSegs(&b, o, l)
			b.WriteByte('\n')
		}
		return b.String()
	}
	beside := w >= textCol+textWidth(text)
	start := (orbRows - len(text)) / 2
	g := grid(o.Theme)
	for row := 0; row < orbRows; row++ {
		var line []seg
		if beside && row >= start && row-start < len(text) {
			line = text[row-start]
		}
		n := writeOrbRow(&b, o, g, row, 0, len(line) > 0)
		if len(line) > 0 {
			b.WriteString(strings.Repeat(" ", textCol-n))
			writeSegs(&b, o, line)
		}
		b.WriteByte('\n')
	}
	if !beside {
		b.WriteByte('\n')
		for _, l := range text {
			writeSegs(&b, o, l)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Inline returns "◖│◗ deedglass <command>" (ASCII: "(|) deedglass <command>"), or "" when
// o.Quiet. It has no trailing newline.
func Inline(o Options) string {
	if o.Quiet {
		return ""
	}
	var b strings.Builder
	b.Grow(128)
	writeInline(&b, o, o.Command, false)
	return b.String()
}

// writeInline writes the inline mark, the bold wordmark and either the command or (banner mode)
// the dimmed version.
func writeInline(b *strings.Builder, o Options, cmd string, version bool) {
	switch o.Depth {
	case DepthASCII:
		b.WriteString("(|)")
	case DepthNone:
		b.WriteString("◖│◗")
	default:
		left, right := cauroraBlue, cfrost
		if o.Theme == ThemeLight {
			right = cfrostLight
		}
		sgrFG(b, o, left, 4)
		b.WriteString("◖")
		if o.Theme == ThemeLight {
			b.WriteString("\x1b[39m")
		} else {
			sgrFG(b, o, cpaneWhite, 15)
		}
		b.WriteString("│")
		c16 := 7
		if o.Theme == ThemeLight {
			c16 = 8
		}
		sgrFG(b, o, right, c16)
		b.WriteString("◗\x1b[0m")
	}
	b.WriteByte(' ')
	segs := []seg{{styleTitle, "deedglass"}}
	if version && o.Version != "" {
		segs = append(segs, seg{stylePlain, "  "}, seg{styleDim, o.Version})
	}
	if cmd != "" {
		segs = append(segs, seg{stylePlain, " " + cmd})
	}
	writeSegs(b, o, segs)
}

func grid(t Theme) *[orbSize][orbSize]px {
	if t == ThemeLight {
		return &orbLight
	}
	return &orbDark
}

// --- the orb ---

// paint is what one pixel asks for.
type paint struct {
	set   bool // false: transparent
	deffg bool // the terminal's foreground (the pane on light terminals)
	c     colour
	c16   int
}

func pixelPaint(o Options, p px, y int) paint {
	if p.cov == 0 {
		return paint{}
	}
	pt := paint{set: true, c: colour{p.r, p.g, p.b, p.x256}}
	light := o.Theme == ThemeLight
	switch p.region {
	case regionLeft:
		pt.c16 = 4 // blue over cyan, like the aurora
		if y >= orbSize/2 {
			pt.c16 = 6
		}
	case regionPane:
		pt.c16 = 15
		pt.deffg = light
	case regionRight:
		pt.c16 = 7
		if light {
			pt.c16 = 8
		}
	}
	return pt
}

// sgr state, tracked so a row emits only changes.
type pen struct {
	fg, bg paint // set == false means the terminal default
}

// writeOrbRow writes one row of cells from pixel rows 2·row and 2·row+1, shifting the left half
// by -shift and the right half by +shift (the Sealed frames; 0 for the banner), and returns
// the columns written. Trailing transparent cells are trimmed unless keep is set.
func writeOrbRow(b *strings.Builder, o Options, g *[orbSize][orbSize]px, row, shift int, keep bool) int {
	cols := orbSize + 2*shift
	cell := func(y, col int) (px, int) {
		// Map the output column back to a source pixel.
		x := col - shift
		if x >= 0 && x < orbSize && g[y][x].region == regionPane {
			return g[y][x], x
		}
		if xl := col; xl >= 0 && xl < orbSize && g[y][xl].region == regionLeft {
			return g[y][xl], xl
		}
		if xr := col - 2*shift; xr >= 0 && xr < orbSize && g[y][xr].region == regionRight {
			return g[y][xr], xr
		}
		return px{}, -1
	}
	// The last column holding any pixel, so trailing cells can be trimmed.
	last := -1
	for col := 0; col < cols; col++ {
		if t, _ := cell(2*row, col); t.cov > 0 {
			last = col
		} else if u, _ := cell(2*row+1, col); u.cov > 0 {
			last = col
		}
	}
	n := last + 1
	if keep {
		n = cols
	}
	if o.Depth == DepthNone {
		cells := make([]string, n)
		for col := 0; col < n; col++ {
			t, _ := cell(2*row, col)
			u, _ := cell(2*row+1, col)
			cells[col] = monoCell(t, u)
		}
		if !keep {
			for n > 0 && cells[n-1] == " " {
				n--
			}
		}
		for _, c := range cells[:n] {
			b.WriteString(c)
		}
		return n
	}
	var cur pen
	for col := 0; col < n; col++ {
		tp, _ := cell(2*row, col)
		bp, _ := cell(2*row+1, col)
		t, u := pixelPaint(o, tp, 2*row), pixelPaint(o, bp, 2*row+1)
		var want pen
		var glyph string
		switch {
		case !t.set && !u.set:
			want, glyph = pen{fg: cur.fg}, " "
		case t.set && u.set && t.deffg && u.deffg:
			// The pane on a light terminal: reverse video paints the whole cell in the
			// terminal's foreground (a █ glyph leaves seams where fonts add line spacing).
			setPen(b, o, &cur, pen{})
			b.WriteString("\x1b[7m \x1b[27m")
			continue
		case t.set && u.set && t.deffg:
			want, glyph = pen{fg: t, bg: u}, "▀"
		case t.set && u.set:
			// ▄ with the upper pixel as the background: block glyphs sit low in some fonts,
			// and the background fills the leading above them with the right colour.
			want, glyph = pen{fg: u, bg: t}, "▄"
		case t.set:
			want, glyph = pen{fg: t}, "▀"
		default:
			want, glyph = pen{fg: u}, "▄"
		}
		setPen(b, o, &cur, want)
		b.WriteString(glyph)
	}
	if cur.fg.set || cur.bg.set {
		b.WriteString("\x1b[0m")
	}
	return n
}

// monoCell is a cell with no colour: shades where the cell is mostly inside the orb.
func monoCell(t, u px) string {
	if int(t.cov)+int(u.cov) < 255 {
		return " "
	}
	r := t.region
	if t.cov == 0 || (u.cov > 0 && u.region == regionPane) {
		r = u.region
	}
	switch r {
	case regionLeft:
		return "▓"
	case regionPane:
		return "│"
	case regionRight:
		return "░"
	}
	return " "
}

// same reports whether two paints render identically at this depth.
func same(o Options, a, b paint) bool {
	if a.set != b.set || a.deffg != b.deffg {
		return false
	}
	if a.deffg {
		return true
	}
	switch o.Depth {
	case Depth16:
		return a.c16 == b.c16
	case Depth256:
		return a.c.x256 == b.c.x256
	}
	return a.c == b.c
}

// setPen emits the SGR codes that move the terminal from cur to want.
func setPen(b *strings.Builder, o Options, cur *pen, want pen) {
	fgChange := !same(o, cur.fg, want.fg) && !(!cur.fg.set && !want.fg.set)
	bgChange := !same(o, cur.bg, want.bg) && !(!cur.bg.set && !want.bg.set)
	if !fgChange && !bgChange {
		return
	}
	var buf [48]byte
	p := append(buf[:0], "\x1b["...)
	if fgChange {
		p = appendColour(p, o, want.fg, false)
	}
	if bgChange {
		if fgChange {
			p = append(p, ';')
		}
		p = appendColour(p, o, want.bg, true)
	}
	p = append(p, 'm')
	b.Write(p)
	*cur = want
}

func appendColour(p []byte, o Options, c paint, bg bool) []byte {
	if !c.set || c.deffg {
		if bg {
			return append(p, "49"...)
		}
		return append(p, "39"...)
	}
	switch o.Depth {
	case Depth16:
		base := 30
		if bg {
			base = 40
		}
		if c.c16 >= 8 {
			base += 60
		}
		return strconv.AppendInt(p, int64(base+c.c16%8), 10)
	case Depth256:
		if bg {
			p = append(p, "48;5;"...)
		} else {
			p = append(p, "38;5;"...)
		}
		return strconv.AppendInt(p, int64(c.c.x256), 10)
	}
	if bg {
		p = append(p, "48;2;"...)
	} else {
		p = append(p, "38;2;"...)
	}
	p = strconv.AppendInt(p, int64(c.c.r), 10)
	p = append(p, ';')
	p = strconv.AppendInt(p, int64(c.c.g), 10)
	p = append(p, ';')
	return strconv.AppendInt(p, int64(c.c.b), 10)
}

// sgrFG writes a foreground colour at o's depth (text, not pixels).
func sgrFG(b *strings.Builder, o Options, c colour, c16 int) {
	var buf [32]byte
	p := append(buf[:0], "\x1b["...)
	p = appendColour(p, o, paint{set: true, c: c, c16: c16}, false)
	b.Write(append(p, 'm'))
}

// --- the text block ---

type style int

const (
	stylePlain style = iota
	styleTitle
	styleBody
	styleDim
	styleAccent
)

type seg struct {
	style style
	text  string
}

func textLines(o Options) [][]seg {
	sep := " · "
	if o.Depth == DepthASCII {
		sep = " " + asciiSep + " "
	}
	title := []seg{{styleTitle, "deedglass"}}
	if o.Version != "" {
		title = append(title, seg{stylePlain, "  "}, seg{styleDim, o.Version})
	}
	docs := DocsHost
	if o.API != "" {
		docs += sep + "API " + o.API
	}
	lines := [][]seg{title, {{styleBody, Tagline}}, nil, {{styleDim, docs}}}
	if o.Next != "" {
		for _, l := range nextLines(o.Next) {
			if o.Depth == DepthASCII {
				for i := range l {
					l[i].text = strings.ReplaceAll(l[i].text, "·", asciiSep)
				}
			}
			lines = append(lines, l)
		}
	}
	return lines
}

// nextLines splits Next into lines and `backticked` commands into accent segments.
func nextLines(next string) [][]seg {
	var out [][]seg
	for _, l := range strings.Split(next, "\n") {
		var segs []seg
		for i, part := range strings.Split(l, "`") {
			if part == "" {
				continue
			}
			st := styleDim
			if i%2 == 1 {
				st = styleAccent
			}
			segs = append(segs, seg{st, part})
		}
		out = append(out, segs)
	}
	return out
}

func textWidth(lines [][]seg) int {
	w := 0
	for _, l := range lines {
		n := 0
		for _, s := range l {
			n += utf8.RuneCountInString(s.text)
		}
		w = max(w, n)
	}
	return w
}

// writeSegs writes styled text; every styled segment ends with a reset.
func writeSegs(b *strings.Builder, o Options, segs []seg) {
	for _, s := range segs {
		code := segCode(o, s.style)
		if code == "" {
			b.WriteString(s.text)
			continue
		}
		b.WriteString(code)
		b.WriteString(s.text)
		b.WriteString("\x1b[0m")
	}
}

func segCode(o Options, st style) string {
	if o.Depth < Depth16 || st == stylePlain {
		return ""
	}
	if o.Depth == Depth16 {
		switch st {
		case styleTitle:
			return "\x1b[1m"
		case styleDim:
			return "\x1b[2m"
		case styleAccent:
			return "\x1b[34m"
		}
		return ""
	}
	light := o.Theme == ThemeLight
	var c colour
	switch st {
	case styleTitle, styleBody:
		if light {
			if st == styleTitle {
				return "\x1b[1m"
			}
			return ""
		}
		c = csnow
	case styleDim:
		c = cslate400
		if light {
			c = cslate500
		}
	case styleAccent:
		c = caccentDark
		if light {
			c = caccentLight
		}
	}
	var buf [40]byte
	p := append(buf[:0], "\x1b["...)
	if st == styleTitle {
		p = append(p, "1;"...)
	}
	p = appendColour(p, o, paint{set: true, c: c}, false)
	return string(append(p, 'm'))
}
