package decoder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/gowebpki/jcs"
	"github.com/jackc/pglogrepl"
)

func TestAssembleBound(t *testing.T) {
	f := newFx(7461)
	b := assemble(t, boundTx(f, 0x3E65000), AssembleOptions{})
	if len(b.Frames) != 1 {
		t.Fatalf("%d frames", len(b.Frames))
	}
	fr := b.Frames[0]
	if !Classify(fr, f.org, false) || fr.Class != ClassBound || fr.ActionID != f.action {
		t.Fatalf("class %s action %s flags %v", fr.Class, fr.ActionID, fr.Flags)
	}
	ApplyKeys(b.Frames, map[uint32][]string{relOrders: {"id"}, relLedger: {"action_id"}})
	c := fr.Changes[0]
	if fr.Total != 1 || len(fr.Changes) != 1 || c.Op != "U" || c.Table != "public.orders" || *c.Key["id"] != "42" ||
		!slices.Equal(c.Changed, []string{"status"}) {
		t.Fatalf("%+v", fr)
	}
	if fr.Counts["public.orders"] != (OpCounts{Update: 1}) || fr.MarkerLSN != 0x3E65028 || fr.CommitLSN != 0x3E65100 || fr.EndLSN != 0x3E65130 {
		t.Fatalf("%+v %+v", fr.Counts, fr)
	}
	if _, ok := fr.Counts["deedglass.ledger"]; ok {
		t.Fatal("the ledger's own insert is counted as a change")
	}
	if fr.Tables["public.orders"].RelID != relOrders || !slices.Equal(fr.Tables["public.orders"].PK, []string{"id"}) {
		t.Fatalf("%+v", fr.Tables)
	}
	if len(fr.Ledger) != 1 || fr.Ledger[0].Invalid != "" || fr.Ledger[0].XID8 != "4294974757" {
		t.Fatalf("%+v", fr.Ledger)
	}
	if b.LastCommitEnd != 0x3E65130 {
		t.Fatalf("last commit end %s", b.LastCommitEnd)
	}
}

