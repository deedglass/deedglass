package intent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gowebpki/jcs"
)

// golden is a canonical (JCS) TRANSFER intent. Key order is RFC 8785 order.
const golden = `{"args":{"amount":"1000000","currency":"USD","from":"alice","to":"bob"},` +
	`"aud":"9b1c2d3e-0000-4000-8000-00000000d001",` +
	`"bundle":"sha256:4f00000000000000000000000000000000000000000000000000000000000001",` +
	`"exp":1759050300000,"iat":1759050000000,"iss":"ops-approver",` +
	`"kid":"3fa9c1d2e3f405162738495a6b7c8d9e","nonce":"b7e2a1c3d4e5f60718293a4b5c6d7e8f",` +
	`"op":"TRANSFER","op_v":1,"v":"deedglass.intent/1"}`

// u returns a JSON backslash-u escape for hex digits h. The escape is built at
// run time so the source never contains it literally (some editors and tools
// unescape such sequences).
func u(h string) string { return "\\" + "u" + h }

func goldenMap(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	d := json.NewDecoder(strings.NewReader(golden))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// canon encodes m and canonicalizes it, so a test input fails only for the
// reason the test introduces.
func canon(t *testing.T, m map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	c, err := jcs.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGoldenIsCanonical(t *testing.T) {
	c, err := jcs.Transform([]byte(golden))
	if err != nil || !bytes.Equal(c, []byte(golden)) {
		t.Fatalf("golden is not canonical: err=%v\n got %s", err, c)
	}
}

func TestParseValid(t *testing.T) {
	in, err := Parse([]byte(golden))
	if err != nil {
		t.Fatal(err)
	}
	want := Intent{
		V: Type, Iss: "ops-approver", Kid: "3fa9c1d2e3f405162738495a6b7c8d9e",
		Aud: "9b1c2d3e-0000-4000-8000-00000000d001", Op: OpTransfer, OpV: 1,
		Bundle: "sha256:4f00000000000000000000000000000000000000000000000000000000000001",
		Nonce:  "b7e2a1c3d4e5f60718293a4b5c6d7e8f", Iat: 1759050000000, Exp: 1759050300000,
		Args: Args{Amount: "1000000", Currency: "USD", From: "alice", To: "bob"},
	}
	if in.Agent != nil {
		t.Fatalf("agent: got %q, want absent", *in.Agent)
	}
	in.Agent = nil
	if *in != want {
		t.Fatalf("got %+v\nwant %+v", *in, want)
	}
}

func TestParseValidWithAgent(t *testing.T) {
	m := goldenMap(t)
	m["agent"] = "claude-code/demo"
	in, err := Parse(canon(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if in.Agent == nil || *in.Agent != "claude-code/demo" {
		t.Fatalf("agent not parsed: %+v", in.Agent)
	}
}

func TestParseStrictRejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
		code Code
	}{
		{"duplicate key", `{"a":1,"a":2}`, CodeDuplicateKey},
		{"duplicate nested key", `{"a":{"b":1,"b":2}}`, CodeDuplicateKey},
		{"duplicate key via escape", `{"a":1,"` + u("0061") + `":2}`, CodeDuplicateKey},
		{"fraction", `{"n":1.5}`, CodeNonInteger},
		{"fraction zero", `{"n":1.0}`, CodeNonInteger},
		{"exponent", `{"n":1e3}`, CodeNonInteger},
		{"negative exponent", `{"n":-2E-1}`, CodeNonInteger},
		{"2^53", `{"n":9007199254740992}`, CodeIntegerRange},
		{"2^53+1 (JCS rounds this)", `{"n":9007199254740993}`, CodeIntegerRange},
		{"-2^53", `{"n":-9007199254740992}`, CodeIntegerRange},
		{"21 digits", `{"n":123456789012345678901}`, CodeIntegerRange},
		{"lone high surrogate", `{"s":"\ud800"}`, CodeLoneSurrogate},
		{"lone low surrogate", `{"s":"\udc00"}`, CodeLoneSurrogate},
		{"high then non-low escape", `{"s":"` + u("d800") + u("0041") + `"}`, CodeLoneSurrogate},
		{"high then plain char", `{"s":"\ud800x"}`, CodeLoneSurrogate},
		{"invalid UTF-8", "{\"s\":\"\xff\"}", CodeInvalidUTF8},
		{"UTF-8 encoded surrogate", "{\"s\":\"\xed\xa0\x80\"}", CodeInvalidUTF8},
		{"overlong UTF-8", "{\"s\":\"\xc0\xaf\"}", CodeInvalidUTF8},
		{"trailing data", `{} x`, CodeSyntax},
		{"two values", `{}{}`, CodeSyntax},
		{"control char in string", "{\"s\":\"a\x01\"}", CodeSyntax},
		{"leading zero", `{"n":01}`, CodeSyntax},
		{"bare minus", `{"n":-}`, CodeSyntax},
		{"empty input", ``, CodeSyntax},
		{"bad escape", `{"s":"\x"}`, CodeSyntax},
		{"short unicode escape", `{"s":"\u12"}`, CodeSyntax},
		{"unterminated string", `{"s":"abc`, CodeSyntax},
		{"missing colon", `{"a" 1}`, CodeSyntax},
		{"trailing comma", `{"a":1,}`, CodeSyntax},
		{"single quotes", `{'a':1}`, CodeSyntax},
		{"bad literal", `{"a":tru}`, CodeSyntax},
		{"too deep", strings.Repeat("[", maxDepth+1) + strings.Repeat("]", maxDepth+1), CodeTooDeep},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseStrict([]byte(tc.in))
			if got := CodeOf(err); got != tc.code {
				t.Fatalf("ParseStrict(%q): code %q (err %v), want %q", tc.in, got, err, tc.code)
			}
			if r := err.(*Error).Reason(); r != ReasonNonCanonical {
				t.Fatalf("reason %q, want %q", r, ReasonNonCanonical)
			}
		})
	}
}

