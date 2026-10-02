package undo

import (
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/witness/l2"
)

// guardOf is the guard image of an orders row: every column but the generated total_cents.
func guardOf(img Image) map[string]Value { return without(img, "total_cents") }

// TestNetEffect is S23 §6's net-effect table, through Collapse and Build.
func TestNetEffect(t *testing.T) {
	big := strings.Repeat("x", 4096)
	tests := []struct {
		name    string
		changes []RowChange
		cat     *Catalog
		kind    string // the one net row's Kind ("" when there are several)
		steps   []Step
		guards  []Guard
		counts  Counts
		rows    int
		notes   []string
	}{
		{
			name:    "insert id 7: delete it, guard = the inserted image",
			changes: []RowChange{ins(1, ordersRef, order("7", "new"))},
			kind:    "created",
			steps:   []Step{{Op: OpDelete, Table: 100, Key: key("7")}},
			guards:  []Guard{{Table: 100, Key: key("7"), Expect: guardOf(order("7", "new"))}},
			counts:  Counts{Delete: 1}, rows: 1,
		},
		{
			name:    "update status paid to shipped: put status back, guard = the new image",
			changes: []RowChange{upd(1, ordersRef, order("1", "paid"), order("1", "shipped"))},
			kind:    "changed",
			steps:   []Step{{Op: OpUpdate, Table: 100, Key: key("1"), Set: map[string]Value{"status": tv("paid")}}},
			guards:  []Guard{{Table: 100, Key: key("1"), Expect: guardOf(order("1", "shipped"))}},
			counts:  Counts{Update: 1}, rows: 1,
		},
		{
			name: "update with an unchanged TOAST column: not written, guard takes the old value",
			changes: []RowChange{upd(1, ordersRef, with(order("1", "paid"), "note", big),
				with(order("1", "shipped"), "note", uv))},
			kind:   "changed",
			steps:  []Step{{Op: OpUpdate, Table: 100, Key: key("1"), Set: map[string]Value{"status": tv("paid")}}},
			guards: []Guard{{Table: 100, Key: key("1"), Expect: guardOf(with(order("1", "shipped"), "note", big))}},
			counts: Counts{Update: 1}, rows: 1,
		},
		{
			name:    "delete id 9: insert every non-generated column, guard the key absent",
			changes: []RowChange{del(1, ordersRef, order("9", "paid"))},
			kind:    "removed",
			steps:   []Step{{Op: OpInsert, Table: 100, Key: key("9"), Set: guardOf(order("9", "paid"))}},
			guards:  []Guard{{Table: 100, Key: key("9")}},
			counts:  Counts{Insert: 1}, rows: 1,
			notes: []string{NoteSequences},
		},
		{
			name: "insert then update: delete, guard = the last image",
			changes: []RowChange{ins(1, ordersRef, order("7", "new")),
				upd(2, ordersRef, order("7", "new"), order("7", "paid"))},
			kind:   "created",
			steps:  []Step{{Op: OpDelete, Table: 100, Key: key("7")}},
			guards: []Guard{{Table: 100, Key: key("7"), Expect: guardOf(order("7", "paid"))}},
			counts: Counts{Delete: 1}, rows: 1,
		},
		{
			name: "update then delete: insert the first old image",
			changes: []RowChange{upd(1, ordersRef, order("9", "paid"), order("9", "shipped")),
				del(2, ordersRef, order("9", "shipped"))},
			kind:   "removed",
			steps:  []Step{{Op: OpInsert, Table: 100, Key: key("9"), Set: guardOf(order("9", "paid"))}},
			guards: []Guard{{Table: 100, Key: key("9")}},
			counts: Counts{Insert: 1}, rows: 1,
			notes: []string{NoteSequences},
		},
		{
			name: "insert then delete: no step, a note",
			changes: []RowChange{ins(1, ordersRef, order("7", "new")),
				del(2, ordersRef, order("7", "new"))},
			kind:   "none",
			steps:  []Step{},
			guards: []Guard{},
			notes:  []string{NoteNoop},
		},
		{
			name: "delete then insert id 9: update back to the deleted values",
			changes: []RowChange{del(1, ordersRef, order("9", "paid")),
				ins(2, ordersRef, with(order("9", "new"), "note", "other"))},
			kind: "replaced",
			steps: []Step{{Op: OpUpdate, Table: 100, Key: key("9"),
				Set: map[string]Value{"status": tv("paid"), "note": tv("n-9")}}},
			guards: []Guard{{Table: 100, Key: key("9"), Expect: guardOf(with(order("9", "new"), "note", "other"))}},
			counts: Counts{Update: 1}, rows: 1,
		},
		{
			name:    "update id 42 to 1042: update row 1042 back with its key, guard 1042 = new and 42 absent",
			changes: []RowChange{upd(1, ordersRef, order("42", "paid"), with(order("42", "shipped"), "id", "1042"))},
			kind:    "moved",
			steps: []Step{{Op: OpUpdate, Table: 100, Key: key("1042"),
				Set: map[string]Value{"id": tv("42"), "status": tv("paid")}}},
			guards: []Guard{
				{Table: 100, Key: key("1042"), Expect: guardOf(with(order("42", "shipped"), "id", "1042"))},
				{Table: 100, Key: key("42")},
			},
			counts: Counts{Update: 1}, rows: 1,
		},
		{
			name:    "update id 42 to 1042 on a GENERATED ALWAYS identity: delete at 1042, insert 42 overriding",
			changes: []RowChange{upd(1, ordersRef, order("42", "paid"), with(order("42", "shipped"), "id", "1042"))},
			cat:     catalog(ordersCat("a")),
			kind:    "moved",
			steps: []Step{
				{Op: OpDelete, Table: 100, Key: key("1042")},
				{Op: OpInsert, Table: 100, Key: key("42"), Set: guardOf(order("42", "paid")), Overriding: true},
			},
			guards: []Guard{
				{Table: 100, Key: key("1042"), Expect: guardOf(with(order("42", "shipped"), "id", "1042"))},
				{Table: 100, Key: key("42")},
			},
			counts: Counts{Delete: 1, Insert: 1}, rows: 1,
			notes: []string{NoteSequences},
		},
		{
			name:    "delete on a GENERATED ALWAYS identity: insert overriding",
			changes: []RowChange{del(1, ordersRef, order("9", "paid"))},
			cat:     catalog(ordersCat("a")),
			kind:    "removed",
			steps:   []Step{{Op: OpInsert, Table: 100, Key: key("9"), Set: guardOf(order("9", "paid")), Overriding: true}},
			guards:  []Guard{{Table: 100, Key: key("9")}},
			counts:  Counts{Insert: 1}, rows: 1,
			notes: []string{NoteSequences},
		},
		{
			name:    "update that only changes the generated column: no step, a note",
			changes: []RowChange{upd(1, ordersRef, order("1", "paid"), with(order("1", "paid"), "total_cents", "1"))},
			kind:    "changed",
			steps:   []Step{},
			guards:  []Guard{{Table: 100, Key: key("1"), Expect: guardOf(order("1", "paid"))}},
			notes:   []string{NoteSameValue},
		},
		{
			name: "parent insert, then child insert: the child is deleted first",
			changes: []RowChange{ins(1, ordersRef, order("7", "new")),
				ins(2, itemsRef, item("70", "7"))},
			steps: []Step{{Op: OpDelete, Table: 200, Key: key("70")}, {Op: OpDelete, Table: 100, Key: key("7")}},
			guards: []Guard{
				{Table: 100, Key: key("7"), Expect: guardOf(order("7", "new"))},
				{Table: 200, Key: key("70"), Expect: item("70", "7")},
			},
			counts: Counts{Delete: 2}, rows: 2,
		},
		{
			name: "children deleted, then their parent: the parent is put back first",
			changes: []RowChange{del(1, itemsRef, item("90", "9")), del(2, itemsRef, item("91", "9")),
				del(3, ordersRef, order("9", "paid"))},
			steps: []Step{
				{Op: OpInsert, Table: 100, Key: key("9"), Set: guardOf(order("9", "paid"))},
				{Op: OpInsert, Table: 200, Key: key("91"), Set: item("91", "9")},
				{Op: OpInsert, Table: 200, Key: key("90"), Set: item("90", "9")},
			},
			guards: []Guard{{Table: 100, Key: key("9")}, {Table: 200, Key: key("90")}, {Table: 200, Key: key("91")}},
			counts: Counts{Insert: 3}, rows: 3,
			notes: []string{NoteSequences},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := evidence(tc.changes...)
			if tc.kind != "" {
				net := Collapse(ev.Changes, map[string][]string{"public.orders": {"id"}, "public.order_items": {"id"}})
				if len(net.Problems) > 0 || len(net.Rows) != 1 {
					t.Fatalf("Collapse: %d rows, problems %v", len(net.Rows), net.Problems)
				}
				if k := net.Rows[0].Kind(); k != tc.kind {
					t.Fatalf("Kind = %q, want %q", k, tc.kind)
				}
			}
			cat := tc.cat
			if cat == nil {
				cat = defaultCatalog()
			}
			p, fs := build(ev, cat)
			if p == nil || len(fs) > 0 {
				t.Fatalf("Build: plan %v, findings %v", p, fs)
			}
			if !reflect.DeepEqual(p.Steps, tc.steps) {
				t.Errorf("steps\n got %+v\nwant %+v", p.Steps, tc.steps)
			}
			if !reflect.DeepEqual(p.Guards, tc.guards) {
				t.Errorf("guards\n got %+v\nwant %+v", p.Guards, tc.guards)
			}
			if p.Counts != tc.counts || p.Rows != tc.rows {
				t.Errorf("counts %+v rows %d, want %+v rows %d", p.Counts, p.Rows, tc.counts, tc.rows)
			}
			if !slices.Equal(p.Notes, tc.notes) {
				t.Errorf("notes %q, want %q", p.Notes, tc.notes)
			}
			if !strings.HasPrefix(p.Digest, "sha256:") || p.V != PlanVersion || p.Undoes != actID || p.ConnectorID != connID {
				t.Errorf("plan header: %+v", p)
			}
		})
	}
}

