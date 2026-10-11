package l2

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/witness"
)

func TestGapResults(t *testing.T) {
	cases := []struct {
		r    Result
		code witness.Code
		msg  string
	}{
		{Timeout(10 * time.Second), witness.CodeL2Timeout, "Postgres's change log didn't show this action within 10s."},
		{Timeout(1500 * time.Millisecond), witness.CodeL2Timeout, "within 1.5s."},
		{Lost("slot_invalidated"), witness.CodeSlotInvalidated, "(slot_invalidated)"},
		{Lost("slot_missing"), witness.CodeSlotDropped, "was dropped while this action waited"},
		{Lost("dropped"), witness.CodeSlotDropped, "can't be read"},
		{Lost("connector_deleted"), witness.CodeSlotDropped, "dropped"},
		{Lost("publication_missing"), witness.CodeDecoderHalted, "(publication_missing)"},
		{NoEvidence(), witness.CodeNoEvidence, "never appeared in the change log"},
	}
	for _, c := range cases {
		if len(c.r.Reasons) != 1 || c.r.Reasons[0].Code != c.code || c.r.Reasons[0].Class != witness.Gap || c.r.Reasons[0].Level != witness.L2 ||
			!strings.Contains(c.r.Reasons[0].Message, c.msg) || c.r.HasFrame() {
			t.Errorf("%s: %+v", c.code, c.r.Reasons)
		}
	}
	if ev := Lost("dropped").Reasons[0].Evidence; ev["reason"] != "dropped" {
		t.Errorf("evidence %v", ev)
	}
	if r := Evaluate(Input{}); len(r.Reasons) != 0 || r.HasFrame() {
		t.Errorf("no frames: %+v", r)
	}
}

func TestFrameClasses(t *testing.T) {
	in := heroInput(frame(upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}})))
	// Two bound frames: multiple_transactions, and the comparison still reads the first.
	second := in.Frames[0]
	second.ID, second.CommitLSN = "dtx_01m3x71p6wesj7xewec6bwbwy1", "0/3E70000"
	in.Frames = append(in.Frames, second)
	r := Evaluate(in)
	if got := codeSet(r.Reasons); got != "[footprint_matched multiple_transactions triggers_present]" || r.Evidence.Frames != 2 || len(r.Evidence.FrameIDs) != 2 {
		t.Fatalf("multiple: %s %+v", got, r.Evidence)
	}
	// marker_count and marker_mismatch name their evidence.
	mc := frame(upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}}))
	mc.Class, mc.Markers = ClassMarkerCount, []string{actID, actID}
	mm := frame(upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}}))
	mm.Class, mm.Flags = ClassMarkerMismatch, []string{"ledger_xid_mismatch"}
	for _, c := range []struct {
		f    Frame
		code witness.Code
		msg  string
	}{{mc, witness.CodeMarkerCount, "1 ledger rows and 2 markers"}, {mm, witness.CodeMarkerMismatch, "the ledger row names another transaction"}} {
		in := heroInput(c.f)
		found := false
		for _, x := range Evaluate(in).Reasons {
			if x.Code == c.code && strings.Contains(x.Message, c.msg) && x.Class == witness.Violation {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: %v", c.code, Evaluate(in).Reasons)
		}
	}
	// A publication gap alone: the gap, the transaction, no rows.
	pg := frame(upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}}))
	pg.Class, pg.Ledger = ClassPublicationGap, nil
	in = heroInput(pg)
	r = Evaluate(in)
	if codeSet(r.Reasons) != "[publication_gap]" || !r.HasFrame() || len(r.Evidence.Rows) != 0 || r.Evidence.Expected != nil {
		t.Fatalf("publication gap: %v %+v", r.Reasons, r.Evidence)
	}
}

func TestCrossCheck(t *testing.T) {
	base := heroInput(frame(upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}})))
	h := "sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		name   string
		mutate func(a *Action, lr *LedgerRow)
		fields string
	}{
		{"sql digest", func(a *Action, lr *LedgerRow) { lr.SQLDigest = "sha256:" + strings.Repeat("0", 64) }, "sql_digest"},
		{"agent", func(a *Action, lr *LedgerRow) { lr.AgentID = "agt_01m3x70000000000000000000z" }, "agent_id"},
		{"org", func(a *Action, lr *LedgerRow) { lr.OrgID = "org_01m3x70000000000000000000z" }, "org_id"},
		{"intent on one side", func(a *Action, lr *LedgerRow) { a.IntentHash = &h }, "intent_hash"},
		{"invalid row", func(a *Action, lr *LedgerRow) { lr.Invalid = "bad xid8" }, "row"},
		{"undo plan digest", func(a *Action, lr *LedgerRow) { a.PlanDigest = h }, "sql_digest"},
		{"same intent", func(a *Action, lr *LedgerRow) { a.IntentHash, lr.IntentHash = &h, &h }, ""},
		{"empty intent equals none", func(a *Action, lr *LedgerRow) { e := ""; a.IntentHash = &e }, ""},
	}
	for _, c := range cases {
		in := base
		in.Frames = []Frame{frame(upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}}))}
		c.mutate(&in.Action, &in.Frames[0].Ledger[0])
		var got string
		for _, r := range Evaluate(in).Reasons {
			if r.Code == witness.CodeLedgerMismatch {
				got = strings.Join(anyStrings(r.Evidence["fields"]), ",")
				if strings.Contains(r.Message, "sha256:") {
					t.Errorf("%s: the message shows a value: %s", c.name, r.Message)
				}
			}
		}
		if got != c.fields {
			t.Errorf("%s: fields %q, want %q", c.name, got, c.fields)
		}
	}
}

func anyStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, len(x))
		for i, s := range x {
			out[i], _ = s.(string)
		}
		return out
	}
	return nil
}

func TestCanon(t *testing.T) {
	cases := []struct {
		oid      uint32
		a, b     string
		key, ok  bool
		same     bool
		comments string
	}{
		{oidInt4, "042", "42", true, true, true, "leading zeros"},
		{oidInt8, "9007199254740993", "9007199254740993", false, true, true, "past 2^53"},
		{oidInt2, "x", "1", false, false, false, "not a number"},
		{oidBool, "true", "t", false, true, true, "booleans"},
		{oidBool, "off", "f", false, true, true, "off"},
		{oidBool, "maybe", "t", false, false, false, "not a boolean"},
		{oidBpchar, "ab  ", "ab", false, true, true, "bpchar padding"},
		{oidVarchar, "a", "A", false, true, false, "case matters"},
		{oidUUID, "{A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11}", "a0eebc999c0b4ef8bb6d6bb9bd380a11", true, true, true, "uuid keys"},
		{oidUUID, "a", "a", false, false, false, "uuid values aren't compared"},
		{1700, "1.0", "1", false, false, false, "numeric isn't compared"},
	}
	for _, c := range cases {
		x, ok1 := canon(c.oid, c.a, c.key)
		y, ok2 := canon(c.oid, c.b, c.key)
		if ok1 != c.ok || c.ok && ok2 && (x == y) != c.same {
			t.Errorf("%s: %q %q → %q %v, %q %v", c.comments, c.a, c.b, x, ok1, y, ok2)
		}
	}
}

func TestValueTextAndOps(t *testing.T) {
	for _, c := range []struct {
		v    Value
		s    string
		ok   bool
		name string
	}{
		{Value{D: "sha256:aa"}, Hidden, true, "sensitive"},
		{Value{N: true}, "NULL", true, "null"},
		{Value{U: true}, "", false, "unchanged toast"},
		{Value{X: "ff"}, `\xff`, true, "hex"},
		{Value{T: sp("x")}, "x", true, "text"},
		{Value{}, "", false, "empty"},
	} {
		if s, ok := c.v.Text(); s != c.s || ok != c.ok {
			t.Errorf("%s: %q %v", c.name, s, ok)
		}
	}
	if opName("I") != OpInsert || opName("D") != OpDelete || opName("T") != "T" {
		t.Error("opName")
	}
	if expectedOps([]string{OpInsert, OpUpdate}) != "inserts and updates rows" || expectedOps([]string{OpDelete}) != "deletes rows" {
		t.Errorf("expectedOps %q", expectedOps([]string{OpInsert, OpUpdate}))
	}
	for id, w := range map[string]string{"f": "FULL", "n": "NOTHING", "i": "USING INDEX", "d": "DEFAULT"} {
		if identityWord(id) != w {
			t.Errorf("identity %s", id)
		}
	}
	if count(1204) != "1,204" || count(301) != "301" || count(1000000) != "1,000,000" {
		t.Errorf("count %s", count(1204))
	}
	if AgentLine(witness.L2, witness.Mismatch, "Postgres changed 301 rows in public.orders. The agent reported 1.") !=
		"Deedglass check · Proven · Doesn't match · Postgres changed 301 rows in public.orders. The agent reported 1." {
		t.Error("AgentLine")
	}
	if !strings.HasPrefix(AgentPending, "Deedglass check · pending · ") {
		t.Error("AgentPending")
	}
}

func TestParseParts(t *testing.T) {
	f := heroFrame()
	ledger, _ := json.Marshal(f.Ledger)
	tables, _ := json.Marshal(f.Tables)
	counts, _ := json.Marshal(f.Counts)
	changes, _ := json.Marshal(f.Changes)
	var g Frame
	if err := g.ParseParts(ledger, tables, counts, changes); err != nil {
		t.Fatal(err)
	}
	if len(g.Changes) != 301 || g.Counts["public.orders"].Update != 301 || g.Ledger[0].ActionID != actID || g.Tables["public.orders"].TypeOf("status") != oidText {
		t.Fatalf("%+v", g.Counts)
	}
	if err := g.ParseParts([]byte("{"), nil, nil, nil); err == nil {
		t.Error("a broken ledger parsed")
	}
	if err := (&Frame{}).ParseParts(nil, []byte("null"), nil, nil); err != nil {
		t.Error(err)
	}
}

