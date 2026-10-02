package undo

import (
	"reflect"
	"slices"
	"testing"

	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/internal/witness/l2"
)

// footprintPlan updates orders 1, inserts orders 7, deletes orders 9 and inserts order_items 70;
// its undo deletes 70, re-inserts 9, deletes 7 and puts orders 1 back.
func footprintPlan(t *testing.T) *Plan {
	t.Helper()
	p, fs := build(evidence(upd(1, ordersRef, order("1", "paid"), order("1", "shipped")), ins(2, ordersRef, order("7", "new")),
		del(3, ordersRef, order("9", "paid")), ins(4, itemsRef, item("70", "7"))), defaultCatalog())
	if p == nil {
		t.Fatal(fs)
	}
	return p
}

func TestExactFootprint(t *testing.T) {
	fp := ExactFootprint(footprintPlan(t))
	one, three := int64(1), int64(3)
	want := l2.Footprint{
		Derivation: l2.DerivationExplicit,
		Tables: []l2.TableExpect{
			{Table: "public.order_items", Ops: []string{"delete"}, Rows: &one, Keys: []map[string]string{{"id": "70"}}},
			{Table: "public.orders", Ops: []string{"delete", "insert", "update"}, Rows: &three,
				Keys: []map[string]string{{"id": "9"}, {"id": "7"}, {"id": "1"}}, Columns: []string{"status", "total_cents"}},
		},
		Exact: []l2.ExactRow{
			{Table: "public.order_items", Op: "delete", Key: map[string]string{"id": "70"}},
			{Table: "public.orders", Op: "insert", Key: map[string]string{"id": "9"}, Values: guardOf(order("9", "paid")),
				Ignore: []string{"total_cents"}},
			{Table: "public.orders", Op: "delete", Key: map[string]string{"id": "7"}, Ignore: []string{"total_cents"}},
			{Table: "public.orders", Op: "update", Key: map[string]string{"id": "1"}, Values: map[string]l2.Value{"status": tv("paid")},
				Ignore: []string{"total_cents"}},
		},
	}
	if !reflect.DeepEqual(fp, want) {
		t.Fatalf("ExactFootprint\n got %+v\nwant %+v", fp, want)
	}
}

// undoFrame is the undo's own transaction as the decoder would record it: the ledger row and the
// planned changes, with exact counts.
func undoFrame(changes ...l2.Change) *l2.Frame {
	f := boundFrame(append([]l2.Change{{Seq: 0, Op: "I", Table: l2.LedgerTable, Key: map[string]*string{"action_id": sp(actID)},
		New: im("action_id", actID)}}, changes...)...)
	f.Total = len(changes)
	return &f
}

func ch(seq int, op, table string, id string, old, nw Image, changed ...string) l2.Change {
	return l2.Change{Seq: seq, Op: op, Table: table, Key: map[string]*string{"id": sp(id)}, Old: old, New: nw, Changed: changed}
}

// cleanUndo is exactly footprintPlan's undo; the update changes the generated total_cents too.
func cleanUndo() []l2.Change {
	return []l2.Change{
		ch(1, "D", "public.order_items", "70", item("70", "7"), nil),
		ch(2, "I", "public.orders", "9", nil, order("9", "paid")),
		ch(3, "D", "public.orders", "7", order("7", "new"), nil),
		ch(4, "U", "public.orders", "1", order("1", "shipped"), order("1", "paid"), "status", "total_cents"),
	}
}

func codes(rs []witness.Reason) []witness.Code {
	var out []witness.Code
	for _, r := range rs {
		out = append(out, r.Code)
	}
	return out
}

func clean(rs []witness.Reason) bool {
	for _, r := range rs {
		if r.Class == witness.Violation || r.Class == witness.Gap {
			return false
		}
	}
	return true
}

