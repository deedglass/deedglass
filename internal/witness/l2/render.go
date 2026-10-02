package l2

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/witness"
)

// Styler colours the diff on a terminal (the CLI's ui.Style). A nil Styler, or NO_COLOR, prints
// plain text: the symbols and words carry the meaning, colour only repeats it (brand §13.3).
type Styler interface {
	Teal(string) string
	Red(string) string
	Amber(string) string
	Dim(string) string
}

type plain struct{}

func (plain) Teal(s string) string  { return s }
func (plain) Red(s string) string   { return s }
func (plain) Amber(s string) string { return s }
func (plain) Dim(s string) string   { return s }

// RenderOptions set the terminal: its width (80 and 120 are the goldens; anything under 100 is
// narrow) and its colours.
type RenderOptions struct {
	Width  int
	Style  Styler
	Indent string // before every line; "  " in deedglass act and actions show
}

// Symbols (brand §13.3).
const (
	symOK    = "✓"
	symFail  = "✕"
	symGap   = "○"
	symSep   = "·"
	symArrow = "→"
	symMore  = "…"
)

// Render writes levels.L2 of a detail as S21 §0 prints it: the expected and observed pair, the row
// table, the truncation line and the "Witnessed by Postgres" footer. It returns nil when the detail
// has no L2 evidence. The claim line itself (L2 · MISMATCH · summary) is the caller's.
func Render(d witness.Detail, o RenderOptions) []string {
	l2 := d.Levels.L2
	if l2 == nil || l2.L2Evidence == nil {
		return nil
	}
	ev := l2.L2Evidence
	st := o.Style
	if st == nil {
		st = plain{}
	}
	narrow := o.Width > 0 && o.Width < 100
	cell := 36
	if narrow {
		cell = 22
	}
	var out []string
	line := func(s string) { out = append(out, strings.TrimRight(o.Indent+s, " ")) }
	label := func(s string) string { return st.Dim(pad(s, 11)) }
	if e := ev.Expected; e != nil {
		line(label("expected") + expectedText(e))
	}
	if ev.Observed != nil {
		line(label("observed") + observedText(ev, d.Reasons, narrow))
	}
	if len(ev.Rows) > 0 {
		line("")
		out = append(out, rowTable(ev, st, o.Indent, cell)...)
	}
	if ev.RowsMore > 0 {
		total := ev.RowsShown + ev.RowsMore
		line(st.Dim(pad(symMore, 17) + fmt.Sprintf("first %s of %s rows %s the rest in Console › Actions", count(ev.RowsShown), count(total), symSep)))
	}
	if ev.ColumnsMore > 0 {
		line(st.Dim(pad("", 17) + fmt.Sprintf("%d more changed %s in Console › Actions", ev.ColumnsMore, plural(int64(ev.ColumnsMore), "column", "columns"))))
	}
	if len(ev.Redacted) > 0 {
		line(st.Dim(pad("", 17) + "••• hides " + strings.Join(ev.Redacted, ", ") + " (witness.sensitive_columns)"))
	}
	if t := ev.Transaction; t != nil {
		line("")
		line(Footer(t))
	}
	return out
}

// Footer is "Witnessed by Postgres · commit 0/3E65020 · 2026-10-07 13:12:09 UTC" (the Console's
// footer copy, CopyFooter).
func Footer(t *witness.L2Transaction) string {
	at := t.CommittedAt
	if ts, err := time.Parse("2006-01-02T15:04:05.000Z", t.CommittedAt); err == nil {
		at = ts.UTC().Format("2006-01-02 15:04:05") + " UTC"
	}
	return fmt.Sprintf(CopyFooter, t.CommitLSN, at)
}

func pad(s string, w int) string {
	n := utf8.RuneCountInString(s)
	if n >= w {
		return s + " "
	}
	return s + strings.Repeat(" ", w-n)
}