// TestNetEffectCascadeOrder is the spec's "parent DELETE with ON DELETE CASCADE child deletes in
// the evidence" row. Postgres logs the parent's delete first and the cascaded child deletes after it
// (RI triggers fire at the end of the statement), so the evidence is parent seq 1, children seq 2-3.
// S23 §6 expects the parent re-inserted first, then its children.
func TestNetEffectCascadeOrder(t *testing.T) {
	ev := evidence(del(1, ordersRef, order("9", "paid")), del(2, itemsRef, item("90", "9")), del(3, itemsRef, item("91", "9")))
	p, fs := build(ev, defaultCatalog())
	if p == nil {
		t.Fatalf("Build: %v", fs)
	}
	got := stepsText(p)
	want := []string{"insert public.orders id=9", "insert public.order_items id=91", "insert public.order_items id=90"}
	if !slices.Equal(got, want) {
		t.Fatalf("steps run in reverse order of each row's last change, "+
			"so cascaded child deletes logged after their parent are re-inserted before it (an FK violation at run time).\n got %q\nwant %q", got, want)
	}
}

func TestNetEffectUnavailable(t *testing.T) {
	sensitiveRow := with(order("9", "paid"), "note", dv("secret"))
	noPK := evidence(ins(1, ordersRef, order("7", "new")))
	info := noPK.Tables["public.orders"]
	info.PK = nil
	noPK.Tables["public.orders"] = info

	tests := []struct {
		name  string
		ev    *Evidence
		cat   *Catalog
		check string
		text  string
	}{
		{"truncate flag", fromFlags("truncate:public.orders"), defaultCatalog(), CheckEvidence, ProblemTruncate("public.orders").Text},
		{"relation_changed flag", fromFlags("relation_changed:public.orders"), defaultCatalog(), CheckEvidence,
			ProblemRelationChanged("public.orders").Text},
		{"images_truncated flag", fromFlags("images_truncated"), defaultCatalog(), CheckEvidence, ProblemImagesTruncated(5, 1).Text},
		{"table without a primary key", noPK, defaultCatalog(), CheckPrimaryKey, ProblemNoPrimaryKey("public.orders").Text},
		{"a sensitive value that must be written", evidence(del(1, ordersRef, sensitiveRow)), defaultCatalog(), CheckEvidence,
			ProblemSensitive("public.orders", "note").Text},
		{"a sensitive value an update must put back", evidence(upd(1, ordersRef, sensitiveRow, with(sensitiveRow, "note", dv("other")))),
			defaultCatalog(), CheckEvidence, ProblemSensitive("public.orders", "note").Text},
		{"an unchanged TOAST value with no old value", evidence(upd(1, ordersRef, without(order("1", "paid"), "note"),
			with(order("1", "shipped"), "note", uv))), defaultCatalog(), CheckEvidence, ProblemToast("public.orders").Text},
		{"an unchanged TOAST value in the old image of a delete", evidence(del(1, ordersRef, with(order("9", "paid"), "note", uv))),
			defaultCatalog(), CheckEvidence, ProblemToast("public.orders").Text},
		{"an update without an old image", evidence(upd(1, ordersRef, nil, order("1", "x"))), defaultCatalog(), CheckEvidence,
			ProblemOldImage("public.orders", "").Text},
		{"a delete without an old image", evidence(del(1, ordersRef, nil)), defaultCatalog(), CheckEvidence,
			ProblemOldImage("public.orders", "").Text},
		{"a new column the old image lacks", evidence(upd(1, ordersRef, without(order("1", "paid"), "note"), order("1", "shipped"))),
			defaultCatalog(), CheckEvidence, ProblemOldImage("public.orders", "").Text},
		{"a NULL key", evidence(ins(1, ordersRef, with(order("7", "new"), "id", nv))), defaultCatalog(), CheckPrimaryKey,
			ProblemKeyUnreadable("public.orders").Text},
		{"a sensitive key on update", evidence(upd(1, ordersRef, with(order("1", "a"), "id", dv("1")), order("1", "b"))),
			defaultCatalog(), CheckPrimaryKey, ProblemKeyUnreadable("public.orders").Text},
		{"a hex key on delete", evidence(del(1, ordersRef, with(order("1", "a"), "id", xv([]byte{0xff})))), defaultCatalog(),
			CheckPrimaryKey, ProblemKeyUnreadable("public.orders").Text},
		{"inserting a key that is live", evidence(ins(1, ordersRef, order("7", "a")), ins(2, ordersRef, order("7", "b"))),
			defaultCatalog(), CheckEvidence, ProblemInconsistent("public.orders").Text},
		{"moving onto a key that is live", evidence(ins(1, ordersRef, order("7", "a")),
			upd(2, ordersRef, order("8", "a"), order("7", "a"))), defaultCatalog(), CheckEvidence, ProblemInconsistent("public.orders").Text},
		{"the table is gone", evidence(ins(1, ordersRef, order("7", "a"))), catalog(itemsCat()), CheckColumns,
			"public.orders no longer exists."},
		{"no catalog", evidence(ins(1, ordersRef, order("7", "a"))), nil, CheckColumns, "public.orders no longer exists."},
		{"the relid names nothing now", evidence(ins(1, ordersRef, order("7", "a"))), catalog(func() *TableCatalog {
			tc := ordersCat("")
			tc.Exists = false
			return tc
		}()), CheckColumns, "public.orders no longer exists."},
		{"the table was renamed", evidence(ins(1, ordersRef, order("7", "a"))), catalog(func() *TableCatalog {
			tc := ordersCat("")
			tc.Name = "orders_old"
			return tc
		}()), CheckColumns, "public.orders is now named public.orders_old, so the plan won't guess that it is the same table."},
		{"the catalog has no primary key now", evidence(ins(1, ordersRef, order("7", "a"))), catalog(func() *TableCatalog {
			tc := ordersCat("")
			tc.PK = nil
			return tc
		}()), CheckPrimaryKey, ProblemNoPrimaryKey("public.orders").Text},
		{"the primary key changed", evidence(ins(1, ordersRef, order("7", "a"))), catalog(func() *TableCatalog {
			tc := ordersCat("")
			tc.PK = []string{"id", "status"}
			return tc
		}()), CheckPrimaryKey, "The primary key of public.orders is now (id, status); the action's rows were keyed by (id)."},
		{"a column was dropped", evidence(ins(1, ordersRef, order("7", "a"))), catalog(func() *TableCatalog {
			tc := ordersCat("")
			tc.Columns = slices.DeleteFunc(tc.Columns, func(c ColumnCatalog) bool { return c.Name == "note" })
			return tc
		}()), CheckColumns, "public.orders.note was dropped since the action."},
		{"a column changed type", evidence(ins(1, ordersRef, order("7", "a"))), catalog(func() *TableCatalog {
			tc := ordersCat("")
			tc.Columns[1] = ColumnCatalog{Name: "status", Type: "order_status", TypeOID: 70000}
			return tc
		}()), CheckColumns, "public.orders.status changed type since the action (now order_status)."},
		{"a type name that fails the pattern", evidence(ins(1, ordersRef, order("7", "a"))), catalog(func() *TableCatalog {
			tc := ordersCat("")
			tc.Columns[2].Type = "text; drop table x"
			return tc
		}()), CheckColumns, "public.orders.note has a type name Deedglass won't put in a statement."},
		{"a column added since, with a re-insert", evidence(del(1, ordersRef, order("9", "a"))), catalog(func() *TableCatalog {
			tc := ordersCat("")
			tc.Columns = append(tc.Columns, ColumnCatalog{Name: "added", Type: "text", TypeOID: oidText})
			return tc
		}()), CheckColumns, "public.orders.added was added since the action, so a row put back would get a value Postgres never recorded."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, fs := build(tc.ev, tc.cat)
			if p != nil {
				t.Fatalf("Build gave a plan: %+v", p)
			}
			if !hasFinding(fs, tc.check, tc.text) {
				t.Fatalf("findings %+v\nwant %s: %q", fs, tc.check, tc.text)
			}
		})
	}
}