func TestParseStrictAccepts(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{`9007199254740991`, int64(9007199254740991)},
		{`-9007199254740991`, int64(-9007199254740991)},
		{`0`, int64(0)},
		{` {"a" : [1, true, false, null] } `, map[string]any{"a": []any{int64(1), true, false, nil}}},
		{`"` + u("d83d") + u("de00") + `"`, "\U0001F600"},
		{`"\"\\\/\b\f\n\r\t"`, "\"\\/\b\f\n\r\t"},
		{`"é€"`, "é€"},
		{strings.Repeat("[", maxDepth) + strings.Repeat("]", maxDepth), nil},
	}
	for _, tc := range cases {
		got, err := ParseStrict([]byte(tc.in))
		if err != nil {
			t.Fatalf("ParseStrict(%q): %v", tc.in, err)
		}
		if tc.want == nil {
			continue
		}
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(tc.want)
		if !bytes.Equal(gj, wj) {
			t.Fatalf("ParseStrict(%q) = %s, want %s", tc.in, gj, wj)
		}
	}
}

func TestParseSchemaRejects(t *testing.T) {
	type mut func(m map[string]any)
	args := func(m map[string]any) map[string]any { return m["args"].(map[string]any) }
	cases := []struct {
		name string
		mut  mut
		code Code
	}{
		{"unknown top-level key", func(m map[string]any) { m["extra"] = "x" }, CodeUnknownKey},
		{"unknown args key", func(m map[string]any) { args(m)["memo"] = "x" }, CodeUnknownKey},
		{"missing nonce", func(m map[string]any) { delete(m, "nonce") }, CodeMissingKey},
		{"missing args.to", func(m map[string]any) { delete(args(m), "to") }, CodeMissingKey},
		{"amount as number", func(m map[string]any) { args(m)["amount"] = 1000000 }, CodeWrongType},
		{"amount leading zero", func(m map[string]any) { args(m)["amount"] = "01" }, CodeBadValue},
		{"amount zero", func(m map[string]any) { args(m)["amount"] = "0" }, CodeBadValue},
		{"amount negative", func(m map[string]any) { args(m)["amount"] = "-5" }, CodeBadValue},
		{"amount 19 digits", func(m map[string]any) { args(m)["amount"] = "1234567890123456789" }, CodeBadValue},
		{"currency lowercase", func(m map[string]any) { args(m)["currency"] = "usd" }, CodeBadValue},
		{"account injection", func(m map[string]any) { args(m)["to"] = "bob'; DROP TABLE x;--" }, CodeBadValue},
		{"op_v 2", func(m map[string]any) { m["op_v"] = 2 }, CodeBadValue},
		{"op_v as string", func(m map[string]any) { m["op_v"] = "1" }, CodeWrongType},
		{"wrong v", func(m map[string]any) { m["v"] = "deedglass.intent/2" }, CodeBadValue},
		{"wrong op", func(m map[string]any) { m["op"] = "WITHDRAW" }, CodeBadValue},
		{"kid uppercase", func(m map[string]any) { m["kid"] = "3FA9C1D2E3F405162738495A6B7C8D9E" }, CodeBadValue},
		{"nonce short", func(m map[string]any) { m["nonce"] = "b7e2" }, CodeBadValue},
		{"bundle without prefix", func(m map[string]any) { m["bundle"] = strings.Repeat("0", 64) }, CodeBadValue},
		{"aud not a uuid", func(m map[string]any) { m["aud"] = "prod" }, CodeBadValue},
		{"iss uppercase", func(m map[string]any) { m["iss"] = "Ops" }, CodeBadValue},
		{"ttl above 300000 ms", func(m map[string]any) { m["exp"] = 1759050300001 }, CodeBadValue},
		{"exp equals iat", func(m map[string]any) { m["exp"] = 1759050000000 }, CodeBadValue},
		{"iat as string", func(m map[string]any) { m["iat"] = "1759050000000" }, CodeWrongType},
		{"args not object", func(m map[string]any) { m["args"] = []any{} }, CodeWrongType},
		{"agent not string", func(m map[string]any) { m["agent"] = 7 }, CodeWrongType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := goldenMap(t)
			tc.mut(m)
			_, err := Parse(canon(t, m))
			if got := CodeOf(err); got != tc.code {
				t.Fatalf("code %q (err %v), want %q", got, err, tc.code)
			}
			if r := err.(*Error).Reason(); r != ReasonSchema {
				t.Fatalf("reason %q, want %q", r, ReasonSchema)
			}
		})
	}
	if _, err := Parse([]byte(`[]`)); CodeOf(err) != CodeWrongType {
		t.Fatalf("top-level array: %v", err)
	}
}

