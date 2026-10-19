package witness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// RowSpec is the row read-back a connector offers for one write (TY-04, DL-109): the one row the
// write changed, worked out by the connector from its own query language (Postgres: a single-row
// UPDATE by key). The Witness never parses a vendor's language; it reads the row back through the
// Observer and compares what comes back.
type RowSpec struct {
	Table string // "public.orders"
	Key   string // "id = 42"
	// Why is set when the write isn't one the row read-back can check; it is the readback_skipped
	// note's text.
	Why string
}

// RowObservation is the row read-back's observation body. XminMatches says the row's version
// marker still names the action's transaction (Postgres: xmin against low32(xid8)).
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

// rowMethod is the row read-back's method, readback.<connector type>.row, and its tool,
// <connector type>.row.
func rowMethod(s Subject) (method, tool string) {
	ct := nonEmptyOr(s.ConnectorType, "connector")
	return clipMethod("readback." + ct + ".row"), ct + ".row"
}

func rowReadBack(ctx context.Context, s Subject, obs Observer, final bool) L1Result {
	t := s.Spec.Row
	if t == nil || t.Why != "" {
		why := ""
		if t != nil {
			why = t.Why
		}
		return L1Result{Reasons: []Reason{R(L1, CodeReadbackSkipped, "readback", map[string]any{"why": why})}}
	}
	method, tool := rowMethod(s)
	res := L1Result{Method: method, Readback: &ReadbackDetail{Tool: tool, Target: t.Key + " in " + t.Table, Attempts: 1}}
	o, err := obs.ReadBack(ctx, s, ReadRequest{Target: res.Readback.Target}, 1)
	res.Readback.LatencyMS = o.LatencyMS
	vars := map[string]any{"key": t.Key, "table": t.Table}
	var se *SkipError
	var re *ReadError
	switch {
	case err == nil:
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		if !final {
			return L1Result{Deferred: true, Method: method, Readback: res.Readback}
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
			"key": t.Key, "table": t.Table, "expected": c.Expected}).WithDiff([]FieldDiff{{Path: c.Column, Expected: c.Expected, Observed: c.Observed}}))
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
