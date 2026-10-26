package ui

import (
	"io"
	"strings"
	"unicode/utf8"
)

// Layout helpers (docs/design/CLI_STYLE.md). Every command builds its text output from these, so
// spacing, alignment and wrapping are the same everywhere:
//
//	Header    one line: the title in bold, then dim meta ("deedglass scan  agentproj · read-only")
//	Section   a bold title over an indented block, with one blank line before it
//	KV        aligned key/value rows: dim keys padded to the longest, values wrapped under themselves
//	Table     a dim UPPERCASE header row, two-space gutters, cells clipped with …
//	Next      "  next  <command>", the last line of an error or a result
//
// Widths are display columns (Width), never bytes, and every helper keeps to MaxWidth.

// MaxWidth is the widest any helper lays text out, even on a wider terminal: a block that reads
// well at 80 columns reads well everywhere, and the eye doesn't travel.
const MaxWidth = 80

// Indent is one level of indentation: a block's body sits one level under its first line.
const Indent = "  "

// LayoutWidth is the width to lay text out at: the terminal's, capped at MaxWidth; 0 (unknown, a
// pipe) is MaxWidth.
func LayoutWidth(term int) int {
	if term <= 0 || term > MaxWidth {
		return MaxWidth
	}
	return term
}

// ShortID shows an id as its prefix and the first eight characters after it, then …:
// act_01m4x8kq…. Text output only; --json, -v and any command a person copies carry the full id.
func ShortID(id string) string {
	i := strings.IndexByte(id, '_')
	if i < 0 || len(id) <= i+9 {
		return id
	}
	return id[:i+9] + More
}

// Header is a command's first line: the title in bold, then the meta in dim, two spaces apart.
// Meta items are joined with " · "; keep them to three.
func (s Style) Header(title string, meta ...string) string {
	var m []string
	for _, x := range meta {
		if x != "" {
			m = append(m, x)
		}
	}
	if len(m) == 0 {
		return s.Bold(title)
	}
	return s.Bold(title) + "  " + s.Dim(strings.Join(m, " "+Sep+" "))
}

// Section is a bold title; the caller prints a blank line before it and indents the block under it.
func (s Style) Section(title string) string { return s.Bold(title) }

// Status is a status line: the glyph in its role's colour, a space, the text.
func (s Style) Status(glyph, text string) string {
	switch glyph {
	case OK:
		return s.Success(glyph) + " " + text
	case Fail:
		return s.Error(glyph) + " " + text
	case Pending, Unknown:
		return s.Warn(glyph) + " " + text
	case Bullet:
		return s.Dim(glyph) + " " + text
	}
	return glyph + " " + text
}

// Next is the "what to do now" line under a result or an error: the dim label, then the command.
func (s Style) Next(cmd string) string { return Indent + s.Dim("next") + "  " + cmd }

// Row is one key/value row of a KV block. Glyph, when set, is a status glyph drawn in its role's
// colour before the value, with the value's later lines indented under the text, not the glyph.
// Tag, when set, is a dim word at the right edge of the row's first line (deedglass play's live
// and replay).
type Row struct {
	Key, Glyph, Value, Tag string
}

// KV lays rows out as an aligned block: indent, the key in dim padded to the longest key, two
// spaces, the value. A value that doesn't fit wraps under itself (never under the key); a value
// with newlines keeps them, each line wrapped. Rows with an empty key continue the row above.
// Values are plain text (they are measured and wrapped); colour comes from Glyph.
func (s Style) KV(indent string, width int, rows ...Row) []string {
	width = LayoutWidth(width)
	kw := 0
	for _, r := range rows {
		kw = max(kw, Width(r.Key))
	}
	col := Width(indent) + kw + 2
	var out []string
	for _, r := range rows {
		avail := width - col
		lead, hang := "", ""
		if r.Glyph != "" {
			lead = s.Status(r.Glyph, "")
			hang = strings.Repeat(" ", Width(r.Glyph)+1)
			avail -= Width(hang)
		}
		tagW := 0
		if r.Tag != "" {
			tagW = Width(r.Tag) + 2
		}
		first := true
		for _, para := range strings.Split(r.Value, "\n") {
			for _, l := range wrapTo(para, avail, tagW, first) {
				key := strings.Repeat(" ", kw)
				pre := hang
				if first {
					key = s.Dim(Pad(r.Key, kw))
					pre = lead
				}
				line := strings.TrimRight(indent+key+"  "+pre+l, " ")
				if first && r.Tag != "" {
					pad := width - Width(stripANSI(line)) - Width(r.Tag)
					line += strings.Repeat(" ", max(2, pad)) + s.Dim(r.Tag)
				}
				out = append(out, line)
				first = false
			}
		}
	}
	return out
}

// wrapTo wraps t (which may hold ANSI codes) to avail columns; the first line of the first
// paragraph leaves room for a tag.
func wrapTo(t string, avail, tagW int, first bool) []string {
	if avail < 20 {
		return []string{t}
	}
	plain := stripANSI(t)
	limit := avail
	if first {
		limit -= tagW
	}
	if Width(plain) <= limit {
		return []string{t}
	}
	head := Wrap(t, limit, 0)
	if len(head) == 1 {
		return head
	}
	rest := strings.Join(strings.Fields(strings.Join(head[1:], " ")), " ")
	return append(head[:1], Wrap(rest, avail, 0)...)
}

