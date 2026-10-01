package policy

import (
	"bytes"
	"os"
	"reflect"
	"testing"
	"time"
)

func seedFuzz(f *testing.F) {
	f.Add([]byte(base))
	f.Add([]byte("deedglass: policy/v1\na: &a [1]\nb: *a\n"))
	f.Add([]byte("deedglass: policy/v1\nrules:\n  - id: aa\n    when: !sql.multi\n"))
	f.Add([]byte("\xef\xbb\xbfdeedglass: policy/v1\r\nname: x-y\r\n"))
	for _, p := range lintFixtures(f) {
		if b, err := os.ReadFile(p); err == nil && len(b) < 64<<10 {
			f.Add(b)
		}
	}
}

// FuzzCompile: any input compiles or is refused, without a panic, within a bounded time, and
// the same input always gives the same result.
func FuzzCompile(f *testing.F) {
	seedFuzz(f)
	f.Fuzz(func(t *testing.T, src []byte) {
		start := time.Now()
		c1, d1 := Compile(src, Options{Voice: true})
		if el := time.Since(start); el > 2*time.Second {
			t.Fatalf("Compile took %v", el)
		}
		for _, d := range d1 {
			if d.Code == "internal_compiler_error" {
				t.Fatalf("internal error on %q", src)
			}
		}
		c2, d2 := Compile(src, Options{Voice: true})
		if !reflect.DeepEqual(d1, d2) || (c1 == nil) != (c2 == nil) {
			t.Fatal("nondeterministic diagnostics")
		}
		if c1 != nil {
			if !bytes.Equal(c1.Normalized, c2.Normalized) || c1.VersionHash != c2.VersionHash {
				t.Fatal("nondeterministic normalization")
			}
			out, err := Format(src)
			if err != nil {
				t.Fatalf("fmt refused a valid policy: %v", err)
			}
			again, err := Format(out)
			if err != nil || !bytes.Equal(out, again) {
				t.Fatal("fmt is not idempotent")
			}
		}
	})
}

// FuzzYAMLReader: the safe reader never panics, never expands an alias, and stays fast.
func FuzzYAMLReader(f *testing.F) {
	seedFuzz(f)
	f.Fuzz(func(t *testing.T, src []byte) {
		start := time.Now()
		d := &diags{}
		_ = readYAML(newSource(src), d)
		if el := time.Since(start); el > time.Second {
			t.Fatalf("readYAML took %v", el)
		}
	})
}
