package undo

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the SQL goldens in testdata/sql")

var (
	sqlOrders = PlanTable{RelID: 100, Schema: "public", Name: "orders", PK: []string{"id"},
		Columns: []PlanColumn{{Name: "id", Type: "bigint"}, {Name: "status", Type: "text"}, {Name: "note", Type: "text"}}}
	sqlOrdersIdentity = PlanTable{RelID: 100, Schema: "public", Name: "orders", PK: []string{"id"},
		Columns: []PlanColumn{{Name: "id", Type: "bigint", IdentityAlways: true}, {Name: "status", Type: "text"}, {Name: "note", Type: "text"}}}
	sqlLines = PlanTable{RelID: 300, Schema: "public", Name: "line_items", PK: []string{"order_id", "line"},
		Columns: []PlanColumn{{Name: "order_id", Type: "bigint"}, {Name: "line", Type: "integer"}, {Name: "qty", Type: "integer"},
			{Name: "price", Type: "numeric(12,2)"}, {Name: "code", Type: "character varying(20)"}, {Name: "tags", Type: "integer[]"},
			{Name: "seen_at", Type: "timestamp with time zone"}}}
	sqlQuoted = PlanTable{RelID: 400, Schema: `My"Schema`, Name: "we\"ird table\x00", PK: []string{"Id"},
		Columns: []PlanColumn{{Name: "Id", Type: "bigint"}, {Name: `col"x`, Type: `"My Type"`}}}
)

// render writes a statement and its parameters for a golden file.
func render(title string, st Statement) string {
	var b strings.Builder
	fmt.Fprintf(&b, "-- %s\n%s;\n", title, st.SQL)
	for i, a := range st.Args {
		if a == nil {
			fmt.Fprintf(&b, "-- $%d = NULL\n", i+1)
			continue
		}
		fmt.Fprintf(&b, "-- $%d = %#v\n", i+1, a)
	}
	return b.String()
}

func TestSQLGoldens(t *testing.T) {
	type gen func() (Statement, error)
	cases := []struct {
		file  string
		title string
		gen   gen
	}{
		{"guard_single", "guard, one key column, preview (no lock)", func() (Statement, error) {
			return GuardSelect(sqlOrders, []string{"note", "status"}, []Key{key("9"), key("1042"), key("42")}, false)
		}},
		{"guard_single_for_update", "guard, one key column, inside the undo (FOR UPDATE)", func() (Statement, error) {
			return GuardSelect(sqlOrders, []string{"note", "status"}, []Key{key("9"), key("1042"), key("42")}, true)
		}},
		{"guard_composite", "guard, a composite key", func() (Statement, error) {
			return GuardSelect(sqlLines, []string{"price", "qty", "tags"},
				[]Key{{"order_id": "7", "line": "2"}, {"order_id": "7", "line": "1"}, {"order_id": "10", "line": "1"}}, true)
		}},
		{"step_update", "update a moved row back, its key included", func() (Statement, error) {
			return StepStatement(sqlOrders, Step{Op: OpUpdate, Table: 100, Key: key("1042"),
				Set: map[string]Value{"id": tv("42"), "status": tv("paid"), "note": nv}})
		}},
		{"step_delete", "delete a created row", func() (Statement, error) {
			return StepStatement(sqlLines, Step{Op: OpDelete, Table: 300, Key: Key{"order_id": "7", "line": "2"}})
		}},
		{"step_insert", "insert a removed row, types with modifiers and arrays", func() (Statement, error) {
			return StepStatement(sqlLines, Step{Op: OpInsert, Table: 300, Key: Key{"order_id": "7", "line": "2"},
				Set: map[string]Value{"order_id": tv("7"), "line": tv("2"), "qty": tv("3"), "price": tv("12.50"), "code": tv("AB-1"),
					"tags": tv("{1,2}"), "seen_at": tv("2026-10-07 13:12:09.114+00")}})
		}},
		{"step_insert_overriding", "insert a removed row with a GENERATED ALWAYS identity, non-UTF-8 text from hex", func() (Statement, error) {
			return StepStatement(sqlOrdersIdentity, Step{Op: OpInsert, Table: 100, Key: key("9"),
				Set: map[string]Value{"id": tv("9"), "status": tv("paid"), "note": xv([]byte{0xff, 'A'})}, Overriding: true})
		}},
		{"quoted_identifiers", "identifiers with double quotes, spaces, capitals and a NUL; a quoted type", func() (Statement, error) {
			return StepStatement(sqlQuoted, Step{Op: OpUpdate, Table: 400, Key: Key{"Id": "1"}, Set: map[string]Value{`col"x`: tv("it's")}})
		}},
		{"quoted_identifiers_guard", "guard on the quoted table", func() (Statement, error) {
			return GuardSelect(sqlQuoted, []string{`col"x`}, []Key{{"Id": "1"}}, true)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			st, err := tc.gen()
			if err != nil {
				t.Fatal(err)
			}
			got := render(tc.title, st)
			path := filepath.Join("testdata", "sql", tc.file+".sql")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to write it)", err)
			}
			if got != string(want) {
				t.Fatalf("%s differs\n got:\n%s\nwant:\n%s", path, got, want)
			}
		})
	}
}

