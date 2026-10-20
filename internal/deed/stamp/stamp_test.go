package stamp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed/deedtest"
	"github.com/deedglass/deedglass/internal/deed/stamp"
	"github.com/deedglass/deedglass/internal/deed/stamp/fetch"
	"github.com/deedglass/deedglass/internal/deed/stamp/stamptest"
)

func headOf(t *testing.T, c *deedtest.Chain, seq int) stamp.Head {
	t.Helper()
	e := c.Deeds[seq-1]
	return stamp.NewHead(c.Org, e.Body.Seq, e.Body.DeedID, e.BodyHash, e.Body.IssuedAt)
}

type fixture struct {
	chain *deedtest.Chain
	tsa1  *stamptest.TSA
	tsa2  *stamptest.TSA
	svc   *stamptest.Service
	cfg   *fetch.Config
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{chain: deedtest.NewChain(12, deedtest.ChainOpts{}),
		tsa1: stamptest.NewTSA("one"), tsa2: stamptest.NewTSA("two"), svc: stamptest.NewService()}
	t.Cleanup(func() { f.tsa1.Close(); f.tsa2.Close(); f.svc.Close() })
	key := f.svc.Key
	f.cfg = &fetch.Config{TSAs: []string{f.tsa1.URL, f.tsa2.URL}, MinTSA: 2, Service: f.svc.URL, ServiceKey: &key,
		Signer: deedtest.TestKey("b").Signer, Issuer: deedtest.Issuer, Client: f.tsa1.Client(),
		Now: func() time.Time { return time.Date(2026, 10, 20, 9, 31, 0, 0, time.UTC) }}
	return f
}

func (f *fixture) stamp(t *testing.T, seq int) stamp.Entry {
	t.Helper()
	e, err := fetch.Stamp(context.Background(), f.cfg, headOf(t, f.chain, seq))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestStampRoundTrip(t *testing.T) {
	f := newFixture(t)
	e := f.stamp(t, 12)
	if len(e.RFC3161) != 2 || len(e.SCITT) != 1 {
		t.Fatalf("got %d tokens and %d registrations", len(e.RFC3161), len(e.SCITT))
	}
	file := stamp.NewFile(f.chain.Org)
	if err := file.Add(e); err != nil {
		t.Fatal(err)
	}
	raw, err := file.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := stamp.ParseFile(raw, stamp.MaxBundleBytes)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := stamp.ParseRoots(append(f.tsa1.RootPEM(), f.tsa2.RootPEM()...))
	if err != nil {
		t.Fatal(err)
	}
	checked, probs, warns := stamp.Check(back, stamp.CheckOptions{Keys: deedtest.KeySet(), TSARoots: roots, ServiceKeys: []stamp.JWK{f.svc.Key}})
	if len(probs) != 0 || len(warns) != 0 || len(checked) != 1 {
		t.Fatalf("checked %d, problems %v, warnings %v", len(checked), probs, warns)
	}
	if got := checked[0].Earliest; !got.Equal(time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("earliest %s", got)
	}
	// Unpinned: integrity only, with both warnings.
	_, probs, warns = stamp.Check(back, stamp.CheckOptions{Keys: deedtest.KeySet()})
	if len(probs) != 0 || len(warns) != 2 {
		t.Fatalf("unpinned: problems %v, warnings %v", probs, warns)
	}
	// Compare: the full chain matches; a truncated one is missing its tail; a rewritten one mismatches.
	hashes := map[int64]string{}
	for _, d := range f.chain.Deeds {
		hashes[d.Body.Seq] = d.BodyHash
	}
	if p, _ := stamp.Compare(checked, stamp.Observed{Org: f.chain.Org, FirstSeq: 1, LastSeq: 12, BodyHash: hashes}); len(p) != 0 {
		t.Fatalf("full chain: %v", p)
	}
	p, _ := stamp.Compare(checked, stamp.Observed{Org: f.chain.Org, FirstSeq: 1, LastSeq: 9, BodyHash: hashes})
	if len(p) != 1 || p[0].Code != stamp.CodeChainTailMissing || !strings.Contains(p[0].Message, "Deeds after 9 are missing: deed 12 was timestamped at 2026-10-20 09:30:00 UTC") {
		t.Fatalf("truncated: %+v", p)
	}
	hashes[12] = "sha256:" + strings.Repeat("0", 64)
	p, _ = stamp.Compare(checked, stamp.Observed{Org: f.chain.Org, FirstSeq: 1, LastSeq: 12, BodyHash: hashes})
	if len(p) != 1 || p[0].Code != stamp.CodeTimestampMismatch {
		t.Fatalf("rewritten: %+v", p)
	}
	if _, w := stamp.Compare(checked, stamp.Observed{Org: f.chain.Org, FirstSeq: 13, LastSeq: 20, BodyHash: hashes}); len(w) != 1 || w[0].Code != stamp.WarnBeforeRange {
		t.Fatalf("before range: %+v", w)
	}
}

func TestTSAFailures(t *testing.T) {
	for _, mode := range []string{stamptest.TSARefuse, stamptest.TSAWrongImprint, stamptest.TSABadSignature, stamptest.TSANoESS, stamptest.TSAGarbage} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.tsa2.SetMode(mode)
			_, err := fetch.Stamp(context.Background(), f.cfg, headOf(t, f.chain, 3))
			if err == nil || !strings.Contains(err.Error(), "1 of 2 TSAs answered; 2 needed") {
				t.Fatalf("err = %v", err)
			}
			f.cfg.MinTSA = 1
			e, err := fetch.Stamp(context.Background(), f.cfg, headOf(t, f.chain, 3))
			if err != nil || len(e.RFC3161) != 1 {
				t.Fatalf("min 1: %v, %d tokens", err, len(e.RFC3161))
			}
		})
	}
}