// One row for each class of S20 §3.7, built from synthetic frames.
func TestClassifyTable(t *testing.T) {
	type mk func(f fx) msgs
	tx := func(f fx, parts ...[]byte) msgs {
		var m msgs
		m.add(1, encBegin(0x200, t0, f.xid))
		m.add(1, encRelation(relLedger, "deedglass", "ledger", 'f', ledgerCols))
		m.add(1, encRelation(relOrders, "public", "orders", 'f', orderCols))
		for _, p := range parts {
			m.add(2, p)
		}
		m.add(3, encCommit(0x200, 0x230, t0))
		return m
	}
	upd := encUpdate(relOrders, 'O', []wval{tv("1"), tv("paid"), tv("1"), vNull}, []wval{tv("1"), tv("shipped"), tv("1"), vNull})
	marker := func(s string) []byte { return encMessage(true, 0x150, "deedglass", []byte(s)) }
	ledger := func(f fx) []byte { return encInsert(relLedger, f.ledgerRow()) }
	other := newFx(1)
	cases := []struct {
		name   string
		build  mk
		org    func(f fx) string
		pub    bool
		keep   bool
		class  Class
		action func(f fx) string
		flag   string
	}{
		{"C0 nothing", func(f fx) msgs { return tx(f) }, nil, false, false, "", nil, ""},
		{"C0 other prefix only", func(f fx) msgs { return tx(f, encMessage(true, 1, "app", []byte("x"))) }, nil, false, false, "", nil, ""},
		{"C1 unattributed", func(f fx) msgs { return tx(f, upd) }, nil, false, true, ClassUnattributed, nil, ""},
		{"C2 forged marker", func(f fx) msgs { return tx(f, marker(f.action)) }, nil, false, true, ClassForgedMarker, nil, ""},
		{"C2 forged marker with a change", func(f fx) msgs { return tx(f, upd, marker(f.action)) }, nil, false, true, ClassForgedMarker, nil, ""},
		{"publication gap", func(f fx) msgs { return tx(f, marker(f.action)) }, nil, true, true, ClassPublicationGap,
			func(f fx) string { return f.action }, "publication_changed"},
		{"C4 two markers", func(f fx) msgs { return tx(f, ledger(f), marker(f.action), upd, marker(f.action)) }, nil, false, true,
			ClassMarkerCount, func(f fx) string { return f.action }, ""},
		{"C4 no marker", func(f fx) msgs { return tx(f, ledger(f), upd) }, nil, false, true, ClassMarkerCount,
			func(f fx) string { return f.action }, ""},
		{"C4 two ledger rows", func(f fx) msgs { g := f; g.action = other.action; return tx(f, ledger(f), ledger(g), marker(f.action)) },
			nil, false, true, ClassMarkerCount, func(fx) string { return "" }, ""},
		{"C4 mismatch", func(f fx) msgs { return tx(f, ledger(f), marker(other.action), upd) }, nil, false, true,
			ClassMarkerMismatch, func(f fx) string { return f.action }, ""},
		{"xid mismatch", func(f fx) msgs {
			g := f
			g.xid = f.xid + 1
			m := tx(f, encInsert(relLedger, g.ledgerRow()), marker(f.action), upd)
			return m
		}, nil, false, true, ClassMarkerMismatch, func(f fx) string { return f.action }, "ledger_xid_mismatch"},
		{"foreign ledger", func(f fx) msgs { return tx(f, ledger(f), marker(f.action), upd) }, func(fx) string { return other.org }, false, true,
			ClassForeignLedger, func(fx) string { return "" }, ""},
		{"invalid ledger row", func(f fx) msgs {
			row := f.ledgerRow()
			row[4] = tv("md5:nope")
			return tx(f, encInsert(relLedger, row), marker(f.action), upd)
		}, nil, false, true, ClassMarkerMismatch, func(f fx) string { return f.action }, "ledger_invalid"},
		{"C5 bound", func(f fx) msgs { return tx(f, ledger(f), marker(f.action), upd) }, nil, false, true, ClassBound,
			func(f fx) string { return f.action }, ""},
		{"C5 bound, marker first", func(f fx) msgs { return tx(f, marker(f.action), ledger(f), upd) }, nil, false, true, ClassBound,
			func(f fx) string { return f.action }, ""},
		{"C5 bound, no row change", func(f fx) msgs { return tx(f, ledger(f), marker(f.action)) }, nil, false, true, ClassBound,
			func(f fx) string { return f.action }, ""},
		{"ledger delete is unattributed", func(f fx) msgs {
			return tx(f, encDelete(relLedger, 'O', f.ledgerRow()))
		}, nil, false, true, ClassUnattributed, nil, "ledger_modified"},
	}
	for _, c := range cases {
		f := newFx(900)
		b := assemble(t, c.build(f), AssembleOptions{})
		if len(b.Frames) != 1 {
			t.Fatalf("%s: %d frames", c.name, len(b.Frames))
		}
		org := f.org
		if c.org != nil {
			org = c.org(f)
		}
		fr := b.Frames[0]
		keep := Classify(fr, org, c.pub)
		if keep != c.keep || fr.Class != c.class {
			t.Errorf("%s: keep %v class %q, want %v %q (flags %v)", c.name, keep, fr.Class, c.keep, c.class, fr.Flags)
			continue
		}
		if c.action != nil && fr.ActionID != c.action(f) {
			t.Errorf("%s: action %q, want %q", c.name, fr.ActionID, c.action(f))
		}
		if c.flag != "" && !fr.HasFlag(c.flag) {
			t.Errorf("%s: flags %v, want %s", c.name, fr.Flags, c.flag)
		}
	}
}

func TestNonTransactionalAndHeartbeat(t *testing.T) {
	hb := NewHeartbeat("con_01j9zq7c4m8x2v6t0r3n5k1h7d", t0)
	var m msgs
	m.add(0x10, encMessage(false, 0x18, "deedglass", []byte(hb.Content(t0.Add(1)))))
	m.add(0x20, encMessage(false, 0x28, "deedglass", []byte("act_01j9zt3a5c8x2v6t0r3n5k1h7d")))
	m.add(0x30, encMessage(false, 0x38, "other", []byte("x")))
	b := assemble(t, m, AssembleOptions{})
	if len(b.NonTx) != 3 || len(b.Frames) != 0 || b.LastNonTx != 0x38 {
		t.Fatalf("%+v", b)
	}
	if hb.Check(b.NonTx[0].Content) != HeartbeatValid || hb.Check(b.NonTx[1].Content) != HeartbeatForged {
		t.Fatal("heartbeat check")
	}
	f := NonTxFrame(b.NonTx[1])
	if f.Class != ClassForgedMarkerNonTx || f.CommitLSN != 0x28 || !f.NonTx || f.Markers[0] == "" {
		t.Fatalf("%+v", f)
	}
}