// TestStatementsUseParameters is S23 §3.9 invariant 2 at the statement level: no value of a step
// appears in its SQL text; every value is a parameter.
func TestStatementsUseParameters(t *testing.T) {
	marker := "zz_value_marker"
	st, err := StepStatement(sqlOrders, Step{Op: OpUpdate, Key: key(marker + "1"), Set: map[string]Value{"status": tv(marker + "2")}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st.SQL, marker) || !slices.Equal(st.Args, []any{marker + "2", marker + "1"}) {
		t.Fatalf("%q %v", st.SQL, st.Args)
	}
	st, err = GuardSelect(sqlOrders, nil, []Key{key(marker)}, false)
	if err != nil || strings.Contains(st.SQL, marker) {
		t.Fatalf("%q %v", st.SQL, err)
	}
}

func TestSQLRefusals(t *testing.T) {
	badType := sqlOrders
	badType.Columns = []PlanColumn{{Name: "id", Type: "bigint"}, {Name: "status", Type: "text); DROP TABLE x; --"}}
	noKeyType := sqlOrders
	noKeyType.Columns = []PlanColumn{{Name: "status", Type: "text"}}
	noName := sqlOrders
	noName.Name = ""
	noPK := sqlOrders
	noPK.PK = nil
	tests := []struct {
		name string
		t    PlanTable
		s    Step
		want string
	}{
		{"a sensitive value", sqlOrders, Step{Op: OpUpdate, Key: key("1"), Set: map[string]Value{"status": dv("paid")}}, "sensitive"},
		{"a sensitive value in an insert", sqlOrders, Step{Op: OpInsert, Key: key("1"), Set: map[string]Value{"id": tv("1"), "note": dv("x")}},
			"sensitive"},
		{"an unchanged TOAST value", sqlOrders, Step{Op: OpUpdate, Key: key("1"), Set: map[string]Value{"note": uv}}, "TOAST"},
		{"bad hex", sqlOrders, Step{Op: OpUpdate, Key: key("1"), Set: map[string]Value{"note": {X: "zz"}}}, "invalid"},
		{"an empty value", sqlOrders, Step{Op: OpInsert, Key: key("1"), Set: map[string]Value{"note": {}}}, "nothing in it"},
		{"a type name that fails the pattern", badType, Step{Op: OpDelete, Key: key("1")}, "isn't safe in a cast"},
		{"a key column with no type", noKeyType, Step{Op: OpDelete, Key: key("1")}, "has no type in the plan"},
		{"no table name", noName, Step{Op: OpDelete, Key: key("1")}, "no name or no primary key"},
		{"no primary key", noPK, Step{Op: OpDelete, Key: key("1")}, "no name or no primary key"},
		{"a set column the plan doesn't type", sqlOrders, Step{Op: OpUpdate, Key: key("1"), Set: map[string]Value{"ghost": tv("x")}},
			"ghost has no type"},
		{"a key without its column (delete)", sqlOrders, Step{Op: OpDelete, Key: Key{"other": "1"}}, "no value for key column id"},
		{"a key without its column (update)", sqlOrders, Step{Op: OpUpdate, Key: Key{}, Set: map[string]Value{"status": tv("x")}},
			"no value for key column id"},
		{"an update that sets nothing", sqlOrders, Step{Op: OpUpdate, Key: key("1")}, "sets nothing"},
		{"an insert with no columns", sqlOrders, Step{Op: OpInsert, Key: key("1")}, "has no columns"},
		{"an unknown step", sqlOrders, Step{Op: "merge", Key: key("1")}, "unknown step"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, err := StepStatement(tc.t, tc.s)
			if err == nil || !strings.Contains(err.Error(), tc.want) || st.SQL != "" {
				t.Fatalf("StepStatement = %q, %v; want an error with %q", st.SQL, err, tc.want)
			}
		})
	}
	if _, err := GuardSelect(badType, nil, []Key{key("1")}, false); err == nil {
		t.Fatal("GuardSelect accepted a bad type name")
	}
}

