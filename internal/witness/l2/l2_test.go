package l2

import (
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/witness"
)

// decideHero runs L2 with S21 §3.6's L0 and L1 reasons and decides.
func decideHero(t *testing.T, r Result, required witness.Level) witness.Decision {
	t.Helper()
	by := map[witness.Level][]witness.Reason{
		witness.L0: {witness.R(witness.L0, witness.CodeToolReportedSuccess, "status", nil)},
		witness.L1: {witness.R(witness.L1, witness.CodeRowWritten, "row.xmin", map[string]any{"key": "id = 42", "table": "public.orders"})},
		witness.L2: r.Reasons,
	}
	return witness.Decide(required, by, map[witness.Level]string{witness.L1: "postgres.row", witness.L2: Method})
}

func TestHero(t *testing.T) {
	r := Evaluate(heroInput(heroFrame()))
	if got := codeSet(r.Reasons); got != "[row_count_exceeded triggers_present unexpected_key]" {
		t.Fatalf("reasons %s", got)
	}
	d := decideHero(t, r, witness.L2)
	if d.Level != witness.L2 || d.Verdict != witness.Mismatch || d.Method != Method {
		t.Fatalf("claim %s %s %s", d.Level, d.Verdict, d.Method)
	}
	const want = "Postgres changed 301 rows in public.orders. The agent reported 1."
	if s := witness.Summary(d); s != want {
		t.Fatalf("summary %q", s)
	}
	for _, x := range d.Reasons {
		if x.Code == witness.CodeUnexpectedKey && x.Message != "300 changed rows in public.orders are outside the rows the statement named (id = 42)." {
			t.Errorf("unexpected_key: %q", x.Message)
		}
		if x.Code == witness.CodeTriggersPresent && !strings.Contains(x.Message, "orders_ship_open_batch") {
			t.Errorf("triggers_present: %q", x.Message)
		}
	}
	// The L1 pass stays listed: row 42 really is right.
	if d.PerLevel[witness.L1] != witness.Verified {
		t.Errorf("L1 %s", d.PerLevel[witness.L1])
	}
	ev := r.Evidence
	if ev.Transaction == nil || ev.Transaction.CommitLSN != "0/3E65020" || ev.Transaction.SystemID == "" || ev.Transaction.Timeline != 1 {
		t.Fatalf("transaction %+v", ev.Transaction)
	}
	if ev.Observed.Total != 301 || ev.Observed.Tables[0].Update != 301 {
		t.Errorf("observed %+v", ev.Observed)
	}
	if ev.RowsShown != 20 || ev.RowsMore != 281 || ev.ColumnsMore != 0 || !ev.Rows[0].Expected || ev.Rows[0].Key["id"] != "42" ||
		ev.Rows[1].Expected {
		t.Errorf("rows shown %d more %d first %+v", ev.RowsShown, ev.RowsMore, ev.Rows[0])
	}
	if c := ev.Rows[0].Cells; len(c) != 1 || c[0].Column != "status" || *c[0].Before != "paid" || *c[0].After != "shipped" {
		t.Errorf("cells %+v", c)
	}
	if e := ev.Expected; e.Derivation != DerivationExactKeys || *e.Tables[0].Rows != 1 || e.Tables[0].Keys[0]["id"] != "42" ||
		strings.Join(e.Tables[0].Columns, ",") != "status" {
		t.Errorf("expected %+v", e)
	}
}

func TestHeroClean(t *testing.T) {
	r := Evaluate(heroInput(frame(upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}}))))
	if got := codeSet(r.Reasons); got != "[footprint_matched triggers_present]" {
		t.Fatalf("reasons %s", got)
	}
	d := decideHero(t, r, witness.L2)
	if d.Level != witness.L2 || d.Verdict != witness.Verified {
		t.Fatalf("claim %s %s", d.Level, d.Verdict)
	}
	if s := witness.Summary(d); s != "Postgres committed 1 row in public.orders, as the statement said." {
		t.Fatalf("summary %q", s)
	}
}

