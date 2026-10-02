package sqlguard

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The shape table lives in testdata/shape: NN_name.sql holds one input and NN_name.json the Shape
// ShapeOf returns for it (json.MarshalIndent, two spaces, trailing newline). Regenerate the JSON
// with
//
//	go test ./internal/sqlguard -run TestShapeGolden -update
//
// and review the diff. shapeWhy pins Simple and Why per file as well, so a regenerated golden can't
// quietly change either.

var updateShape = flag.Bool("update", false, "rewrite testdata/shape/*.json from ShapeOf")

// shapeWhy is the expected Why for every file in testdata/shape ("" means Simple).
var shapeWhy = map[string]string{
	"01_update_qualified":       "",
	"02_update_in_list":         "",
	"03_update_params":          "",
	"04_update_expr":            "",
	"05_update_from":            ShapeWhyFromClause,
	"06_cte":                    ShapeWhyCTE,
	"07_insert_on_conflict":     ShapeWhyOnConflict,
	"08_multi_statement":        ShapeWhyMultiStatement,
	"09_quoted_idents":          "",
	"10_estring":                "",
	"11_comments":               "",
	"12_cast_date":              "",
	"13_flag_t":                 "",
	"14_numeric":                "",
	"15_set_null":               "",
	"16_set_now":                ShapeWhyFunctionCall,
	"17_email_key":              "",
	"18_where_or":               ShapeWhyWhereNotKey,
	"19_where_gt":               ShapeWhyWhereNotKey,
	"20_no_where":               ShapeWhyNoWhere,
	"21_only_alias":             "",
	"22_delete_returning":       "",
	"23_returning_expr":         ShapeWhyReturningExpr,
	"24_insert_rows":            "",
	"25_insert_select":          ShapeWhyInsertSelect,
	"26_insert_default_values":  ShapeWhyDefaultValues,
	"27_insert_no_cols":         ShapeWhyOther,
	"28_select":                 ShapeWhyNotDML,
	"29_empty":                  ShapeWhyEmpty,
	"30_set_subquery":           ShapeWhySubquery,
	"31_where_parens":           "",
	"32_where_and":              "",
	"33_where_duplicate_column": ShapeWhyWhereNotKey,
	"34_negative_key":           "",
	"35_param_cast":             "",
	"36_cast_function_form":     "",
	"37_exponent":               "",
	"38_true_upper":             "",
	"39_mixed_case":             "",
	"40_upper_delete":           "",
	"41_row_set":                ShapeWhyOther,
	"42_dollar_quoted":          "",
	"43_unterminated_string":    ShapeWhyLexError,
	"44_delete_using":           ShapeWhyUsingClause,
	"45_unknown_qualifier":      ShapeWhyUnknownQualifier,
	"46_where_current_of":       ShapeWhyWhereNotKey,
	"47_values_expr":            ShapeWhyValuesExpr,
	"48_numeric_cast":           "",
	"49_timestamptz_cast":       "",
	"50_estring_other_escape":   "",
	"51_in_with_and":            ShapeWhyWhereNotKey,
	"52_where_null":             ShapeWhyWhereNotKey,
	"53_merge":                  ShapeWhyOther,
	"54_set_qualified_target":   ShapeWhyOther,
	"55_ops_without_spaces":     "",
	"56_alias_hides_table_name": ShapeWhyUnknownQualifier,
	"57_schema_qualifier":       "",
	"58_nstring":                "",
	"59_hex_number":             "",
	"60_current_timestamp":      ShapeWhyFunctionCall,
	"61_returning_whole_row":    ShapeWhyReturningExpr,
	"62_insert_row_width":       ShapeWhyOther,
	"63_set_default":            ShapeWhyOther,
	"64_bit_string":             "",
	"65_insert_quoted":          "",
	"66_delete_join":            ShapeWhyJoin,
	"67_is_distinct_from":       "",
	"68_trailing_semicolon":     "",
	"69_in_subquery":            ShapeWhyWhereNotKey,
	"70_function_in_where":      ShapeWhyWhereNotKey,
}

