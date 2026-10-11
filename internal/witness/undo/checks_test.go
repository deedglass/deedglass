package undo

import (
	"slices"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 7, 13, 18, 9, 0, time.UTC)

// basePlan is an update (orders 1), an insert (orders 7) and a delete (order_items 90): the plan
// deletes orders 7, updates orders 1 and re-inserts order_items 90.
func basePlan(t *testing.T, cat *Catalog) (*Evidence, *Plan) {
	t.Helper()
	ev := evidence(upd(1, ordersRef, order("1", "paid"), order("1", "shipped")), ins(2, ordersRef, order("7", "new")),
		del(3, itemsRef, item("90", "1")))
	p, fs := build(ev, cat)
	if p == nil {
		t.Fatalf("Build: %v", fs)
	}
	return ev, p
}

// baseCurrent is the rows as the original transaction left them.
func baseCurrent() Current {
	return Current{
		100: {"1": order("1", "shipped"), "7": order("7", "new")},
		200: {},
	}
}

// baseInput passes every check.
func baseInput(t *testing.T) CheckInput {
	cat := defaultCatalog()
	ev, p := basePlan(t, cat)
	return CheckInput{
		Action: ActionFacts{ID: actID, Sealed: true, Mode: "gateway", Tool: "postgres.execute", WitnessLevel: "L2", Verdict: "VERIFIED",
			SealedAt: now.Add(-6 * time.Minute), Executed: true},
		Connector: ConnectorFacts{Exists: true, Name: "postgres", Type: "postgres", Status: "active", HasCredentials: true, WitnessEnabled: true},
		Now:       now, MaxAge: 7 * 24 * time.Hour, MaxRows: 1000,
		Evidence: ev, Plan: p, Catalog: cat, Current: baseCurrent(),
	}
}

func checkByID(cs []Check) map[string]Check {
	out := map[string]Check{}
	for _, c := range cs {
		out[c.ID] = c
	}
	return out
}

func TestChecksAllPass(t *testing.T) {
	cs := Checks(baseInput(t))
	var ids []string
	for _, c := range cs {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, CheckOrder) {
		t.Fatalf("ids %q, want %q", ids, CheckOrder)
	}
	want := map[string]string{
		CheckEvidence:        "L2 · VERIFIED · 3 row images · xid 13130",
		CheckConnector:       "postgres (native Postgres) · active",
		CheckAge:             "sealed 6 min ago · limit 7 days",
		CheckNotUndone:       "",
		CheckRowLimit:        "3 of 1,000",
		CheckReplicaIdentity: "full on public.order_items and public.orders",
		CheckPrimaryKey:      "public.order_items (id); public.orders (id)",
		CheckColumns:         "unchanged since the action",
		CheckTriggers:        "No enabled triggers on public.order_items and public.orders.",
		CheckCascades:        "no cascading foreign keys",
		CheckUnchanged:       "3 of 3 rows still hold what the action wrote",
		CheckPrivileges:      "deedglass_agent can SELECT and INSERT public.order_items; SELECT, UPDATE and DELETE public.orders",
	}
	for _, c := range cs {
		if c.Status != StatusPass || c.Detail != want[c.ID] || c.Title != CheckTitles[c.ID] || c.DocURL != "" || c.Fix != "" || c.FixSQL != "" {
			t.Errorf("%s: %+v\nwant pass %q", c.ID, c, want[c.ID])
		}
	}
	if !Available(cs) || Failed(cs) != nil {
		t.Fatalf("Available %v, Failed %q", Available(cs), Failed(cs))
	}
}