func TestHeartbeatCheck(t *testing.T) {
	con := "con_01j9zq7c4m8x2v6t0r3n5k1h7d"
	hb := NewHeartbeat(con, t0)
	other := NewHeartbeat("con_01j9zq7c4m8x2v6t0r3n5k1h7e", t0)
	ok := hb.Content(t0.Add(5))
	// The MAC's last hex digit, changed: always a different digit (a fixed "0" equalled the real
	// one 1 time in 16 and turned the forged case into the valid one; S21).
	last := "0"
	if strings.HasSuffix(ok, "0") {
		last = "1"
	}
	cases := map[string]HeartbeatVerdict{
		ok:                                     HeartbeatValid,
		hb.Content(t0.Add(-1)):                 HeartbeatStale,
		other.Content(t0.Add(5)):               HeartbeatForeign,
		strings.Replace(ok, ":", ":0", 2):      HeartbeatForged,
		ok[:len(ok)-1] + last:                  HeartbeatForged,
		strings.ToUpper(ok[:4]) + ok[4:]:       HeartbeatForged,
		"hb1:" + con + ":5:" + ok[len(ok)-64:]: HeartbeatStale,
		"hb1:nope":                             HeartbeatForged,
		"act_01j9zt3a5c8x2v6t0r3n5k1h7d":       HeartbeatForged,
	}
	for c, want := range cases {
		if got := hb.Check([]byte(c)); got != want {
			t.Errorf("%q: %v, want %v", c, got, want)
		}
	}
	// A key from another runner of the same connector doesn't verify.
	again := NewHeartbeat(con, t0)
	if again.Check([]byte(ok)) != HeartbeatForged {
		t.Error("another runner's heartbeat verified")
	}
}

func TestOldImages(t *testing.T) {
	var m msgs
	m.add(1, encBegin(0x200, t0, 5))
	m.add(1, encRelation(relPlain, "public", "plain", 'd', plainCols))
	m.add(1, encRelation(relWide, "public", "wide", 'f', wideCols))
	m.add(2, encUpdate(relPlain, 0, nil, []wval{tv("1"), tv("x")}))
	m.add(2, encUpdate(relWide, 'O', []wval{tv("1"), tv("0"), tv("blob")}, []wval{tv("1"), tv("1"), vUnchanged}))
	m.add(2, encDelete(relPlain, 'K', []wval{tv("1"), vNull}))
	m.add(3, encCommit(0x200, 0x230, t0))
	b := assemble(t, m, AssembleOptions{})
	f := b.Frames[0]
	Classify(f, "org_x", false)
	if f.Class != ClassUnattributed || !f.HasFlag("old_image_missing:public.plain") {
		t.Fatalf("%s %v", f.Class, f.Flags)
	}
	if f.Changes[0].Old != nil || f.Changes[0].Changed != nil {
		t.Fatalf("identity default: %+v", f.Changes[0])
	}
	w := f.Changes[1]
	if !slices.Equal(w.Changed, []string{"counter"}) || !w.New["blob"].Unchanged {
		t.Fatalf("TOAST u must count as unchanged: %+v", w)
	}
	d := f.Changes[2]
	if len(d.Old) != 1 || d.Old["id"].Text == nil {
		t.Fatalf("a K image keeps only key columns: %+v", d.Old)
	}
	ApplyKeys(b.Frames, map[uint32][]string{relPlain: {"id"}})
	if !f.HasFlag("no_primary_key:public.wide") || f.Changes[1].Key != nil || *f.Changes[2].Key["id"] != "1" {
		t.Fatalf("%v %+v", f.Flags, f.Changes)
	}
}

func TestImageCapAndDigest(t *testing.T) {
	build := func() msgs {
		var m msgs
		m.add(1, encBegin(0x200, t0, 5))
		m.add(1, encRelation(relOrders, "public", "orders", 'f', orderCols))
		for i := range 1500 {
			id := fmt.Sprint(i)
			m.add(2, encUpdate(relOrders, 'O', []wval{tv(id), tv("paid"), tv("1"), vNull}, []wval{tv(id), tv("shipped"), tv("1"), vNull}))
		}
		m.add(3, encCommit(0x200, 0x230, t0))
		return m
	}
	capped := assemble(t, build(), AssembleOptions{MaxImages: 1000}).Frames[0]
	all := assemble(t, build(), AssembleOptions{MaxImages: 100_000}).Frames[0]
	if len(capped.Changes) != 1000 || capped.Total != 1500 || capped.Counts["public.orders"].Update != 1500 || !capped.HasFlag("images_truncated") {
		t.Fatalf("%d images, total %d, %+v", len(capped.Changes), capped.Total, capped.Flags)
	}
	if len(all.Changes) != 1500 || capped.Digest != all.Digest {
		t.Fatalf("the digest must not depend on the cap: %s vs %s", capped.Digest, all.Digest)
	}
	// The digest is sha256 over JCS(change)+"\n" for every change, computed independently.
	h := sha256.New()
	for _, c := range all.Changes {
		raw, _ := json.Marshal(map[string]any{"seq": c.Seq, "op": c.Op, "table": c.Table, "old": c.Old, "new": c.New})
		canon, err := jcs.Transform(raw)
		if err != nil {
			t.Fatal(err)
		}
		h.Write(append(canon, '\n'))
	}
	if want := "sha256:" + hex.EncodeToString(h.Sum(nil)); want != all.Digest {
		t.Fatalf("digest %s, independent %s", all.Digest, want)
	}
}