type shapeFile struct {
	name string // NN_name
	sql  string
}

func shapeFiles(t testing.TB) []shapeFile {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "shape", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var out []shapeFile
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, shapeFile{name: strings.TrimSuffix(filepath.Base(p), ".sql"), sql: string(b)})
	}
	return out
}

func shapeJSON(t testing.TB, s Shape) []byte {
	t.Helper()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestShapeGolden(t *testing.T) {
	files := shapeFiles(t)
	if len(files) < 45 {
		t.Fatalf("testdata/shape has %d inputs, want at least 45", len(files))
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.name] = true
		t.Run(f.name, func(t *testing.T) {
			got := ShapeOf(f.sql, ShapeOptions{})
			want, ok := shapeWhy[f.name]
			if !ok {
				t.Fatalf("%s has no row in shapeWhy", f.name)
			}
			if got.Why != want || got.Simple != (want == "") {
				t.Errorf("Simple %v, Why %q; want Why %q", got.Simple, got.Why, want)
			}
			b := shapeJSON(t, got)
			path := filepath.Join("testdata", "shape", f.name+".json")
			if *updateShape {
				if err := os.WriteFile(path, b, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			golden, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if !bytes.Equal(golden, b) {
				t.Errorf("differs from %s:\n got: %s\nwant: %s", path, b, golden)
			}
		})
	}
	for name := range shapeWhy {
		if !seen[name] {
			t.Errorf("shapeWhy names %s, which has no .sql file", name)
		}
	}
}

func iv(s string) Value { return Value{Kind: ShapeValueInteger, Text: s} }
func sv(s string) Value { return Value{Kind: ShapeValueString, Text: s} }

// TestShapeFields checks the fields the S19 Witness reads, independently of the goldens.
func TestShapeFields(t *testing.T) {
	for _, c := range []struct {
		sql  string
		want Shape
	}{
		{"UPDATE public.orders SET status = 'shipped' WHERE id = 42", Shape{Simple: true, Kind: "update",
			Table: "public.orders", Set: []Assign{{"status", sv("shipped")}},
			Where: &KeyPred{Columns: []string{"id"}, Rows: [][]Value{{iv("42")}}}}},
		{"UPDATE orders SET status = 'shipped' WHERE id IN (1,2,3)", Shape{Simple: true, Kind: "update",
			Table: "public.orders", Set: []Assign{{"status", sv("shipped")}},
			Where: &KeyPred{Columns: []string{"id"}, Rows: [][]Value{{iv("1")}, {iv("2")}, {iv("3")}}}}},
		{"UPDATE orders SET status = $2 WHERE id = $1", Shape{Simple: true, Kind: "update", Table: "public.orders",
			Set:   []Assign{{"status", Value{Kind: ShapeValueParam, Text: "$2", Param: 2}}},
			Where: &KeyPred{Columns: []string{"id"}, Rows: [][]Value{{{Kind: ShapeValueParam, Text: "$1", Param: 1}}}}}},
		{"UPDATE orders SET total = total + 1 WHERE id = 7", Shape{Simple: true, Kind: "update", Table: "public.orders",
			Set:   []Assign{{"total", Value{Kind: ShapeValueExpr, Text: "total + 1"}}},
			Where: &KeyPred{Columns: []string{"id"}, Rows: [][]Value{{iv("7")}}}}},
		{"update t set c = E'it\\'s\\n', d = -2.5e-3, e = false, f = null where a = 1 and \"B\" = 'x' returning *, c",
			Shape{Simple: true, Kind: "update", Table: "public.t",
				Set: []Assign{{"c", sv("it's\n")}, {"d", Value{Kind: ShapeValueNumeric, Text: "-2.5e-3"}},
					{"e", Value{Kind: ShapeValueBool, Text: "false"}}, {"f", Value{Kind: ShapeValueNull}}},
				Where:     &KeyPred{Columns: []string{"a", "B"}, Rows: [][]Value{{iv("1"), sv("x")}}},
				Returning: []string{"*", "c"}}},
		{"DELETE FROM app.\"Orders\" x WHERE ((x.id = '7'::bigint))", Shape{Simple: true, Kind: "delete", Table: "app.Orders",
			Where: &KeyPred{Columns: []string{"id"}, Rows: [][]Value{{{Kind: ShapeValueString, Text: "7", Cast: "bigint"}}}}}},
		{"INSERT INTO t (a, b) VALUES ($1::int, CAST('x' AS character varying(10))), (-3, 'it''s')",
			Shape{Simple: true, Kind: "insert", Table: "public.t", InsertCols: []string{"a", "b"},
				InsertRows: [][]Value{
					{{Kind: ShapeValueParam, Text: "$1", Param: 1, Cast: "int"}, {Kind: ShapeValueString, Text: "x", Cast: "character varying(10)"}},
					{iv("-3"), sv("it's")}}}},
		{"UPDATE t SET a = '1'::int[], b = 2::pg_catalog.int8 WHERE id = 1", Shape{Simple: true, Kind: "update", Table: "public.t",
			Set: []Assign{{"a", Value{Kind: ShapeValueString, Text: "1", Cast: "int[]"}},
				{"b", Value{Kind: ShapeValueInteger, Text: "2", Cast: "pg_catalog.int8"}}},
			Where: &KeyPred{Columns: []string{"id"}, Rows: [][]Value{{iv("1")}}}}},
		{"UPDATE t SET a = ARRAY[1, 2], b = x[1:2] WHERE id = 1", Shape{Simple: true, Kind: "update", Table: "public.t",
			Set:   []Assign{{"a", Value{Kind: ShapeValueExpr, Text: "ARRAY[1, 2]"}}, {"b", Value{Kind: ShapeValueExpr, Text: "x[1:2]"}}},
			Where: &KeyPred{Columns: []string{"id"}, Rows: [][]Value{{iv("1")}}}}},
	} {
		got := ShapeOf(c.sql, ShapeOptions{})
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s\n got: %s\nwant: %s", c.sql, shapeJSON(t, got), shapeJSON(t, c.want))
		}
	}
}

// TestShapeCastTypes: a cast is read only when everything after "::" is a type name.
func TestShapeCastTypes(t *testing.T) {
	for _, c := range []struct{ value, kind, text, cast string }{
		{"'1'::interval day to second(3)", ShapeValueString, "1", "interval day to second(3)"},
		{"'1.5'::DOUBLE PRECISION", ShapeValueString, "1.5", "double precision"},
		{"'a'::character varying(10)[]", ShapeValueString, "a", "character varying(10)[]"},
		{"'10:00'::timestamp(3) without time zone", ShapeValueString, "10:00", "timestamp(3) without time zone"},
		{"CAST(-7 AS numeric(5,1))", ShapeValueInteger, "-7", "numeric(5,1)"},
		{"CAST(NULL AS text)", ShapeValueNull, "", "text"},
		{"'x'::timestamp at time zone z", ShapeValueExpr, "'x'::timestamp at time zone z", ""},
		{"'x'::text OR b", ShapeValueExpr, "'x'::text OR b", ""},
		{"'x'::text || 'y'", ShapeValueExpr, "'x'::text || 'y'", ""},
		{"'x'::\"MyType\"", ShapeValueExpr, "'x'::\"MyType\"", ""},
		{"'5'::int::text", ShapeValueExpr, "'5'::int::text", ""},
		{"-5::int", ShapeValueExpr, "-5::int", ""},
		{"CAST(b AS int)", ShapeValueExpr, "CAST(b AS int)", ""},
		{"'1'::numeric(10,)", ShapeValueExpr, "'1'::numeric(10,)", ""},
	} {
		got := ShapeOf("UPDATE t SET a = "+c.value+" WHERE id = 1", ShapeOptions{})
		if !got.Simple || len(got.Set) != 1 {
			t.Errorf("%s: %s", c.value, shapeJSON(t, got))
			continue
		}
		v := got.Set[0].Value
		if v.Kind != c.kind || v.Text != c.text || v.Cast != c.cast {
			t.Errorf("%s: got %+v, want kind %s text %q cast %q", c.value, v, c.kind, c.text, c.cast)
		}
	}
}

// TestShapeLiterals: how each literal form reads in a key position.
func TestShapeLiterals(t *testing.T) {
	for _, c := range []struct{ lit, kind, text string }{
		{`E'a\\b\tc\rd''e'`, ShapeValueString, "a\\b\tc\rd'e"},
		{`e'x\''''`, ShapeValueString, "x''"},
		{`E'\b'`, "", ""},
		{`E'\101'`, "", ""},
		{`n'it''s'`, ShapeValueString, "it's"},
		{`''`, ShapeValueString, ""},
		{"007", ShapeValueInteger, "007"},
		{"1.", ShapeValueNumeric, "1."},
		{"- 2.5E+10", ShapeValueNumeric, "-2.5E+10"},
		{"$12", ShapeValueParam, "$12"},
		{"fAlSe", ShapeValueBool, "false"},
		{"0o17", "", ""},
		{"0b1_0", "", ""},
		{"1_000_000", "", ""},
		{"0X_FF", "", ""},
		{"X'1F'", "", ""},
	} {
		got := ShapeOf("DELETE FROM t WHERE k = "+c.lit, ShapeOptions{})
		if c.kind == "" {
			if got.Why != ShapeWhyWhereNotKey {
				t.Errorf("%s: want where_not_key, got %s", c.lit, shapeJSON(t, got))
			}
			continue
		}
		if !got.Simple || got.Where.Rows[0][0].Kind != c.kind || got.Where.Rows[0][0].Text != c.text {
			t.Errorf("%s: want %s %q, got %s", c.lit, c.kind, c.text, shapeJSON(t, got))
		}
	}
	for _, bad := range []string{"1__0", "1_", "0x", "0xG", "0o8", "0b2", "1e+", "1.2e3.4", "1.2.3"} {
		if got := ShapeOf("UPDATE t SET a = "+bad+" WHERE k = 1", ShapeOptions{}); got.Why != ShapeWhyOther {
			t.Errorf("%s: want other, got %s", bad, shapeJSON(t, got))
		}
	}
}

// TestShapeNotSimple covers reasons and edges the golden files don't.
func TestShapeNotSimple(t *testing.T) {
	for _, c := range []struct{ sql, why, kind string }{
		{"SELECT 1 -- " + strings.Repeat("x", DefaultMaxBytes), ShapeWhyTooLarge, "other"},
		{"UPDATE t SET a = " + strings.Repeat("(", 300) + "1" + strings.Repeat(")", 300) + " WHERE id = 1", ShapeWhyOther, "update"},
		{"", ShapeWhyEmpty, "other"},
		{";;", ShapeWhyEmpty, "other"},
		{"WITH d AS (DELETE FROM t RETURNING *) SELECT 1", ShapeWhyCTE, "other"},
		{"WITH d AS (SELECT 1) DELETE FROM t WHERE id = 1", ShapeWhyCTE, "delete"},
		{"UPDATE t SET a = 1 WHERE id = 1; SELECT 1", ShapeWhyMultiStatement, "update"},
		{"UPDATE t SET a = 1, a = 2 WHERE id = 1", ShapeWhyOther, "update"},
		{"UPDATE t SET a[1] = 2 WHERE id = 1", ShapeWhyOther, "update"},
		{"UPDATE t SET a = b[1, c = 2 WHERE id = 1", ShapeWhyOther, "update"},
		{"UPDATE t SET a = b], c = 2 WHERE id = 1", ShapeWhyOther, "update"},
		{"UPDATE t SET a = coalesce(b, 1) WHERE id = 1", ShapeWhyFunctionCall, "update"},
		{"UPDATE t SET a = \"f\"(1) WHERE id = 1", ShapeWhyFunctionCall, "update"},
		{"UPDATE t SET a = extract(year from b) WHERE id = 1", ShapeWhyFunctionCall, "update"},
		{"UPDATE t SET a = EXISTS (SELECT 1) WHERE id = 1", ShapeWhySubquery, "update"},
		{"UPDATE t SET a = 1 WHERE id = 1 RETURNING a AS b", ShapeWhyReturningExpr, "update"},
		{"UPDATE t SET a = 1 WHERE id = 1 RETURNING old", ShapeWhyReturningExpr, "update"},
		{"UPDATE t SET a = 1 WHERE id = 1 RETURNING t.a", ShapeWhyReturningExpr, "update"},
		{"UPDATE t SET a = 1 WHERE id = $0", ShapeWhyOther, "update"},
		{"UPDATE t SET a = 1 WHERE id = $65536", ShapeWhyOther, "update"},
		{"UPDATE t SET a = 12abc WHERE id = 1", ShapeWhyOther, "update"},
		{"UPDATE t SET a = 1 + 12abc WHERE id = 1", ShapeWhyOther, "update"},
		{"UPDATE t SET a = 1 WHERE id = 1_000", ShapeWhyWhereNotKey, "update"},
		{"UPDATE t SET a = 1 WHERE id = 1 + 1", ShapeWhyWhereNotKey, "update"},
		{"UPDATE t SET a = 1 WHERE 1 = id", ShapeWhyWhereNotKey, "update"},
		{"UPDATE t SET a = 1 WHERE id IN ()", ShapeWhyWhereNotKey, "update"},
		{"UPDATE t SET a = 1 WHERE id NOT IN (1)", ShapeWhyWhereNotKey, "update"},
		{"UPDATE t SET a = 1 WHERE (a = 1) AND (b = 2)", ShapeWhyWhereNotKey, "update"},
		{"UPDATE t SET a = 1 WHERE id = 'a' 'b'", ShapeWhyWhereNotKey, "update"},
		{"UPDATE t SET a = 1 WHERE id = U&'x'", ShapeWhyWhereNotKey, "update"},
		{"UPDATE t SET a = 1 WHERE id = -5::int", ShapeWhyWhereNotKey, "update"},
		{"UPDATE t SET a = 1 WHERE id = '5'::int::text", ShapeWhyWhereNotKey, "update"},
		{"UPDATE t SET a = 1 WHERE s.t.id = 1", ShapeWhyUnknownQualifier, "update"},
		{"UPDATE t SET a = 1 WHERE public.t.id = 1", "", "update"},
		{"UPDATE t SET a = 1 RETURNING a", ShapeWhyNoWhere, "update"},
		{"DELETE FROM t", ShapeWhyNoWhere, "delete"},
		{"DELETE t WHERE id = 1", ShapeWhyOther, "delete"},
		{"UPDATE a.b.c SET x = 1 WHERE id = 1", ShapeWhyOther, "update"},
		{"UPDATE U&\"t\" SET x = 1 WHERE id = 1", ShapeWhyOther, "update"},
		{"UPDATE select SET x = 1 WHERE id = 1", ShapeWhyOther, "update"},
		{"UPDATE t SET x = 1 WHERE id = 1 LIMIT 1", ShapeWhyWhereNotKey, "update"},
		{"INSERT INTO t (a, a) VALUES (1, 2)", ShapeWhyOther, "insert"},
		{"INSERT INTO t (a) VALUES (DEFAULT)", ShapeWhyValuesExpr, "insert"},
		{"INSERT INTO t (a) (SELECT 1)", ShapeWhyInsertSelect, "insert"},
		{"INSERT INTO t (SELECT 1)", ShapeWhyInsertSelect, "insert"},
		{"INSERT INTO t (a) VALUES (1) RETURNING a + 1", ShapeWhyReturningExpr, "insert"},
		{"INSERT INTO t AS x (a) VALUES (1)", ShapeWhyOther, "insert"},
		{"INSERT INTO t (a) OVERRIDING SYSTEM VALUE VALUES (1)", ShapeWhyOther, "insert"},
		{"INSERT INTO t (a) VALUES (1) ON CONFLICT (a) DO UPDATE SET a = 2", ShapeWhyOnConflict, "insert"},
		{"UPDATE t SET a = 'x\\y' WHERE id = 1", ShapeWhyLexError, "update"},
		{"UPDATE t SET a = 1 WHERE id = 1 /* open", ShapeWhyLexError, "update"},
		{"UPDATE t\xd0\xb0ble SET a = 1 WHERE id = 1", ShapeWhyLexError, "update"}, // a Cyrillic letter
		{"EXPLAIN ANALYZE DELETE FROM t WHERE id = 1", ShapeWhyNotDML, "other"},
		{"(DELETE FROM t WHERE id = 1)", ShapeWhyNotDML, "other"},
		{"COPY t FROM STDIN", ShapeWhyNotDML, "other"},
	} {
		got := ShapeOf(c.sql, ShapeOptions{})
		name := c.sql
		if len(name) > 80 {
			name = name[:80] + "…"
		}
		if got.Why != c.why || got.Simple != (c.why == "") || got.Kind != c.kind {
			t.Errorf("%q: Simple %v, Why %q, Kind %q; want Why %q, Kind %q", name, got.Simple, got.Why, got.Kind, c.why, c.kind)
		}
		if !got.Simple && (got.Set != nil || got.Where != nil || got.InsertCols != nil || got.InsertRows != nil || got.Returning != nil) {
			t.Errorf("%q: not simple but carries fields: %s", name, shapeJSON(t, got))
		}
	}
}

func TestShapeDefaultSchema(t *testing.T) {
	got := ShapeOf("DELETE FROM orders WHERE orders.id = 1", ShapeOptions{DefaultSchema: "app"})
	if !got.Simple || got.Table != "app.orders" {
		t.Fatalf("%s", shapeJSON(t, got))
	}
	got = ShapeOf("DELETE FROM orders WHERE app.orders.id = 1", ShapeOptions{DefaultSchema: "app"})
	if !got.Simple {
		t.Fatalf("%s", shapeJSON(t, got))
	}
	got = ShapeOf("DELETE FROM orders WHERE public.orders.id = 1", ShapeOptions{DefaultSchema: "app"})
	if got.Why != ShapeWhyUnknownQualifier {
		t.Fatalf("%s", shapeJSON(t, got))
	}
}

// TestShapeIgnoresTrivia: comments and whitespace between tokens never change a shape.
func TestShapeIgnoresTrivia(t *testing.T) {
	for _, f := range shapeFiles(t) {
		toks, err := Tokenize(f.sql)
		if err != nil {
			continue
		}
		var b strings.Builder
		for _, tk := range toks {
			b.WriteString(tk.Text)
			if !tk.trivia() {
				b.WriteString(" /* x */\n")
			}
		}
		a, c := ShapeOf(f.sql, ShapeOptions{}), ShapeOf(b.String(), ShapeOptions{})
		if a.Why == ShapeWhyEmpty {
			continue
		}
		// Expr text is the exact source, comments included, so compare everything else.
		for _, s := range []*Shape{&a, &c} {
			for i := range s.Set {
				if s.Set[i].Value.Kind == ShapeValueExpr {
					s.Set[i].Value.Text = ""
				}
			}
		}
		if !reflect.DeepEqual(a, c) {
			t.Errorf("%s: comments changed the shape\n%s\n%s", f.name, shapeJSON(t, a), shapeJSON(t, c))
		}
	}
}

func TestShapeWhysListed(t *testing.T) {
	if len(AllShapeWhys) != 20 || slices.Contains(AllShapeWhys, "") {
		t.Fatalf("AllShapeWhys: %v", AllShapeWhys)
	}
	for _, w := range shapeWhy {
		if w != "" && !slices.Contains(AllShapeWhys, w) {
			t.Errorf("%q is not in AllShapeWhys", w)
		}
	}
}

// TestShapeCorpusNeverPanics runs every corpus.yaml input (generated ones included) through ShapeOf
// and checks the invariants FuzzShape checks.
func TestShapeCorpusNeverPanics(t *testing.T) {
	for _, c := range loadCorpus(t).Cases {
		checkShapeInvariants(t, corpusSQL(c))
	}
}

func checkShapeInvariants(t *testing.T, sql string) {
	t.Helper()
	got := ShapeOf(sql, ShapeOptions{})
	if again := ShapeOf(sql, ShapeOptions{}); !reflect.DeepEqual(got, again) {
		t.Fatalf("not deterministic on %q:\n%s\n%s", sql, shapeJSON(t, got), shapeJSON(t, again))
	}
	if got.Simple == (got.Why != "") {
		t.Fatalf("Simple %v with Why %q on %q", got.Simple, got.Why, sql)
	}
	if got.Why != "" && !slices.Contains(AllShapeWhys, got.Why) {
		t.Fatalf("unknown Why %q on %q", got.Why, sql)
	}
	switch got.Kind {
	case ShapeKindUpdate, ShapeKindDelete, ShapeKindInsert, ShapeKindOther:
	default:
		t.Fatalf("Kind %q on %q", got.Kind, sql)
	}
	if !got.Simple {
		if got.Set != nil || got.Where != nil || got.InsertCols != nil || got.InsertRows != nil || got.Returning != nil {
			t.Fatalf("not simple but carries fields on %q: %s", sql, shapeJSON(t, got))
		}
		return
	}
	if Analyze(sql, Options{}).Ambiguous {
		t.Fatalf("Simple, but Analyze is ambiguous on %q", sql)
	}
	if got.Kind == ShapeKindOther || got.Table == "" || got.Kind != ShapeKindInsert && got.Where == nil {
		t.Fatalf("Simple with Kind %q, Table %q, Where %v on %q", got.Kind, got.Table, got.Where, sql)
	}
	if got.Where != nil {
		for _, r := range got.Where.Rows {
			if len(r) != len(got.Where.Columns) {
				t.Fatalf("key row width on %q: %s", sql, shapeJSON(t, got))
			}
		}
	}
	for _, r := range got.InsertRows {
		if len(r) != len(got.InsertCols) {
			t.Fatalf("insert row width on %q: %s", sql, shapeJSON(t, got))
		}
		for _, v := range r {
			if v.Kind == ShapeValueExpr {
				t.Fatalf("expr in VALUES on %q", sql)
			}
		}
	}
}

func FuzzShape(f *testing.F) {
	for _, sf := range shapeFiles(f) {
		f.Add(sf.sql)
	}
	for _, s := range corpusSeeds(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkShapeInvariants(t, s)
	})
}

