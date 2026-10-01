package attack

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
	"github.com/deedglass/deedglass/internal/keys"
)

// Library-level implementations of A13, F01 and F02 (S04). S05 adds the CLI-level F01–F04
// through deedglass verify. Expected outcomes are in attack_meta.json and never change here.

// A13 · Small-order public key offered at registration → BLOCKED (registration refuses the key).
func TestA13SmallOrderSealKey(t *testing.T) {
	for _, k := range deedtest.SmallOrderKeys {
		pub := k.PublicKey()
		// Why it matters: the stdlib accepts the identity key with the all-zero-S signature.
		if k.Name == "identity (order 1)" {
			sig := make([]byte, 64)
			sig[0] = 1
			if !ed25519.Verify(pub, []byte("any deed at all"), sig) {
				t.Fatal("expected Go's crypto/ed25519 to accept the identity-key forgery (spike S4)")
			}
		}
		if _, err := keys.ParsePublicKey(pub); err == nil {
			t.Errorf("%s: keys.ParsePublicKey accepted it", k.Name)
		}
		// A key file carrying it is refused before registration.
		kf, _, err := deed.NewKeyFile(deedtest.ID("sk", 70), bytes.Repeat([]byte{7}, 32), deedtest.T0)
		if err != nil {
			t.Fatal(err)
		}
		kf.PublicKey = base64.RawURLEncoding.EncodeToString(pub)
		p := filepath.Join(t.TempDir(), "k.json")
		b, _ := json.Marshal(kf)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		var kfe *deed.KeyFileError
		if _, err := deed.LoadKeyFile(p, deed.LoadOptions{}); !errors.As(err, &kfe) || kfe.Problem != deed.KeyFileUnsafe {
			t.Errorf("%s: LoadKeyFile: %v", k.Name, err)
		}
		// A key set that lists it is refused whole.
		recs := deedtest.KeyRecords()
		recs[2].PublicKey = pub
		if _, err := deed.NewKeySet(deedtest.Issuer, recs); deed.CodeOf(err) != deed.CodeSealKeyRejected {
			t.Errorf("%s: NewKeySet: %v", k.Name, err)
		}
	}
	// The published vector: a key set containing the identity point.
	raw, err := os.ReadFile("../testdata/deeds/invalid/small-order-key-in-set/keys.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deed.ParseKeySet(raw); deed.CodeOf(err) != deed.CodeSealKeyRejected {
		t.Fatalf("vector key set: %v", err)
	}
	t.Log("A13: BLOCKED (registration refuses the key)")
}

// F01 · Deed sealed by the wrong key → DETECTED.
func TestF01WrongKey(t *testing.T) {
	raw, err := os.ReadFile("../testdata/deeds/invalid/wrong-key-known-id/deed.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deed.VerifyEnvelope(raw, deedtest.KeySet(), deed.VerifyOptions{}); deed.CodeOf(err) != deed.CodeSealInvalid {
		t.Fatalf("got %v, want seal_invalid", err)
	}
	t.Log("F01: DETECTED by verify (seal_invalid)")
}

// F02 · A byte flipped anywhere in a deed → DETECTED, at every position.
func TestF02ByteFlipped(t *testing.T) {
	raw, err := os.ReadFile("../testdata/deeds/valid/01-action-verified-l2.json")
	if err != nil {
		t.Fatal(err)
	}
	ks := deedtest.KeySet()
	if _, err := deed.VerifyEnvelope(raw, ks, deed.VerifyOptions{}); err != nil {
		t.Fatalf("control: %v", err)
	}
	codes := map[deed.Code]int{}
	for i := range raw {
		for _, bit := range []byte{0x01, 0x20} {
			b := bytes.Clone(raw)
			b[i] ^= bit
			_, err := deed.VerifyEnvelope(b, ks, deed.VerifyOptions{})
			if err == nil {
				t.Fatalf("byte %d xor %#x verified: %q", i, bit, b[max(0, i-20):min(len(b), i+20)])
			}
			codes[deed.CodeOf(err)]++
		}
	}
	t.Logf("F02: DETECTED at all %d positions × 2 flips: %v", len(raw), codes)
}