func TestServiceFailures(t *testing.T) {
	for _, mode := range []string{stamptest.ServiceBadSignature, stamptest.ServiceWrongLeaf, stamptest.ServiceRefuse} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.svc.SetMode(mode)
			if _, err := fetch.Stamp(context.Background(), f.cfg, headOf(t, f.chain, 3)); err == nil {
				t.Fatal("a bad service answer was accepted")
			}
		})
	}
}

// TestCheckCatchesEdits changes one thing in a good file at a time; Check must refuse each.
func TestCheckCatchesEdits(t *testing.T) {
	f := newFixture(t)
	good := f.stamp(t, 12)
	ks := deedtest.KeySet()
	cases := map[string]func(e *stamp.Entry){
		"head body_hash": func(e *stamp.Entry) { e.Head.BodyHash = "sha256:" + strings.Repeat("1", 64) },
		"head seq":       func(e *stamp.Entry) { e.Head.Seq = 13 },
		"token imprint byte": func(e *stamp.Entry) {
			der, _ := base64.RawURLEncoding.DecodeString(e.RFC3161[0].Token)
			imp, _ := e.Head.Imprint()
			i := bytes.Index(der, imp)
			der[i] ^= 0x01
			e.RFC3161[0].Token = stamp.EncodeB64(der)
		},
		"token signature byte": func(e *stamp.Entry) {
			der, _ := base64.RawURLEncoding.DecodeString(e.RFC3161[0].Token)
			der[len(der)-3] ^= 0x01 // the signature is the token's last field
			e.RFC3161[0].Token = stamp.EncodeB64(der)
		},
		"token from another head": func(e *stamp.Entry) { e.RFC3161[0] = f.stamp(t, 11).RFC3161[0] },
		"statement from another head": func(e *stamp.Entry) {
			e.SCITT[0].Statement = f.stamp(t, 11).SCITT[0].Statement
		},
		"inclusion from another entry": func(e *stamp.Entry) {
			e.SCITT[0].Inclusion = f.stamp(t, 11).SCITT[0].Inclusion
		},
		"service key swapped": func(e *stamp.Entry) {
			other := stamptest.NewService()
			defer other.Close()
			e.SCITT[0].ServiceKey = other.Key
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			e := good
			e.RFC3161 = append([]stamp.Token(nil), good.RFC3161...)
			e.SCITT = append([]stamp.Registration(nil), good.SCITT...)
			edit(&e)
			file := &stamp.File{Object: stamp.ObjectFile, V: 1, Org: f.chain.Org, Heads: []stamp.Entry{e}}
			_, probs, _ := stamp.Check(file, stamp.CheckOptions{Keys: ks})
			if len(probs) == 0 || probs[0].Code != stamp.CodeTimestampInvalid {
				t.Fatalf("not caught: %+v", probs)
			}
		})
	}
	// A statement sealed by a revoked or unknown key.
	e := good
	file := &stamp.File{Object: stamp.ObjectFile, V: 1, Org: f.chain.Org, Heads: []stamp.Entry{e}}
	f.cfg.Signer = deedtest.TestKey("r").Signer
	e2 := f.stamp(t, 12)
	file.Heads[0].SCITT = e2.SCITT
	if _, probs, _ := stamp.Check(file, stamp.CheckOptions{Keys: ks}); len(probs) == 0 || !strings.Contains(probs[0].Message, "revoked") {
		t.Fatalf("revoked key: %+v", probs)
	}
	// A pinned root that is not the TSA's.
	other := stamptest.NewTSA("other")
	defer other.Close()
	roots, _ := stamp.ParseRoots(other.RootPEM())
	file.Heads[0] = good
	if _, probs, _ := stamp.Check(file, stamp.CheckOptions{Keys: ks, TSARoots: roots}); len(probs) != 2 {
		t.Fatalf("foreign roots: %+v", probs)
	}
}

