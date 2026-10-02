package undo

import (
	"slices"
	"testing"

	"github.com/deedglass/deedglass/internal/witness/l2"
)

// boundFrame is one bound frame over orders and order_items with exact counts for changes.
func boundFrame(changes ...l2.Change) l2.Frame {
	f := l2.Frame{ID: "dtx_01m3x71p6wesj7xewec6bwbwy0", Class: l2.ClassBound, CommitLSN: "0/3E65020", Xid: 13130, CommitTime: t0,
		Tables: map[string]l2.TableInfo{"public.orders": ordersInfo(), "public.order_items": itemsInfo(),
			l2.LedgerTable: {RelID: 1, PK: []string{"action_id"}, Identity: "d"}},
		Counts: map[string]l2.OpCounts{}, Digest: "sha256:abc"}
	for _, c := range changes {
		cnt := f.Counts[c.Table]
		switch c.Op {
		case "I":
			cnt.Insert++
		case "U":
			cnt.Update++
		case "D":
			cnt.Delete++
		}
		f.Counts[c.Table] = cnt
	}
	f.Changes = changes
	f.Total = len(changes)
	return f
}

func TestFromFrames(t *testing.T) {
	ledger := l2.Change{Seq: 0, Op: "I", Table: l2.LedgerTable, New: im("action_id", actID)}
	f := boundFrame(
		l2.Change{Seq: 3, Op: "D", Table: "public.orders", Old: order("9", "paid")},
		ledger,
		l2.Change{Seq: 1, Op: "I", Table: "public.orders", New: order("7", "new")},
		l2.Change{Seq: 2, Op: "U", Table: "public.order_items", Old: item("70", "7"), New: item("70", "8")},
	)
	f.Total = 3
	missing := l2.Frame{Class: l2.ClassMissing}
	ev := FromFrames(FrameInput{Action: actID, ConnectorID: connID, Frames: []l2.Frame{missing, f}})
	if len(ev.Problems) > 0 {
		t.Fatalf("problems: %+v", ev.Problems)
	}
	if ev.Action != actID || ev.ConnectorID != connID || ev.Class != l2.ClassBound || ev.Total != 3 {
		t.Fatalf("header: %+v", ev)
	}
	wantRef := EvidenceRef{DecodedTx: f.ID, Xid: 13130, CommitLSN: "0/3E65020", CommittedAt: "2026-10-07T13:12:09.114Z", Changes: 3,
		ChangesDigest: "sha256:abc"}
	if ev.Ref != wantRef {
		t.Fatalf("ref %+v, want %+v", ev.Ref, wantRef)
	}
	if !ev.CommittedAt().Equal(t0) {
		t.Fatalf("CommittedAt = %v", ev.CommittedAt())
	}
	if got := ev.TableNames(); !slices.Equal(got, []string{"public.order_items", "public.orders"}) {
		t.Fatalf("TableNames = %q (the ledger must be left out)", got)
	}
	var seqs []int
	var ops []Op
	for _, c := range ev.Changes {
		seqs = append(seqs, c.Seq)
		ops = append(ops, c.Op)
	}
	if !slices.Equal(seqs, []int{1, 2, 3}) || !slices.Equal(ops, []Op{OpInsert, OpUpdate, OpDelete}) {
		t.Fatalf("changes in seq order: %v %v", seqs, ops)
	}
	if ev.Changes[1].Table != itemsRef {
		t.Fatalf("table ref %+v", ev.Changes[1].Table)
	}
	if p, fs := build(ev, defaultCatalog()); p == nil || p.Rows != 3 {
		t.Fatalf("Build: %+v %v", p, fs)
	}
}