// fromFlags is FromFrames over one bound frame with flags, an insert and a total of 5.
func fromFlags(flags ...string) *Evidence {
	f := boundFrame(l2.Change{Seq: 1, Op: "I", Table: "public.orders", New: order("7", "new")})
	f.Flags = flags
	if slices.Contains(flags, "images_truncated") {
		f.Total = 5
	}
	return FromFrames(FrameInput{Action: actID, ConnectorID: connID, Frames: []l2.Frame{f}})
}

func TestBuildAddedColumnWithoutInsertIsFine(t *testing.T) {
	tc := ordersCat("")
	tc.Columns = append(tc.Columns, ColumnCatalog{Name: "added", Type: "text", TypeOID: oidText})
	p, fs := build(evidence(upd(1, ordersRef, order("1", "paid"), order("1", "shipped"))), catalog(tc))
	if p == nil || len(fs) > 0 {
		t.Fatalf("Build: %v", fs)
	}
}

func TestSensitiveOnlyInGuard(t *testing.T) {
	old := with(order("1", "paid"), "note", dv("secret"))
	p, fs := build(evidence(upd(1, ordersRef, old, with(old, "status", "shipped"))), defaultCatalog())
	if p == nil || len(fs) > 0 {
		t.Fatalf("Build: %v", fs)
	}
	if !slices.Contains(p.Notes, NoteSensitive) {
		t.Fatalf("notes %q, want %q", p.Notes, NoteSensitive)
	}
	if got := p.Guards[0].Expect["note"]; got.D != digestOf("secret") {
		t.Fatalf("guard note = %+v", got)
	}
	if _, ok := p.Steps[0].Set["note"]; ok {
		t.Fatal("a sensitive column that didn't change was written")
	}
}