// TestCompareTable is S21 §6's compare table over synthetic frames.
func TestCompareTable(t *testing.T) {
	shipped := map[string][2]string{"status": {"paid", "shipped"}}
	exact := func(cols ...string) Footprint {
		return Footprint{Derivation: DerivationExactKeys, Tables: []TableExpect{{Table: "public.orders", Ops: []string{OpUpdate}, Rows: ip(1),
			Keys: []map[string]string{{"id": "42"}}, Columns: cols}}}
	}
	withValue := func(fp Footprint, col, lit, kind string) Footprint {
		fp.Tables[0].Values = map[string]Literal{col: {Kind: kind, Text: lit}}
		return fp
	}
	countOnly := Footprint{Derivation: DerivationCountOnly, Tables: []TableExpect{{Table: "public.orders", Ops: []string{OpUpdate}, Rows: ip(5),
		Columns: []string{"status"}}}}
	audit := frame(upd(1, 42, shipped), Change{Seq: 2, Op: "I", Table: "public.audit", Key: map[string]*string{"id": sp("1")},
		New: map[string]Value{"id": txt("1"), "note": txt("shipped 42")}})
	del := frame(Change{Seq: 1, Op: "D", Table: "public.orders", Key: map[string]*string{"id": sp("42")}, Old: map[string]Value{"id": txt("42")}})
	twoCols := frame(upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}, "total_cents": {"1200", "0"}}))
	noOld := frame(upd(1, 42, shipped))
	noOld.Tables["public.orders"] = ordersInfo("d")
	noOld.Changes[0].Old, noOld.Changes[0].Changed = nil, nil
	noOld.Flags = []string{"old_image_missing:public.orders"}
	zero := frame()
	zero.Counts["public.orders"] = OpCounts{}
	five := frame(upd(1, 1, shipped), upd(2, 2, shipped), upd(3, 3, shipped), upd(4, 4, shipped), upd(5, 5, shipped))
	cancelled := frame(upd(1, 42, map[string][2]string{"status": {"paid", "cancelled"}}))
	flag := frame(upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}}))
	flag.Tables["public.orders"] = TableInfo{PK: []string{"id"}, Identity: "f", Columns: []string{"id", "flag"}, TypeOIDs: []uint32{oidInt4, oidBool}}
	flag.Changes[0] = Change{Seq: 1, Op: "U", Table: "public.orders", Key: map[string]*string{"id": sp("42")},
		Old: map[string]Value{"id": txt("42"), "flag": txt("f")}, New: map[string]Value{"id": txt("42"), "flag": txt("t")}, Changed: []string{"flag"}}
	// 1,500 changes, 1,000 images, the key needed is past the cap.
	big := frame()
	for i := 1; i <= 1000; i++ {
		big.add(upd(i, i, shipped))
	}
	big.count("public.orders", "U", 500)
	big.Flags = []string{"images_truncated"}
	bigFP := Footprint{Derivation: DerivationExactKeys, Tables: []TableExpect{{Table: "public.orders", Ops: []string{OpUpdate}, Rows: ip(1),
		Keys: []map[string]string{{"id": "1200"}}, Columns: []string{"status"}}}}
	trunc := frame(upd(1, 42, shipped))
	trunc.count("public.orders", "T", 1)
	trunc.Flags = []string{"truncate:public.orders"}
	rel := frame(upd(1, 42, shipped))
	rel.Flags = []string{"relation_changed:public.orders"}
	sens := frame(upd(1, 42, shipped))
	sens.Changes[0].Old["status"], sens.Changes[0].New["status"] = Value{D: "sha256:aa"}, Value{D: "sha256:bb"}
	cases := []struct {
		name string
		fp   Footprint
		f    Frame
		cfg  Config
		want string
	}{
		{"U 1 on id 42, expected id 42, status", exact("status"), frame(upd(1, 42, shipped)), Config{}, "[footprint_matched]"},
		{"U 301, expected 1 (hero)", exact("status"), heroFrame(), Config{}, "[row_count_exceeded unexpected_key]"},
		{"U 1 plus I 1 on public.audit", exact("status"), audit, Config{}, "[unexpected_table]"},
		{"D 1 where only an update was expected", exact("status"), del, Config{}, "[missing_change unexpected_operation]"},
		{"U 1 changing status and total_cents; SET only status", exact("status"), twoCols, Config{}, "[unexpected_column]"},
		{"the same with auto_columns total_cents", exact("status", "total_cents"), twoCols, Config{}, "[footprint_matched]"},
		{"identity default, a column check needed", exact("status"), noOld, Config{}, "[old_image_missing]"},
		{"U 0 where reported 1", exact("status"), zero, Config{}, "[missing_change]"},
		{"count_only U 5, reported 5", countOnly, five, Config{}, "[footprint_matched]"},
		{"SET status = 'shipped', committed 'cancelled'", withValue(exact("status"), "status", "shipped", "string"), cancelled, Config{}, "[value_mismatch]"},
		{"SET flag = true, committed t", withValue(exact("flag"), "flag", "true", "bool"), flag, Config{}, "[footprint_matched]"},
		{"1,500 changes, 1,000 images, key 1,200 needed", bigFP, big, Config{}, "[keys_unknown row_count_exceeded unexpected_key]"},
		{"TRUNCATE in the transaction", exact("status"), trunc, Config{}, "[unexpected_operation]"},
		{"relation_changed flag", exact("status"), rel, Config{}, "[relation_changed]"},
		{"max_rows 1 on the hero", exact("status"), heroFrame(), Config{MaxRows: map[string]int64{"public.orders": 1}},
			"[row_count_exceeded row_limit_exceeded unexpected_key]"},
		{"sensitive column changed", exact("status"), sens, Config{}, "[footprint_matched]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.f
			got := codeSet(Compare(c.fp, &f, c.cfg))
			if got != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
	// The sensitive column shows ••• and is listed in redacted.
	ev := BuildDiff(exact("status"), &sens, Config{})
	if len(ev.Rows) != 1 || *ev.Rows[0].Cells[0].After != Hidden || strings.Join(ev.Redacted, ",") != "public.orders.status" {
		t.Fatalf("redaction: %+v %v", ev.Rows, ev.Redacted)
	}
}

// TestNoGapBecomesViolation: a gap alone never gives MISMATCH at L2, and a violation always does
// (S21 §3.12 invariant 1, over Decide with the L2 codes).
func TestNoGapBecomesViolation(t *testing.T) {
	for _, e := range witness.Catalog() {
		if !strings.Contains(e.Levels, "L2") {
			continue
		}
		r := witness.R(witness.L2, e.Code, "test", e.Sample)
		for _, req := range witness.Levels {
			d := witness.Decide(req, map[witness.Level][]witness.Reason{witness.L2: {r}}, nil)
			switch e.Class {
			case witness.Gap, witness.Note:
				if d.Verdict == witness.Mismatch || d.Verdict == witness.Verified && e.Class == witness.Gap {
					t.Errorf("%s (%s) required %s gave %s", e.Code, e.Class, req, d.Verdict)
				}
			case witness.Violation:
				if d.Verdict != witness.Mismatch {
					t.Errorf("%s gave %s", e.Code, d.Verdict)
				}
			}
		}
	}
}
