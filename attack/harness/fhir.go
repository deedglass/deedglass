//go:build attack

package harness

//deedglass:allow-sql-build test harness: table and column names come from information_schema, quoted with pgx.Identifier.Sanitize; the searched value is a $n parameter

import (
	"context"

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