func TestChecksOneEach(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(in *CheckInput)
		id     string
		status Status
		detail string
		fix    string
		fixSQL string
	}{
		// 1. evidence
		{"not sealed", func(in *CheckInput) { in.Action.Sealed = false }, CheckEvidence, StatusFail, MsgNotSealed, FixFailing, ""},
		{"nothing written", func(in *CheckInput) { in.Action.Executed = false }, CheckEvidence, StatusFail, MsgNotWritten, FixFailing, ""},
		{"witnessed at L1", func(in *CheckInput) {
			in.Action.WitnessLevel = "L1"
			in.Evidence = FromFrames(FrameInput{Action: actID})
			in.Plan, in.Findings = build(in.Evidence, in.Catalog)
		}, CheckEvidence, StatusFail,
			"Undo needs L2 evidence from Postgres. This action was witnessed at L1, so Deedglass doesn't know every row it changed.", FixFailing, ""},
		{"not witnessed at all", func(in *CheckInput) {
			in.Action.WitnessLevel = ""
			in.Evidence = FromFrames(FrameInput{Action: actID})
			in.Plan, in.Findings = build(in.Evidence, in.Catalog)
		}, CheckEvidence, StatusFail, ProblemNoFrame.Text, FixFailing, ""},
		{"a truncate in the evidence", func(in *CheckInput) {
			in.Evidence = fromFlags("truncate:public.orders")
			in.Plan, in.Findings = build(in.Evidence, in.Catalog)
		}, CheckEvidence, StatusFail, ProblemTruncate("public.orders").Text, FixFailing, ""},
		{"nothing to put back", func(in *CheckInput) {
			in.Evidence = evidence(ins(1, ordersRef, order("7", "a")), del(2, ordersRef, order("7", "a")))
			in.Plan, in.Findings = build(in.Evidence, in.Catalog)
		}, CheckEvidence, StatusFail, MsgNothing, FixFailing, ""},
		{"an old image the evidence lacks, with fix SQL", func(in *CheckInput) {
			in.Findings = []Finding{ProblemOldImage("public.orders", "d")}
		}, CheckEvidence, StatusFail, ProblemOldImage("public.orders", "d").Text, FixFailing, "ALTER TABLE public.orders REPLICA IDENTITY FULL;"},
		// 2. connector
		{"proxy mode", func(in *CheckInput) { in.Action.Mode = "proxy" }, CheckConnector, StatusFail, MsgConnectorProxy, FixFailing, ""},
		{"connector deleted", func(in *CheckInput) { in.Connector.Exists = false }, CheckConnector, StatusFail,
			`The "postgres" connector was deleted. Undo runs through the connector that made the change.`, FixFailing, ""},
		{"not Postgres", func(in *CheckInput) { in.Connector.Type = "http" }, CheckConnector, StatusFail,
			"Undo works for Postgres writes with a Proven check. This action ran through http.", FixFailing, ""},
		{"no connector type", func(in *CheckInput) { in.Connector.Type = "" }, CheckConnector, StatusFail,
			"Undo works for Postgres writes with a Proven check. This action ran through a connector without L2.", FixFailing, ""},
		{"connector paused", func(in *CheckInput) { in.Connector.Status = "paused" }, CheckConnector, StatusFail,
			`The "postgres" connector is paused. Undo runs through the connector that made the change; turn it back on first.`, FixFailing, ""},
		{"no credentials", func(in *CheckInput) { in.Connector.HasCredentials = false }, CheckConnector, StatusFail,
			`The "postgres" connector has no credentials. Set them in Console › Connectors, then preview again.`, FixFailing, ""},
		{"witness off", func(in *CheckInput) { in.Connector.WitnessEnabled = false }, CheckConnector, StatusFail,
			`Witness is off for the "postgres" connector, so the undo itself couldn't be witnessed. Turn on witness in the connector's config.`,
			FixFailing, ""},
		// 3. age
		{"pruned", func(in *CheckInput) {
			at := time.Date(2026, 10, 6, 23, 0, 0, 0, time.FixedZone("IST", 19800))
			in.Action.PayloadPruned = &at
		}, CheckAge, StatusFail, "The row images of this action were pruned on 2026-10-06. Restore from a backup.", FixFailing, ""},
		{"9 days old", func(in *CheckInput) { in.Action.SealedAt = now.Add(-9 * 24 * time.Hour) }, CheckAge, StatusFail,
			"This action is 9 days old; undo works for 7 days while the row images are kept.", FixFailing, ""},
		{"not sealed skips age", func(in *CheckInput) { in.Action.Sealed = false }, CheckAge, StatusSkip, "", "", ""},
		{"sealed in the future reads 0 s", func(in *CheckInput) { in.Action.SealedAt = now.Add(time.Minute) }, CheckAge, StatusPass,
			"sealed just now · limit 7 days", "", ""},
		// 4. not_undone
		{"already undone", func(in *CheckInput) {
			in.Other = &OtherUndo{ID: "act_01j9zx8kqqqqqqqqqqqqqqqqqq", State: "applied", At: "10:14 IST"}
		}, CheckNotUndone, StatusFail, "act_01j9zx8k… already undid this action at 10:14 IST.", "To put it back, preview an undo of act_01j9zx8k…", ""},
		{"an undo running", func(in *CheckInput) { in.Other = &OtherUndo{ID: "act_01j9zx8kqqqq", State: "running"} }, CheckNotUndone, StatusFail,
			"An undo of this action is already running: act_01j9zx8k…", "Wait for it to finish, then preview again.", ""},
		{"an undo waiting", func(in *CheckInput) { in.Other = &OtherUndo{ID: "act_01j9zx8kqqqq", State: "pending"} }, CheckNotUndone, StatusFail,
			"An undo of this action is already waiting: act_01j9zx8k…", "Countersign or cancel it first.", ""},
		// 5. row_limit
		{"over the row limit", func(in *CheckInput) { in.MaxRows = 2 }, CheckRowLimit, StatusFail,
			"This undo touches 3 rows; the limit is 2. Restore from a backup, or raise undo.max_rows.", "", ""},
		// 6. replica_identity
		{"replica identity default", func(in *CheckInput) { in.Catalog.Tables[200].ReplIdent = "d" }, CheckReplicaIdentity, StatusFail,
			"Replica identity public.order_items is default, so the undo itself couldn't be witnessed.", FixReplica,
			"ALTER TABLE public.order_items REPLICA IDENTITY FULL;"},
		{"replica identity nothing and index", func(in *CheckInput) {
			in.Catalog.Tables[200].ReplIdent = "n"
			in.Catalog.Tables[100].ReplIdent = "i"
		}, CheckReplicaIdentity, StatusFail,
			"Replica identity public.order_items is nothing; public.orders is index, so the undo itself couldn't be witnessed.", FixReplica,
			"ALTER TABLE public.order_items REPLICA IDENTITY FULL;\nALTER TABLE public.orders REPLICA IDENTITY FULL;"},
		// 7. primary_key, 8. columns (Build's findings)
		{"primary key finding", func(in *CheckInput) { in.Findings = []Finding{ProblemNoPrimaryKey("public.events")} }, CheckPrimaryKey, StatusFail,
			"public.events has no primary key, so Deedglass can't point at one row safely.", FixFailing, ""},
		{"columns finding with its own fix", func(in *CheckInput) {
			in.Findings = []Finding{{Check: CheckColumns, Text: "b.", Fix: "Do b.", FixSQL: "SELECT 2;"},
				{Check: CheckColumns, Text: "a.", Fix: "Do a.", FixSQL: "SELECT 1;"}, {Check: CheckColumns, Text: "c.", FixSQL: "SELECT 1;"}}
		}, CheckColumns, StatusFail, "b. a. c.", "Do b.", "SELECT 2;\nSELECT 1;"},
		{"a warning finding", func(in *CheckInput) { in.Findings = []Finding{{Check: CheckColumns, Text: "Heads up.", Warn: true}} },
			CheckColumns, StatusWarn, "Heads up.", "", ""},
		// 9. triggers
		{"an enabled trigger", func(in *CheckInput) { in.Catalog.Tables[100].Triggers = []string{"orders_bulk_touch"} }, CheckTriggers, StatusFail,
			"public.orders has trigger orders_bulk_touch. Undo would fire this trigger again.", FixAllowTrig,
			"ALTER TABLE public.orders DISABLE TRIGGER orders_bulk_touch;"},
		{"an enabled trigger with allow_triggers", func(in *CheckInput) {
			in.Catalog.Tables[100].Triggers = []string{"orders_bulk_touch"}
			in.AllowTriggers = true
		}, CheckTriggers, StatusWarn,
			"public.orders has trigger orders_bulk_touch. The undo fires it again; the witness shows what it did.", "", ""},
		{"a rule", func(in *CheckInput) { in.Catalog.Tables[200].Rules = []string{"Keep_Log"} }, CheckTriggers, StatusFail,
			"public.order_items has rule Keep_Log. Undo would fire this trigger again.", FixAllowTrig,
			`ALTER TABLE public.order_items DISABLE RULE "Keep_Log";`},
		// 10. cascades
		{"ON DELETE CASCADE with a planned delete", func(in *CheckInput) {
			in.Catalog.Tables[100].Referrers = []ForeignKey{{Name: "items_order", From: "public.order_items", Columns: []string{"id"}, OnDelete: "c"}}
		}, CheckCascades, StatusFail,
			"public.order_items references public.orders ON DELETE CASCADE. Deleting public.orders id=7 would change its rows in public.order_items too.",
			"Undo refuses a step that would change rows it didn't plan.", ""},
		{"ON DELETE SET NULL", func(in *CheckInput) {
			in.Catalog.Tables[100].Referrers = []ForeignKey{{From: "public.order_items", Columns: []string{"id"}, OnDelete: "n"}}
		}, CheckCascades, StatusFail,
			"public.order_items references public.orders ON DELETE SET NULL. Deleting public.orders id=7 would change its rows in public.order_items too.",
			"Undo refuses a step that would change rows it didn't plan.", ""},
		{"ON UPDATE SET DEFAULT on a column put back", func(in *CheckInput) {
			in.Catalog.Tables[100].Referrers = []ForeignKey{{From: "public.order_items", Columns: []string{"status"}, OnUpdate: "d"}}
		}, CheckCascades, StatusFail,
			"public.order_items references public.orders ON UPDATE SET DEFAULT. Putting back status would change its rows in public.order_items too.",
			"Undo refuses a step that would change rows it didn't plan.", ""},
		{"restrict and no action are fine", func(in *CheckInput) {
			in.Catalog.Tables[100].Referrers = []ForeignKey{{From: "public.order_items", Columns: []string{"id"}, OnDelete: "r", OnUpdate: "a"},
				{From: "public.x", Columns: []string{"note"}, OnUpdate: "c"}}
		}, CheckCascades, StatusPass, "no cascading foreign keys", "", ""},
		// 11. unchanged
		{"not read", func(in *CheckInput) { in.Current = nil }, CheckUnchanged, StatusSkip, "", "", ""},
		{"a row changed since", func(in *CheckInput) { in.Current[100]["1"] = order("1", "cancelled") }, CheckUnchanged, StatusFail,
			"public.orders id=1 changed since the action: status is now 'cancelled'.", FixRowsChanged, ""},
		{"a re-insert key exists again, and a row is gone", func(in *CheckInput) {
			in.Current[200]["90"] = item("90", "1")
			delete(in.Current[100], "7")
		}, CheckUnchanged, StatusFail, "public.orders id=7 is gone since the action. 1 more row differs.", FixRowsChanged, ""},
		// 12. privileges
		{"no INSERT", func(in *CheckInput) { in.Catalog.Tables[200].Insert = false }, CheckPrivileges, StatusFail,
			"deedglass_agent can't INSERT public.order_items.", FixPrivileges, "GRANT INSERT ON public.order_items TO deedglass_agent;"},
		{"no SELECT, UPDATE or DELETE, another role", func(in *CheckInput) {
			in.Catalog.Role = "app_writer"
			tc := in.Catalog.Tables[100]
			tc.Select, tc.Update, tc.Delete = false, false, false
		}, CheckPrivileges, StatusFail, "app_writer can't SELECT, UPDATE and DELETE public.orders.", FixPrivileges,
			"GRANT SELECT, UPDATE, DELETE ON public.orders TO deedglass_agent;"},
		{"row-level security", func(in *CheckInput) { in.Catalog.Tables[100].RLS = true }, CheckPrivileges, StatusWarn,
			"public.orders has row-level security; a row deedglass_agent can't see fails the undo cleanly.", "", ""},
		{"row-level security with BYPASSRLS", func(in *CheckInput) {
			in.Catalog.Tables[100].RLS = true
			in.Catalog.BypassRLS = true
			in.Catalog.Role = ""
		}, CheckPrivileges, StatusPass,
			"deedglass_agent can SELECT and INSERT public.order_items; SELECT, UPDATE and DELETE public.orders", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput(t)
			tc.mutate(&in)
			cs := Checks(in)
			if len(cs) != len(CheckOrder) {
				t.Fatalf("%d checks", len(cs))
			}
			c := checkByID(cs)[tc.id]
			if c.Status != tc.status || c.Detail != tc.detail || c.Fix != tc.fix || c.FixSQL != tc.fixSQL {
				t.Fatalf("%s:\n got %s %q fix %q sql %q\nwant %s %q fix %q sql %q", tc.id, c.Status, c.Detail, c.Fix, c.FixSQL,
					tc.status, tc.detail, tc.fix, tc.fixSQL)
			}
			wantDoc := ""
			if c.Status == StatusFail || c.Status == StatusWarn {
				wantDoc = DocURL
			}
			if c.DocURL != wantDoc {
				t.Fatalf("doc url %q", c.DocURL)
			}
			failed := Failed(cs)
			if tc.status == StatusFail && Available(cs) || slices.Contains(failed, tc.id) != (tc.status == StatusFail) {
				t.Fatalf("Available %v, Failed %q", Available(cs), failed)
			}
		})
	}
}

