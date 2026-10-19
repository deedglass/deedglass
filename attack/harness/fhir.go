//go:build attack

package harness

//deedglass:allow-sql-build test harness: table, column, database and role names come from information_schema or constants, quoted with pgx.Identifier.Sanitize; the searched value is a $n parameter; the witness password is a constant derived in harnessKey

import (
	"context"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Waiting stops a case whose feature a later ticket builds: it is reported as "waiting on
// <ticket>", never matched and never differs (healthcare phase 1: HC-P05's Proven checks for FHIR
// servers, HC-P06's healthcare-fhir pack).
func (h *H) Waiting(ticket string) { panic(errWaiting{session: ticket}) }

// PlantedRows counts, per control-plane table, the rows that hold s anywhere: in the row's text
// form, or inside a bytea column as raw bytes (a deed envelope). HC-P02's I11 uses it to show a
// PHI-mode org's planted values are gone.
func (e *Env) PlantedRows(s string) map[string]int {
	c := e.adminConn(e.CP.Name)
	defer c.Close(context.Background())
	ctx := context.Background()
	rows, err := c.Query(ctx, `SELECT c.table_name, string_agg(CASE WHEN c.data_type = 'bytea' THEN c.column_name END, ',')
	    FROM information_schema.columns c JOIN information_schema.tables t USING (table_schema, table_name)
	    WHERE c.table_schema = 'deedglass' AND t.table_type = 'BASE TABLE' GROUP BY 1 ORDER BY 1`)
	must(err)
	type tbl struct {
		name  string
		bytea []string
	}
	var tables []tbl
	for rows.Next() {
		var name string
		var cols *string
		must(rows.Scan(&name, &cols))
		t := tbl{name: name}
		if cols != nil {
			for _, col := range splitComma(*cols) {
				t.bytea = append(t.bytea, col)
			}
		}
		tables = append(tables, t)
	}
	rows.Close()
	out := map[string]int{"_tables": len(tables)}
	for _, t := range tables {
		cond := `x::text LIKE '%' || $1 || '%'`
		for _, col := range t.bytea {
			cond += ` OR position(convert_to($1, 'UTF8') IN x.` + pgx.Identifier{col}.Sanitize() + `) > 0`
		}
		var n int
		must(c.QueryRow(ctx, `SELECT count(*) FROM deedglass.`+pgx.Identifier{t.name}.Sanitize()+` x WHERE `+cond, s).Scan(&n))
		if n > 0 {
			out[t.name] = n
		}
	}
	return out
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// FHIRDatabase creates a database for a Postgres-backed FHIR server stand-in (HC-P05) and a witness
// role that can decode it (LOGIN REPLICATION), and drops both, with the role's slots, when the run
// ends. It returns the superuser's DSN to the database (the server's own writes) and the witness's.
func (e *Env) FHIRDatabase(name string) (adminDSN, witnessDSN string) {
	role := name + "_witness"
	pw := strings.NewReplacer("+", "a", "/", "b", "=", "c").Replace(harnessKey("fhir-witness"))[:24]
	ctx := context.Background()
	c := e.adminConn("postgres")
	defer c.Close(ctx)
	_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	_, err := c.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	must(err)
	_, _ = c.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize())
	_, err = c.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN REPLICATION PASSWORD '"+pw+"'")
	must(err)
	e.cleanups = append(e.cleanups, func() {
		c := e.adminConn("postgres")
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), `SELECT pg_catalog.pg_drop_replication_slot(slot_name) FROM pg_catalog.pg_replication_slots WHERE database = $1 AND NOT active`, name)
		e.dropDB(name)
		_, _ = c.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize())
	})
	u, err := url.Parse(e.AdminURL)
	must(err)
	u.Path = "/" + name
	adminDSN = u.String()
	u.User = url.UserPassword(role, pw)
	witnessDSN = u.String()
	return adminDSN, witnessDSN
}

// FHIRPublication creates deedglass_pub on db for tables (schema-qualified), as the database owner
// would for a witnessed fhir connector (ADR-108 §2).
func (e *Env) FHIRPublication(db string, tables []string) {
	c := e.adminConn(db)
	defer c.Close(context.Background())
	var names []string
	for _, t := range tables {
		schema, table, _ := strings.Cut(t, ".")
		names = append(names, pgx.Identifier{schema, table}.Sanitize())
	}
	_, err := c.Exec(context.Background(), "CREATE PUBLICATION deedglass_pub FOR TABLE "+strings.Join(names, ", "))
	must(err)
}
