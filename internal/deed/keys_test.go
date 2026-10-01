package deed_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
	"github.com/deedglass/deedglass/internal/keys"
)

func keySetJSON(t testing.TB, ks *deed.KeySet) []byte {
	t.Helper()
	b, err := json.Marshal(ks)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKeySetRoundTrip(t *testing.T) {
	ks := deedtest.KeySet()
	got, err := deed.ParseKeySet(keySetJSON(t, ks))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Keys) != 3 || got.Keys[0].Status != deed.KeyRevoked || got.Keys[2].Status != deed.KeyActive {
		t.Fatalf("keys not sorted by activated_at: %+v", got.Keys)
	}
	if got.UpdatedAt != deed.FormatTime(deedtest.ARetired) {
		t.Fatalf("updated_at %s", got.UpdatedAt)
	}
	for _, k := range got.Keys {
		if _, pub, ok := got.Lookup(k.ID); !ok || deed.Thumbprint(pub) != k.Thumbprint || k.JWK.X != k.PublicKey {
			t.Fatalf("lookup %s", k.ID)
		}
	}
	if !strings.Contains(string(keySetJSON(t, ks)), `"jwk":{"kty":"OKP","crv":"Ed25519","x":`) || strings.Contains(string(keySetJSON(t, ks)), `"alg":"EdDSA"`) {
		t.Fatal("jwk shape")
	}
}

// A13: every small-order or non-canonical encoding is refused wherever a key enters.
func TestSmallOrderKeysRefused(t *testing.T) {
	for _, k := range deedtest.SmallOrderKeys {
		t.Run(k.Name, func(t *testing.T) {
			if _, err := keys.ParsePublicKey(k.PublicKey()); err == nil {
				t.Fatal("keys.ParsePublicKey accepted it")
			}
			// In a key set.
			recs := deedtest.KeyRecords()
			ks, _ := deed.NewKeySet(deedtest.Issuer, recs[1:2])
			raw := keySetJSON(t, ks)
			x := base64.RawURLEncoding.EncodeToString(k.PublicKey())
			raw = bytes.ReplaceAll(raw, []byte(ks.Keys[0].PublicKey), []byte(x))
			raw = bytes.Replace(raw, []byte(ks.Keys[0].Thumbprint), []byte(deed.Thumbprint(k.PublicKey())), 1)
			_, err := deed.ParseKeySet(raw)
			var ve *deed.VerifyError
			if !errors.As(err, &ve) || ve.Code != deed.CodeSealKeyRejected || ve.Key != ks.Keys[0].ID {
				t.Fatalf("ParseKeySet: %v", err)
			}
			// As a server record.
			recs[1].PublicKey = k.PublicKey()
			if _, err := deed.NewKeySet(deedtest.Issuer, recs); deed.CodeOf(err) != deed.CodeSealKeyRejected {
				t.Fatalf("NewKeySet: %v", err)
			}
			// In a key file.
			dir := t.TempDir()
			kf, _, _ := deed.NewKeyFile(deedtest.ID("sk", 7), bytes.Repeat([]byte{7}, 32), deedtest.T0)
			kf.PublicKey = x
			path := writeKF(t, dir, kf, 0o600)
			var kfe *deed.KeyFileError
			if _, err := deed.LoadKeyFile(path, deed.LoadOptions{}); !errors.As(err, &kfe) || kfe.Problem != deed.KeyFileUnsafe {
				t.Fatalf("LoadKeyFile: %v", err)
			}
		})
	}
	if len(deedtest.SmallOrderKeys) != 12 {
		t.Fatal("the 8 torsion points and 4 non-canonical aliases")
	}
}

func TestKeySetRefusals(t *testing.T) {
	ks := deedtest.KeySet()
	raw := string(keySetJSON(t, ks))
	k0 := ks.Keys[0]
	dup := strings.Replace(raw, `"keys":[`, `"keys":[`+string(mustJSON(t, k0))+`,`, 1)
	cases := map[string]struct {
		raw  string
		code deed.Code
	}{
		"duplicate id":       {dup, deed.CodeSealKeyRejected},
		"unknown alg":        {strings.Replace(raw, `"alg":"Ed25519"`, `"alg":"EdDSA"`, 1), deed.CodeSealKeyRejected},
		"thumbprint":         {strings.Replace(raw, k0.Thumbprint, deed.Digest([]byte("x")), 1), deed.CodeSealKeyRejected},
		"jwk x":              {strings.Replace(raw, `"x":"`+k0.PublicKey, `"x":"`+ks.Keys[1].PublicKey, 1), deed.CodeSealKeyRejected},
		"status vs times":    {strings.Replace(raw, `"status":"revoked"`, `"status":"active"`, 1), deed.CodeSealKeyRejected},
		"short key":          {strings.Replace(raw, `"public_key":"`+k0.PublicKey, `"public_key":"AAAA`, 1), deed.CodeSealKeyRejected},
		"unknown member":     {strings.Replace(raw, `"issuer":`, `"extra":1,"issuer":`, 1), deed.CodeSchemaInvalid},
		"wrong object":       {strings.Replace(raw, `"seal_key_set"`, `"key_set"`, 1), deed.CodeSchemaInvalid},
		"not json":           {raw[:20], deed.CodeMalformed},
		"duplicate member":   {strings.Replace(raw, `"issuer":`, `"issuer":"x","issuer":`, 1), deed.CodeMalformed},
		"bad activated time": {strings.Replace(raw, k0.ActivatedAt, "2026-10-02", 1), deed.CodeSchemaInvalid},
	}
	for name, tc := range cases {
		if _, err := deed.ParseKeySet([]byte(tc.raw)); deed.CodeOf(err) != tc.code {
			t.Errorf("%s: got %v, want %s", name, err, tc.code)
		}
	}
}