func TestPlanTables(t *testing.T) {
	p, fs := build(evidence(ins(1, ordersRef, order("7", "new")), del(2, itemsRef, item("90", "9"))), defaultCatalog())
	if p == nil {
		t.Fatal(fs)
	}
	want := []PlanTable{
		{RelID: 200, Schema: "public", Name: "order_items", PK: []string{"id"},
			Columns: []PlanColumn{{Name: "id", Type: "bigint"}, {Name: "order_id", Type: "bigint"}, {Name: "sku", Type: "text"}}},
		{RelID: 100, Schema: "public", Name: "orders", PK: []string{"id"},
			Columns:   []PlanColumn{{Name: "id", Type: "bigint"}, {Name: "status", Type: "text"}, {Name: "note", Type: "text"}},
			Generated: []string{"total_cents"}},
	}
	if !reflect.DeepEqual(p.Tables, want) {
		t.Fatalf("tables\n got %+v\nwant %+v", p.Tables, want)
	}
	if got := p.TableNames(); !slices.Equal(got, []string{"public.order_items", "public.orders"}) {
		t.Fatalf("TableNames = %q", got)
	}
	if pt, ok := p.Table(100); !ok || pt.Ref() != ordersRef || pt.String() != "public.orders" || pt.Type("status") != "text" || pt.Type("nope") != "" {
		t.Fatalf("Table(100) = %+v, %v", pt, ok)
	}
	if _, ok := p.Table(999); ok {
		t.Fatal("Table(999) found")
	}
	if p.Counts.Of(OpInsert) != 1 || p.Counts.Of(OpDelete) != 1 || p.Counts.Of(OpUpdate) != 0 || p.Counts.Of("merge") != 0 {
		t.Fatalf("Counts = %+v", p.Counts)
	}
	ident, _ := build(evidence(del(1, ordersRef, order("9", "a"))), catalog(ordersCat("a")))
	if pt, _ := ident.Table(100); !pt.Columns[0].IdentityAlways {
		t.Fatalf("identity column not marked: %+v", pt.Columns)
	}
}

