package witness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deedglass/deedglass/internal/sqlguard"
)

// WhyNotRowReadBack is the readback_skipped note for a Postgres write the row read-back can't check.
const WhyNotRowReadBack = "the statement isn't a single-row update by primary key; L2 covers it when the connector is witnessed"

// PostgresRowTarget is what the Postgres row read-back reads: one table, one key, and the SET list.
type PostgresRowTarget struct {
	Shape sqlguard.Shape
	Key   string // "id = 42"
}

// PostgresTarget recognises a statement the row read-back can check: one UPDATE of one table whose
// WHERE names exactly one row (S19 §3.5). The primary-key check needs the catalog, so the observer
// makes it and answers SkipError when the WHERE isn't the primary key.
func PostgresTarget(sql string, defaultSchema string) (PostgresRowTarget, string) {
	sh := sqlguard.ShapeOf(sql, sqlguard.ShapeOptions{DefaultSchema: defaultSchema})
	if !sh.Simple || sh.Kind != "update" || sh.Where == nil || len(sh.Where.Rows) != 1 || len(sh.Set) == 0 {
		return PostgresRowTarget{}, WhyNotRowReadBack
	}
	parts := make([]string, len(sh.Where.Columns))
	for i, c := range sh.Where.Columns {
		parts[i] = c + " = " + valueText(sh.Where.Rows[0][i])
	}
	return PostgresRowTarget{Shape: sh, Key: strings.Join(parts, " AND ")}, ""
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

// postgresTarget is PostgresTarget over the subject's arguments.
func postgresTarget(s Subject) (PostgresRowTarget, string) {
	var a struct {
		SQL string `json:"sql"`
	}
	if json.Unmarshal(s.Args, &a) != nil || a.SQL == "" || s.Spec.Tool != "execute" {
		return PostgresRowTarget{}, WhyNotRowReadBack
	}
	return PostgresTarget(a.SQL, s.Spec.DefaultSchema)
}

// RowObservation is the Postgres row read-back's observation body.
type RowObservation struct {
	RowFound    bool           `json:"row_found"`
	XminMatches bool           `json:"xmin_matches"`
	Table       string         `json:"table"`
	Key         string         `json:"key"`
	Columns     []ColumnResult `json:"columns"`
}

// ColumnResult is one SET column as the read-back compared it. Compared is false for a value the
// statement computed (x + 1, now()), which a read-back can't predict.
type ColumnResult struct {
	Column   string `json:"column"`
	Compared bool   `json:"compared"`
	Match    bool   `json:"match"`
	Expected any    `json:"expected"`
	Observed any    `json:"observed"`
}

func postgresRow(ctx context.Context, s Subject, obs Observer, final bool) L1Result {
	t, why := postgresTarget(s)
	if why != "" {
		return L1Result{Reasons: []Reason{R(L1, CodeReadbackSkipped, "readback", map[string]any{"why": why})}}
	}
	res := L1Result{Method: MethodPostgresRow, Readback: &ReadbackDetail{Tool: "postgres.row", Target: t.Key + " in " + t.Shape.Table, Attempts: 1}}
	o, err := obs.ReadBack(ctx, s, ReadRequest{Target: res.Readback.Target}, 1)
	res.Readback.LatencyMS = o.LatencyMS
	vars := map[string]any{"key": t.Key, "table": t.Shape.Table}
	var se *SkipError
	var re *ReadError
	switch {
	case err == nil:
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		if !final {
			return L1Result{Deferred: true, Method: MethodPostgresRow, Readback: res.Readback}
		}
		res.Reasons = []Reason{R(L1, CodeReadbackTimeout, "row.read", map[string]any{"readback": "the row read-back", "budget": budgetText(ctx)})}
		return res
	case errors.As(err, &se):
		return L1Result{Reasons: []Reason{R(L1, CodeReadbackSkipped, "readback", map[string]any{"why": se.Why})}}
	case errors.Is(err, ErrNotFound):
		res.Reasons = []Reason{R(L1, CodeRowMissing, "row.read", vars)}
		return res
	case errors.As(err, &re):
		res.Reasons = []Reason{R(L1, CodeReadbackFailed, "row.read", map[string]any{"readback": "the row read-back", "error_code": nonEmptyOr(re.Code, "upstream_error")})}
		return res
	default:
		res.Reasons = []Reason{R(L1, CodeReadbackFailed, "row.read", map[string]any{"readback": "the row read-back", "error_code": "upstream_error"})}
		return res
	}
	row, ok := o.Body.(RowObservation)
	if !ok {
		b, _ := json.Marshal(o.Body)
		if json.Unmarshal(b, &row) != nil {
			res.Reasons = []Reason{R(L1, CodeInternalError, "row.read", nil)}
			return res
		}
	}
	switch {
	case !row.RowFound:
		res.Reasons = []Reason{R(L1, CodeRowMissing, "row.read", vars)}
		return res
	case !row.XminMatches:
		res.Reasons = []Reason{R(L1, CodeRowSuperseded, "row.xmin", vars)}
		return res
	}
	for _, c := range row.Columns {
		if !c.Compared || c.Match {
			continue
		}
		res.Reasons = append(res.Reasons, R(L1, CodeValueMismatch, "row.value", map[string]any{"column": c.Column, "observed": c.Observed,
			"key": t.Key, "table": t.Shape.Table, "expected": c.Expected}).WithDiff([]FieldDiff{{Path: c.Column, Expected: c.Expected, Observed: c.Observed}}))
	}
	if len(res.Reasons) == 0 {
		res.Reasons = []Reason{R(L1, CodeRowWritten, "row.xmin", vars).WithEvidence(map[string]any{"compared": comparedCount(row.Columns)})}
	}
	return res
}

func comparedCount(cs []ColumnResult) string {
	n := 0
	for _, c := range cs {
		if c.Compared {
			n++
		}
	}
	return fmt.Sprintf("%d of %d columns", n, len(cs))
}