func TestSensitiveColumns(t *testing.T) {
	var m msgs
	m.add(1, encBegin(0x200, t0, 5))
	m.add(1, encRelation(relOrders, "public", "orders", 'f', orderCols))
	m.add(2, encInsert(relOrders, []wval{tv("1"), tv("paid"), tv("1"), tv("call me at 555-0100")}))
	m.add(3, encCommit(0x200, 0x230, t0))
	f := assemble(t, m, AssembleOptions{Sensitive: map[string]bool{"public.orders.note": true}}).Frames[0]
	v := f.Changes[0].New["note"]
	sum := sha256.Sum256([]byte("call me at 555-0100"))
	if v.Text != nil || v.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("%+v", v)
	}
	b, _ := json.Marshal(f.Changes)
	if strings.Contains(string(b), "555-0100") {
		t.Fatal("a sensitive value reached the stored images")
	}
}

func TestRelationChanged(t *testing.T) {
	shapes := map[uint32]string{}
	var m1 msgs
	m1.add(1, encBegin(0x200, t0, 5))
	m1.add(1, encRelation(relOrders, "public", "orders", 'f', orderCols[:3]))
	m1.add(2, encInsert(relOrders, []wval{tv("1"), tv("paid"), tv("1")}))
	m1.add(3, encCommit(0x200, 0x230, t0))
	if f := assemble(t, m1, AssembleOptions{Shapes: shapes}).Frames[0]; len(f.Flags) != 0 {
		t.Fatalf("first sighting flagged: %v", f.Flags)
	}
	var m2 msgs
	m2.add(4, encBegin(0x300, t0, 6))
	m2.add(4, encRelation(relOrders, "public", "orders", 'f', orderCols))
	m2.add(5, encInsert(relOrders, []wval{tv("2"), tv("paid"), tv("1"), vNull}))
	m2.add(6, encCommit(0x300, 0x330, t0))
	if f := assemble(t, m2, AssembleOptions{Shapes: shapes}).Frames[0]; !f.HasFlag("relation_changed:public.orders") {
		t.Fatalf("a new shape wasn't flagged: %v", f.Flags)
	}
	// R re-sent per peek with the same shape: no flag.
	if f := assemble(t, m2, AssembleOptions{Shapes: shapes}).Frames[0]; f.HasFlag("relation_changed:public.orders") {
		t.Fatal("the same shape was flagged again")
	}
}

func TestAssembleMalformedStreams(t *testing.T) {
	cases := map[string]msgs{
		"change outside a transaction": {{1, encRelation(relOrders, "public", "orders", 'f', orderCols)},
			{2, encInsert(relOrders, []wval{tv("1"), tv("a"), tv("1"), vNull})}},
		"unknown relation": {{1, encBegin(1, t0, 1)}, {2, encInsert(99, []wval{tv("1")})}, {3, encCommit(1, 2, t0)}},
		"tuple columns":    {{1, encBegin(1, t0, 1)}, {1, encRelation(relOrders, "public", "orders", 'f', orderCols)}, {2, encInsert(relOrders, []wval{tv("1")})}},
		"nested begin":     {{1, encBegin(1, t0, 1)}, {2, encBegin(1, t0, 2)}},
		"commit alone":     {{1, encCommit(1, 2, t0)}},
		"open at the end":  {{1, encBegin(1, t0, 1)}},
		"tx message alone": {{1, encMessage(true, 1, "deedglass", []byte("x"))}},
	}
	for name, m := range cases {
		a := NewAssembler(AssembleOptions{})
		var err error
		for _, r := range m {
			msg, perr := SafeParse(r.Data)
			if perr != nil {
				t.Fatalf("%s: %v", name, perr)
			}
			if err = a.Add(r.LSN, msg); err != nil {
				break
			}
		}
		if err == nil {
			_, err = a.Finish()
		}
		var me *MalformedError
		if err == nil || !asMalformed(err, &me) {
			t.Errorf("%s: %v, want a *MalformedError", name, err)
		}
	}
}