func TestCollapse(t *testing.T) {
	pk := map[string][]string{"public.orders": {"id"}}
	t.Run("replaced", func(t *testing.T) {
		r := Collapse([]RowChange{del(1, ordersRef, order("9", "a")), ins(2, ordersRef, order("9", "b"))}, pk)
		if len(r.Rows) != 1 || !r.Rows[0].Replaced || r.Rows[0].LastSeq != 2 {
			t.Fatalf("%+v", r.Rows)
		}
	})
	t.Run("insert, delete, insert is created", func(t *testing.T) {
		r := Collapse([]RowChange{ins(1, ordersRef, order("9", "a")), del(2, ordersRef, order("9", "a")),
			ins(3, ordersRef, order("9", "b"))}, pk)
		if len(r.Rows) != 1 || r.Rows[0].Replaced || r.Rows[0].Kind() != "created" {
			t.Fatalf("%+v", r.Rows[0])
		}
	})
	t.Run("a key change followed through the chain", func(t *testing.T) {
		r := Collapse([]RowChange{upd(1, ordersRef, order("1", "a"), order("2", "a")), upd(2, ordersRef, order("2", "a"), order("3", "b"))}, pk)
		if len(r.Rows) != 1 || r.Rows[0].Kind() != "moved" || r.Rows[0].BeforeKey["id"] != "1" || r.Rows[0].AfterKey["id"] != "3" {
			t.Fatalf("%+v", r.Rows[0])
		}
	})
	t.Run("a key changed and changed back is changed", func(t *testing.T) {
		r := Collapse([]RowChange{upd(1, ordersRef, order("1", "a"), order("2", "a")), upd(2, ordersRef, order("2", "a"), order("1", "b"))}, pk)
		if r.Rows[0].Kind() != "changed" {
			t.Fatalf("%s", r.Rows[0].Kind())
		}
	})
	t.Run("problems are listed once", func(t *testing.T) {
		r := Collapse([]RowChange{ins(1, ordersRef, order("1", "a")), ins(2, ordersRef, order("2", "a"))}, map[string][]string{})
		if len(r.Problems) != 1 || r.Problems[0].Check != CheckPrimaryKey {
			t.Fatalf("%+v", r.Problems)
		}
	})
}

