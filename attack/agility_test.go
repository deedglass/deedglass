package attack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

// Group Y, crypto agility (TY-01, TY-02; ADR-109): library- and CLI-level cases on forged deeds and
// key sets, run by plain go test. Expected outcomes are in attack_meta.json, declared before any
// deed v2 code, and never change here.

// v2Base is a v2 deed sealed by Ed25519 key a alone, and the same body sealed by a and key x (an
// algorithm no verifier implements).
func v2Base(t *testing.T) (single, withX *deed.Envelope) {
	t.Helper()
	a, x := deedtest.TestKey("a").Signer, deedtest.KeyX()
	c := deedtest.NewChainV2(1, deedtest.V2ChainOpts{ChainOpts: deedtest.ChainOpts{Org: deedtest.Org(1)},
		SignersFor: func(int64) []deed.AlgSigner { return []deed.AlgSigner{a} }})
	return c.Deeds[0], deedtest.ResealV2(c.Deeds[0], []deed.AlgSigner{a, x}, nil)
}

// editEnvelope decodes a canonical envelope, applies f and re-encodes it.
func editEnvelope(t *testing.T, raw []byte, f func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	f(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Y05 · Algorithm confusion inside the deed → DETECTED (seal_key_mismatch).
func TestY05EnvelopeAlgConfusion(t *testing.T) {
	_, withX := v2Base(t)
	raw := deedtest.Canonical(withX)
	ks := deedtest.KeySetV2()
	cases := map[string]func(m map[string]any){
		"seals swapped": func(m map[string]any) {
			s := m["seals"].([]any)
			s[0], s[1] = s[1], s[0]
		},
		"alg renamed": func(m map[string]any) {
			s := m["seals"].([]any)[1].(map[string]any)
			s["alg"], s["seal"] = "XMSS", "xmss:"+strings.SplitN(s["seal"].(string), ":", 2)[1]
		},
		"key renamed": func(m map[string]any) {
			s := m["seals"].([]any)[0].(map[string]any)
			s["seal_key_id"] = deedtest.TestKey("b").ID
			m["seals"] = append(m["seals"].([]any), map[string]any{"alg": deed.AlgEd25519, "seal": s["seal"], "seal_key_id": deedtest.TestKey("a").ID})
		},
	}
	for name, f := range cases {
		if _, err := deed.VerifyEnvelope(editEnvelope(t, raw, f), ks, deed.VerifyOptions{}); deed.CodeOf(err) != deed.CodeSealKeyMismatch {
			t.Errorf("%s: got %v, want seal_key_mismatch", name, err)
		}
	}
	t.Log("Y05: DETECTED (seal_key_mismatch)")
}

// Y06 · Unknown algorithm → UNVERIFIABLE (unsupported_algorithm, exit 2).
func TestY06UnknownAlgorithm(t *testing.T) {
	_, withX := v2Base(t)
	ks := deedtest.KeySetV2()
	_, err := deed.VerifyEnvelope(deedtest.Canonical(withX), ks, deed.VerifyOptions{})
	if deed.CodeOf(err) != deed.CodeUnsupportedAlgorithm || !deed.CannotDetermine(string(deed.CodeOf(err))) {
		t.Fatalf("got %v, want unsupported_algorithm (cannot determine)", err)
	}
	r := verifyCLI(t, "../testdata/deeds/v2/invalid/unsupported-algorithm/deed.json", "--keys", "../testdata/deeds/v2/keys.json")
	if r.exit != 2 || r.rep.Result == deed.ResultValid {
		t.Fatalf("exit %d result %s\n%s", r.exit, r.rep.Result, r.text)
	}
	if f := r.rep.FirstFailure(); f == nil || f.Code != string(deed.CodeUnsupportedAlgorithm) {
		t.Fatalf("first failure %+v", f)
	}
	t.Log("Y06: UNVERIFIABLE (unsupported_algorithm, exit 2)")
}

// Y07 · A key set holding an unknown-algorithm key → VERIFIED for deeds of known keys.
func TestY07UnknownKeyInSet(t *testing.T) {
	ks := deedtest.KeySetV2()
	if _, _, ok := ks.Lookup(deedtest.KeyX().ID); !ok {
		t.Fatal("key x was dropped from the set")
	}
	raw, err := json.Marshal(ks)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := deed.ParseKeySet(raw)
	if err != nil {
		t.Fatalf("a key set with an unknown-algorithm key was refused: %v", err)
	}
	g := deedtest.NewChain(20, deedtest.ChainOpts{})
	for _, l := range g.Lines {
		if _, err := deed.VerifyEnvelope(l, parsed, deed.VerifyOptions{Strict: true}); err != nil {
			t.Fatalf("v1 deed with key x in the set: %v", err)
		}
	}
	single, _ := v2Base(t)
	if _, err := deed.VerifyEnvelope(deedtest.Canonical(single), parsed, deed.VerifyOptions{Strict: true}); err != nil {
		t.Fatalf("v2 deed with key x in the set: %v", err)
	}
	r := verifyCLI(t, "../testdata/deeds/v2/valid/chain-v1-then-v2.jsonl", "--keys", "../testdata/deeds/v2/keys.json", "--strict")
	if r.exit != 0 || r.rep.Result != deed.ResultValid {
		t.Fatalf("exit %d\n%s", r.exit, r.text)
	}
	t.Log("Y07: VERIFIED for deeds of known keys")
}

// Y09 · Ed25519 alone on a deed issued on or after 2035-01-01 → DETECTED (seal_missing).
func TestY09Ed25519AloneAfter2035(t *testing.T) {
	ks := deedtest.KeySet()
	b := deedtest.TestKey("b")
	g := deedtest.NewChain(12, deedtest.ChainOpts{})
	for _, at := range []time.Time{deed.Ed25519AloneUntil, deed.Ed25519AloneUntil.Add(time.Millisecond), time.Date(2046, 1, 1, 0, 0, 0, 0, time.UTC)} {
		v1 := deedtest.Reseal(g.Deeds[11], b, func(x *deed.Body) { x.IssuedAt = deed.FormatTime(at) })
		if _, err := deed.VerifyEnvelope(deedtest.Canonical(v1), ks, deed.VerifyOptions{}); deed.CodeOf(err) != deed.CodeSealMissing {
			t.Errorf("v1 at %s: got %v, want seal_missing", at, err)
		}
		v2 := deedtest.ResealV2(g.Deeds[11], []deed.AlgSigner{b.Signer}, func(x *deed.Body) { x.IssuedAt = deed.FormatTime(at) })
		if _, err := deed.VerifyEnvelope(deedtest.Canonical(v2), ks, deed.VerifyOptions{}); deed.CodeOf(err) != deed.CodeSealMissing {
			t.Errorf("v2 at %s: got %v, want seal_missing", at, err)
		}
	}
	// The millisecond before still verifies: the date is exact.
	ok := deedtest.Reseal(g.Deeds[11], b, func(x *deed.Body) { x.IssuedAt = deed.FormatTime(deed.Ed25519AloneUntil.Add(-time.Millisecond)) })
	if _, err := deed.VerifyEnvelope(deedtest.Canonical(ok), ks, deed.VerifyOptions{}); err != nil {
		t.Fatalf("the last millisecond: %v", err)
	}
	t.Log("Y09: DETECTED (seal_missing)")
}

// Y10 · The 2046 test: every release bundle verifies with the v2 verifier, byte for byte
// unchanged → VERIFIED. The SHA256SUMS beside each bundle pin its bytes (make release-bundles).
func TestY10ReleaseBundles(t *testing.T) {
	dirs, _ := filepath.Glob("../testdata/release-bundles/*")
	n := 0
	for _, d := range dirs {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			continue
		}
		n++
		r := verifyCLI(t, filepath.Join(d, "bundle.tar.gz"), "--keys", filepath.Join(d, "keys.json"), "--strict")
		if r.exit != 0 || r.rep.Result != deed.ResultValid {
			t.Errorf("%s: exit %d\n%s", d, r.exit, r.text)
		}
	}
	if n < 6 {
		t.Fatalf("%d release bundles, want at least 6 (v0.1.0 to v0.4.0)", n)
	}
	t.Logf("Y10: VERIFIED (%d release bundles)", n)
}
