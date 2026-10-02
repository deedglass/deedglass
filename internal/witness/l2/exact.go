package l2

import (
	"slices"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/witness"
)

// exactChecks are the row-by-row rules of an exact footprint (S23 §3.5): every observed change in a
// footprint table must be one planned row (same key and operation), every planned row must be
// observed once, an insert or update must have written exactly the planned values, and an update
// must change no column it didn't plan (generated columns aside). Table, operation and count rules
// stay with compareTable; this adds what they can't see.
func exactChecks(fp Footprint, f *Frame) []witness.Reason {
	var rs []witness.Reason
	type rowKey struct{ table, id string }
	// Planned rows by key: one key may carry several steps (a delete, then an insert of a row
	// that moves back onto it), so each observed change consumes one unused planned row of its op.
	planned := map[rowKey][]*ExactRow{}
	used := map[*ExactRow]bool{}
	var order []*ExactRow
	for i := range fp.Exact {
		r := &fp.Exact[i]
		k := rowKey{r.Table, exactID(r.Key)}
		planned[k] = append(planned[k], r)
		order = append(order, r)
	}
	values := map[string]*mismatch{}
	var valueOrder []string
	extra := map[string]int64{}
	var extraOrder []string
	ops := map[string]int64{}
	var opOrder []string
	unplanned := map[string]int64{}
	var unplannedOrder []string
	for _, ch := range f.Changes {
		if ch.Table == LedgerTable {
			continue
		}
		ti := f.Tables[ch.Table]
		id, ok := changeID(ch, ti)
		cands := planned[rowKey{ch.Table, id}]
		if !ok || len(cands) == 0 {
			if unplanned[ch.Table] == 0 {
				unplannedOrder = append(unplannedOrder, ch.Table)
			}
			unplanned[ch.Table]++
			continue
		}
		var r *ExactRow
		for _, c := range cands {
			if !used[c] && c.Op == opName(ch.Op) {
				r = c
				break
			}
		}
		if r == nil {
			if ops[ch.Table] == 0 {
				opOrder = append(opOrder, ch.Table)
			}
			ops[ch.Table]++
			continue
		}
		used[r] = true
		if ch.Op == "D" {
			continue
		}
		for _, col := range sortedKeys(r.Values) {
			want := r.Values[col]
			got, present := ch.New[col]
			if present && got.U {
				if old, ok := ch.Old[col]; ok {
					got = old
				}
			}
			if !present || !sameValue(want, got) {
				obs := "missing"
				if present {
					obs, _ = got.Text()
				}
				exp, _ := want.Text()
				note(values, &valueOrder, ch.Table+"."+col, keyText(ch, ti), obs, exp)
			}
		}
		if ch.Op == "U" {
			for _, col := range ch.Changed {
				if _, plannedCol := r.Values[col]; plannedCol || slices.Contains(r.Ignore, col) {
					continue
				}
				id := ch.Table + "." + col
				if extra[id] == 0 {
					extraOrder = append(extraOrder, id)
				}
				extra[id]++
			}
		}
	}
	for _, t := range unplannedOrder {
		n := unplanned[t]
		rs = append(rs, witness.R(witness.L2, witness.CodeUnexpectedKey, "keys", map[string]any{"n": n, "rows": rowsWord(n), "table": t,
			"are": areWord(n), "keys": "the planned keys"}).WithEvidence(map[string]any{"outside": n, "exact": true}))
	}
	for _, t := range opOrder {
		n := ops[t]
		rs = append(rs, witness.R(witness.L2, witness.CodeUnexpectedOperation, "operation", map[string]any{"op_past": "changed", "n": n,
			"rows": rowsWord(n), "table": t, "expected_op": "runs the planned operation on each row"}).WithEvidence(map[string]any{"exact": true}))
	}
	missing := map[string]int64{}
	var missingOrder []string
	for _, r := range order {
		if !used[r] {
			if missing[r.Table] == 0 {
				missingOrder = append(missingOrder, r.Table)
			}
			missing[r.Table]++
		}
	}
	for _, t := range missingOrder {
		n := missing[t]
		rs = append(rs, witness.R(witness.L2, witness.CodeMissingChange, "rows", map[string]any{"expected": n, "rows": rowsWord(n), "table": t,
			"observed": 0}).WithEvidence(map[string]any{"exact": true, "unobserved": n}))
	}
	for _, id := range extraOrder {
		n := extra[id]
		t, col := splitColumn(id)
		rs = append(rs, witness.R(witness.L2, witness.CodeUnexpectedColumn, "columns", map[string]any{"column": col, "n": n, "rows": rowsWord(n),
			"table": t, "columns": "the planned columns"}))
	}
	for _, id := range valueOrder {
		m := values[id]
		t, col := splitColumn(id)
		r := witness.R(witness.L2, witness.CodeValueMismatch, "value", map[string]any{"column": col, "observed": m.observed, "key": m.key,
			"table": t, "expected": m.exp}).WithDiff([]witness.FieldDiff{{Path: col, Expected: m.exp, Observed: m.observed}})
		if m.n > 1 {
			r = r.WithEvidence(map[string]any{"rows": m.n})
		}
		rs = append(rs, r)
	}
	return rs
}

func areWord(n int64) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func splitColumn(id string) (table, col string) {
	i := strings.LastIndexByte(id, '.')
	return id[:i], id[i+1:]
}

// exactID is a planned key's canonical form: its columns sorted, values joined.
func exactID(k map[string]string) string {
	cols := sortedKeys(k)
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c + "\x00" + k[c]
	}
	return strings.Join(parts, "\x01")
}

// changeID is an observed change's key in exactID's form: the decoder's key (the old image for an
// update or delete, the new one for an insert), as text.
func changeID(ch Change, ti TableInfo) (string, bool) {
	if ch.Key == nil {
		return "", false
	}
	cols := ti.PK
	if len(cols) == 0 {
		cols = sortedKeys(ch.Key)
	}
	k := make(map[string]string, len(cols))
	for _, c := range cols {
		v, ok := ch.Key[c]
		if !ok || v == nil {
			return "", false
		}
		k[c] = *v
	}
	return exactID(k), true
}

// sameValue compares a planned value with an observed one exactly as the change log printed them.
func sameValue(want, got Value) bool {
	switch {
	case want.N || got.N:
		return want.N && got.N
	case want.D != "" || got.D != "":
		return want.D != "" && want.D == got.D
	case want.X != "" || got.X != "":
		return want.X == got.X
	case want.T != nil && got.T != nil:
		return *want.T == *got.T
	}
	return false
}

// exactTables are the exact footprint's tables, sorted (the footprint's Tables carry the counts).
func exactTables(fp Footprint) []string {
	set := map[string]bool{}
	for _, r := range fp.Exact {
		set[r.Table] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
