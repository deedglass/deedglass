package deed_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

// seedDeeds adds every deed vector, and truncations of one, to the corpus.
func seedDeeds(f *testing.F) {
	_ = filepath.WalkDir(vecRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(p, ".json") || strings.HasSuffix(p, ".jsonl")) || strings.Contains(p, "expected") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, l := range strings.Split(string(b), "\n") {
			if l != "" {
				f.Add([]byte(l))
			}
		}
		return nil
	})
	_, raw := base(f)
	for i := 0; i < len(raw); i += 37 {
		f.Add(raw[:i])
	}
}

// FuzzParseEnvelope: never panics; an error is always a *VerifyError.
func FuzzParseEnvelope(f *testing.F) {
	seedDeeds(f)
	f.Fuzz(func(t *testing.T, b []byte) {
		env, err := deed.ParseEnvelope(b)
		if err != nil {
			if deed.CodeOf(err) == "" {
				t.Fatalf("not a *VerifyError: %v", err)
			}
			return
		}
		if env.ID != env.Body.DeedID {
			t.Fatal("parsed an envelope whose id differs from its body")
		}
	})
}

// FuzzVerifyEnvelope: never panics, always a *VerifyError or a result, never both.
func FuzzVerifyEnvelope(f *testing.F) {
	seedDeeds(f)
	ks := deedtest.KeySet()
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := deed.VerifyEnvelope(b, ks, deed.VerifyOptions{})
		switch {
		case err == nil && r == nil, err != nil && r != nil:
			t.Fatal("exactly one of result and error")
		case err != nil && deed.CodeOf(err) == "":
			t.Fatalf("not a *VerifyError: %v", err)
		}
	})
}

// FuzzParseKeySet: never panics; every accepted key passes keys.ParsePublicKey.
func FuzzParseKeySet(f *testing.F) {
	f.Add(keySetJSON(f, deedtest.KeySet()))
	if b, err := os.ReadFile(filepath.Join(vecRoot, "invalid/small-order-key-in-set/keys.json")); err == nil {
		f.Add(b)
	}
	f.Add([]byte(`{"object":"seal_key_set","issuer":"x","updated_at":"2026-10-02T09:00:00.000Z","keys":[]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		ks, err := deed.ParseKeySet(b)
		if err != nil {
			if deed.CodeOf(err) == "" {
				t.Fatalf("not a *VerifyError: %v", err)
			}
			return
		}
		for _, k := range ks.Keys {
			if _, _, ok := ks.Lookup(k.ID); !ok {
				t.Fatal("lookup")
			}
		}
	})
}

// FuzzVerifyCOSE: hostile CBOR never panics the COSE path (go-cose + fxamacker/cbor).
func FuzzVerifyCOSE(f *testing.F) {
	if b, err := os.ReadFile(filepath.Join(vecRoot, "cose/01-action-verified-l2.cose")); err == nil {
		f.Add(b)
		for i := 0; i < len(b); i += 61 {
			f.Add(b[:i])
		}
	}
	f.Add([]byte{0xd2, 0x84, 0x40, 0xa0, 0xf6, 0x40})
	ks := deedtest.KeySet()
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := deed.VerifyCOSE(b, ks)
		if (r == nil) == (err == nil) {
			t.Fatal("exactly one of result and error")
		}
	})
}
