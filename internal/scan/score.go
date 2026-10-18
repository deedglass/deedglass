package scan

import (
	"fmt"
	"sort"
	"strings"
)

// Grade maps points to A–F (A8 v1): A ≥ 90 · B 80–89 · C 65–79 · D 50–64 · F < 50.
func Grade(points int) string {
	switch {
	case points >= 90:
		return "A"
	case points >= 80:
		return "B"
	case points >= 65:
		return "C"
	case points >= 50:
		return "D"
	}
	return "F"
}

// GradeRank orders grades (A best): --fail-under compares with it.
func GradeRank(g string) int { return strings.Index("ABCDF", g) }

// Path is one unchecked write path: a write-like tool or a table the role can DELETE or TRUNCATE
// that isn't routed through deedglass mcp wrap with an active policy.
type Path struct {
	Subject string
	Detail  string
}

// UncheckedPaths lists the unchecked write paths, tables first (in rank order), then tools.
func UncheckedPaths(r *Report, policyFound bool) []Path {
	var out []Path
	dbServers, dbRouted := 0, 0
	for _, c := range r.Configs {
		for _, s := range c.Servers {
			if e := entryByID(s.CatalogID); e != nil && e.Database {
				dbServers++
				if s.Routed {
					dbRouted++
				}
			}
		}
	}
	tablesRouted := dbServers > 0 && dbServers == dbRouted && policyFound
	tablesKnown := dbServers > 0
	if r.Postgres != nil && !tablesRouted {
		for _, t := range r.Postgres.Tables {
			if !t.CanDelete() {
				continue
			}
			d := strings.Join(t.Can, ", ")
			var trig []string
			for _, tr := range t.Triggers {
				if tr.Enabled {
					trig = append(trig, tr.Name)
				}
			}
			if len(trig) > 0 {
				d += " · trigger " + strings.Join(trig, ", ")
			}
			if !tablesKnown {
				d += " · not shown to be routed"
			}
			out = append(out, Path{t.Name(), d})
		}
	}
	var tools []Path
	for _, t := range r.Tools {
		if !writeLike(t.SideEffect) || (t.Routed && policyFound) {
			continue
		}
		d := t.SideEffect + " · " + t.Where
		if t.Routed && !policyFound {
			d += " · wrapped, but no active policy was found"
		} else if !t.Routed && !strings.HasSuffix(t.Where, ".json") {
			d += " · not shown to be routed"
		}
		tools = append(tools, Path{t.Server + "." + t.Tool, d})
	}
	sort.SliceStable(tools, func(i, j int) bool { return tools[i].Subject < tools[j].Subject })
	return append(out, tools...)
}

// score applies formula v1 literally (S32 §3.13.6).
func score(r *Report, policyFound bool) Score {
	s := Score{FormulaVersion: FormulaVersion, Deductions: []Deduction{}, DatabaseChecked: r.Postgres != nil}
	pts := 100
	add := func(id string, p int, subject, detail string, capped bool) {
		if capped {
			p = 0
			detail += " (over the cap)"
		}
		pts -= p
		s.Deductions = append(s.Deductions, Deduction{ID: id, Points: p, Subject: subject, Detail: detail})
	}
	if pg := r.Postgres; pg != nil && (pg.RoleAttributes.Superuser || pg.RoleAttributes.BypassRLS) {
		what := "superuser"
		if !pg.RoleAttributes.Superuser {
			what = "BYPASSRLS"
		} else if pg.RoleAttributes.BypassRLS {
			what = "superuser · BYPASSRLS"
		}
		add("role_superuser_or_bypassrls", 35, pg.Role, "Role "+pg.Role+" is "+what, false)
	}
	high, med := 0, 0
	for _, f := range r.Findings {
		// S47 §3.2: the role's superuser and BYPASSRLS facts count once, toward the 35 above,
		// never again as high findings.
		if f.ID == "role_superuser" || f.ID == "role_bypassrls" {
			continue
		}
		switch f.Severity {
		case "high":
			add("high_finding", 20, f.ID, f.Title, high >= 2)
			high++
		case "medium":
			add("medium_finding", 8, f.ID, f.Title, med >= 3)
			med++
		}
	}
	paths := UncheckedPaths(r, policyFound)
	for i, p := range paths {
		add("unchecked_write_path", 4, p.Subject, p.Detail, i >= 5)
	}
	s.UncheckedPaths = len(paths)
	if pts < 0 {
		pts = 0
	}
	s.Points, s.Grade = pts, Grade(pts)
	sort.SliceStable(s.Deductions, func(i, j int) bool {
		a, b := s.Deductions[i], s.Deductions[j]
		if a.Points != b.Points {
			return a.Points > b.Points
		}
		return a.Subject < b.Subject
	})
	switch {
	case len(r.Findings) > 0 && r.Findings[0].Severity != "info" && r.Findings[0].Severity != "low":
		s.FixFirst = r.Findings[0].Title
	case len(paths) > 0:
		s.FixFirst = paths[0].Subject
	case len(r.Findings) > 0:
		s.FixFirst = r.Findings[0].Title
	}
	return s
}

// ScoreLine is the first line of every output (§3.13.6, exact).
func ScoreLine(s Score) string {
	line := fmt.Sprintf("Blast radius: %s · %d write %s with no check", s.Grade, s.UncheckedPaths, plural(s.UncheckedPaths, "path", "paths"))
	if s.FixFirst != "" {
		line += " · fix this first: " + s.FixFirst
	}
	if !s.DatabaseChecked {
		line += " · database not checked"
	}
	return line
}

// ShareEntry is one finding or deduction id as the /s page shows it.
type ShareEntry struct {
	ID       string `json:"id"`
	Severity string `json:"severity,omitempty"`
	Title    string `json:"title"`
}

// ShareCatalog is the closed list of ids a share link may carry, with the words /s shows for each
// (cmd/playgen writes it to examples/play/share-catalog.json). A test keeps it equal to the ids
// the scan can produce.
func ShareCatalog() map[string]any {
	return map[string]any{
		"object": "share_catalog", "v": 1, "formula_version": FormulaVersion,
		"findings": []ShareEntry{
			{"archived_postgres_mcp", "high", ArchivedTitle},
			{"role_superuser", "high", "The database role is a superuser"},
			{"role_bypassrls", "high", "The database role bypasses row-level security"},
			{"shell_destructive_unprompted", "high", "An agent can run a destructive database command without a prompt"},
			{"env_database_url_readable", "medium", "An agent can read a database URL in .env without a prompt"},
			{"shell_destructive_no_rule", "medium", "No ask or deny rule covers a destructive database command"},
			{"database_url_in_client_args", "medium", "A database URL sits in a client config"},
			{"role_replication", "medium", "The database role can replicate"},
			{"role_createrole", "medium", "The database role can create roles"},
			{"security_definer_executable", "medium", "The role can run SECURITY DEFINER functions"},
			{"role_createdb", "low", "The database role can create databases"},
			{"no_primary_key_on_writable_table", "low", "Writable tables without a primary key"},
			{"wal_level_not_logical", "info", "wal_level isn't logical"},
		},
		"deductions": []ShareEntry{
			{ID: "role_superuser_or_bypassrls", Title: "Superuser or BYPASSRLS database role"},
			{ID: "high_finding", Title: "High findings"},
			{ID: "medium_finding", Title: "Medium findings"},
			{ID: "unchecked_write_path", Title: "Write paths with no check"},
		},
		"footnote": "A means a small blast radius, not a safe agent.",
	}
}