// TestChecksWithoutPlan: with no plan (or no catalog) the catalog checks skip, except where Build
// already found why.
func TestChecksWithoutPlan(t *testing.T) {
	in := baseInput(t)
	in.Plan = nil
	in.Findings = []Finding{{Check: CheckColumns, Text: "public.orders no longer exists."}}
	cs := checkByID(Checks(in))
	for _, id := range []string{CheckRowLimit, CheckReplicaIdentity, CheckPrimaryKey, CheckTriggers, CheckCascades, CheckUnchanged, CheckPrivileges} {
		if cs[id].Status != StatusSkip || cs[id].Detail != "" || cs[id].DocURL != "" {
			t.Errorf("%s: %+v, want skip", id, cs[id])
		}
	}
	if c := cs[CheckColumns]; c.Status != StatusFail || c.Detail != "public.orders no longer exists." || c.Fix != FixFailing {
		t.Errorf("columns: %+v", c)
	}
	if !slices.Equal(Failed(Checks(in)), []string{CheckColumns}) {
		t.Errorf("Failed = %q", Failed(Checks(in)))
	}

	in = baseInput(t)
	in.Catalog = nil
	in.Evidence = nil
	cs = checkByID(Checks(in))
	if cs[CheckRowLimit].Status != StatusPass || cs[CheckPrivileges].Status != StatusSkip {
		t.Errorf("no catalog: row_limit %s, privileges %s", cs[CheckRowLimit].Status, cs[CheckPrivileges].Status)
	}
	if cs[CheckEvidence].Status != StatusPass || cs[CheckEvidence].Detail != "" {
		t.Errorf("no evidence: %+v", cs[CheckEvidence])
	}
}