func TestParseFileRefusals(t *testing.T) {
	f := newFixture(t)
	file := stamp.NewFile(f.chain.Org)
	if err := file.Add(f.stamp(t, 5)); err != nil {
		t.Fatal(err)
	}
	if err := file.Add(f.stamp(t, 3)); err != nil {
		t.Fatal(err)
	}
	raw, err := file.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if file.Heads[0].Head.Seq != 3 {
		t.Fatal("Add doesn't keep seq order")
	}
	if _, err := stamp.ParseFile(raw, stamp.MaxBundleBytes); err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for name, bad := range map[string]string{
		"not canonical":  " " + s,
		"unknown member": strings.Replace(s, `{"heads":`, `{"extra":1,"heads":`, 1),
		"wrong object":   strings.Replace(s, `"object":"head_timestamps"`, `"object":"head_timestampz"`, 1),
		"version":        strings.Replace(s, `"v":1}`, `"v":2}`, 1),
		"too large":      s,
	} {
		t.Run(name, func(t *testing.T) {
			max := int64(stamp.MaxBundleBytes)
			if name == "too large" {
				max = int64(len(s) - 1)
			}
			if _, err := stamp.ParseFile([]byte(bad), max); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// Two different heads at one seq are refused by Add.
	e := f.stamp(t, 5)
	e.Head.BodyHash = "sha256:" + strings.Repeat("2", 64)
	if err := file.Add(e); err == nil {
		t.Fatal("Add accepted a second head at one seq")
	}
	// Another org's head.
	e.Head.Org = deedtest.Org(3)
	if err := file.Add(e); err == nil {
		t.Fatal("Add accepted another org's head")
	}
}

func TestInclusionPaths(t *testing.T) {
	for n := 1; n <= 33; n++ {
		var leaves [][]byte
		for i := 0; i < n; i++ {
			leaves = append(leaves, stamp.LeafHash([]byte{byte(i), byte(n)}))
		}
		root := stamptest.TreeHead(leaves)
		for m := 0; m < n; m++ {
			got, err := stamp.RootFromInclusion(leaves[m], uint64(m), uint64(n), stamptest.Path(uint64(m), leaves))
			if err != nil || string(got) != string(root) {
				t.Fatalf("n=%d m=%d: %v", n, m, err)
			}
			if n > 1 { // another leaf's hash on this path doesn't lead to the root
				if got, _ := stamp.RootFromInclusion(leaves[(m+1)%n], uint64(m), uint64(n), stamptest.Path(uint64(m), leaves)); string(got) == string(root) {
					t.Fatalf("n=%d m=%d: another leaf gave the root", n, m)
				}
			}
		}
		if _, err := stamp.RootFromInclusion(leaves[0], uint64(n), uint64(n), nil); err == nil {
			t.Fatal("an index outside the tree was accepted")
		}
	}
	// The CBOR shape round-trips; anything else is refused.
	path := [][]byte{make([]byte, 32), make([]byte, 32)}
	enc := stamp.EncodeInclusion(300, 299, path)
	if len(enc) != 1+3+3+1+2*(2+32) || enc[0] != 0x83 {
		t.Fatal("not a 3-array")
	}
}

func TestNoNetworkWhenOff(t *testing.T) {
	var c *fetch.Config
	if c.Enabled() || (&fetch.Config{}).Enabled() {
		t.Fatal("an empty config is enabled")
	}
	tsa := stamptest.NewTSA("idle")
	defer tsa.Close()
	if _, err := fetch.Stamp(context.Background(), &fetch.Config{Client: tsa.Client()}, stamp.Head{}); err == nil {
		t.Fatal("Stamp with nothing configured did something")
	}
	if tsa.Calls() != 0 {
		t.Fatal("a call went out")
	}
}

func TestImprintIsJCSOfHead(t *testing.T) {
	c := deedtest.NewChain(1, deedtest.ChainOpts{})
	h := headOf(t, c, 1)
	b, err := h.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"body_hash":"` + c.Deeds[0].BodyHash + `","deed_id":"` + c.Deeds[0].Body.DeedID + `","issued_at":"` + c.Deeds[0].Body.IssuedAt +
		`","object":"chain_head","org":"` + c.Org + `","seq":1,"v":1}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
	imp, _ := h.Imprint()
	s := sha256.Sum256(b)
	if string(imp) != string(s[:]) {
		t.Fatal("imprint")
	}
}

// TestOpenSSLTokens checks tokens made by OpenSSL 3.6's own TSA (`openssl ts -reply`, RSA and
// P-256, chain included, ESS signing-cert v2), so the verifier is not only checked against the fake
// TSA written beside it. The files are in testdata/openssl (test keys; regenerated by hand).
func TestOpenSSLTokens(t *testing.T) {
	for _, alg := range []string{"rsa", "ec"} {
		t.Run(alg, func(t *testing.T) {
			dir := "testdata/openssl/" + alg + "/"
			head, _ := os.ReadFile(dir + "head.json")
			reply, _ := os.ReadFile(dir + "reply.tsr")
			rootPEM, _ := os.ReadFile(dir + "root.pem")
			tok, err := stamp.ParseResponse(reply)
			if err != nil {
				t.Fatal(err)
			}
			imp := sha256.Sum256(head)
			roots, err := stamp.ParseRoots(rootPEM)
			if err != nil {
				t.Fatal(err)
			}
			info, err := stamp.VerifyToken(tok, imp[:], roots)
			if err != nil {
				t.Fatal(err)
			}
			if !info.Pinned || info.Policy != "1.3.6.1.4.1.99999.2" || info.GenTime.Year() != 2026 {
				t.Fatalf("info %+v", info)
			}
			other := sha256.Sum256(append(head, ' '))
			if _, err := stamp.VerifyToken(tok, other[:], roots); err == nil {
				t.Fatal("a token was accepted for other data")
			}
		})
	}
}