func asMalformed(err error, me **MalformedError) bool {
	m, ok := err.(*MalformedError)
	*me = m
	return ok
}

// TestCanonMatchesJCS: the assembler's direct JCS writer equals gowebpki/jcs on random changes,
// including non-ASCII column names (UTF-16 order), control characters and empty values.
func TestCanonMatchesJCS(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	alphabet := []rune("aZ_0\x01\x1f\"\\é€😀\u2028/<>\t\n ")
	str := func() string {
		n := r.IntN(6)
		var b strings.Builder
		for range n {
			b.WriteRune(alphabet[r.IntN(len(alphabet))])
		}
		return b.String()
	}
	img := func() map[string]Value {
		if r.IntN(4) == 0 {
			return nil
		}
		m := map[string]Value{}
		for range r.IntN(5) {
			var v Value
			switch r.IntN(5) {
			case 0:
				v.Null = true
			case 1:
				v.Unchanged = true
			case 2:
				v.Digest = "sha256:" + strings.Repeat("b", 64)
			case 3:
				v.Hex = "c3"
			default:
				s := str()
				v.Text = &s
			}
			m[str()] = v
		}
		return m
	}
	for i := range 3000 {
		old, nw := img(), img()
		table := str()
		got := appendChangeJCS(nil, i, "U", table, old, nw)
		obj := map[string]any{"seq": i, "op": "U", "table": table}
		if old != nil {
			obj["old"] = old
		}
		if nw != nil {
			obj["new"] = nw
		}
		raw, _ := json.Marshal(obj)
		want, err := jcs.Transform(raw)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("case %d:\n got %s\nwant %s", i, got, want)
		}
	}
}

func TestLedgerRowStrict(t *testing.T) {
	f := newFx(5)
	img := func(vals []wval) map[string]Value {
		m := map[string]Value{}
		for i, c := range ledgerCols {
			v := vals[i]
			switch v.kind {
			case 'n':
				m[c.name] = Value{Null: true}
			default:
				s := v.text
				m[c.name] = Value{Text: &s}
			}
		}
		return m
	}
	if r := ParseLedgerRow(img(f.ledgerRow())); r.Invalid != "" {
		t.Fatal(r.Invalid)
	}
	bad := []func(v []wval){
		func(v []wval) { v[0] = tv("act_nope") },
		func(v []wval) { v[1] = tv("ORG_01J9ZQ7C4M8X2V6T0R3N5K1H7D") },
		func(v []wval) { v[3] = tv("sha256:short") },
		func(v []wval) { v[4] = vNull },
		func(v []wval) { v[5] = tv("-1") },
		func(v []wval) { v[6] = tv("2026-10-07 09:12:09.114-07") },
	}
	for i, mut := range bad {
		v := f.ledgerRow()
		mut(v)
		if r := ParseLedgerRow(img(v)); r.Invalid == "" {
			t.Errorf("case %d accepted", i)
		}
	}
	extra := img(f.ledgerRow())
	extra["note"] = Value{Null: true}
	if r := ParseLedgerRow(extra); r.Invalid == "" {
		t.Error("an extra column was accepted")
	}
}

func TestParseMarker(t *testing.T) {
	good := newFx(1).action
	if id, ok := ParseMarker([]byte(good)); !ok || id != good {
		t.Fatal("valid marker refused")
	}
	for _, bad := range []string{"", "act_", good + " ", strings.ToUpper(good), "agt_" + good[4:], good[:len(good)-1] + "u"} {
		if _, ok := ParseMarker([]byte(bad)); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if e := markerEvidence([]byte("txv1|deadbeef")); !strings.HasPrefix(e, "sha256:") {
		t.Errorf("a forged marker's content is kept as %q", e)
	}
}

func BenchmarkAssembler(b *testing.B) {
	f := newFx(7461)
	var stream []Raw
	for i := range 200 {
		stream = append(stream, boundTx(f, LSN(0x1000000+i*0x1000))...)
	}
	parsed := make([]pglogrepl.Message, len(stream))
	for i, r := range stream {
		m, err := SafeParse(r.Data)
		if err != nil {
			b.Fatal(err)
		}
		parsed[i] = m
	}
	b.ReportAllocs()
	b.ResetTimer()
	n := 0
	for b.Loop() {
		a := NewAssembler(AssembleOptions{Shapes: map[uint32]string{}})
		for i, r := range stream {
			_ = a.Add(r.LSN, parsed[i])
		}
		n += len(stream)
	}
	b.ReportMetric(float64(n)/b.Elapsed().Seconds(), "msgs/s")
}