// stripANSI removes SGR sequences, for measuring styled text.
func stripANSI(t string) string {
	if !strings.Contains(t, "\x1b[") {
		return t
	}
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		if t[i] == 0x1b && i+1 < len(t) && t[i+1] == '[' {
			j := i + 2
			for j < len(t) && (t[j] >= '0' && t[j] <= '9' || t[j] == ';') {
				j++
			}
			if j < len(t) {
				i = j
				continue
			}
		}
		b.WriteByte(t[i])
	}
	return b.String()
}

// StripANSI is stripANSI for callers that measure styled text.
func StripANSI(t string) string { return stripANSI(t) }

// Table lays out rows under a dim UPPERCASE header with two-space gutters. Column i is at most
// limit[i] columns wide (0: no limit); a longer cell is clipped with …. Cells may be styled: their
// width is measured without the codes, and a styled cell is never clipped.
func (s Style) Table(header []string, rows [][]string, limit []int) []string {
	n := len(header)
	w := make([]int, n)
	for i, h := range header {
		w[i] = Width(h)
	}
	cell := func(r []string, i int) string {
		if i >= len(r) {
			return ""
		}
		c := r[i]
		if i < len(limit) && limit[i] > 0 && stripANSI(c) == c && Width(c) > limit[i] {
			c = clipTo(c, limit[i])
		}
		return c
	}
	for _, r := range rows {
		for i := range n {
			w[i] = max(w[i], Width(stripANSI(cell(r, i))))
		}
	}
	line := func(cells []string, dim bool) string {
		var b strings.Builder
		for i, c := range cells {
			if i == len(cells)-1 {
				b.WriteString(c)
				break
			}
			b.WriteString(c + strings.Repeat(" ", w[i]-Width(stripANSI(c))+2))
		}
		l := strings.TrimRight(b.String(), " ")
		if dim {
			return s.Dim(l)
		}
		return l
	}
	out := []string{line(header, true)}
	for _, r := range rows {
		cells := make([]string, n)
		for i := range n {
			cells[i] = cell(r, i)
		}
		out = append(out, line(cells, false))
	}
	return out
}

func clipTo(t string, n int) string {
	if Width(t) <= n {
		return t
	}
	out, w := "", 0
	for _, r := range t {
		rw := Width(string(r))
		if w+rw > n-1 {
			break
		}
		out += string(r)
		w += rw
	}
	return out + More
}

// Lines writes each line with a newline.
func Lines(w io.Writer, lines ...string) {
	for _, l := range lines {
		io.WriteString(w, l+"\n")
	}
}

// Commands lays out commands a person can run next, each with what it does: the command as typed,
// padded to the longest, then the description in dim, wrapped under itself.
func (s Style) Commands(indent string, width int, rows ...[2]string) []string {
	width = LayoutWidth(width)
	cw := 0
	for _, r := range rows {
		cw = max(cw, Width(r[0]))
	}
	col := Width(indent) + cw + 2
	var out []string
	for _, r := range rows {
		if r[1] == "" {
			out = append(out, indent+r[0])
			continue
		}
		for i, l := range wrapTo(r[1], width-col, 0, false) {
			cmd := strings.Repeat(" ", cw)
			if i == 0 {
				cmd = Pad(r[0], cw)
			}
			out = append(out, indent+cmd+"  "+s.Dim(l))
		}
	}
	return out
}

// HangWrap wraps one line of an aligned block to width, continuing under the line's value column:
// the column after the last two-space gap in its first 40 columns ("  check      Proven …" continues
// under "Proven"). A line with no such gap continues under its own indent plus two.
func HangWrap(line string, width int) []string {
	width = LayoutWidth(width)
	plain := stripANSI(line)
	if Width(plain) <= width {
		return []string{line}
	}
	lead := len(plain) - len(strings.TrimLeft(plain, " "))
	col := lead + 2
	runes := []rune(plain)
	for i := lead; i+2 < len(runes) && i < 40; i++ {
		if runes[i] == ' ' && runes[i+1] == ' ' && runes[i+2] != ' ' {
			col = Width(string(runes[:i+2]))
		}
	}
	if width-col < 20 {
		col = lead + 2
	}
	// The prefix keeps its spacing exactly; only the value after the column wraps.
	head, rest := splitAtCol(line, col)
	parts := Wrap(rest, width-col, 0)
	out := []string{head + parts[0]}
	for _, p := range parts[1:] {
		out = append(out, strings.Repeat(" ", col)+p)
	}
	return out
}

// splitAtCol splits styled text at display column col, keeping escape codes with the side they
// precede.
func splitAtCol(t string, col int) (string, string) {
	w := 0
	for i := 0; i < len(t); {
		if t[i] == 0x1b && i+1 < len(t) && t[i+1] == '[' {
			j := i + 2
			for j < len(t) && (t[j] >= '0' && t[j] <= '9' || t[j] == ';') {
				j++
			}
			i = j + 1
			continue
		}
		if w >= col {
			return t[:i], t[i:]
		}
		r, n := utf8.DecodeRuneInString(t[i:])
		w += Width(string(r))
		i += n
	}
	return t, ""
}

// WrapWriter writes through w with every line wrapped by HangWrap. Writes are split at newlines;
// a partial line waits for its newline.
func WrapWriter(w io.Writer, width int) io.Writer { return &wrapWriter{w: w, width: width} }

type wrapWriter struct {
	w     io.Writer
	width int
	part  []byte
}

func (x *wrapWriter) Write(p []byte) (int, error) {
	x.part = append(x.part, p...)
	for {
		i := strings.IndexByte(string(x.part), '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(x.part[:i])
		x.part = x.part[i+1:]
		for _, l := range HangWrap(line, x.width) {
			if _, err := io.WriteString(x.w, l+"\n"); err != nil {
				return 0, err
			}
		}
	}
}
