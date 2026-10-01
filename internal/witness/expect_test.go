package witness

import (
	"encoding/json"
	"reflect"
	"testing"
)

func n(s string) json.Number { return json.Number(s) }

func TestEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b any
		want bool
	}{
		{"same string", "shipped", "shipped", true},
		{"different string", "shipped", "pending", false},
		{`"42" vs 42`, "42", n("42"), true},
		{`42 vs "42"`, n("42"), "42", true},
		{`"042" vs 42`, "042", n("42"), false},
		{`42 vs "042"`, n("42"), "042", false},
		{`"-1" vs -1`, "-1", n("-1"), false},
		{`"4.2" vs 4.2`, "4.2", n("4.2"), false},
		{"1.0 vs 1", n("1.0"), n("1"), true},
		{"1e2 vs 100", n("1e2"), n("100"), true},
		{"1 vs 2", n("1"), n("2"), false},
		{"number vs float64", n("1.5"), 1.5, true},
		{"float64 vs number", 1.5, n("1.5"), true},
		{"int vs number", 3, n("3"), true},
		{"number vs int", n("3"), 3, true},
		{"int64 vs number", int64(3), n("3.0"), true},
		{"number vs int64", n("3"), int64(3), true},
		{"int vs string digits", 42, "42", true},
		{"string digits vs int", "42", 42, true},
		{"float64 vs int", 2.0, 2, true},
		{"bool", true, true, true},
		{"bool vs string", true, "true", false},
		{"null", nil, nil, true},
		{"null vs string", nil, "", false},
		{"string vs null", "", nil, false},
		{"bad number", n("x"), n("1"), false},
		{"array", []any{"a", n("1")}, []any{"a", n("1.0")}, true},
		{"array order", []any{"a", "b"}, []any{"b", "a"}, false},
		{"array length", []any{"a"}, []any{"a", "a"}, false},
		{"array vs object", []any{}, map[string]any{}, false},
		{"object", map[string]any{"a": n("1"), "b": []any{nil}}, map[string]any{"b": []any{nil}, "a": n("1")}, true},
		{"object extra key", map[string]any{"a": n("1")}, map[string]any{"a": n("1"), "b": n("2")}, false},
		{"object missing key", map[string]any{"a": n("1"), "c": nil}, map[string]any{"a": n("1"), "b": nil}, false},
		{"nested", map[string]any{"o": map[string]any{"x": []any{n("1"), "2"}}}, map[string]any{"o": map[string]any{"x": []any{"1", n("2")}}}, true},
		{"unknown type", struct{}{}, struct{}{}, false},
	}
	for _, c := range cases {
		if got := Equal(c.a, c.b); got != c.want {
			t.Errorf("%s: Equal(%#v, %#v) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
		if got := Equal(c.b, c.a); got != c.want {
			t.Errorf("%s (swapped): Equal(%#v, %#v) = %v, want %v", c.name, c.b, c.a, got, c.want)
		}
	}
}

func TestTemplateRef(t *testing.T) {
	cases := map[any]string{
		"{{args.id}}":                 "args.id",
		"{{ args.id }}":               "args.id",
		"{{args.items[0].id}}":        "args.items[0].id",
		"{{result.body.id}}":          "result.body.id",
		"{{args}}":                    "args",
		"x{{args.id}}":                "",
		"{{args.id}}x":                "",
		"/v1/orders/{{args.id}}":      "",
		"{{args.id}}{{args.status}}":  "",
		"{{result.status}}":           "",
		"{{env.HOME}}":                "",
		"{{args.a b}}":                "",
		"shipped":                     "",
		42:                            "",
		"{{args.../admin}}":           "",
		"{{result.body.items[x]}}":    "",
		"{{args.id}}\n":               "",
		"{{args.id | upper}}":         "",
		"{{args.id}} and {{args.id}}": "",
	}
	for in, want := range cases {
		if got := templateRef(in); got != want {
			t.Errorf("templateRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolve(t *testing.T) {
	s := Subject{
		Args:   json.RawMessage(`{"id":"42","n":7,"items":[{"id":"a"},{"id":"b"}],"nested":{"deep":{"x":true}}}`),
		Result: ResultView{Output: json.RawMessage(`{"status":200,"body":{"id":"42","tags":["x","y"]}}`)},
	}
	env := newTemplateEnv(s)
	cases := []struct {
		in    any
		want  any
		found bool
	}{
		{"{{args.id}}", "42", true},
		{"{{args.n}}", n("7"), true},
		{"{{args.items[1].id}}", "b", true},
		{"{{args.nested.deep.x}}", true, true},
		{"{{args.items[2].id}}", nil, false},
		{"{{args.missing}}", nil, false},
		{"{{args.id.x}}", nil, false},
		{"{{args.items.id}}", nil, false},
		{"{{args.nested[0]}}", nil, false},
		{"{{result.body.id}}", "42", true},
		{"{{result.body.tags[0]}}", "x", true},
		{"{{result.body.missing}}", nil, false},
		{"literal", "literal", true},
		{n("5"), n("5"), true},
	}
	for _, c := range cases {
		got, found := env.resolve(c.in)
		if found != c.found || !reflect.DeepEqual(got, c.want) {
			t.Errorf("resolve(%v) = %#v, %v; want %#v, %v", c.in, got, found, c.want, c.found)
		}
	}
	// No body, or an output that isn't an object.
	if v, ok := newTemplateEnv(Subject{Result: ResultView{Output: json.RawMessage(`[1]`)}}).resolve("{{result.body.id}}"); ok || v != nil {
		t.Errorf("array output: %v %v", v, ok)
	}
	if decode([]byte("  ")) != nil || decode([]byte("{")) != nil {
		t.Error("decode accepted empty or invalid input")
	}
}

func TestLookup(t *testing.T) {
	root := map[string]any{"a": []any{map[string]any{"b": "c"}}}
	cases := []struct {
		path string
		want any
		ok   bool
	}{
		{"", root, true},
		{".a[0].b", "c", true},
		{".a[0]", map[string]any{"b": "c"}, true},
		{".a[1]", nil, false},
		{".a[-1]", nil, false},
		{".a[x]", nil, false},
		{".a[0", nil, false},
		{"a", nil, false},
		{".a.b", nil, false},
	}
	for _, c := range cases {
		got, ok := lookup(root, c.path)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("lookup(%q) = %v, %v", c.path, got, ok)
		}
	}
}

func TestRunExpect(t *testing.T) {
	s := Subject{Args: json.RawMessage(`{"id":"42","status":"shipped","qty":3}`)}
	env := newTemplateEnv(s)
	o := Observation{Status: 200, Body: decode([]byte(`{"id":42,"status":"pending","qty":"3","items":[{"sku":"a"}],"note":null}`))}
	spec := map[string]any{
		"body.status":       "{{args.status}}",
		"body.id":           "{{args.id}}",
		"body.qty":          "{{args.qty}}",
		"body.items[0].sku": "a",
		"body.note":         nil,
		"body.missing":      "x",
		"status":            200,
		"meta.attempts":     nil,
	}
	r := runExpect(spec, env, o)
	var matched, diffs []string
	for _, m := range r.matched {
		matched = append(matched, m.Path)
	}
	for _, d := range r.diffs {
		diffs = append(diffs, d.Path)
	}
	wantMatched := []string{"body.id", "body.items[0].sku", "body.note", "body.qty", "meta.attempts", "status"}
	wantDiffs := []string{"body.missing", "body.status"}
	if !reflect.DeepEqual(matched, wantMatched) {
		t.Errorf("matched %v, want %v", matched, wantMatched)
	}
	if !reflect.DeepEqual(diffs, wantDiffs) {
		t.Errorf("diffs %v, want %v", diffs, wantDiffs)
	}
	want := FieldDiff{Path: "body.status", Expected: "shipped", Observed: "pending"}
	if !reflect.DeepEqual(r.diffs[1], want) {
		t.Errorf("diff %+v, want %+v", r.diffs[1], want)
	}
	if r.diffs[0].Observed != nil || r.diffs[0].Expected != "x" {
		t.Errorf("missing field diff %+v", r.diffs[0])
	}
}

func TestFieldsPhrase(t *testing.T) {
	one := []FieldDiff{{Path: "body.status", Observed: "shipped"}}
	two := append(one, FieldDiff{Path: "body.qty", Observed: n("3")})
	three := append(two, FieldDiff{Path: "status", Observed: n("200")})
	cases := map[string]string{
		fieldsPhrase(nil):   "the read-back answered",
		fieldsPhrase(one):   `status is "shipped"`,
		fieldsPhrase(two):   `status is "shipped" and qty is 3`,
		fieldsPhrase(three): `status is "shipped" and 2 more fields agree`,
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestShowValue(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{nil, "null"},
		{"a\"b", `"a\"b"`},
		{n("1.50"), "1.50"},
		{true, "true"},
		{1.5, "1.5"},
		{7, "7"},
		{int64(8), "8"},
		{[]any{"a", n("1")}, `["a",1]`},
		{map[string]any{"k": "v"}, `{"k":"v"}`},
		{func() {}, "0x"},
	}
	for _, c := range cases {
		got := ShowValue(c.v)
		if c.want == "0x" {
			if got == "" {
				t.Error("ShowValue(func) is empty")
			}
			continue
		}
		if got != c.want {
			t.Errorf("ShowValue(%#v) = %q, want %q", c.v, got, c.want)
		}
	}
}
