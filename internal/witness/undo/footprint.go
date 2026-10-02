package undo

import (
	"sort"

	"github.com/deedglass/deedglass/internal/witness/l2"
)

// ExactFootprint turns a plan into S21's expected footprint in exact mode (S23 §3.5): per table the
// operations, the exact number of rows, the keys and the columns an update may change; and per row
// the operation, key and values Postgres must commit. The Gate stores it in actions.witness_expect
// before the connector runs, and the L2 witness compares the undo's own transaction with it.
func ExactFootprint(p *Plan) l2.Footprint {
	fp := l2.Footprint{Derivation: l2.DerivationExplicit, Tables: []l2.TableExpect{}}
	type agg struct {
		ops  map[string]bool
		rows int64
		keys []map[string]string
		cols map[string]bool
		upd  bool
	}
	by := map[uint32]*agg{}
	for _, s := range p.Steps {
		t, _ := p.Table(s.Table)
		a := by[s.Table]
		if a == nil {
			a = &agg{ops: map[string]bool{}, cols: map[string]bool{}}
			by[s.Table] = a
		}
		a.ops[string(s.Op)] = true
		a.rows++
		a.keys = append(a.keys, map[string]string(s.Key))
		row := l2.ExactRow{Table: t.String(), Op: string(s.Op), Key: map[string]string(s.Key), Ignore: t.Generated}
		if s.Op != OpDelete {
			row.Values = map[string]l2.Value{}
			for c, v := range s.Set {
				row.Values[c] = v
			}
		}
		if s.Op == OpUpdate {
			a.upd = true
			for c := range s.Set {
				a.cols[c] = true
			}
		}
		fp.Exact = append(fp.Exact, row)
	}
	for _, t := range p.Tables {
		a := by[t.RelID]
		if a == nil {
			continue
		}
		te := l2.TableExpect{Table: t.String(), Rows: &a.rows, Keys: a.keys}
		for _, op := range []string{l2.OpDelete, l2.OpInsert, l2.OpUpdate} {
			if a.ops[op] {
				te.Ops = append(te.Ops, op)
			}
		}
		if a.upd {
			for c := range a.cols {
				te.Columns = append(te.Columns, c)
			}
			te.Columns = append(te.Columns, t.Generated...)
			sort.Strings(te.Columns)
		}
		fp.Tables = append(fp.Tables, te)
	}
	sort.Slice(fp.Tables, func(i, j int) bool { return fp.Tables[i].Table < fp.Tables[j].Table })
	return fp
}