// TestRowLimitsAndUnknown: an unknown footprint still gets the connector's max_rows, and never a pass.
func TestRowLimitsAndUnknown(t *testing.T) {
	in := heroInput(heroFrame())
	in.Derive.SQL = "UPDATE public.orders o SET status = 'shipped' FROM public.customers c WHERE c.id = o.customer_id AND o.id = 42"
	in.Config.MaxRows = map[string]int64{"public.orders": 1}
	r := Evaluate(in)
	if got := codeSet(r.Reasons); got != "[footprint_unknown row_limit_exceeded]" {
		t.Fatalf("%s", got)
	}
	if r.Evidence.Expected.Derivation != DerivationUnknown || len(r.Evidence.Rows) == 0 || r.Evidence.Rows[0].Expected {
		t.Fatalf("an unknown footprint marks no row expected: %+v", r.Evidence.Rows[0])
	}
	f := heroFrame()
	if got := RowLimits(&f, Config{}); len(got) != 0 {
		t.Errorf("no max_rows: %v", got)
	}
}

// TestDiffInsertDelete: inserts and deletes show their columns, a contract's tables, and an
// image without a full old row shows the SET columns it has.
func TestDiffInsertDelete(t *testing.T) {
	f := frame(
		Change{Seq: 1, Op: "I", Table: "public.refunds", Key: map[string]*string{"id": sp("901")}, New: map[string]Value{"id": txt("901"), "note": txt("damaged")}},
		Change{Seq: 2, Op: "D", Table: "public.refunds", Key: map[string]*string{"id": sp("7")}, Old: map[string]Value{"id": txt("7"), "note": {N: true}}},
		Change{Seq: 3, Op: "U", Table: "public.orders", Key: map[string]*string{"id": sp("42")}, New: map[string]Value{"id": txt("42"), "status": txt("shipped")}},
	)
	fp := Footprint{Derivation: DerivationContract, Tables: []TableExpect{
		{Table: "public.refunds", Ops: []string{OpInsert, OpDelete}},
		{Table: "public.orders", Ops: []string{OpUpdate}, Columns: []string{"status"}},
	}}
	ev := BuildDiff(fp, &f, Config{})
	if len(ev.Rows) != 3 || ev.Rows[0].Op != OpInsert || *ev.Rows[0].Cells[0].After != "damaged" || ev.Rows[1].Cells[0].Before != nil ||
		ev.Rows[2].Cells[0].Before != nil || *ev.Rows[2].Cells[0].After != "shipped" {
		t.Fatalf("%+v", ev.Rows)
	}
	lines := strings.Join(Render(witness.Detail{Levels: witness.LevelsDetail{L2: &witness.LevelDetail{Verdict: witness.Verified, Method: Method, L2Evidence: ev}}},
		RenderOptions{Width: 80}), "\n")
	for _, want := range []string{"→ damaged", "NULL → deleted", "? → shipped", "INSERT or DELETE rows in public.refunds", "row "} {
		if !strings.Contains(lines, want) {
			t.Errorf("render lacks %q:\n%s", want, lines)
		}
	}
	// A narrow terminal clips long values.
	long := frame(upd(1, 42, map[string][2]string{"status": {strings.Repeat("p", 40), "shipped"}}))
	ev = BuildDiff(Footprint{Derivation: DerivationCountOnly, Tables: []TableExpect{{Table: "public.orders", Ops: []string{OpUpdate}, Columns: []string{"status"}}}}, &long, Config{})
	out := strings.Join(Render(witness.Detail{Levels: witness.LevelsDetail{L2: &witness.LevelDetail{Verdict: witness.Verified, Method: Method, L2Evidence: ev}}},
		RenderOptions{Width: 80, Indent: "  "}), "\n")
	if !strings.Contains(out, "…") {
		t.Errorf("not clipped:\n%s", out)
	}
	if Render(witness.Detail{}, RenderOptions{}) != nil {
		t.Error("no L2 evidence renders nothing")
	}
}

// TestDetailCap: a detail over witness.max_detail_bytes keeps fewer rows, counted.
func TestDetailCap(t *testing.T) {
	r := Evaluate(heroInput(heroFrame()))
	d := decideHero(t, r, witness.L2)
	out, err := witness.FinishWith(d, map[witness.Level]string{witness.L2: Method}, witness.Extras{L2: r.Evidence}, time.Now(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	l2 := out.Detail.Levels.L2
	if len(out.JSON) > 4096 || l2.RowsShown >= 20 || l2.RowsShown+l2.RowsMore != 301 || out.Detail.Truncated == 0 {
		t.Fatalf("%d bytes, shown %d, more %d, truncated %d", len(out.JSON), l2.RowsShown, l2.RowsMore, out.Detail.Truncated)
	}
}
