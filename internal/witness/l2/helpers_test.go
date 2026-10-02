package l2

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/deedglass/deedglass/internal/witness"
)

// Test fixtures: the Acme orders table as the decoder records it (S20's frame JSON), and builders
// for frames and changes.

const (
	actID  = "act_01m3x71p6wesj7xewec6bwbwxm"
	orgID  = "org_01m3x70000000000000000000a"
	agtID  = "agt_01m3x70000000000000000000b"
	digest = "sha256:2f3f33ad0000000000000000000000000000000000000000000000000000000b"
)

func sp(s string) *string { return &s }
func ip(n int64) *int64   { return &n }

func ordersInfo(identity string) TableInfo {
	return TableInfo{RelID: 16400, PK: []string{"id"}, Identity: identity,
		Columns:  []string{"id", "customer_id", "status", "total_cents", "updated_at"},
		TypeOIDs: []uint32{oidInt4, oidInt4, oidText, oidInt4, 1184}}
}

func txt(s string) Value { return Value{T: sp(s)} }

// upd is an UPDATE of one order with a full old image; changes maps column → [before, after].
func upd(seq, id int, changes map[string][2]string) Change {
	old := map[string]Value{"id": txt(strconv.Itoa(id)), "customer_id": txt("7"), "status": txt("paid"), "total_cents": txt("1200"),
		"updated_at": txt("2026-10-01 09:00:00+00")}
	nw := map[string]Value{}
	for k, v := range old {
		nw[k] = v
	}
	var changed []string
	for _, c := range ordersInfo("f").Columns {
		if ch, ok := changes[c]; ok {
			old[c], nw[c] = txt(ch[0]), txt(ch[1])
			if ch[0] != ch[1] {
				changed = append(changed, c)
			}
		}
	}
	if changed == nil {
		changed = []string{}
	}
	return Change{Seq: seq, Op: "U", Table: "public.orders", Key: map[string]*string{"id": sp(strconv.Itoa(id))}, Old: old, New: nw, Changed: changed}
}

// frame wraps changes in a bound frame naming the action, with exact counts.
func frame(changes ...Change) Frame {
	f := Frame{ID: "dtx_01m3x71p6wesj7xewec6bwbwy0", Class: ClassBound, SystemID: "7690386503164422802", Timeline: 1, CommitLSN: "0/3E65020",
		Xid: 13130, CommitTime: time.Date(2026, 10, 7, 13, 12, 9, 114e6, time.UTC), MarkerLSN: "0/3E64FD0",
		Ledger:  []LedgerRow{{ActionID: actID, OrgID: orgID, AgentID: agtID, SQLDigest: digest, XID8: "13130", CreatedAt: "2026-10-07T13:12:09Z"}},
		Markers: []string{actID}, Tables: map[string]TableInfo{}, Counts: map[string]OpCounts{},
		Digest: "sha256:9d4100000000000000000000000000000000000000000000000000000000007ac"}
	for _, c := range changes {
		f.add(c)
	}
	return f
}

func (f *Frame) add(c Change) {
	f.Changes = append(f.Changes, c)
	f.count(c.Table, c.Op, 1)
	if _, ok := f.Tables[c.Table]; !ok {
		if c.Table == "public.orders" {
			f.Tables[c.Table] = ordersInfo("f")
		} else {
			f.Tables[c.Table] = TableInfo{PK: []string{"id"}, Identity: "f", Columns: []string{"id", "note"}, TypeOIDs: []uint32{oidInt4, oidText}}
		}
	}
}

func (f *Frame) count(t, op string, n int) {
	c := f.Counts[t]
	switch op {
	case "I":
		c.Insert += n
	case "U":
		c.Update += n
	case "D":
		c.Delete += n
	case "T":
		c.Truncate += n
	}
	f.Counts[t] = c
	f.Total += n
}

// heroFrame is S21 §0's transaction: order 42 paid → shipped, and the trigger ships 300 more.
func heroFrame() Frame {
	cs := []Change{upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}})}
	id := 1
	for i := 0; i < 300; i++ {
		if id == 42 {
			id++
		}
		before := "paid"
		if id%3 == 2 {
			before = "pending"
		}
		cs = append(cs, upd(i+2, id, map[string][2]string{"status": {before, "shipped"}}))
		id++
	}
	return frame(cs...)
}

func heroInput(f Frame) Input {
	return Input{
		Action:   Action{ID: actID, Org: orgID, Agent: agtID, SQLDigest: digest},
		Frames:   []Frame{f},
		Derive:   DeriveInput{Tool: ToolExecute, SQL: "UPDATE public.orders SET status = 'shipped' WHERE id = 42", Reported: ip(1)},
		Config:   Config{Watched: []string{"public.customers", "public.orders", "public.refunds"}},
		Triggers: map[string][]string{"public.orders": {"orders_ship_open_batch"}},
	}
}

func codes(rs []witness.Reason) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, string(r.Code))
	}
	sort.Strings(out)
	return out
}

func codeSet(rs []witness.Reason) string { return fmt.Sprint(codes(rs)) }