func TestParseRejectsNonCanonical(t *testing.T) {
	spaced := strings.Replace(golden, `"op":"TRANSFER"`, `"op": "TRANSFER"`, 1)
	reordered := `{"aud":"9b1c2d3e-0000-4000-8000-00000000d001",` +
		`"args":{"amount":"1000000","currency":"USD","from":"alice","to":"bob"},` +
		`"bundle":"sha256:4f00000000000000000000000000000000000000000000000000000000000001",` +
		`"exp":1759050300000,"iat":1759050000000,"iss":"ops-approver",` +
		`"kid":"3fa9c1d2e3f405162738495a6b7c8d9e","nonce":"b7e2a1c3d4e5f60718293a4b5c6d7e8f",` +
		`"op":"TRANSFER","op_v":1,"v":"deedglass.intent/1"}`
	escaped := strings.Replace(golden, `"alice"`, `"`+u("0061")+`lice"`, 1)
	trailingNL := golden + "\n"
	for name, in := range map[string]string{
		"whitespace": spaced, "key order": reordered, "needless escape": escaped, "trailing newline": trailingNL,
	} {
		_, err := Parse([]byte(in))
		if CodeOf(err) != CodeNonCanonical {
			t.Fatalf("%s: got %v, want %s", name, err, CodeNonCanonical)
		}
		if r := err.(*Error).Reason(); r != ReasonNonCanonical {
			t.Fatalf("%s: reason %q", name, r)
		}
	}
	// A duplicate key is refused by the strict parser before JCS is consulted.
	dup := strings.Replace(golden, `"op":"TRANSFER"`, `"op":"TRANSFER","op":"TRANSFER"`, 1)
	if _, err := Parse([]byte(dup)); CodeOf(err) != CodeDuplicateKey {
		t.Fatalf("duplicate key: got %v", err)
	}
}

func TestParseTooLarge(t *testing.T) {
	m := goldenMap(t)
	m["agent"] = ""
	base := canon(t, m)
	m["agent"] = strings.Repeat("a", MaxBytes-len(base)) // exactly MaxBytes
	ok := canon(t, m)
	if len(ok) != MaxBytes {
		t.Fatalf("setup: %d bytes", len(ok))
	}
	if _, err := Parse(ok); err != nil {
		t.Fatalf("%d bytes should pass: %v", len(ok), err)
	}
	m["agent"] = strings.Repeat("a", MaxBytes-len(base)+1)
	if _, err := Parse(canon(t, m)); CodeOf(err) != CodeTooLarge {
		t.Fatalf("%d bytes: got %v", MaxBytes+1, err)
	}
}

// TestJCSRoundsAboveSafeInteger pins the spike S4 finding that makes the strict
// pre-parser necessary. If a future gowebpki/jcs release stops rounding, this
// test fails and the note in plan §3.0 #6 should be revisited (the pre-parser
// stays either way).
func TestJCSRoundsAboveSafeInteger(t *testing.T) {
	got, err := jcs.Transform([]byte(`{"n":9007199254740993}`))
	if err != nil || string(got) != `{"n":9007199254740992}` {
		t.Fatalf("jcs behaviour changed: %q %v", got, err)
	}
	if _, err := ParseStrict([]byte(`{"n":9007199254740993}`)); CodeOf(err) != CodeIntegerRange {
		t.Fatalf("pre-parser must refuse it: %v", err)
	}
	got, err = jcs.Transform([]byte(`{"n":1.5}`))
	if err != nil || string(got) != `{"n":1.5}` {
		t.Fatalf("jcs behaviour changed: %q %v", got, err)
	}
}

