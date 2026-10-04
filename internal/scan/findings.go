package scan

import (
	"fmt"
	"sort"
	"strings"
)

// The sources every archived_postgres_mcp finding cites (S32 §1; CLAIMS_REGISTER rows, checked
// 2026-10-05).
var archivedSources = []string{
	"https://github.com/modelcontextprotocol/servers-archived/tree/main/src/postgres",
	"https://securitylabs.datadoghq.com/articles/mcp-vulnerability-case-study-SQL-injection-in-the-postgresql-mcp-server/",
}

// Copy (exact, S32 §3.13.2 and §5).
const (
	ArchivedTitle  = "Archived Postgres MCP server"
	ArchivedDetail = "This Postgres MCP server was archived on 2025-05-28. Its read-only mode runs queries in a read-only transaction that a stacked COMMIT; can end (Datadog Security Labs). If its database role can write, the agent can write."
	ArchivedFix1   = "Give the server a role that can only SELECT."
	ArchivedFix2   = "Or route it through Deedglass: deedglass mcp wrap --connector <name> -- <the server's command>."
)

func checkedRole(pg *Postgres) string {
	return fmt.Sprintf("Checked role %s from %s. If the server uses a different role, check that one.", pg.Role, pg.EnvVar)
}

// roleCanWrite: any INSERT, UPDATE, DELETE or TRUNCATE on any table, or a superuser or BYPASSRLS role.
func roleCanWrite(pg *Postgres) bool {
	return pg.RoleAttributes.Superuser || pg.RoleAttributes.BypassRLS || len(pg.Tables) > 0
}

// findings builds every finding, in a deterministic order: severity, then id, then path.
func findings(r *Report, o Options) []Finding {
	var out []Finding
	sev := "medium"
	checked := ""
	if r.Postgres != nil {
		checked = checkedRole(r.Postgres)
		if roleCanWrite(r.Postgres) {
			sev = "high"
		} else {
			sev = "low"
		}
	}
	archived := func(where string) {
		out = append(out, Finding{ID: "archived_postgres_mcp", Severity: sev, Title: ArchivedTitle,
			Detail: ArchivedDetail, Checked: checked, Fix: []string{ArchivedFix1, ArchivedFix2}, Paths: []string{where}, Sources: archivedSources})
	}
	for _, c := range r.Configs {
		for _, s := range c.Servers {
			if s.Package == ArchivedPostgres {
				archived(c.Path)
			}
			if s.DatabaseURLInArgs {
				out = append(out, Finding{ID: "database_url_in_client_args", Severity: "medium",
					Title:  "A database URL sits in a client config",
					Detail: fmt.Sprintf("Server %q has a database URL in its arguments, so the password travels with the file.", s.Name),
					Fix:    []string{"Move it to an environment variable the client reads, so it isn't copied with the file."},
					Paths:  []string{c.Path}, Sources: []string{}})
			}
		}
	}
	for _, d := range r.Dependencies {
		if d.Name == ArchivedPostgres {
			archived(d.File)
		}
	}
	if pg := r.Postgres; pg != nil {
		ra := pg.RoleAttributes
		role := func(id, severity, title, detail, fix string) {
			out = append(out, Finding{ID: id, Severity: severity, Title: title, Detail: detail, Checked: checkedRole(pg),
				Fix: []string{fix}, Paths: []string{}, Sources: []string{}})
		}
		if ra.Superuser {
			role("role_superuser", "high", "The database role is a superuser", fmt.Sprintf("Role %s can change anything in the cluster, including roles and other databases.", pg.Role), "Give the agent a role with only the grants it needs.")
		}
		if ra.BypassRLS {
			role("role_bypassrls", "high", "The database role bypasses row-level security", fmt.Sprintf("Role %s ignores every row-level security policy.", pg.Role), "Use a role without BYPASSRLS.")
		}
		if ra.Replication {
			role("role_replication", "medium", "The database role can replicate", fmt.Sprintf("Role %s can read every change in the cluster through replication.", pg.Role), "Use a role without REPLICATION.")
		}
		if ra.CreateRole {
			role("role_createrole", "medium", "The database role can create roles", fmt.Sprintf("Role %s can create and change other roles.", pg.Role), "Use a role without CREATEROLE.")
		}
		if ra.CreateDB {
			role("role_createdb", "low", "The database role can create databases", fmt.Sprintf("Role %s can create databases.", pg.Role), "Use a role without CREATEDB.")
		}
		if n := len(pg.SecurityDefiner); n > 0 {
			var sigs []string
			for i, f := range pg.SecurityDefiner {
				if i == 10 {
					break
				}
				sigs = append(sigs, f.Signature)
			}
			out = append(out, Finding{ID: "security_definer_executable", Severity: "medium",
				Title:  "The role can run SECURITY DEFINER functions",
				Detail: fmt.Sprintf("%d SECURITY DEFINER %s with the owner's rights when %s calls %s: %s.", n, plural(n, "function runs", "functions run"), pg.Role, plural(n, "it", "them"), strings.Join(sigs, ", ")),
				Fix:    []string{"Revoke EXECUTE from the agent's role on the ones it doesn't need."},
				Paths:  []string{}, Sources: []string{}})
		}
		var nopk []string
		for _, t := range pg.Tables {
			if !t.HasPK {
				nopk = append(nopk, t.Name())
			}
		}
		if len(nopk) > 0 {
			out = append(out, Finding{ID: "no_primary_key_on_writable_table", Severity: "low",
				Title:  "Writable tables without a primary key",
				Detail: "Undo and L2 witnessing can't anchor rows in " + strings.Join(nopk, ", ") + ".",
				Fix:    []string{"Add a primary key, or set REPLICA IDENTITY FULL."}, Paths: []string{}, Sources: []string{}})
		}
		if pg.WALLevel != "logical" {
			out = append(out, Finding{ID: "wal_level_not_logical", Severity: "info",
				Title:  "wal_level isn't logical",
				Detail: fmt.Sprintf("This database has wal_level=%s; L2 witnessing needs logical.", pg.WALLevel),
				Fix:    []string{"Set wal_level=logical and restart Postgres."}, Paths: []string{}, Sources: []string{}})
		}
	}
	rankSev := map[string]int{"high": 0, "medium": 1, "low": 2, "info": 3}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if rankSev[a.Severity] != rankSev[b.Severity] {
			return rankSev[a.Severity] < rankSev[b.Severity]
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return strings.Join(a.Paths, ",") < strings.Join(b.Paths, ",")
	})
	if out == nil {
		out = []Finding{}
	}
	_ = o
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
