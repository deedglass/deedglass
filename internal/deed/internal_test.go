package deed

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/deedglass/deedglass/internal/ids"
)

func TestTestKeyThumbprintsDerive(t *testing.T) {
	if len(testKeyThumbprints) != 4 {
		t.Fatal("four test keys")
	}
	for tp, name := range testKeyThumbprints {
		seed := sha256.Sum256([]byte(TestKeySeedLabel + name))
		var pub []byte = ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey)
		if name == "q" { // ML-DSA-65 (ADR-109)
			s, err := NewMLDSASigner(ids.New[ids.SealKeyKind]().String(), seed[:])
			if err != nil {
				t.Fatal(err)
			}
			pub = s.PublicKeyBytes()
		}
		if Digest(pub) != tp {
			t.Errorf("test key %s: thumbprint %s, derived %s", name, tp, Digest(pub))
		}
	}
}

// canonicalEnvelope (the fast path) must equal StrictCanonical (JCS through gowebpki/jcs).
func TestCanonicalEnvelopeMatchesJCS(t *testing.T) {
	seed := bytes.Repeat([]byte{3}, 32)
	s, err := NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "Priya’s agent — ☕", "\"q\" \\ </script> " + string(rune(0x2028)), "日本語"} {
		b := Body{V: 1, Kind: KindAdmin, DeedID: ids.New[ids.DeedKind]().String(), Org: ids.New[ids.OrgKind]().String(), Seq: 9007199254740991,
			PrevHash: Digest(nil), IssuedAt: "2026-10-02T09:00:00.000Z",
			Agent: &AgentRef{ID: ids.New[ids.AgentKind]().String(), Name: name},
			Admin: &AdminSection{Event: ids.New[ids.AdminEventKind]().String(), Action: "agent.stopped", Actor: Ref{Type: "system", ID: "operator"}}}
		r, err := sealBody(s, b)
		if err != nil {
			t.Fatal(err)
		}
		want, err := StrictCanonical(r.env)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(r.envJCS, want) {
			t.Fatalf("fast path differs:\n%s\n%s", r.envJCS, want)
		}
	}
}

func TestStrictCanonicalRefuses(t *testing.T) {
	type big struct {
		N int64 `json:"n"`
	}
	if _, err := StrictCanonical(big{9007199254740993}); CodeOf(err) != CodeMalformed {
		t.Fatalf("2^53+1: %v", err)
	}
	if _, err := StrictCanonical(big{-1}); CodeOf(err) != CodeMalformed {
		t.Fatalf("-1: %v", err)
	}
	if _, err := StrictCanonical(map[string]float64{"x": 1.5}); CodeOf(err) != CodeMalformed {
		t.Fatalf("1.5: %v", err)
	}
	if _, err := StrictCanonical(func() {}); err == nil {
		t.Fatal("unmarshalable")
	}
	c, err := StrictCanonical(map[string]any{"b": 1, "a": "x"})
	if err != nil || string(c) != `{"a":"x","b":1}` {
		t.Fatalf("%s %v", c, err)
	}
}

// Payload digests use plain RFC 8785: the reference vectors in testdata/jcs canonicalize exactly.
func TestDigestJSONReferenceVectors(t *testing.T) {
	ins, _ := filepath.Glob("../../testdata/jcs/input/*.json")
	if len(ins) != 6 {
		t.Fatalf("%d vectors", len(ins))
	}
	for _, in := range ins {
		src, _ := os.ReadFile(in)
		want, _ := os.ReadFile(filepath.Join("../../testdata/jcs/output", filepath.Base(in)))
		got, err := DigestJSON(src)
		if err != nil {
			t.Fatal(err)
		}
		if got != Digest(bytes.TrimRight(want, "\n")) {
			t.Errorf("%s: digest of JCS differs from the reference output", filepath.Base(in))
		}
	}
	if _, err := DigestJSON([]byte(`{"a":`)); err == nil {
		t.Fatal("invalid JSON")
	}
	d1, _ := DigestValue(map[string]any{"amount": 1.5, "x": -2})
	d2, _ := DigestJSON([]byte(`{ "x": -2, "amount": 1.50 }`))
	if d1 != d2 {
		t.Fatal("payload digests ignore formatting, and allow fractions and negatives")
	}
	if _, err := DigestValue(func() {}); err == nil {
		t.Fatal("unmarshalable")
	}
}

func TestSealInputBytes(t *testing.T) {
	in := SealInput(Digest([]byte("x")))
	if len(in) != 89 || !strings.HasPrefix(string(in), "deedglass/deed/v1\nsha256:") {
		t.Fatalf("%q", in)
	}
}

