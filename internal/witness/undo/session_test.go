package undo

import (
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/witness/l2"
)

const obsID = "obs_01m3x71p6wesj7xewec6bwbwxm"

// sframe is one unattributed frame holding changes on orders and order_items.
func sframe(id string, at time.Time, lsn string, changes ...l2.Change) l2.Frame {
	f := l2.Frame{ID: id, Class: l2.ClassUnattributed, CommitLSN: lsn, CommitTime: at, Digest: "sha256:" + strings.Repeat("a", 64),
		Tables: map[string]l2.TableInfo{"public.orders": ordersInfo(), "public.order_items": itemsInfo()}, Counts: map[string]l2.OpCounts{}}
	for _, c := range changes {
		x := f.Counts[c.Table]
		switch c.Op {
		case "I":
			x.Insert++
		case "U":
			x.Update++
		case "D":
			x.Delete++
		}
		f.Counts[c.Table] = x
	}
	f.Changes, f.Total = changes, len(changes)
	return f
}

func lu(seq int, table string, old, nw Image) l2.Change {
	return l2.Change{Seq: seq, Op: "U", Table: table, Old: old, New: nw}
}

func TestFromSessionFramesNetAcrossTransactions(t *testing.T) {
	// tx1 sets order 1 to shipped and inserts item 9; tx2 (later) sets order 1 to lost and deletes
	// order 2. Given out of order, the evidence follows commit order.
	tx1 := sframe("dtx_01m3x71p6wesj7xewec6bwbw01", t0, "0/16B3748",
		lu(1, "public.orders", order("1", "paid"), order("1", "shipped")),
		l2.Change{Seq: 2, Op: "I", Table: "public.order_items", New: item("9", "1")})
	tx2 := sframe("dtx_01m3x71p6wesj7xewec6bwbw02", t0.Add(time.Second), "0/16B3900",
		lu(1, "public.orders", order("1", "shipped"), order("1", "lost")),
		l2.Change{Seq: 2, Op: "D", Table: "public.orders", Old: order("2", "paid")})
	ev := FromSessionFrames(SessionInput{Session: obsID, ConnectorID: connID, Frames: []l2.Frame{tx2, tx1}})
	if len(ev.Problems) != 0 {
		t.Fatalf("problems: %+v", ev.Problems)
	}
	if ev.Total != 4 || len(ev.Changes) != 4 || ev.Ref.DecodedTx != obsID || ev.Ref.CommitLSN != "0/16B3900" || ev.Class != "observed" {
		t.Fatalf("evidence %+v", ev.Ref)
	}
	for i, c := range ev.Changes {
		if c.Seq != i+1 {
			t.Fatalf("seq %d at %d", c.Seq, i)
		}
	}
	p, fs := Build(BuildInput{Undoes: obsID, ConnectorID: connID, Evidence: ev, Catalog: defaultCatalog()})
	if p == nil {
		t.Fatalf("findings %+v", fs)
	}
	got := strings.Join(stepsText(p), "; ")
	// Newest first: order 2 back, order 1 to paid (one step for two transactions), item 9 removed.
	want := "insert public.orders id=2; update public.orders id=1; delete public.order_items id=9"
	if got != want {
		t.Fatalf("steps %q\nwant %q", got, want)
	}
	for _, s := range p.Steps {
		if s.Op == OpUpdate && *s.Set["status"].T != "paid" {
			t.Fatalf("order 1 goes back to its value before the session: %+v", s.Set)
		}
	}
	if p.Rows != 3 || p.Undoes != obsID {
		t.Fatalf("rows %d undoes %s", p.Rows, p.Undoes)
	}
	// The same frames in any order give the same digest.
	ev2 := FromSessionFrames(SessionInput{Session: obsID, ConnectorID: connID, Frames: []l2.Frame{tx1, tx2}})
	p2, _ := Build(BuildInput{Undoes: obsID, ConnectorID: connID, Evidence: ev2, Catalog: defaultCatalog()})
	if p2.Digest != p.Digest || ev2.Ref.ChangesDigest != ev.Ref.ChangesDigest {
		t.Fatal("frame order changed the plan")
	}
	// Leaving tx2 out changes the plan.
	ev3 := FromSessionFrames(SessionInput{Session: obsID, ConnectorID: connID, Frames: []l2.Frame{tx1}})
	p3, _ := Build(BuildInput{Undoes: obsID, ConnectorID: connID, Evidence: ev3, Catalog: defaultCatalog()})
	if p3.Digest == p.Digest || strings.Join(stepsText(p3), "; ") != "delete public.order_items id=9; update public.orders id=1" {
		t.Fatalf("excluded plan %v", stepsText(p3))
	}
}

