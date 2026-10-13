package observe

import (
	"sort"

	"github.com/deedglass/deedglass/internal/witness/l2"
)

// DiffRows is how many rows of each transaction's diff a person sees (S25's rule).
const DiffRows = 20

// Cell is one column of a diff row: its value before and after (nil = NULL), "•••" for a
// sensitive column, and whether it changed.
type Cell struct {
	Column  string  `json:"column"`
	Before  *string `json:"before"`
	After   *string `json:"after"`
	Changed bool    `json:"changed"`
}

// Row is one changed row of a transaction.
type Row struct {
	Op    string            `json:"op"` // insert, update, delete
	Table string            `json:"table"`
	Key   map[string]string `json:"key"`
	Cells []Cell            `json:"cells"`
}

// Diff is a transaction's first rows.
type Diff struct {
	Rows      []Row `json:"rows"`
	Total     int   `json:"total"`
	Truncated bool  `json:"truncated"`
}

// BuildDiff is the first n changed rows of a stored frame, in commit order: an insert shows its new
// row, a delete its old row, an update the columns that changed (every column when the old image
// isn't full). The ledger's own rows never show.
func BuildDiff(f l2.Frame, n int) Diff {
	changes := append([]l2.Change{}, f.Changes...)
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Seq < changes[j].Seq })
	d := Diff{Rows: []Row{}, Total: f.Total}
	for _, c := range changes {
		if c.Table == l2.LedgerTable {
			continue
		}
		if len(d.Rows) >= n {
			d.Truncated = true
			break
		}
		r := Row{Table: c.Table, Key: map[string]string{}, Cells: []Cell{}}
		for k, v := range c.Key {
			if v == nil {
				r.Key[k] = "NULL"
			} else {
				r.Key[k] = *v
			}
		}
		switch c.Op {
		case "I":
			r.Op = "insert"
			for _, col := range sortedCols(c.New) {
				r.Cells = append(r.Cells, Cell{Column: col, After: shown(c.New[col]), Changed: true})
			}
		case "D":
			r.Op = "delete"
			for _, col := range sortedCols(c.Old) {
				r.Cells = append(r.Cells, Cell{Column: col, Before: shown(c.Old[col]), Changed: true})
			}
		default:
			r.Op = "update"
			cols := c.Changed
			if len(cols) == 0 {
				cols = sortedCols(c.New)
			}
			for _, col := range cols {
				before, after := c.Old[col], c.New[col]
				cell := Cell{Column: col, After: shown(after), Changed: true}
				if c.Old != nil {
					cell.Before = shown(before)
				}
				r.Cells = append(r.Cells, cell)
			}
		}
		d.Rows = append(d.Rows, r)
	}
	if f.Total > len(d.Rows) {
		d.Truncated = true
	}
	return d
}

// Hidden is how a sensitive value shows (its text was never stored).
const Hidden = "•••"

func shown(v l2.Value) *string {
	switch {
	case v.N:
		return nil
	case v.D != "":
		s := Hidden
		return &s
	case v.U:
		s := "(unchanged)"
		return &s
	case v.X != "":
		s := `\x` + v.X
		return &s
	case v.T != nil:
		s := *v.T
		return &s
	}
	return nil
}

func sortedCols(m map[string]l2.Value) []string {
	out := make([]string, 0, len(m))
	for c := range m {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
