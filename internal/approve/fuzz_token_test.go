package approve

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// FuzzApprovalToken (S34): the link token check. A token is well formed exactly when it is 43
// strict base64url characters that decode to 32 bytes; every input hashes to 32 bytes; two
// different inputs never share a hash; and nothing panics.
func FuzzApprovalToken(f *testing.F) {
	raw, _, _ := newLinkToken()
	for _, s := range []string{raw, "", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAB",
		raw + "=", raw[:42], "+/" + raw[2:], "‮" + raw[3:], raw[:42] + "\x00"} {
		f.Add(s, raw)
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		ok := wellFormed(a)
		dec, err := base64.RawURLEncoding.Strict().DecodeString(a)
		want := len(a) == tokenLen && err == nil && len(dec) == tokenBytes
		if ok != want {
			t.Fatalf("wellFormed(%q) = %v, want %v", a, ok, want)
		}
		if ok && base64.RawURLEncoding.EncodeToString(dec) != a {
			t.Fatalf("%q is not canonical but was accepted", a)
		}
		ha, hb := hashLink(a), hashLink(b)
		if len(ha) != 32 {
			t.Fatalf("hash length %d", len(ha))
		}
		if a != b && bytes.Equal(ha, hb) {
			t.Fatalf("hash collision %q %q", a, b)
		}
	})
}