// TestCollapseContradictions feeds sequences Postgres can't produce. Collapse should call them
// inconsistent rather than plan two steps that put rows back at one key.
func TestCollapseContradictions(t *testing.T) {
	tests := []struct {
		name    string
		changes []RowChange
	}{
		{"a key deleted twice", []RowChange{del(1, ordersRef, order("9", "a")), del(2, ordersRef, order("9", "a"))}},
		{"a key moved away, then updated at its old key", []RowChange{upd(1, ordersRef, order("42", "a"), order("1042", "a")),
			upd(2, ordersRef, order("42", "a"), order("43", "a"))}},
		{"a key deleted, then updated", []RowChange{del(1, ordersRef, order("9", "a")), upd(2, ordersRef, order("9", "a"), order("9", "b"))}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, fs := build(evidence(tc.changes...), defaultCatalog())
			if p == nil {
				return
			}
			t.Fatalf("contradictory evidence is planned instead of refused: steps %q, findings %v",
				stepsText(p), fs)
		})
	}
}

func TestNetKind(t *testing.T) {
	a, b := order("1", "a"), order("2", "a")
	tests := []struct {
		n    Net
		want string
	}{
		{Net{}, "none"},
		{Net{After: a, AfterKey: key("1")}, "created"},
		{Net{Before: a, BeforeKey: key("1")}, "removed"},
		{Net{Before: a, BeforeKey: key("1"), After: a, AfterKey: key("1"), Replaced: true}, "replaced"},
		{Net{Before: a, BeforeKey: key("1"), After: b, AfterKey: key("2")}, "moved"},
		{Net{Before: a, BeforeKey: key("1"), After: a, AfterKey: key("1")}, "changed"},
	}
	for _, tc := range tests {
		if got := tc.n.Kind(); got != tc.want {
			t.Errorf("Kind(%+v) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestKeyID(t *testing.T) {
	k := Key{"a": "1", "b": "x"}
	if got := k.ID([]string{"a", "b"}); got != "1\x00x" {
		t.Fatalf("ID = %q", got)
	}
	if got := k.ID([]string{"b", "a"}); got != "x\x001" {
		t.Fatalf("ID = %q", got)
	}
}

func TestSame(t *testing.T) {
	tests := []struct {
		a, b Value
		want bool
	}{
		{tv("a"), tv("a"), true},
		{tv("a"), tv("b"), false},
		{tv(""), tv(""), true},
		{nv, nv, true},
		{nv, tv("NULL"), false},
		{dv("x"), dv("x"), true},
		{dv("x"), dv("y"), false},
		{dv("x"), tv("x"), false},
		{xv([]byte{1}), xv([]byte{1}), true},
		{xv([]byte{1}), tv("01"), false},
		{uv, uv, false},
		{Value{}, Value{}, false},
	}
	for _, tc := range tests {
		if got := Same(tc.a, tc.b); got != tc.want {
			t.Errorf("Same(%+v, %+v) = %v", tc.a, tc.b, got)
		}
	}
}

// TestDigestDeterministic is S23 §3.9 invariant 1: the same evidence and catalog give the same digest.
func TestDigestDeterministic(t *testing.T) {
	changes := []RowChange{
		upd(1, ordersRef, order("1", "paid"), order("1", "shipped")),
		ins(2, ordersRef, order("7", "new")),
		del(3, ordersRef, order("9", "paid")),
		ins(4, itemsRef, item("70", "7")),
		del(5, itemsRef, item("90", "9")),
		upd(6, ordersRef, order("42", "paid"), with(order("42", "shipped"), "id", "1042")),
	}
	p1, fs := build(evidence(changes...), defaultCatalog())
	if p1 == nil {
		t.Fatal(fs)
	}
	p2, _ := build(evidence(changes...), defaultCatalog())
	if p1.Digest != p2.Digest || !reflect.DeepEqual(p1, p2) {
		t.Fatalf("two builds differ: %s %s", p1.Digest, p2.Digest)
	}
	// Each row has one change, so the slice order is free; the map orders are free anyway.
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20; i++ {
		sh := append([]RowChange{}, changes...)
		rng.Shuffle(len(sh), func(i, j int) { sh[i], sh[j] = sh[j], sh[i] })
		ev := evidence(sh...)
		ev.Tables = map[string]l2.TableInfo{"public.order_items": itemsInfo(), "public.orders": ordersInfo()}
		p, _ := build(ev, defaultCatalog())
		if p == nil || p.Digest != p1.Digest {
			t.Fatalf("shuffle %d: digest %v, want %s", i, p, p1.Digest)
		}
	}
	// One value changed changes the digest.
	changed := append([]RowChange{}, changes...)
	changed[2] = del(3, ordersRef, with(order("9", "paid"), "note", "different"))
	p3, _ := build(evidence(changed...), defaultCatalog())
	if p3.Digest == p1.Digest {
		t.Fatal("a changed old value kept the digest")
	}
	// The digest covers the plan without its digest and notes.
	c := *p1
	c.Notes = []string{"something else"}
	c.Digest = "sha256:bogus"
	if d, err := ComputeDigest(&c); err != nil || d != p1.Digest {
		t.Fatalf("ComputeDigest with other notes = %s, %v; want %s", d, err, p1.Digest)
	}
	c.Rows++
	if d, _ := ComputeDigest(&c); d == p1.Digest {
		t.Fatal("ComputeDigest ignored rows")
	}
}