// TestFixSQLQuotesTableNames: fix SQL is copied into psql as is. A table whose name needs quoting
// (mixed case) must be quoted, or the statement names another table.
func TestFixSQLQuotesTableNames(t *testing.T) {
	in := baseInput(t)
	in.Catalog.Tables[200].ReplIdent = "d"
	in.Catalog.Tables[200].Name = "OrderItems"
	in.Plan.Tables[0].Name = "OrderItems"
	c := checkByID(Checks(in))[CheckReplicaIdentity]
	if !strings.Contains(c.FixSQL, `"OrderItems"`) {
		t.Fatalf("fix SQL writes the table "+
			"unquoted (%q); Postgres folds OrderItems to orderitems", c.FixSQL)
	}
}

func TestQuoteIdentForFixSQL(t *testing.T) {
	for in, want := range map[string]string{
		"orders_bulk_touch": "orders_bulk_touch",
		"t2":                "t2",
		"2t":                `"2t"`,
		"Keep_Log":          `"Keep_Log"`,
		`a"b`:               `"a""b"`,
		"":                  `""`,
		"has space":         `"has space"`,
	} {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFKWordsAndJoins(t *testing.T) {
	for in, want := range map[string]string{"c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT", "r": "RESTRICT", "a": "NO ACTION", "": "NO ACTION"} {
		if got := fkWord(in); got != want {
			t.Errorf("fkWord(%q) = %q", in, got)
		}
	}
	for _, tc := range []struct {
		in   []string
		want string
	}{{nil, ""}, {[]string{"a"}, "a"}, {[]string{"a", "b"}, "a and b"}, {[]string{"a", "b", "c"}, "a, b and c"}} {
		if got := joinAnd(tc.in); got != tc.want {
			t.Errorf("joinAnd(%q) = %q", tc.in, got)
		}
	}
	for in, want := range map[string]string{"f": "full", "n": "nothing", "i": "index", "d": "default", "": "default"} {
		if got := identityWord(in); got != want {
			t.Errorf("identityWord(%q) = %q", in, got)
		}
	}
}

func TestHolds(t *testing.T) {
	tests := []struct {
		name     string
		exp, cur Value
		want     bool
	}{
		{"same text", tv("paid"), tv("paid"), true},
		{"other text", tv("paid"), tv("Paid"), false},
		{"empty text", tv(""), tv(""), true},
		{"numeric printed differently fails closed", tv("1.0"), tv("1.00"), false},
		{"NULL and NULL", nv, nv, true},
		{"NULL expected, text now", nv, tv("NULL"), false},
		{"text expected, NULL now", tv(""), nv, false},
		{"sensitive, same text", dv("4111"), tv("4111"), true},
		{"sensitive, other text", dv("4111"), tv("4112"), false},
		{"sensitive, NULL now", dv("4111"), nv, false},
		{"sensitive, hex now", dv("\xff\xfe"), xv([]byte{0xff, 0xfe}), true},
		{"sensitive, bad hex now", dv("x"), Value{X: "zz"}, false},
		{"sensitive, nothing now", dv("x"), Value{}, false},
		{"hex and hex", xv([]byte{0xff}), xv([]byte{0xff}), true},
		{"hex and other hex", xv([]byte{0xff}), xv([]byte{0xfe}), false},
		{"hex and text", xv([]byte{0xff}), tv("ff"), false},
	}
	for _, tc := range tests {
		if got := Holds(tc.exp, tc.cur); got != tc.want {
			t.Errorf("%s: Holds = %v", tc.name, got)
		}
	}
	// The sensitive digest is "sha256:" + hex(sha256(text)).
	if !Holds(Value{D: "sha256:2bb80d537b1da3e38bd30361aa855686bde0eacd7162fef6a25fe97bf527a25b"}, tv("secret")) {
		t.Fatal("digest form")
	}
}

func TestCompare(t *testing.T) {
	p := &Plan{
		Tables: []PlanTable{{RelID: 100, Schema: "public", Name: "orders", PK: []string{"id"}}},
		Guards: []Guard{
			{Table: 100, Key: key("1"), Expect: map[string]Value{"id": tv("1"), "status": tv("shipped"), "note": dv("secret"),
				"raw": xv([]byte{0xff}), "gone": nv}},
			{Table: 100, Key: key("9")},
		},
	}
	holds := func() Image {
		return im("id", "1", "status", "shipped", "note", "secret", "raw", xv([]byte{0xff}), "gone", nv, "extra", "not guarded")
	}
	tests := []struct {
		name string
		cur  func() Current
		want string // the one mismatch's text, "" for none
	}{
		{"every guard holds", func() Current { return Current{100: {"1": holds()}} }, ""},
		{"text changed", func() Current { return Current{100: {"1": with(holds(), "status", "cancelled")}} },
			"public.orders id=1 changed since the action: status is now 'cancelled'."},
		{"sensitive changed shows hidden", func() Current { return Current{100: {"1": with(holds(), "note", "other")}} },
			"public.orders id=1 changed since the action: note is now •••."},
		{"hex changed", func() Current { return Current{100: {"1": with(holds(), "raw", xv([]byte{0xfe}))}} },
			`public.orders id=1 changed since the action: raw is now '\xfe'.`},
		{"NULL is now text", func() Current { return Current{100: {"1": with(holds(), "gone", "")}} },
			"public.orders id=1 changed since the action: gone is now ''."},
		{"text is now NULL", func() Current { return Current{100: {"1": with(holds(), "status", nv)}} },
			"public.orders id=1 changed since the action: status is now NULL."},
		{"a guarded column is missing", func() Current { return Current{100: {"1": without(holds(), "status")}} },
			"public.orders id=1 changed since the action: status is now missing."},
		{"the row is gone", func() Current { return Current{100: {}} }, "public.orders id=1 is gone since the action."},
		{"no rows read for the table", func() Current { return Current{} }, "public.orders id=1 is gone since the action."},
		{"a re-insert key exists again", func() Current { return Current{100: {"1": holds(), "9": order("9", "x")}} },
			"public.orders id=9 exists again since the action."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bad, total := Compare(p, tc.cur())
			if total != 2 {
				t.Fatalf("total %d", total)
			}
			if tc.want == "" {
				if len(bad) != 0 {
					t.Fatalf("mismatches %+v", bad)
				}
				return
			}
			if len(bad) != 1 || bad[0].Text != tc.want || bad[0].Table != "public.orders" {
				t.Fatalf("mismatches %+v\nwant %q", bad, tc.want)
			}
		})
	}
}

func TestValueText(t *testing.T) {
	for _, tc := range []struct {
		v    Value
		want string
	}{{tv("a'b"), "'a'b'"}, {nv, "NULL"}, {dv("x"), Hidden}, {xv([]byte{0xab}), `'\xab'`}, {uv, "?"}, {Value{}, "?"}} {
		if got := ValueText(tc.v); got != tc.want {
			t.Errorf("ValueText(%+v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}
