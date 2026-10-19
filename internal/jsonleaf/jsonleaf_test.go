package jsonleaf

import (
	"reflect"
	"testing"
)

var key = []byte("test key, not a secret")

func leaves(t *testing.T, s string) Leaves {
	t.Helper()
	l, err := Parse(key, []byte(s))
	if err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return l
}

func TestCanonicalNumbers(t *testing.T) {
	a, b, c := leaves(t, `{"v":72}`), leaves(t, `{"v":72.0}`), leaves(t, `{"v":7.2e1}`)
	if a["/v"] != b["/v"] || a["/v"] != c["/v"] {
		t.Fatalf("72, 72.0 and 7.2e1 differ: %v %v %v", a, b, c)
	}
	if leaves(t, `{"v":"72"}`)["/v"] == a["/v"] {
		t.Fatal("the string 72 equals the number 72")
	}
	if leaves(t, `{"v":9007199254740993}`)["/v"] == leaves(t, `{"v":9007199254740992}`)["/v"] {
		t.Fatal("2^53+1 rounded")
	}
}

func TestKeyMatters(t *testing.T) {
	a := leaves(t, `{"status":"final"}`)
	b, _ := Parse([]byte("another key"), []byte(`{"status":"final"}`))
	if a["/status"] == b["/status"] {
		t.Fatal("the key doesn't change the digest")
	}
	if Digest(key, "/a", "s:x") == Digest(key, "/b", "s:x") {
		t.Fatal("the pointer doesn't change the digest")
	}
}

func TestDiff(t *testing.T) {
	sent := `{"resourceType":"Observation","status":"final","code":{"coding":[{"system":"http://loinc.org","code":"8867-4"}]},"valueQuantity":{"value":72}}`
	cases := []struct {
		name, got string
		want      []string
	}{
		{"same", sent, nil},
		{"server added a field", `{"resourceType":"Observation","status":"final","issued":"2026-10-19","code":{"coding":[{"system":"http://loinc.org","code":"8867-4"}]},"valueQuantity":{"value":72.0}}`, nil},
		{"changed", `{"resourceType":"Observation","status":"preliminary","code":{"coding":[{"system":"http://loinc.org","code":"8867-4"}]},"valueQuantity":{"value":72}}`, []string{"status"}},
		{"missing", `{"resourceType":"Observation","code":{"coding":[{"system":"http://loinc.org","code":"8867-4"}]},"valueQuantity":{"value":72}}`, []string{"status"}},
		{"array element changed", `{"resourceType":"Observation","status":"final","code":{"coding":[{"system":"http://loinc.org","code":"0000-0"}]},"valueQuantity":{"value":72}}`, []string{"code.coding"}},
		{"array element added", `{"resourceType":"Observation","status":"final","code":{"coding":[{"system":"http://loinc.org","code":"8867-4"},{"code":"x"}]},"valueQuantity":{"value":72}}`, []string{"code.coding"}},
		{"field added inside an array element", `{"resourceType":"Observation","status":"final","code":{"coding":[{"system":"http://loinc.org","code":"8867-4","display":"HR"}]},"valueQuantity":{"value":72}}`, []string{"code.coding"}},
		{"nested value", `{"resourceType":"Observation","status":"final","code":{"coding":[{"system":"http://loinc.org","code":"8867-4"}]},"valueQuantity":{"value":73}}`, []string{"valueQuantity.value"}},
	}
	for _, c := range cases {
		if got := Diff(leaves(t, sent), leaves(t, c.got)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLimitsAndEscapes(t *testing.T) {
	big := []byte(`[`)
	for i := 0; i < MaxLeaves+2; i++ {
		if i > 0 {
			big = append(big, ',')
		}
		big = append(big, '1')
	}
	big = append(big, ']')
	if _, err := Parse(key, big); err != ErrTooLarge {
		t.Fatalf("a %d-leaf array: %v", MaxLeaves+2, err)
	}
	l := leaves(t, `{"a/b":{"c~d":1}}`)
	if _, ok := l["/a~1b/c~0d"]; !ok {
		t.Fatalf("escapes: %v", l)
	}
	if got := Segments("/a~1b/c~0d"); !reflect.DeepEqual(got, []string{"a/b", "c~d"}) {
		t.Fatalf("segments: %v", got)
	}
	if _, err := Parse(key, []byte(`{} {}`)); err == nil {
		t.Fatal("trailing data accepted")
	}
	if got := leaves(t, `{"a":{"b":1},"c":2}`).Without("/a"); len(got) != 1 {
		t.Fatalf("without: %v", got)
	}
}