func mustJSON(t testing.TB, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeKF(t testing.TB, dir string, kf deed.KeyFile, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, kf.ID+".json")
	if err := os.WriteFile(p, mustJSON(t, kf), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKeyFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	id := deedtest.ID("sk", 20)
	kf, s, err := deed.NewKeyFile(id, bytes.Repeat([]byte{9}, 32), deedtest.T0)
	if err != nil {
		t.Fatal(err)
	}
	path, err := deed.WriteKeyFile(dir, kf)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", fi.Mode())
	}
	if _, err := deed.WriteKeyFile(dir, kf); err == nil {
		t.Fatal("overwrote an existing key file")
	}
	got, err := deed.LoadKeyFile(path, deed.LoadOptions{Prod: true})
	if err != nil || got.KeyID() != id || !got.PublicKey().Equal(s.PublicKey()) {
		t.Fatalf("load: %v", err)
	}
	for _, v := range []string{kf.String(), kf.GoString(), got.String(), got.GoString()} {
		if strings.Contains(v, kf.Seed) {
			t.Fatal("seed printed")
		}
	}

	var kfe *deed.KeyFileError
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := deed.LoadKeyFile(path, deed.LoadOptions{}); !errors.As(err, &kfe) || kfe.Problem != deed.KeyFileTooOpen || kfe.Mode.Perm() != 0o644 {
		t.Fatalf("0644: %v", err)
	}
	if !strings.Contains(err2s(deed.LoadKeyFile(path, deed.LoadOptions{})), "(mode 0644)") {
		t.Fatal("mode in message")
	}

	other, _, _ := deed.NewKeyFile(id, bytes.Repeat([]byte{8}, 32), deedtest.T0)
	mismatch := kf
	mismatch.PublicKey = other.PublicKey
	p2 := writeKF(t, t.TempDir(), mismatch, 0o600)
	if _, err := deed.LoadKeyFile(p2, deed.LoadOptions{}); !errors.As(err, &kfe) || kfe.Problem != deed.KeyFileMismatch {
		t.Fatalf("mismatch: %v", err)
	}

	// Test-vector keys load in tests (dev) and never in prod.
	a := deedtest.TestKey("a")
	tk, _, _ := deed.NewKeyFile(a.ID, a.Seed, deedtest.T0)
	p3 := writeKF(t, t.TempDir(), tk, 0o644) // the test-key refusal comes before the mode check
	if _, err := deed.LoadKeyFile(p3, deed.LoadOptions{Prod: true}); !errors.As(err, &kfe) || kfe.Problem != deed.KeyFileTestKey {
		t.Fatalf("test key in prod: %v", err)
	}
	_ = os.Chmod(p3, 0o600)
	if _, err := deed.LoadKeyFile(p3, deed.LoadOptions{}); err != nil {
		t.Fatalf("test key in tests: %v", err)
	}

	for name, content := range map[string]string{
		"not json":      "{",
		"wrong object":  `{"object":"x","v":1,"id":"` + id + `","alg":"Ed25519","seed":"","public_key":"","created_at":""}`,
		"extra member":  strings.TrimSuffix(string(mustJSON(t, kf)), "}") + `,"note":1}`,
		"bad seed":      strings.Replace(string(mustJSON(t, kf)), kf.Seed, "AAAA", 1),
		"bad id":        strings.Replace(string(mustJSON(t, kf)), id, "sk_x", 1),
		"wrong alg":     strings.Replace(string(mustJSON(t, kf)), `"Ed25519"`, `"EdDSA"`, 1),
		"missing field": strings.Replace(string(mustJSON(t, kf)), `"alg":"Ed25519",`, ``, 1),
	} {
		p := filepath.Join(t.TempDir(), "k.json")
		_ = os.WriteFile(p, []byte(content), 0o600)
		if _, err := deed.LoadKeyFile(p, deed.LoadOptions{}); !errors.As(err, &kfe) || kfe.Problem != deed.KeyFileUnreadable {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := deed.LoadKeyFile(filepath.Join(dir, "missing.json"), deed.LoadOptions{}); !errors.As(err, &kfe) || kfe.Problem != deed.KeyFileUnreadable {
		t.Errorf("missing: %v", err)
	}
	if _, err := deed.LoadKeyFile(dir, deed.LoadOptions{}); !errors.As(err, &kfe) || kfe.Problem != deed.KeyFileUnreadable {
		t.Errorf("directory: %v", err)
	}
}

func err2s(_ any, err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestTestKeysAreRecognized(t *testing.T) {
	for _, n := range []string{"a", "b", "r"} {
		if !deed.IsTestKey(deedtest.TestKey(n).Signer.PublicKey()) {
			t.Errorf("test key %s not recognized", n)
		}
	}
	if deed.IsTestKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32)).Public().(ed25519.PublicKey)) {
		t.Error("a random key is not a test key")
	}
}

func TestNewFileSignerRefusals(t *testing.T) {
	if _, err := deed.NewFileSigner("sk_bad", make([]byte, 32)); err == nil {
		t.Error("bad id")
	}
	if _, err := deed.NewFileSigner(deedtest.ID("sk", 1), make([]byte, 31)); err == nil {
		t.Error("short seed")
	}
}
