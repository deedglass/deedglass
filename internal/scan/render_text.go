package scan

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Style colours terminal text; the zero value prints plain text (NO_COLOR, --ci, pipes).
type Style struct {
	Red, Teal, Amber, Violet, Dim, Bold func(string) string
}

func (s Style) apply(f func(string) string, t string) string {
	if f == nil {
		return t
	}
	return f(t)
}

// Minus is the typographic minus the deductions use.
const Minus = "−"

// DedLabel is how a deduction id reads in a sentence.
func DedLabel(id string) string {
	switch id {
	case "role_superuser_or_bypassrls":
		return "superuser or BYPASSRLS"
	case "high_finding":
		return "high finding"
	case "medium_finding":
		return "medium finding"
	case "unchecked_write_path":
		return "unchecked write path"
	}
	return strings.ReplaceAll(id, "_", " ")
}

// DedText is what a deduction says: the finding's title, or the subject and its detail.
func DedText(d Deduction) string {
	if d.ID == "high_finding" || d.ID == "medium_finding" {
		return d.Detail
	}
	return d.Subject + " · " + d.Detail
}

func sevSymbol(st Style, sev string) string {
	switch sev {
	case "high":
		return st.apply(st.Red, "✕")
	case "medium", "low":
		return st.apply(st.Amber, "○")
	}
	return st.apply(st.Dim, "·")
}

func pad(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func lpad(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return strings.Repeat(" ", w-n) + s
	}
	return s
}