func TestShapeAnyWhere(t *testing.T) {
	o := ShapeOptions{AnyWhere: true}
	cases := []struct {
		sql         string
		simple      bool
		other       bool
		kind, table string
		set         int
	}{
		{"UPDATE orders SET status = 'shipped' WHERE status = 'paid' AND total_cents > 100", true, true, "update", "public.orders", 1},
		{"UPDATE orders SET status = 'shipped' WHERE id = 42", true, false, "update", "public.orders", 1},
		{"UPDATE orders SET status = 'shipped'", true, true, "update", "public.orders", 1},
		{"DELETE FROM orders WHERE created_at < now() RETURNING id", true, true, "delete", "public.orders", 0},
		{"UPDATE orders o SET status = 'x' FROM customers c WHERE c.id = o.customer_id", false, false, "update", "", 0},
		{"UPDATE orders SET status = lower('X') WHERE status > 'a'", false, false, "update", "public.orders", 0},
		{"UPDATE orders SET status = 'x' WHERE", false, false, "update", "public.orders", 0},
	}
	for _, c := range cases {
		s := ShapeOf(c.sql, o)
		if s.Simple != c.simple || s.WhereOther != c.other || s.Kind != c.kind || (c.table != "" && s.Table != c.table) || len(s.Set) != c.set {
			t.Errorf("%s: %+v", c.sql, s)
		}
		if s.WhereOther && s.Where != nil {
			t.Errorf("%s: WhereOther with a key predicate", c.sql)
		}
		// Without the option the same statements keep S19's answers.
		if d := ShapeOf(c.sql, ShapeOptions{}); d.WhereOther {
			t.Errorf("%s: WhereOther without AnyWhere", c.sql)
		}
	}
}
