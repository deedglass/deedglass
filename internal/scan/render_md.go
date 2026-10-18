package scan

import (
	"fmt"
	"strings"
)

// settingsCounts is how a settings file reads in the Found list: rule counts, never the rules.
func settingsCounts(c ClaudeSettings) string {
	if c.Error != "" && c.Allow+c.Ask+c.Deny == 0 && c.DefaultMode == "" {
		return c.Error
	}
	s := fmt.Sprintf("%d allow, %d ask, %d deny", c.Allow, c.Ask, c.Deny)
	if c.DefaultMode != "" {
		s += " · defaultMode " + c.DefaultMode
	}
	if c.Error != "" {
		s += " · " + c.Error
	}
	return s
}

func mdEscape(s string) string {
	return strings.NewReplacer("|", `\|`, "<", "&lt;", ">", "&gt;").Replace(s)
}

// RenderMarkdown is deedglass-scan.md: the same words as the terminal, score first.
func RenderMarkdown(r *Report) string {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f, a...); b.WriteByte('\n') }
	p("# deedglass scan · %s", r.Dir)
	p("")
	p("Scanned %s · read-only · deedglass %s", r.ScannedAt, r.DeedglassVersion)
	p("")
	p("## %s", mdEscape(ScoreLine(r.Score)))
	p("")
	p("_%s_", SmallPrint)
	p("")
	p("| Points | Deduction | What |")
	p("|---:|---|---|")
	for _, d := range r.Score.Deductions {
		pts := "0"
		if d.Points > 0 {
			pts = Minus + fmt.Sprint(d.Points)
		}
		p("| %s | %s | %s |", pts, DedLabel(d.ID), mdEscape(DedText(d)))
	}
	p("| **%d** | points | |", r.Score.Points)
	p("")
	p("## Found")
	p("")
	p("- %d MCP client %s · %d %s · %d %s that can write", r.Summary.Configs, plural(r.Summary.Configs, "config", "configs"),
		r.Summary.MCPServers, plural(r.Summary.MCPServers, "server", "servers"), r.Summary.WriteCapableTools, plural(r.Summary.WriteCapableTools, "tool", "tools"))
	for _, c := range r.Configs {
		var names []string
		for _, s := range c.Servers {
			n := s.Name
			if s.Package != "" {
				n += " (" + s.Package + ")"
			}
			if s.Routed {
				n += " · routed through Deedglass"
			}
			names = append(names, n)
		}
		p("- `%s` (%s): %s", c.Path, c.Client, mdEscape(strings.Join(names, "; ")))
	}
	for _, d := range r.Dependencies {
		p("- `%s`: %s %s", d.File, d.Name, mdEscape(d.Spec))
	}
	for _, e := range r.EnvNames {
		p("- `%s`: database URL variables (names only): %s", e.File, strings.Join(e.Names, ", "))
	}
	for _, c := range r.ClaudeSettings {
		p("- `%s`: Claude Code settings (%s) · %s", c.Path, c.Scope, settingsCounts(c))
	}
	p("")
	if len(r.Findings) > 0 {
		p("## Findings")
		p("")
		for _, f := range r.Findings {
			where := ""
			if len(f.Paths) > 0 {
				where = " · `" + strings.Join(f.Paths, "`, `") + "`"
			}
			p("### %s · %s%s", f.Severity, mdEscape(f.Title), where)
			p("")
			p("%s", mdEscape(f.Detail))
			if f.Checked != "" {
				p("")
				p("%s", mdEscape(f.Checked))
			}
			p("")
			for _, fx := range f.Fix {
				p("- Fix: %s", mdEscape(fx))
			}
			for _, s := range f.Sources {
				p("- Source: <%s>", s)
			}
			p("")
		}
	}
	p("## Postgres")
	p("")
	switch {
	case r.Postgres != nil:
		pg := r.Postgres
		p("%s · role %s · wal_level %s · read-only session · %d %s read-only", pg.Database, pg.Role, pg.WALLevel, pg.TablesReadOnly, plural(pg.TablesReadOnly, "table", "tables"))
		p("")
		p("| Table | Can change | Est. rows | Cascades to | Triggers | Undo-ready | Proven-ready |")
		p("|---|---|---:|---|---|---|---|")
		for i, t := range pg.Tables {
			if i == 25 {
				break
			}
			c := tableCells(t)
			p("| %s | %s | %s | %s | %s | %s | %s |", mdEscape(c[0]), c[1], c[2], mdEscape(c[3]), mdEscape(c[4]), c[5], c[6])
		}
	case r.PostgresError != nil:
		p("Couldn't connect with the URL in %s (%s %s). The file scan was written. Run again when the database is reachable.", r.PostgresError.EnvVar, r.PostgresError.SQLState, r.PostgresError.Class)
	default:
		p("Postgres not checked. Add `--postgres-dsn-env VAR` to rank the tables your agent's role can change.")
	}
	p("")
	if len(r.Policies) > 0 {
		p("## %s policies to add first", numberWord(len(r.Policies)))
		p("")
		for i, s := range r.Policies {
			p("%d. **%s** from %s: %s", i+1, s.Rule, s.Starter, mdEscape(s.Why))
		}
		p("")
		for _, s := range r.Policies {
			p("```yaml")
			b.WriteString(s.YAML)
			p("```")
			p("")
		}
	}
	p("## What it doesn't do")
	p("")
	p("It reads your configuration and catalog. It doesn't run your agent, test your tools or connect anywhere you didn't name.")
	p("")
	p("%s", FooterLine)
	return b.String()
}
