package l2

import (
	"slices"
	"sort"

	"github.com/deedglass/deedglass/internal/witness"
)

// BuildDiff builds levels.L2's evidence from the frame the verdict is about (S21 §3.7): the
// transaction, the expected footprint, the exact observed counts and up to cfg.DiffRows changed
// rows (expected rows first, then unexpected ones in stream order), with only the changed columns
// (at most cfg.DiffColumns, the rest counted). A sensitive value is "•••" and its column listed in
// redacted.
func BuildDiff(fp Footprint, f *Frame, cfg Config) *witness.L2Evidence {
	ev := &witness.L2Evidence{Rows: []witness.L2Row{}, Redacted: []string{}}
	ev.Expected = expectedOf(fp)
	if f == nil {
		return ev
	}
	ev.Transaction = &witness.L2Transaction{SystemID: f.SystemID, Timeline: f.Timeline, CommitLSN: f.CommitLSN, Xid: f.Xid,
		CommittedAt: f.CommitTime.UTC().Format("2006-01-02T15:04:05.000Z"), MarkerLSN: f.MarkerLSN, ChangesDigest: f.Digest}
	ev.Observed = observedOf(f)
	rowsCap, colsCap := cfg.DiffRows, cfg.DiffColumns
	if rowsCap <= 0 {
		rowsCap = DefaultDiffRows
	}
	if colsCap <= 0 {
		colsCap = DefaultDiffColumns
	}
	byTable := map[string]TableExpect{}
	for _, te := range fp.Tables {
		byTable[te.Table] = te
	}
	keySets := map[string]keySet{}
	expectedRow := func(ch Change) bool {
		te, ok := byTable[ch.Table]
		if !ok || !slices.Contains(te.Ops, opName(ch.Op)) {
			return false
		}
		if len(te.Keys) == 0 {
			return fp.Derivation != DerivationUnknown
		}
		ks, ok := keySets[ch.Table]
		if !ok {
			ks = newKeySet(te, f.Tables[ch.Table])
			keySets[ch.Table] = ks
		}
		if !ks.ok {
			return true
		}
		id, known := ks.changeKey(f.Tables[ch.Table], ch)
		return known && ks.set[id]
	}
	var exp, unexp []Change
	for _, ch := range f.Changes {
		if expectedRow(ch) {
			exp = append(exp, ch)
		} else {
			unexp = append(unexp, ch)
		}
	}
	shown := append(exp, unexp...)
	if len(shown) > rowsCap {
		shown = shown[:rowsCap]
	}
	// The columns: the union of the changed columns over the shown rows, in first-seen order.
	var cols []string
	for _, ch := range shown {
		for _, c := range changedCols(ch, fp, f) {
			if !slices.Contains(cols, c) {
				cols = append(cols, c)
			}
		}
	}
	if len(cols) > colsCap {
		ev.ColumnsMore = len(cols) - colsCap
		cols = cols[:colsCap]
	}
	redacted := map[string]bool{}
	for i, ch := range shown {
		row := witness.L2Row{Table: ch.Table, Op: opName(ch.Op), Key: map[string]string{}, Expected: i < len(exp), Cells: []witness.L2Cell{}}
		for k, v := range ch.Key {
			if v == nil {
				row.Key[k] = "NULL"
			} else {
				row.Key[k] = *v
			}
		}
		changed := changedCols(ch, fp, f)
		for _, c := range cols {
			if !slices.Contains(changed, c) {
				continue
			}
			cell := witness.L2Cell{Column: c}
			if v, ok := ch.Old[c]; ok {
				cell.Before = shownValue(v, ch.Table+"."+c, redacted)
			}
			if v, ok := ch.New[c]; ok {
				cell.After = shownValue(v, ch.Table+"."+c, redacted)
			}
			row.Cells = append(row.Cells, cell)
		}
		ev.Rows = append(ev.Rows, row)
	}
	for c := range redacted {
		ev.Redacted = append(ev.Redacted, c)
	}
	sort.Strings(ev.Redacted)
	ev.RowsShown = len(ev.Rows)
	ev.RowsMore = max(f.Total-ev.RowsShown, 0)
	return ev
}

// shownValue is a value for the diff: nil when the image didn't hold it.
func shownValue(v Value, col string, redacted map[string]bool) *string {
	if v.D != "" {
		redacted[col] = true
	}
	s, ok := v.Text()
	if !ok || v.N {
		return nil
	}
	return &s
}

// changedCols are the columns a change changed: an update's changed list (or, without a full old
// image, the SET columns it holds), an insert's or delete's every column.
func changedCols(ch Change, fp Footprint, f *Frame) []string {
	switch ch.Op {
	case "U":
		if ch.Changed != nil {
			return ch.Changed
		}
		var out []string
		for _, te := range fp.Tables {
			if te.Table == ch.Table {
				for _, c := range te.Columns {
					if _, ok := ch.New[c]; ok {
						out = append(out, c)
					}
				}
			}
		}
		return out
	case "I":
		return columnsIn(f.Tables[ch.Table], ch.New)
	case "D":
		return columnsIn(f.Tables[ch.Table], ch.Old)
	}
	return nil
}

// columnsIn lists an image's columns in the table's order, leaving out the primary key (it is the
// row's key already).
func columnsIn(ti TableInfo, img map[string]Value) []string {
	var out []string
	for _, c := range ti.Columns {
		if _, ok := img[c]; ok && !slices.Contains(ti.PK, c) {
			out = append(out, c)
		}
	}
	return out
}

func expectedOf(fp Footprint) *witness.L2Expected {
	e := &witness.L2Expected{Derivation: fp.Derivation, Why: fp.Why, Tables: []witness.L2ExpectedTable{}}
	for _, te := range fp.Tables {
		e.Tables = append(e.Tables, witness.L2ExpectedTable{Table: te.Table, Ops: te.Ops, Rows: te.Rows, MaxRows: te.MaxRows,
			Keys: te.Keys, Columns: te.Columns})
	}
	return e
}

func observedOf(f *Frame) *witness.L2Observed {
	o := &witness.L2Observed{Total: f.Total, Tables: []witness.L2ObservedTable{}}
	for _, t := range sortedKeys(f.Counts) {
		if t == LedgerTable {
			continue
		}
		c := f.Counts[t]
		o.Tables = append(o.Tables, witness.L2ObservedTable{Table: t, Insert: c.Insert, Update: c.Update, Delete: c.Delete, Truncate: c.Truncate})
	}
	return o
}