// wrap breaks text into lines of at most w runes.
func wrap(text string, w int) []string {
	if w < 20 {
		w = 20
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line == "" {
			line = word
			continue
		}
		if utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > w {
			out = append(out, line)
			line = word
			continue
		}
		line += " " + word
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// hang wraps t to width with later lines indented by ind; width 0 doesn't wrap.
func hang(t string, width int, ind string) []string {
	if width <= 0 || utf8.RuneCountInString(t) <= width {
		return []string{t}
	}
	first := wrap(t, width)
	rest := wrap(strings.Join(first[1:], " "), width-len(ind))
	out := first[:1]
	for _, l := range rest {
		out = append(out, ind+l)
	}
	return out
}

// Thousands writes 1234 as "1,234".
func Thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// ScoreBlock is the score line, the small print and the deductions: never one without the others.
func ScoreBlock(st Style, s Score) []string {
	return scoreBlock(st, s, 0)
}

// scoreBlock is ScoreBlock with the score line wrapped to width (0: not wrapped).
func scoreBlock(st Style, s Score, width int) []string {
	var out []string
	for _, l := range hang(ScoreLine(s), width, "  ") {
		out = append(out, st.apply(st.Bold, l))
	}
	out = append(out, "  "+SmallPrint)
	for _, d := range s.Deductions {
		pts := lpad("0", 4)
		if d.Points > 0 {
			pts = lpad(Minus+strconv.Itoa(d.Points), 4)
		}
		out = append(out, fmt.Sprintf("  %s  %s  %s", pts, pad(DedLabel(d.ID), 22), DedText(d)))
	}
	out = append(out, fmt.Sprintf("  %s points", lpad(strconv.Itoa(s.Points), 4)))
	return out
}

// RenderText is the terminal report (S32 §5). header is the first line ("deedglass scan · …").
func RenderText(r *Report, st Style, width int, header string, wrote string) string {
	if width <= 0 {
		width = 100
	}
	var b strings.Builder
	w := func(lines ...string) {
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	w(header, "")
	w(scoreBlock(st, r.Score, width)...)
	w("")
	if r.Summary.Configs == 0 && len(r.Dependencies) == 0 && len(r.EnvNames) == 0 && len(r.ClaudeSettings) == 0 {
		w(st.apply(st.Teal, "✓")+" No MCP client configs, agent settings, agent dependencies or database URL variables found under "+r.Dir+".", "")
	} else {
		w("Found")
		w(fmt.Sprintf("  %d MCP client %s · %d %s · %d %s that can write", r.Summary.Configs, plural(r.Summary.Configs, "config", "configs"),
			r.Summary.MCPServers, plural(r.Summary.MCPServers, "server", "servers"), r.Summary.WriteCapableTools, plural(r.Summary.WriteCapableTools, "tool", "tools")))
		if len(r.Dependencies) > 0 {
			var names []string
			for _, d := range r.Dependencies {
				names = append(names, d.Name)
			}
			w(fmt.Sprintf("  %d agent and database %s: %s", len(names), plural(len(names), "dependency", "dependencies"), strings.Join(uniq(names), ", ")))
		}
		var vars []string
		for _, e := range r.EnvNames {
			vars = append(vars, e.Names...)
		}
		if len(vars) > 0 {
			vars = uniq(vars)
			w(fmt.Sprintf("  %d database URL %s (names only): %s", len(vars), plural(len(vars), "variable", "variables"), strings.Join(vars, ", ")))
		}
		if n := len(r.ClaudeSettings); n > 0 {
			var paths []string
			for _, c := range r.ClaudeSettings {
				paths = append(paths, c.Path)
			}
			w(fmt.Sprintf("  %d Claude Code settings %s: %s", n, plural(n, "file", "files"), strings.Join(paths, ", ")))
		}
		w("")
	}
	if r.Walk.Stopped {
		w(st.apply(st.Amber, "○")+fmt.Sprintf(" Stopped after %s files. Narrow --dir to scan the rest.", Thousands(int64(r.Walk.Files))), "")
	}
	ind := "           "
	for _, f := range r.Findings {
		where := ""
		if len(f.Paths) > 0 {
			where = " · " + strings.Join(f.Paths, ", ")
		}
		w(fmt.Sprintf("%s %s %s%s", sevSymbol(st, f.Severity), pad(f.Severity, 8), f.Title, where))
		for _, l := range wrap(f.Detail, width-len(ind)) {
			w(ind + l)
		}
		if f.Checked != "" {
			for _, l := range wrap(f.Checked, width-len(ind)) {
				w(ind + l)
			}
		}
		for i, fx := range f.Fix {
			lead := "Fix: "
			if i > 0 {
				lead = "     "
			}
			for j, l := range wrap(fx, width-len(ind)-len(lead)) {
				if j > 0 {
					lead = "     "
				}
				w(ind + lead + l)
				lead = "     "
			}
		}
	}
	if len(r.Findings) > 0 {
		w("")
	}
	switch {
	case r.Postgres != nil:
		pg := r.Postgres
		w(fmt.Sprintf("Postgres · %s · role %s · wal_level %s · read-only session · %d %s read-only", pg.Database, pg.Role, pg.WALLevel,
			pg.TablesReadOnly, plural(pg.TablesReadOnly, "table", "tables")))
		w("")
		if len(pg.Tables) == 0 {
			w("  The role can't change any table.")
		} else {
			w(TableLines(pg.Tables, 25, "  ")...)
		}
		w("")
		if l := PlayLine(pg.Tables); l != "" {
			w(st.apply(st.Amber, "○")+" "+l, "")
		}
	case r.PostgresError != nil:
		e := r.PostgresError
		w(st.apply(st.Red, "✕")+fmt.Sprintf(" Couldn't connect with the URL in %s (%s %s). The file scan was written. Run again when the database is reachable.", e.EnvVar, e.SQLState, e.Class), "")
	default:
		w(hang(st.apply(st.Amber, "○")+" Postgres not checked. Add --postgres-dsn-env VAR to rank the tables your agent's role can change.", width, "  ")...)
		w("")
	}
	if len(r.Policies) > 0 {
		w(numberWord(len(r.Policies)) + " policies to add first")
		for i, p := range r.Policies {
			head := fmt.Sprintf("  %d. %s %s", i+1, pad(p.Rule, 24), st.apply(st.Dim, "from "+p.Starter))
			w(head)
			for _, l := range wrap(p.Why, width-7) {
				w("     " + st.apply(st.Dim, l))
			}
		}
		w("")
	}
	if wrote != "" {
		w(wrote)
	}
	w(FooterLine)
	return b.String()
}

// PlayURL is the browser playground. It opens on the "said 1, changed 301" scene.
const PlayURL = "https://deedglass.com/play"

// PlayLine points to the playground's 301 scene when the role can change a table with an enabled
// trigger, or one whose deletes or updates cascade to other tables: on such a table one reported row
// can become many in the same transaction. It names the first such table in the ranked order, and is
// empty when there is none.
func PlayLine(tables []Table) string {
	for _, t := range tables {
		for _, tr := range t.Triggers {
			if tr.Enabled {
				return fmt.Sprintf("A trigger on %s (%s) can turn one reported row into many. See it happen in 20 s: %s", t.Name(), tr.Name, PlayURL)
			}
		}
	}
	for _, t := range tables {
		if len(t.CascadesTo) > 0 {
			return fmt.Sprintf("Changes to %s cascade to %s, so one reported row can become many. See it happen in 20 s: %s", t.Name(), strings.Join(t.CascadesTo, ", "), PlayURL)
		}
	}
	return ""
}

func numberWord(n int) string {
	switch n {
	case 1:
		return "One"
	case 2:
		return "Two"
	case 3:
		return "Three"
	}
	return strconv.Itoa(n)
}

func check(ok bool) string {
	if ok {
		return "✓"
	}
	return "✕"
}

// TableLines is the aligned table: Table · Can change · Est. rows · Cascades to · Triggers ·
// Undo-ready · Proven-ready, at most max rows.
func TableLines(ts []Table, max int, indent string) []string {
	head := []string{"Table", "Can change", "Est. rows", "Cascades to", "Triggers", "Undo-ready", "Proven-ready"}
	rows := [][]string{head}
	for i, t := range ts {
		if i == max {
			break
		}
		rows = append(rows, tableCells(t))
	}
	wd := make([]int, len(head))
	for _, r := range rows {
		for i, c := range r {
			wd[i] = max2(wd[i], utf8.RuneCountInString(c))
		}
	}
	var out []string
	for _, r := range rows {
		var cells []string
		for i, c := range r {
			if i == 2 {
				cells = append(cells, lpad(c, wd[i]))
			} else {
				cells = append(cells, pad(c, wd[i]))
			}
		}
		out = append(out, strings.TrimRight(indent+strings.Join(cells, "   "), " "))
	}
	if len(ts) > max {
		out = append(out, fmt.Sprintf("%s… and %d more in --json", indent, len(ts)-max))
	}
	return out
}

func tableCells(t Table) []string {
	rows := "not analysed"
	if t.EstRows >= 0 {
		rows = Thousands(t.EstRows)
	}
	casc := "none"
	if len(t.CascadesTo) > 0 {
		casc = strings.Join(t.CascadesTo, ", ")
	}
	var trig []string
	for _, tr := range t.Triggers {
		if tr.Enabled {
			trig = append(trig, tr.Name)
		}
	}
	tg := "none"
	if len(trig) > 0 {
		tg = strings.Join(trig, ", ")
	}
	return []string{t.Name(), strings.Join(t.Can, ", "), rows, casc, tg, check(t.UndoReady), check(t.L2Ready)}
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