// Every code has a calm sentence and a next step.
func TestMessagesCoverEveryCode(t *testing.T) {
	seen := map[Code]bool{}
	for _, c := range Codes {
		if seen[c] {
			t.Fatalf("%s listed twice", c)
		}
		seen[c] = true
		for _, e := range []*VerifyError{{Code: c}, {Code: c, Seq: 812, Prev: 811, Want: 812, Key: "sk_x", Field: "body.seq", Version: 2, Org: "org_a", WantOrg: "org_b", IssuedAt: "2026-10-02T09:00:00.000Z", Detail: "Detail"}} {
			s, next := Message(e)
			if s == "" || next == "" || strings.Contains(s+next, "!") || strings.Contains(s, "{") {
				t.Errorf("%s: %q / %q", c, s, next)
			}
			if !unicode.IsUpper([]rune(s)[0]) || !strings.HasSuffix(s, ".") {
				t.Errorf("%s: sentence %q", c, s)
			}
			if e.Error() == "" {
				t.Errorf("%s: empty Error()", c)
			}
		}
	}
	if len(seen) != 23 {
		t.Fatalf("%d codes", len(seen))
	}
	s, _ := Message(&VerifyError{Code: "nope"})
	if s == "" {
		t.Fatal("unknown code")
	}
}

func TestCheckKeyStatusUnknown(t *testing.T) {
	if err := checkKey(KeyStatus{Found: true, Status: "paused"}, time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("unknown status must not seal")
	}
}

func TestIDPatternMatchesIDs(t *testing.T) {
	for _, p := range idPrefixes {
		if IDPattern(p) != ids.Pattern(p) {
			t.Errorf("%s: %s != %s", p, IDPattern(p), ids.Pattern(p))
		}
		if !ids.Known(p) {
			t.Errorf("%s is not a registered prefix", p)
		}
	}
	at := time.Date(2026, 10, 2, 9, 0, 0, 512_000_000, time.UTC)
	seen := map[string]bool{}
	for range 1000 {
		id := NewDeedID(at)
		if err := ids.Check(ids.Deed, id); err != nil || seen[id] {
			t.Fatalf("%s: %v", id, err)
		}
		seen[id] = true
		parsed, _ := ids.Parse[ids.DeedKind](id)
		if !parsed.Time().Equal(at.Truncate(time.Millisecond)) {
			t.Fatalf("time %v", parsed.Time())
		}
	}
}

func TestMessageTemplates(t *testing.T) {
	vars := MessageVars(&VerifyError{Code: CodeMalformed})
	for key, m := range Messages {
		for _, s := range []string{m.Sentence, m.Next} {
			for rest := s; ; {
				i := strings.IndexByte(rest, '{')
				if i < 0 {
					break
				}
				j := strings.IndexByte(rest[i:], '}')
				if _, ok := vars[rest[i+1:i+j]]; !ok {
					t.Errorf("%s: unknown placeholder %s", key, rest[i:i+j+1])
				}
				rest = rest[i+j+1:]
			}
		}
	}
	for _, c := range Codes {
		if _, ok := Messages[string(c)]; !ok {
			t.Errorf("%s has no template", c)
		}
	}
	s, next := Message(&VerifyError{Code: CodeChainSeqGap, Seq: 500, Want: 1})
	if s != "The deeds start at deed 500, not deed 1." || !strings.Contains(next, "--anchor 499:") {
		t.Errorf("gap at start: %q / %q", s, next)
	}
	s, next = Message(&VerifyError{Code: CodeChainSeqGap, Seq: 814, Prev: 812, Want: 813})
	if s != "Deed 813 is missing. The chain jumps from 812 to 814." || next != "A deed was removed or the export is incomplete. Ask the issuer for seq 812–814." {
		t.Errorf("gap: %q / %q", s, next)
	}
	if s, _ := Message(&VerifyError{Code: CodeKeysMissing, Seq: 1}); s != "Cannot check deed 1: no seal keys were given." {
		t.Errorf("keys_missing: %q", s)
	}
	if s, _ := Message(&VerifyError{Code: CodeBundleInvalid}); s != "This file is not a valid deed bundle. Nothing was extracted." {
		t.Errorf("bundle_invalid: %q", s)
	}
	if got := RenderMessage("{a} {b} {", map[string]string{"a": "x"}); got != "x {b} {" {
		t.Errorf("render: %q", got)
	}
}
