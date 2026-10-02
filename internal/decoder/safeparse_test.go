package decoder

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pglogrepl"
)

// allCorpus is the synthetic corpus plus every captured message.
func allCorpus(t testing.TB) map[string][]byte {
	out := map[string][]byte{}
	for i, b := range syntheticCorpus() {
		out[fmt.Sprintf("synthetic-%02d-%c", i, b[0])] = b
	}
	for k, v := range capturedCorpus(t) {
		out["captured-"+k] = v
	}
	return out
}

// parseRaw is pglogrepl.Parse with its panics turned into errors.
func parseRaw(b []byte) (m pglogrepl.Message, err error, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked, err = true, fmt.Errorf("panic: %v", r)
		}
	}()
	m, err = pglogrepl.Parse(b)
	return
}

// TestSafeParseMatchesParse: every valid message parses to exactly what pglogrepl.Parse gives.
func TestSafeParseMatchesParse(t *testing.T) {
	types := map[byte]int{}
	for name, b := range allCorpus(t) {
		got, err := SafeParse(b)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		want, werr, _ := parseRaw(b)
		if werr != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: SafeParse and Parse differ: %#v vs %#v (%v)", name, got, want, werr)
		}
		types[b[0]]++
	}
	for _, c := range "BCORYIUDTM" {
		if types[byte(c)] == 0 {
			t.Errorf("no message of type %c in the corpus", c)
		}
	}
}

// TestSafeParsePrefixes: every strict prefix of every message is refused, with no panic and no
// silent accept (spike S3 baseline on raw Parse: 55,510 panics and 1,854 accepted 'M' prefixes).
func TestSafeParsePrefixes(t *testing.T) {
	var prefixes, panics, accepted, rawPanics, rawAccepted int
	for name, b := range allCorpus(t) {
		for n := 0; n < len(b); n++ {
			prefixes++
			func() {
				defer func() {
					if r := recover(); r != nil {
						panics++
						t.Errorf("%s[:%d]: SafeParse panicked: %v", name, n, r)
					}
				}()
				if _, err := SafeParse(b[:n]); err == nil {
					accepted++
					t.Errorf("%s[:%d]: a truncated message was accepted", name, n)
				} else {
					var me *MalformedError
					if !errors.As(err, &me) {
						t.Errorf("%s[:%d]: %T, want *MalformedError", name, n, err)
					}
				}
			}()
			if n > 0 {
				if _, err, p := parseRaw(b[:n]); p {
					rawPanics++
				} else if err == nil {
					rawAccepted++
				}
			}
		}
	}
	t.Logf("%d prefixes: %d panics, %d accepted (raw pglogrepl.Parse on the same prefixes: %d panics, %d accepted)",
		prefixes, panics, accepted, rawPanics, rawAccepted)
	if len(capturedCorpus(t)) > 0 && prefixes < 50_000 {
		t.Errorf("only %d prefixes; the captured corpus should give at least 50,000", prefixes)
	}
}

func TestSafeParseRefusals(t *testing.T) {
	ok := syntheticCorpus()
	commit := ok[1]
	cases := []struct {
		name, reason string
		b            []byte
	}{
		{"empty", "empty", nil},
		{"M declares 100, has 90", "declared_length", func() []byte {
			b := encMessage(true, 1, "deedglass", make([]byte, 100))
			return b[:len(b)-10]
		}()},
		{"M declares 90, has 100", "declared_length", func() []byte {
			b := encMessage(true, 1, "deedglass", make([]byte, 90))
			return append(b, make([]byte, 10)...)
		}()},
		{"trailing byte after C", "trailing_bytes", append(append([]byte{}, commit...), 0)},
		{"trailing byte after B", "trailing_bytes", append(encBegin(1, t0, 1), 0)},
		{"commit flags", "commit_flags", func() []byte { b := append([]byte{}, commit...); b[1] = 1; return b }()},
		{"stream start", "unexpected_message_type", []byte{'S', 0, 0, 0, 1, 1}},
		{"stream stop", "unexpected_message_type", []byte{'E'}},
		{"prepare", "unexpected_message_type", []byte{'P', 0}},
		{"binary message type", "unexpected_message_type", []byte{'b', 0}},
		{"tuple kind b", "binary_tuple_data", func() []byte {
			b := []byte{'I'}
			b = be32(b, 1)
			b = append(b, 'N')
			b = be16(b, 1)
			b = append(b, 'b')
			b = be32(b, 1)
			return append(b, 'x')
		}()},
		{"unknown tuple kind", "unknown_tuple_kind", func() []byte {
			b := []byte{'I'}
			b = be32(b, 1)
			b = append(b, 'N', 0, 1, 'z')
			return b
		}()},
		{"insert without N", "tuple_marker", func() []byte { b := encInsert(1, []wval{tv("1")}); b[5] = 'K'; return b }()},
		{"update K then not N", "tuple_marker", func() []byte {
			b := encUpdate(1, 'K', []wval{tv("1")}, []wval{tv("1")})
			b[1+4+1+2+1+4+1] = 'X' // the byte after the K tuple
			return b
		}()},
		{"delete N", "tuple_marker", func() []byte { b := encDelete(1, 'O', []wval{tv("1")}); b[5] = 'N'; return b }()},
		{"replica identity", "replica_identity", encRelation(1, "a", "b", 'x', nil)},
		{"column flags", "column_flags", func() []byte {
			b := encRelation(1, "a", "b", 'f', []wcol{{"c", 23, true, -1}})
			b[1+4+2+2+1+2] = 7
			return b
		}()},
		{"message flags", "message_flags", func() []byte { b := encMessage(true, 1, "p", nil); b[1] = 2; return b }()},
		{"text length past the end", "declared_length", func() []byte {
			b := []byte{'I'}
			b = be32(b, 1)
			b = append(b, 'N')
			b = be16(b, 1)
			b = append(b, 't')
			b = be32(b, 1<<30)
			return b
		}()},
		{"truncate count past the end", "declared_length", func() []byte { b := []byte{'T'}; b = be32(b, 1<<31); return append(b, 0) }()},
		{"unterminated string", "unterminated_string", []byte{'Y', 0, 0, 0, 1, 'a', 'b'}},
	}
	for _, c := range cases {
		_, err := SafeParse(c.b)
		var me *MalformedError
		if !errors.As(err, &me) || me.Reason != c.reason {
			t.Errorf("%s: %v, want %s", c.name, err, c.reason)
		}
	}
}

// FuzzSafeParse: never panics; anything it accepts parses the same as pglogrepl.Parse and has no
// strict prefix that it also accepts.
func FuzzSafeParse(f *testing.F) {
	for _, b := range allCorpus(f) {
		f.Add(b)
		for n := 0; n < len(b); n += max(1, len(b)/16) {
			f.Add(b[:n])
		}
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := SafeParse(b)
		if err != nil {
			return
		}
		want, werr, p := parseRaw(b)
		if p || werr != nil || !reflect.DeepEqual(m, want) {
			t.Fatalf("accepted %x but Parse says %v", b, werr)
		}
		if len(b) > 1 {
			if _, err := SafeParse(b[:len(b)-1]); err == nil {
				t.Fatalf("both %x and its prefix were accepted", b)
			}
		}
	})
}

func BenchmarkSafeParse(b *testing.B) {
	msg := syntheticCorpus()[6]
	b.ReportAllocs()
	for b.Loop() {
		_, _ = SafeParse(msg)
	}
}

func BenchmarkParse(b *testing.B) {
	msg := syntheticCorpus()[6]
	b.ReportAllocs()
	for b.Loop() {
		_, _ = pglogrepl.Parse(msg)
	}
}
