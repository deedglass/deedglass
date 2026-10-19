package decoder

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// The frame digest (S20 §3.6) is sha256 over JCS(change) + "\n" for every change in stream order,
// including changes past the image cap and truncates. The digested form of a change is what the
// stream said, nothing derived: {"new":…,"old":…,"op":…,"seq":…,"table":…} with sensitive values
// already digested; the key and changed columns are left out because they are computed from these
// (and the key needs a catalog lookup after the peek). canon.go writes RFC 8785 JCS directly, so
// the assembler keeps its pace; TestCanonMatchesJCS checks it against gowebpki/jcs.

// digester accumulates a frame's digest.
type digester struct {
	h   hash.Hash
	buf []byte
}

func newDigester() *digester { return &digester{h: sha256.New()} }

func (d *digester) add(seq int, op, table string, old, nw map[string]Value) {
	d.buf = appendChangeJCS(d.buf[:0], seq, op, table, old, nw)
	d.buf = append(d.buf, '\n')
	d.h.Write(d.buf)
}

func (d *digester) sum() string { return "sha256:" + hex.EncodeToString(d.h.Sum(nil)) }

// appendChangeJCS appends the canonical form of one change.
func appendChangeJCS(b []byte, seq int, op, table string, old, nw map[string]Value) []byte {
	b = append(b, '{')
	first := true
	field := func(k string) {
		if !first {
			b = append(b, ',')
		}
		first = false
		b = appendJCSString(b, k)
		b = append(b, ':')
	}
	if nw != nil {
		field("new")
		b = appendImageJCS(b, nw)
	}
	if old != nil {
		field("old")
		b = appendImageJCS(b, old)
	}
	field("op")
	b = appendJCSString(b, op)
	field("seq")
	b = strconv.AppendInt(b, int64(seq), 10)
	field("table")
	b = appendJCSString(b, table)
	return append(b, '}')
}

func appendImageJCS(b []byte, img map[string]Value) []byte {
	keys := make([]string, 0, len(img))
	for k := range img {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, compareUTF16)
	b = append(b, '{')
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendJCSString(b, k)
		b = append(b, ':')
		b = appendValueJCS(b, img[k])
	}
	return append(b, '}')
}

// appendValueJCS writes a Value with its keys in order d, k, n, t, u, x (omitted when empty, as
// the JSON tags do).
func appendValueJCS(b []byte, v Value) []byte {
	b = append(b, '{')
	n := 0
	sep := func() {
		if n > 0 {
			b = append(b, ',')
		}
		n++
	}
	if v.Digest != "" {
		sep()
		b = append(b, `"d":`...)
		b = appendJCSString(b, v.Digest)
	}
	if len(v.Leaves) > 0 {
		sep()
		b = append(b, `"k":{`...)
		keys := make([]string, 0, len(v.Leaves))
		for k := range v.Leaves {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, compareUTF16)
		for i, k := range keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendJCSString(b, k)
			b = append(b, ':')
			b = appendJCSString(b, v.Leaves[k])
		}
		b = append(b, '}')
	}
	if v.Null {
		sep()
		b = append(b, `"n":true`...)
	}
	if v.Text != nil {
		sep()
		b = append(b, `"t":`...)
		b = appendJCSString(b, *v.Text)
	}
	if v.Unchanged {
		sep()
		b = append(b, `"u":true`...)
	}
	if v.Hex != "" {
		sep()
		b = append(b, `"x":`...)
		b = appendJCSString(b, v.Hex)
	}
	return append(b, '}')
}

const lowerHex = "0123456789abcdef"

// appendJCSString writes s as RFC 8785 (ECMAScript JSON.stringify) does. s must be valid UTF-8
// (values that aren't are stored as hex before they get here).
func appendJCSString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x80 {
			_, size := utf8.DecodeRuneInString(s[i:])
			b = append(b, s[i:i+size]...)
			i += size
			continue
		}
		switch c {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			if c < 0x20 {
				b = append(b, '\\', 'u', '0', '0', lowerHex[c>>4], lowerHex[c&0xf])
			} else {
				b = append(b, c)
			}
		}
		i++
	}
	return append(b, '"')
}

// compareUTF16 orders strings by their UTF-16 code units (RFC 8785 §3.2.3).
func compareUTF16(a, b string) int {
	if isASCII(a) && isASCII(b) {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	}
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			if ua[i] < ub[i] {
				return -1
			}
			return 1
		}
	}
	return len(ua) - len(ub)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
