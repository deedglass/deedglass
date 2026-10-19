package postgres

import (
	"encoding/json"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/sqlguard"
)

var _ connector.RowTargeter = (*Connector)(nil)

// WhyNotRowReadBack is the readback_skipped note for a Postgres write the row read-back can't check.
const WhyNotRowReadBack = "the statement isn't a single-row update by primary key; L2 covers it when the connector is witnessed"

// MethodRow is the Witness's method for the Postgres row read-back: readback.<connector type>.row.
const MethodRow = "readback." + Type + ".row"

// RowTargetOf is what the Postgres row read-back reads: one table, one key, and the SET list.
type RowTargetOf struct {
	Shape sqlguard.Shape
	Key   string // "id = 42"
}

// TargetOf recognises a statement the row read-back can check: one UPDATE of one table whose
// WHERE names exactly one row (S19 §3.5). The primary-key check needs the catalog, so ReadBack
// makes it and answers ErrReadBackSkipped when the WHERE isn't the primary key. Until TY-04 this
// lived in the Witness; the Witness now gets only the table and key (connector.RowTarget).
func TargetOf(sql string, defaultSchema string) (RowTargetOf, string) {
	sh := sqlguard.ShapeOf(sql, sqlguard.ShapeOptions{DefaultSchema: defaultSchema})
	if !sh.Simple || sh.Kind != "update" || sh.Where == nil || len(sh.Where.Rows) != 1 || len(sh.Set) == 0 {
		return RowTargetOf{}, WhyNotRowReadBack
	}
	parts := make([]string, len(sh.Where.Columns))
	for i, c := range sh.Where.Columns {
		parts[i] = c + " = " + valueText(sh.Where.Rows[0][i])
	}
	return RowTargetOf{Shape: sh, Key: strings.Join(parts, " AND ")}, ""
}

func valueText(v sqlguard.Value) string {
	if v.Kind == sqlguard.ShapeValueString {
		return "'" + strings.ReplaceAll(v.Text, "'", "''") + "'"
	}
	if v.Kind == sqlguard.ShapeValueNull {
		return "NULL"
	}
	return v.Text
}

// RowTarget implements connector.RowTargeter: postgres.execute offers the row read-back, and a
// statement it can't check carries the readback_skipped note. Other tools never read back this way.
func (c *Connector) RowTarget(call connector.Call, defaultSchema string) (connector.RowTarget, bool) {
	if call.Tool.Name != ToolExecute {
		return connector.RowTarget{}, false
	}
	var a struct {
		SQL string `json:"sql"`
	}
	if json.Unmarshal(call.Args, &a) != nil || a.SQL == "" {
		return connector.RowTarget{Why: WhyNotRowReadBack}, true
	}
	t, why := TargetOf(a.SQL, defaultSchema)
	if why != "" {
		return connector.RowTarget{Why: why}, true
	}
	return connector.RowTarget{Table: t.Shape.Table, Key: t.Key}, true
}
