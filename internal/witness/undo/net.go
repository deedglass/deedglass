package undo

import (
	"sort"
	"strings"
)

// Key is a row's primary key: column → text. Keys are compared by their canonical string (Key.ID).
type Key map[string]string

// ID is the key's canonical form for one table: the values in the given column order, joined.
func (k Key) ID(cols []string) string {
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			b.WriteByte(0)
		}
		b.WriteString(k[c])
	}
	return b.String()
}

// Net is one original row's net effect over the transaction (S23 §3.2's table): the image before
// the transaction (nil: the row didn't exist) and after it (nil: it no longer exists), with the
// keys it had then.
type Net struct {
	Table     TableRef
	Before    Image // nil: created in the transaction
	BeforeKey Key
	After     Image // nil: removed in the transaction
	AfterKey  Key
	LastSeq   int  // the row's last change in the original transaction (steps run in reverse)
	Replaced  bool // deleted, then a row with the same key inserted
}

// Kind names a net effect: created, changed, moved, removed, replaced or none.
func (n *Net) Kind() string {
	switch {
	case n.Before == nil && n.After == nil:
		return "none"
	case n.Before == nil:
		return "created"
	case n.After == nil:
		return "removed"
	case n.BeforeKey.ID(sortedCols(n.BeforeKey)) != n.AfterKey.ID(sortedCols(n.AfterKey)):
		return "moved" // even when it was deleted and a new row took its new key
	case n.Replaced:
		return "replaced"
	}
	return "changed"
}

// NetResult is the collapse of a transaction: one Net per original row (in first-change order),
// and the problems that stop a plan.
type NetResult struct {
	Rows     []*Net
	Problems []Finding
}

// entity tracks one original row through the transaction.
type entity struct {
	n     *Net
	order int
}

// Collapse folds the changes, in Seq order, into one net effect per original row (S23 §3.2),
// following key changes through the chain. pk gives each table's primary-key columns (from the
// evidence); a table without one, or a key it can't read, is a problem, never a guess.
func Collapse(changes []RowChange, pk map[string][]string) NetResult {
	var res NetResult
	type tk struct {
		table uint32
		id    string
	}
	live := map[tk]*entity{}    // current key → the row that holds it now
	removed := map[tk]*entity{} // key → the last row removed while it held that key
	touched := map[tk]bool{}    // a key some change in this transaction held or vacated: a row
	// found there later must be the one live now (else the evidence contradicts itself)
	var all []*entity
	problem := func(p Finding) {
		for _, x := range res.Problems {
			if x.Text == p.Text {
				return
			}
		}
		res.Problems = append(res.Problems, p)
	}
	for _, c := range changes {
		t := c.Table.String()
		cols := pk[t]
		if len(cols) == 0 {
			problem(ProblemNoPrimaryKey(t))
			continue
		}
		switch c.Op {
		case OpInsert:
			k, ok := keyOf(c.New, cols)
			if !ok {
				problem(ProblemKeyUnreadable(t))
				continue
			}
			id := tk{c.Table.RelID, k.ID(cols)}
			if live[id] != nil {
				problem(ProblemInconsistent(t))
				continue
			}
			e := removed[id]
			if e != nil {
				delete(removed, id)
				if e.n.Before != nil {
					e.n.Replaced = true
				}
			} else {
				e = &entity{n: &Net{Table: c.Table}, order: len(all)}
				all = append(all, e)
			}
			e.n.After, e.n.AfterKey, e.n.LastSeq = c.New, k, c.Seq
			live[id] = e
			touched[id] = true
		case OpUpdate:
			if c.Old == nil {
				problem(ProblemOldImage(t, ""))
				continue
			}
			ko, ok1 := keyOf(c.Old, cols)
			newImg, ok2 := resolveNew(c.Old, c.New)
			if !ok2 {
				problem(ProblemToast(t))
				continue
			}
			kn, ok3 := keyOf(newImg, cols)
			if !ok1 || !ok3 {
				problem(ProblemKeyUnreadable(t))
				continue
			}
			oid, nid := tk{c.Table.RelID, ko.ID(cols)}, tk{c.Table.RelID, kn.ID(cols)}
			e := live[oid]
			if e == nil && touched[oid] {
				problem(ProblemInconsistent(t))
				continue
			}
			if e == nil {
				e = &entity{n: &Net{Table: c.Table, Before: c.Old, BeforeKey: ko}, order: len(all)}
				all = append(all, e)
			}
			delete(live, oid)
			if live[nid] != nil {
				problem(ProblemInconsistent(t))
				continue
			}
			e.n.After, e.n.AfterKey, e.n.LastSeq = newImg, kn, c.Seq
			live[nid] = e
			touched[oid], touched[nid] = true, true
		case OpDelete:
			if c.Old == nil {
				problem(ProblemOldImage(t, ""))
				continue
			}
			ko, ok := keyOf(c.Old, cols)
			if !ok {
				problem(ProblemKeyUnreadable(t))
				continue
			}
			oid := tk{c.Table.RelID, ko.ID(cols)}
			e := live[oid]
			if e == nil && touched[oid] {
				problem(ProblemInconsistent(t))
				continue
			}
			if e == nil {
				e = &entity{n: &Net{Table: c.Table, Before: c.Old, BeforeKey: ko}, order: len(all)}
				all = append(all, e)
			}
			delete(live, oid)
			e.n.After, e.n.AfterKey, e.n.LastSeq = nil, nil, c.Seq
			removed[oid] = e
			touched[oid] = true
		}
	}
	for _, e := range all {
		res.Rows = append(res.Rows, e.n)
	}
	res.Problems = sortFindings(res.Problems)
	return res
}

// resolveNew completes an UPDATE's new image: an Unchanged value (an external TOAST column Postgres
// didn't resend) takes the old image's value, which is complete with REPLICA IDENTITY FULL.
func resolveNew(old, nw Image) (Image, bool) {
	out := make(Image, len(nw))
	for c, v := range nw {
		if v.U {
			ov, ok := old[c]
			if !ok || ov.U {
				return nil, false
			}
			out[c] = ov
			continue
		}
		out[c] = v
	}
	return out, true
}

// keyOf reads a row's key from an image. A key column must hold plain text (never NULL, a digest
// or an unchanged TOAST value).
func keyOf(img Image, cols []string) (Key, bool) {
	k := make(Key, len(cols))
	for _, c := range cols {
		v, ok := img[c]
		if !ok || v.T == nil || v.N || v.U || v.D != "" || v.X != "" {
			return nil, false
		}
		k[c] = *v.T
	}
	return k, true
}

func sortedCols(k Key) []string {
	out := make([]string, 0, len(k))
	for c := range k {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Same reports whether two values are the same stored value (text byte for byte, NULL, the same
// digest, the same hex).
func Same(a, b Value) bool {
	switch {
	case a.N || b.N:
		return a.N && b.N
	case a.D != "" || b.D != "":
		return a.D == b.D
	case a.X != "" || b.X != "":
		return a.X == b.X
	case a.T != nil && b.T != nil:
		return *a.T == *b.T
	}
	return false
}