func TestQuoteIdentSQL(t *testing.T) {
	for in, want := range map[string]string{"orders": `"orders"`, `a"b`: `"a""b"`, "x\x00y": `"xy"`, "": `""`, "Mixed Case": `"Mixed Case"`} {
		if got := QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Qualified(sqlQuoted); got != `"My""Schema"."we""ird table"` {
		t.Fatalf("Qualified = %q", got)
	}
}

func TestGuardColumnsAndKeys(t *testing.T) {
	p, fs := build(evidence(upd(1, ordersRef, order("42", "paid"), with(order("42", "shipped"), "id", "1042")),
		del(2, itemsRef, item("90", "9"))), defaultCatalog())
	if p == nil {
		t.Fatal(fs)
	}
	if got := GuardColumns(p, 100); !slices.Equal(got, []string{"note", "status"}) {
		t.Fatalf("GuardColumns(orders) = %q", got)
	}
	if got := GuardColumns(p, 200); got != nil {
		t.Fatalf("GuardColumns(items) = %q (a re-insert guard compares no columns)", got)
	}
	if got := GuardKeys(p, 100); len(got) != 2 || got[0]["id"] != "1042" || got[1]["id"] != "42" {
		t.Fatalf("GuardKeys(orders) = %v", got)
	}
	if got := GuardKeys(p, 999); got != nil {
		t.Fatalf("GuardKeys(999) = %v", got)
	}
	// Every step of a built plan becomes a statement.
	for _, s := range p.Steps {
		pt, _ := p.Table(s.Table)
		if _, err := StepStatement(pt, s); err != nil {
			t.Fatalf("step %+v: %v", s, err)
		}
	}
	for _, pt := range p.Tables {
		if _, err := GuardSelect(pt, GuardColumns(p, pt.RelID), GuardKeys(p, pt.RelID), true); err != nil {
			t.Fatalf("guard on %s: %v", pt.String(), err)
		}
	}
}

// typeChars is TypePattern's character class, for the fuzz oracle.
const typeChars = `ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_ ."(),[]`

func TestValidTypeName(t *testing.T) {
	for _, s := range []string{"bigint", "text", "character varying(20)", "numeric(12,2)", "integer[]", "timestamp with time zone",
		`"My Type"`, `public."Order Status"`, "double precision", "bit varying(8)[]", strings.Repeat("a", 128)} {
		if !ValidTypeName(s) {
			t.Errorf("ValidTypeName(%q) = false", s)
		}
	}
	for _, s := range []string{"", strings.Repeat("a", 129), "text;", "text'", "$1", "text--", "a/b", "a*b", "text\n", "text\r",
		"text\t", "int8\x00", "tëxt", "a=b", "a:b", "text::int"} {
		if ValidTypeName(s) {
			t.Errorf("ValidTypeName(%q) = true", s)
		}
	}
}

func FuzzTypeName(f *testing.F) {
	for _, s := range []string{"bigint", "character varying(20)", "numeric(12,2)", "integer[]", `"My Type"`, "text;drop", "text\n",
		"a'b", "$1", "--", "/*", strings.Repeat("x", 129), ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ok := ValidTypeName(s)
		oracle := len(s) >= 1 && len(s) <= 128
		for i := 0; i < len(s) && oracle; i++ {
			oracle = strings.IndexByte(typeChars, s[i]) >= 0
		}
		if ok != oracle {
			t.Fatalf("ValidTypeName(%q) = %v, oracle %v", s, ok, oracle)
		}
		if ok && strings.ContainsAny(s, ";'$-/*\n") {
			t.Fatalf("ValidTypeName accepted %q", s)
		}
	})
}