func clipCell(s string, w int) string {
	if utf8.RuneCountInString(s) <= w {
		return s
	}
	r := []rune(s)
	return string(r[:w-1]) + symMore
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// count writes a number with thousands separators (1,204), as the brand writes numbers.
func count(n int) string {
	s := strconv.Itoa(n)
	if n < 0 || len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func opWord(op string) string { return strings.ToUpper(op) }

// expectedText is the footprint in one line: "UPDATE 1 row in public.orders where id = 42 · sets status".
func expectedText(e *witness.L2Expected) string {
	if e.Derivation == DerivationUnknown {
		return "can't be predicted (" + WhyWords(e.Why) + ")"
	}
	parts := make([]string, 0, len(e.Tables))
	for _, t := range e.Tables {
		ops := make([]string, len(t.Ops))
		for i, op := range t.Ops {
			ops[i] = opWord(op)
		}
		s := strings.Join(ops, " or ")
		switch {
		case t.Rows != nil:
			s += " " + count(int(*t.Rows)) + " " + plural(*t.Rows, "row", "rows")
		case t.MaxRows != nil:
			s += " at most " + count(int(*t.MaxRows)) + " " + plural(*t.MaxRows, "row", "rows")
		default:
			s += " rows"
		}
		s += " in " + t.Table
		if len(t.Keys) > 0 {
			s += " where " + keysText(TableExpect{Keys: t.Keys}, TableInfo{})
		}
		if len(t.Columns) > 0 && slices.Contains(t.Ops, OpUpdate) {
			s += " " + symSep + " sets " + strings.Join(t.Columns, ", ")
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " "+symSep+" ")
}

// observedText is what committed, from the exact counts: "UPDATE 301 rows in public.orders · 300
// outside id = 42".
func observedText(ev *witness.L2Evidence, reasons []witness.Reason, narrow bool) string {
	var parts []string
	for _, t := range ev.Observed.Tables {
		var ops []string
		for _, x := range []struct {
			op string
			n  int
		}{{OpInsert, t.Insert}, {OpUpdate, t.Update}, {OpDelete, t.Delete}} {
			if x.n > 0 {
				ops = append(ops, opWord(x.op)+" "+count(x.n)+" "+plural(int64(x.n), "row", "rows"))
			}
		}
		if t.Truncate > 0 {
			ops = append(ops, "TRUNCATE")
		}
		if len(ops) == 0 {
			continue
		}
		parts = append(parts, strings.Join(ops, ", ")+" in "+t.Table)
	}
	if len(parts) == 0 {
		parts = []string{"no rows changed"}
	}
	s := strings.Join(parts, " "+symSep+" ")
	for _, r := range reasons {
		if r.Code != witness.CodeUnexpectedKey || r.Level != witness.L2 {
			continue
		}
		n, _ := r.Evidence["outside"].(int64)
		if n == 0 {
			if f, ok := r.Evidence["outside"].(float64); ok {
				n = int64(f)
			}
		}
		keys := ""
		if e := ev.Expected; e != nil {
			for _, t := range e.Tables {
				if len(t.Keys) > 0 {
					keys = keysText(TableExpect{Keys: t.Keys}, TableInfo{})
				}
			}
		}
		if n > 0 {
			if narrow {
				s += " " + symSep + " " + count(int(n)) + " outside"
			} else {
				s += " " + symSep + " " + count(int(n)) + " outside " + keys
			}
		}
	}
	return s
}

// rowTable is the aligned rows: a label column (✓ expected / ✕ unexpected), the key columns, then
// one column per changed column with "before → after".
func rowTable(ev *witness.L2Evidence, st Styler, indent string, cellW int) []string {
	tables := map[string]bool{}
	var keyCols, cols []string
	for _, r := range ev.Rows {
		tables[r.Table] = true
		for k := range r.Key {
			if !slices.Contains(keyCols, k) {
				keyCols = append(keyCols, k)
			}
		}
		for _, c := range r.Cells {
			if !slices.Contains(cols, c.Column) {
				cols = append(cols, c.Column)
			}
		}
	}
	sort.Strings(keyCols)
	head := "row"
	if len(tables) == 1 {
		for t := range tables {
			head = t
		}
	}
	labelW := max(utf8.RuneCountInString(head), utf8.RuneCountInString(symFail+" unexpected")) + 4
	grid := [][]string{}
	for _, r := range ev.Rows {
		row := []string{}
		for _, k := range keyCols {
			row = append(row, clipCell(r.Key[k], cellW))
		}
		for _, c := range cols {
			v := ""
			for _, x := range r.Cells {
				if x.Column == c {
					v = cellText(x, r.Op)
				}
			}
			row = append(row, clipCell(v, cellW))
		}
		grid = append(grid, row)
	}
	heads := append(append([]string{}, keyCols...), cols...)
	widths := make([]int, len(heads))
	for i, h := range heads {
		widths[i] = utf8.RuneCountInString(h)
		for _, row := range grid {
			widths[i] = max(widths[i], utf8.RuneCountInString(row[i]))
		}
	}
	join := func(cells []string) string {
		var b strings.Builder
		for i, c := range cells {
			if i == len(cells)-1 {
				b.WriteString(c)
			} else {
				b.WriteString(pad(c, widths[i]+3))
			}
		}
		return b.String()
	}
	out := []string{strings.TrimRight(indent+st.Dim(pad(head, labelW)+join(heads)), " ")}
	for i, r := range ev.Rows {
		lbl := st.Teal(symOK) + " " + "expected"
		w := utf8.RuneCountInString(symOK + " expected")
		if !r.Expected {
			lbl = st.Red(symFail) + " " + "unexpected"
			w = utf8.RuneCountInString(symFail + " unexpected")
		}
		out = append(out, strings.TrimRight(indent+lbl+strings.Repeat(" ", labelW-w)+join(grid[i]), " "))
	}
	return out
}

// cellText is "paid → shipped", "→ shipped" for an insert, "paid → deleted" for a delete.
func cellText(c witness.L2Cell, op string) string {
	v := func(p *string) string {
		if p == nil {
			return "NULL"
		}
		return *p
	}
	switch op {
	case OpInsert:
		return symArrow + " " + v(c.After)
	case OpDelete:
		return v(c.Before) + " " + symArrow + " deleted"
	}
	if c.Before == nil && c.After != nil {
		return "? " + symArrow + " " + v(c.After)
	}
	return v(c.Before) + " " + symArrow + " " + v(c.After)
}

// AgentLine is the line an MCP tool result carries when the verdict isn't VERIFIED (S21 §3.9):
// "Deedglass witness · L2 · MISMATCH · <summary>". The agent can repeat it word for word.
func AgentLine(level witness.Level, verdict witness.Verdict, summary string) string {
	return "Deedglass witness " + symSep + " " + string(level) + " " + symSep + " " + string(verdict) + " " + symSep + " " + summary
}

// AgentPending is the line when L2 is still waiting as the call's wait ends.
const AgentPending = "Deedglass witness " + symSep + " pending " + symSep + " Postgres's change log hasn't shown this write yet. The verdict will appear in Console › Actions."