func TestExactFootprintCompare(t *testing.T) {
	fp := ExactFootprint(footprintPlan(t))
	t.Run("the planned undo restores", func(t *testing.T) {
		rs := l2.Compare(fp, undoFrame(cleanUndo()...), l2.Config{})
		if !slices.Equal(codes(rs), []witness.Code{witness.CodeUndoRestored, witness.CodeUndoRestored}) || !clean(rs) {
			t.Fatalf("reasons %+v", rs)
		}
		if rs[0].Message != "Restored 1 row in public.order_items exactly as planned." ||
			rs[1].Message != "Restored 3 rows in public.orders exactly as planned." {
			t.Fatalf("messages %q, %q", rs[0].Message, rs[1].Message)
		}
	})
	tests := []struct {
		name    string
		changes func() []l2.Change
		code    witness.Code
	}{
		{"one extra changed row", func() []l2.Change {
			return append(cleanUndo(), ch(5, "U", "public.orders", "2", order("2", "paid"), order("2", "lost"), "status", "total_cents"))
		}, witness.CodeUnexpectedKey},
		{"an update wrote another value", func() []l2.Change {
			cs := cleanUndo()
			cs[3].New = order("1", "pending")
			return cs
		}, witness.CodeValueMismatch},
		{"an insert wrote another value", func() []l2.Change {
			cs := cleanUndo()
			cs[1].New = with(order("9", "paid"), "note", "changed by a trigger")
			return cs
		}, witness.CodeValueMismatch},
		{"an update changed an unplanned column", func() []l2.Change {
			cs := cleanUndo()
			cs[3].New = with(order("1", "paid"), "note", "touched")
			cs[3].Changed = []string{"note", "status", "total_cents"}
			return cs
		}, witness.CodeUnexpectedColumn},
		{"a planned row is missing", func() []l2.Change { return cleanUndo()[1:] }, witness.CodeMissingChange},
		{"a delete where an update was planned", func() []l2.Change {
			cs := cleanUndo()
			cs[3] = ch(4, "D", "public.orders", "1", order("1", "shipped"), nil)
			return cs
		}, witness.CodeUnexpectedOperation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rs := l2.Compare(fp, undoFrame(tc.changes()...), l2.Config{})
			if clean(rs) || !slices.Contains(codes(rs), tc.code) || slices.Contains(codes(rs), witness.CodeUndoRestored) {
				t.Fatalf("reasons %v, want %s and no undo_restored", codes(rs), tc.code)
			}
		})
	}
}

// TestExactFootprintMoved: a key change put back by UPDATE, and by DELETE + INSERT on a GENERATED
// ALWAYS identity, are both clean when Postgres commits exactly them.
func TestExactFootprintMoved(t *testing.T) {
	moved := []RowChange{upd(1, ordersRef, order("42", "paid"), with(order("42", "shipped"), "id", "1042"))}
	t.Run("update", func(t *testing.T) {
		p, fs := build(evidence(moved...), defaultCatalog())
		if p == nil {
			t.Fatal(fs)
		}
		f := undoFrame(ch(1, "U", "public.orders", "1042", with(order("42", "shipped"), "id", "1042"), order("42", "paid"),
			"id", "status", "total_cents"))
		if rs := l2.Compare(ExactFootprint(p), f, l2.Config{}); !clean(rs) || !slices.Equal(codes(rs), []witness.Code{witness.CodeUndoRestored}) {
			t.Fatalf("reasons %+v", rs)
		}
	})
	t.Run("identity always", func(t *testing.T) {
		p, fs := build(evidence(moved...), catalog(ordersCat("a")))
		if p == nil {
			t.Fatal(fs)
		}
		f := undoFrame(ch(1, "D", "public.orders", "1042", with(order("42", "shipped"), "id", "1042"), nil),
			ch(2, "I", "public.orders", "42", nil, order("42", "paid")))
		if rs := l2.Compare(ExactFootprint(p), f, l2.Config{}); !clean(rs) || !slices.Equal(codes(rs), []witness.Code{witness.CodeUndoRestored}) {
			t.Fatalf("reasons %+v", rs)
		}
	})
}

func TestExactFootprintEmpty(t *testing.T) {
	p, _ := build(evidence(ins(1, ordersRef, order("7", "a")), del(2, ordersRef, order("7", "a"))), defaultCatalog())
	fp := ExactFootprint(p)
	if fp.Derivation != l2.DerivationExplicit || len(fp.Tables) != 0 || fp.Exact != nil {
		t.Fatalf("%+v", fp)
	}
}