// TestJCSReferenceVectors re-runs the RFC 8785 reference pairs (cyberphone
// json-canonicalization testdata) against the pinned gowebpki/jcs.
func TestJCSReferenceVectors(t *testing.T) {
	ins, err := filepath.Glob("../../testdata/jcs/input/*.json")
	if err != nil || len(ins) != 6 {
		t.Fatalf("expected 6 JCS input vectors, found %d (%v)", len(ins), err)
	}
	for _, in := range ins {
		src, err := os.ReadFile(in)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join("../../testdata/jcs/output", filepath.Base(in)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := jcs.Transform(src)
		if err != nil || !bytes.Equal(got, bytes.TrimRight(want, "\n")) {
			t.Errorf("%s: err=%v\n got %s\nwant %s", filepath.Base(in), err, got, want)
		}
	}
}

// TestSchemaFileInSync keeps bundles/transfer_v1/intent.schema.json aligned
// with the Go rules (key sets and patterns).
func TestSchemaFileInSync(t *testing.T) {
	raw, err := os.ReadFile("../../bundles/transfer_v1/intent.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Required             []string `json:"required"`
		AdditionalProperties *bool    `json:"additionalProperties"`
		Properties           map[string]struct {
			Pattern              string   `json:"pattern"`
			Const                any      `json:"const"`
			Required             []string `json:"required"`
			AdditionalProperties *bool    `json:"additionalProperties"`
			Properties           map[string]struct {
				Pattern string `json:"pattern"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	sorted := func(a []string) []string { b := append([]string(nil), a...); sort.Strings(b); return b }
	if strings.Join(sorted(s.Required), ",") != strings.Join(sorted(RequiredKeys), ",") {
		t.Errorf("required: schema %v, Go %v", s.Required, RequiredKeys)
	}
	if s.AdditionalProperties == nil || *s.AdditionalProperties {
		t.Error("schema must set additionalProperties:false at the top level")
	}
	var props []string
	for k := range s.Properties {
		props = append(props, k)
	}
	if strings.Join(sorted(props), ",") != strings.Join(sorted(append(append([]string{}, RequiredKeys...), OptionalKeys...)), ",") {
		t.Errorf("properties: schema %v", sorted(props))
	}
	for field, want := range map[string]string{
		"iss": PatternIss, "kid": PatternKid, "aud": PatternAud, "bundle": PatternBundle, "nonce": PatternNonce,
	} {
		if got := s.Properties[field].Pattern; got != want {
			t.Errorf("%s pattern: schema %q, Go %q", field, got, want)
		}
	}
	a := s.Properties["args"]
	if strings.Join(sorted(a.Required), ",") != strings.Join(sorted(RequiredArgsKeys), ",") {
		t.Errorf("args.required: schema %v", a.Required)
	}
	if a.AdditionalProperties == nil || *a.AdditionalProperties {
		t.Error("schema must set additionalProperties:false on args")
	}
	for field, want := range map[string]string{
		"amount": PatternAmount, "currency": PatternCurrency, "from": PatternAccount, "to": PatternAccount,
	} {
		if got := a.Properties[field].Pattern; got != want {
			t.Errorf("args.%s pattern: schema %q, Go %q", field, got, want)
		}
	}
	if s.Properties["v"].Const != Type || s.Properties["op"].Const != OpTransfer || s.Properties["op_v"].Const != float64(OpVersion) {
		t.Errorf("const values drifted: v=%v op=%v op_v=%v", s.Properties["v"].Const, s.Properties["op"].Const, s.Properties["op_v"].Const)
	}
}

func TestHash(t *testing.T) {
	h := Hash([]byte(golden))
	h2 := Hash([]byte(golden + " "))
	if h == h2 || h == [32]byte{} {
		t.Fatal("hash must depend on the exact bytes")
	}
}

// FuzzParse checks that no input makes the parser panic and that anything
// Parse accepts is canonical and parses again to the same intent.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		golden, `{"a":1,"a":2}`, `{"n":9007199254740993}`, `{"s":"\ud800"}`, "{\"s\":\"\xff\"}",
		`{"n":1.5}`, `[[[[[[]]]]]]`, `"` + u("d83d") + u("de00") + `"`, `{"a":1,"` + u("0061") + `":2}`, ``, `{`, `-`, `"\u`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = ParseStrict(b)
		in, err := Parse(b)
		if err != nil {
			if CodeOf(err) == "" {
				t.Fatalf("untyped error: %v", err)
			}
			return
		}
		if CheckCanonical(b) != nil {
			t.Fatal("Parse accepted non-canonical bytes")
		}
		in2, err := Parse(b)
		if err != nil || in2.Nonce != in.Nonce || in2.Args != in.Args {
			t.Fatal("re-parse differs")
		}
	})
}