func TestFromFramesProblems(t *testing.T) {
	ins7 := l2.Change{Seq: 1, Op: "I", Table: "public.orders", New: order("7", "new")}
	tests := []struct {
		name   string
		frames func() []l2.Frame
		want   []Finding
	}{
		{"no frame", func() []l2.Frame { return nil }, []Finding{ProblemNoFrame}},
		{"only a missing frame", func() []l2.Frame { return []l2.Frame{{Class: l2.ClassMissing}} }, []Finding{ProblemNoFrame}},
		{"a frame that isn't bound", func() []l2.Frame { return []l2.Frame{{Class: "forged_marker"}} },
			[]Finding{ProblemNotBound("forged_marker")}},
		{"two bound frames", func() []l2.Frame { return []l2.Frame{boundFrame(ins7), boundFrame(ins7)} }, []Finding{ProblemManyFrames(2)}},
		{"a bound frame and another", func() []l2.Frame { return []l2.Frame{boundFrame(ins7), {Class: l2.ClassMarkerCount}} },
			[]Finding{ProblemManyFrames(2)}},
		{"truncate flag", func() []l2.Frame {
			f := boundFrame(ins7)
			f.Flags = []string{"truncate:public.orders"}
			return []l2.Frame{f}
		}, []Finding{ProblemTruncate("public.orders")}},
		{"a truncate count without its flag", func() []l2.Frame {
			f := boundFrame(ins7)
			f.Counts["public.order_items"] = l2.OpCounts{Truncate: 1}
			return []l2.Frame{f}
		}, []Finding{ProblemTruncate("public.order_items")}},
		{"relation_changed flag", func() []l2.Frame {
			f := boundFrame(ins7)
			f.Flags = []string{"relation_changed:public.orders"}
			return []l2.Frame{f}
		}, []Finding{ProblemRelationChanged("public.orders")}},
		{"images_truncated flag", func() []l2.Frame {
			f := boundFrame(ins7)
			f.Flags = []string{"images_truncated"}
			f.Total = 1001
			return []l2.Frame{f}
		}, []Finding{ProblemImagesTruncated(1001, 1)}},
		{"fewer images than changes without the flag", func() []l2.Frame {
			f := boundFrame(ins7)
			f.Total = 2
			return []l2.Frame{f}
		}, []Finding{ProblemImagesTruncated(2, 1)}},
		{"a change on a table the frame doesn't describe", func() []l2.Frame {
			c := ins7
			c.Table = "public.ghosts"
			c2 := c
			c2.Seq = 2
			f := boundFrame(c, c2)
			return []l2.Frame{f}
		}, []Finding{ProblemNoRelation("public.ghosts")}},
		{"a truncate change", func() []l2.Frame {
			c := ins7
			c.Op = "T"
			return []l2.Frame{boundFrame(c)}
		}, []Finding{ProblemTruncate("public.orders")}},
		{"an update without full old rows", func() []l2.Frame {
			f := boundFrame(l2.Change{Seq: 1, Op: "U", Table: "public.orders", Old: order("1", "a"), New: order("1", "b")},
				l2.Change{Seq: 2, Op: "D", Table: "public.orders", Old: order("2", "a")})
			info := f.Tables["public.orders"]
			info.Identity = "d"
			f.Tables["public.orders"] = info
			return []l2.Frame{f}
		}, []Finding{ProblemOldImage("public.orders", "d")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := FromFrames(FrameInput{Action: actID, Frames: tc.frames()})
			if len(ev.Problems) != len(tc.want) {
				t.Fatalf("problems %+v, want %+v", ev.Problems, tc.want)
			}
			for i := range tc.want {
				if ev.Problems[i] != tc.want[i] {
					t.Fatalf("problem %d: %+v, want %+v", i, ev.Problems[i], tc.want[i])
				}
			}
			if p, fs := build(ev, defaultCatalog()); p != nil || !hasFinding(fs, tc.want[0].Check, tc.want[0].Text) {
				t.Fatalf("Build: %+v %+v", p, fs)
			}
		})
	}
}

func TestSortFindings(t *testing.T) {
	in := []Finding{
		{Check: CheckColumns, Text: "b"},
		{Check: CheckEvidence, Text: "z"},
		{Check: CheckColumns, Text: "a"},
		{Check: CheckEvidence, Text: "z"},
		{Check: CheckPrimaryKey, Text: "a"},
	}
	got := sortFindings(in)
	want := []Finding{{Check: CheckEvidence, Text: "z"}, {Check: CheckPrimaryKey, Text: "a"}, {Check: CheckColumns, Text: "a"},
		{Check: CheckColumns, Text: "b"}}
	if !slices.Equal(got, want) {
		t.Fatalf("sortFindings = %+v", got)
	}
}

func TestCatalogHelpers(t *testing.T) {
	tc := ordersCat("")
	if tc.String() != "public.orders" || ordersRef.String() != "public.orders" {
		t.Fatal(tc.String())
	}
	if c, ok := tc.Column("status"); !ok || c.Type != "text" {
		t.Fatalf("Column(status) = %+v %v", c, ok)
	}
	if _, ok := tc.Column("nope"); ok {
		t.Fatal("Column(nope) found")
	}
	for a, want := range map[string]bool{"c": true, "n": true, "d": true, "a": false, "r": false, "": false} {
		if Acts(a) != want {
			t.Errorf("Acts(%q) = %v", a, !want)
		}
	}
}