func TestFromSessionFramesProblems(t *testing.T) {
	ok := sframe("dtx_01m3x71p6wesj7xewec6bwbw01", t0, "0/1", lu(1, "public.orders", order("1", "a"), order("1", "b")))
	trunc := ok
	trunc.ID, trunc.Flags = "dtx_01m3x71p6wesj7xewec6bwbw02", []string{"truncate:public.order_items"}
	countsTrunc := sframe("dtx_01m3x71p6wesj7xewec6bwbw03", t0, "0/2")
	countsTrunc.Counts = map[string]l2.OpCounts{"public.orders": {Truncate: 1}}
	capped := sframe("dtx_01m3x71p6wesj7xewec6bwbw04", t0, "0/3", lu(1, "public.orders", order("1", "a"), order("1", "b")))
	capped.Total = 12000
	relmoved := sframe("dtx_01m3x71p6wesj7xewec6bwbw05", t0.Add(time.Minute), "0/4", lu(1, "public.orders", order("3", "a"), order("3", "b")))
	ri := ordersInfo()
	ri.RelID = 101
	relmoved.Tables["public.orders"] = ri
	bound := ok
	bound.ID, bound.Class = "dtx_01m3x71p6wesj7xewec6bwbw06", l2.ClassBound
	noIdentity := sframe("dtx_01m3x71p6wesj7xewec6bwbw07", t0, "0/5", lu(1, "public.orders", order("1", "a"), order("1", "b")))
	ni := ordersInfo()
	ni.Identity = "d"
	noIdentity.Tables["public.orders"] = ni
	norel := sframe("dtx_01m3x71p6wesj7xewec6bwbw08", t0, "0/6", lu(1, "public.ghost", order("1", "a"), order("1", "b")))
	for _, tc := range []struct {
		name   string
		frames []l2.Frame
		want   string
	}{
		{"empty", nil, "nothing to undo"},
		{"truncate flag", []l2.Frame{ok, trunc}, "truncate"},
		{"truncate count", []l2.Frame{countsTrunc}, "truncate"},
		{"images past the cap", []l2.Frame{capped}, "12,000"},
		{"a table recreated between transactions", []l2.Frame{ok, relmoved}, "public.orders"},
		{"a bound frame isn't a session's", []l2.Frame{bound}, ""},
		{"no full old image", []l2.Frame{noIdentity}, "public.orders"},
		{"a change on a table the frame doesn't describe", []l2.Frame{norel}, "public.ghost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := FromSessionFrames(SessionInput{Session: obsID, ConnectorID: connID, Frames: tc.frames})
			if len(ev.Problems) == 0 {
				t.Fatal("want a problem")
			}
			if !strings.Contains(strings.ToLower(ev.Problems[0].Text), strings.ToLower(tc.want)) {
				t.Fatalf("problem %q lacks %q", ev.Problems[0].Text, tc.want)
			}
			if p, _ := Build(BuildInput{Undoes: obsID, ConnectorID: connID, Evidence: ev, Catalog: defaultCatalog()}); p != nil {
				t.Fatal("a problem never gives a plan")
			}
		})
	}
	if lsnKey("0/1A") >= lsnKey("0/1B") || lsnKey("1/0") <= lsnKey("0/FFFFFFFF") || lsnKey("x") != "x" {
		t.Fatal("lsnKey order")
	}
}
