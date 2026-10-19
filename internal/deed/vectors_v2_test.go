package deed_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

// Deed v2 vectors (ADR-109) live in testdata/deeds/v2/, beside the v1 tree, which stays as it was:
// v2/keys.json (keys a, b and r as in v1, plus key x of an algorithm no verifier implements),
// v2/valid/, v2/invalid/<name>/, v2/expected.json and the v2 parity corpus. TestGoldenVectors
// writes and checks them with the v1 files.

const parityCasesV2 = 200

// v2Signers are the signer lists the vectors use.
func v2Signers() (aOnly, bOnly, aX []deed.AlgSigner) {
	a, b, x := deedtest.TestKey("a").Signer, deedtest.TestKey("b").Signer, deedtest.KeyX()
	return []deed.AlgSigner{a}, []deed.AlgSigner{b}, []deed.AlgSigner{a, x}
}

func addV2Vectors(t testing.TB, files map[string][]byte) {
	t.Helper()
	ks := deedtest.KeySetV2()
	files["v2/keys.json"] = pretty(t, ks)
	aOnly, bOnly, _ := v2Signers()
	org1 := deedtest.Org(1)

	// valid/: a v2 deed sealed by Ed25519 alone, and one chain that starts in v1 and continues in v2.
	single := deedtest.NewChainV2(1, deedtest.V2ChainOpts{ChainOpts: deedtest.ChainOpts{Org: org1},
		SignersFor: func(int64) []deed.AlgSigner { return aOnly }})
	mixed := deedtest.NewChainV2(14, deedtest.V2ChainOpts{SignersFor: func(seq int64) []deed.AlgSigner {
		switch {
		case seq <= 4:
			return nil // v1, key a
		case seq <= 10:
			return aOnly
		}
		return bOnly
	}})
	exp := map[string]Expected{}
	files["v2/valid/01-ed25519-only.json"] = single.Lines[0]
	exp["v2/valid/01-ed25519-only.json"] = Expected{Valid: true}
	files["v2/valid/chain-v1-then-v2.jsonl"] = jsonl(mixed.Lines...)
	exp["v2/valid/chain-v1-then-v2.jsonl"] = Expected{Valid: true}
	// The last millisecond Ed25519 alone is accepted (DEED_SPEC.md §11.4).
	late := deedtest.ResealV2(single.Deeds[0], bOnly, func(b *deed.Body) {
		b.IssuedAt = deed.FormatTime(deed.Ed25519AloneUntil.Add(-time.Millisecond))
	})
	files["v2/valid/02-ed25519-last-millisecond.json"] = deedtest.Canonical(late)
	exp["v2/valid/02-ed25519-last-millisecond.json"] = Expected{Valid: true}
	var ind bytes.Buffer
	_ = json.Indent(&ind, single.Lines[0], "", "  ")
	files["v2/lenient/whitespace.json"] = append(ind.Bytes(), '\n')
	nc := string(deed.CodeNonCanonical)
	exp["v2/lenient/whitespace.json"] = Expected{Valid: true, StrictCode: &nc}
	files["v2/expected.json"] = pretty(t, exp)

	for _, iv := range invalidVectorsV2(t, single) {
		dir := "v2/invalid/" + iv.name + "/"
		name := "deed.json"
		if iv.chain {
			name = "deeds.jsonl"
		}
		files[dir+name] = iv.data
		if iv.keys != nil {
			files[dir+"keys.json"] = iv.keys
		}
		files[dir+"expected.json"] = pretty(t, iv.exp)
	}
	files["v2/parity/corpus-200.jsonl"], files["v2/parity/corpus-200.expected.json"] = parityFilesV2(t)
}

func invalidVectorsV2(t testing.TB, single *deedtest.Chain) []invalidVector {
	t.Helper()
	env, raw := single.Deeds[0], single.Lines[0]
	_, bOnly, aX := v2Signers()
	a := deedtest.TestKey("a")
	withX := deedtest.ResealV2(env, aX, nil)
	withXRaw := deedtest.Canonical(withX)
	seals := func(m map[string]any) []any { return m["seals"].([]any) }
	at2035 := deed.FormatTime(deed.Ed25519AloneUntil)
	v1At2035 := deedtest.Canonical(deedtest.Reseal(env, deedtest.TestKey("b"), func(b *deed.Body) {
		b.V, b.SealKeys, b.IssuedAt = deed.Version, nil, at2035
	}))
	v2At2035 := deedtest.Canonical(deedtest.ResealV2(env, bOnly, func(b *deed.Body) { b.IssuedAt = at2035 }))
	v1Tag := edit(t, raw, func(m map[string]any) {
		s := seals(m)[0].(map[string]any)
		s["seal"] = "ed25519:" + base64.RawURLEncoding.EncodeToString(ed25519Sign(a.Seed, []byte(deed.TagDeed+env.BodyHash)))
	})
	return []invalidVector{
		{name: "unsupported-algorithm", data: withXRaw, exp: expect(deed.CodeUnsupportedAlgorithm, 1, "")},
		{name: "seal-stripped", data: edit(t, withXRaw, func(m map[string]any) { m["seals"] = seals(m)[:1] }), exp: expect(deed.CodeSealMissing, 1, "")},
		{name: "seals-reordered", data: edit(t, withXRaw, func(m map[string]any) {
			s := seals(m)
			s[0], s[1] = s[1], s[0]
		}), exp: expect(deed.CodeSealKeyMismatch, 1, "")},
		{name: "seal-alg-differs", data: edit(t, withXRaw, func(m map[string]any) {
			s := seals(m)[1].(map[string]any)
			s["alg"], s["seal"] = "XMSS", "xmss:"+strings.SplitN(s["seal"].(string), ":", 2)[1]
		}), exp: expect(deed.CodeSealKeyMismatch, 1, "")},
		{name: "seal-extra", data: edit(t, raw, func(m map[string]any) {
			x := deedtest.KeyX()
			sig, _ := x.Sign([]byte(x.SealTag() + env.BodyHash))
			m["seals"] = append(seals(m), map[string]any{"alg": deedtest.AlgUnknown, "seal": x.SealPrefix() + base64.RawURLEncoding.EncodeToString(sig), "seal_key_id": x.ID})
		}), exp: expect(deed.CodeSealKeyMismatch, 1, "")},
		{name: "alg-confusion-key-set", data: deedtest.Canonical(deedtest.ResealV2(env, []deed.AlgSigner{lyingAlg{id: deedtest.KeyX().ID, s: a.Signer}}, nil)), exp: expect(deed.CodeSealInvalid, 1, "")},
		{name: "ed25519-alone-2035-v1", data: v1At2035, exp: expect(deed.CodeSealMissing, 1, "")},
		{name: "ed25519-alone-2035-v2", data: v2At2035, exp: expect(deed.CodeSealMissing, 1, "")},
		{name: "v1-tag-in-v2", data: v1Tag, exp: expect(deed.CodeSealInvalid, 1, "")},
		{name: "v1-seal-member", data: edit(t, raw, func(m map[string]any) { m["seal"] = seals(m)[0].(map[string]any)["seal"] }), exp: expect(deed.CodeSchemaInvalid, 1, "seal")},
		{name: "seal-key-id-in-body", data: edit(t, raw, func(m map[string]any) { body(m)["seal_key_id"] = a.ID }), exp: expect(deed.CodeSchemaInvalid, 1, "body.seal_key_id")},
		{name: "seal-keys-empty", data: edit(t, raw, func(m map[string]any) { body(m)["seal_keys"] = []any{} }), exp: expect(deed.CodeSchemaInvalid, 1, "body.seal_keys")},
		{name: "seal-keys-same-alg-twice", data: edit(t, raw, func(m map[string]any) {
			body(m)["seal_keys"] = append(body(m)["seal_keys"].([]any), map[string]any{"alg": deed.AlgEd25519, "id": deedtest.TestKey("b").ID})
		}), exp: expect(deed.CodeSchemaInvalid, 1, "body.seal_keys[1].alg")},
		{name: "seal-shape", data: edit(t, raw, func(m map[string]any) {
			s := seals(m)[0].(map[string]any)
			s["seal"] = s["seal"].(string)[:90]
		}), exp: expect(deed.CodeSchemaInvalid, 1, "seals[0].seal")},
		{name: "body-edit", data: edit(t, raw, func(m map[string]any) { body(m)["execution"].(map[string]any)["latency_ms"] = 42 }), exp: expect(deed.CodeBodyHashMismatch, 1, "")},
		{name: "unknown-key-id", data: deedtest.Canonical(deedtest.ResealV2(env, []deed.AlgSigner{mustSigner(t, deedtest.ID("sk", 9), 9)}, nil)), exp: expect(deed.CodeSealKeyUnknown, 1, "")},
		{name: "revoked-key", data: deedtest.Canonical(deedtest.ResealV2(env, []deed.AlgSigner{deedtest.TestKey("r").Signer}, nil)), exp: expect(deed.CodeSealKeyRevoked, 1, "")},
		{name: "version-3", data: edit(t, raw, func(m map[string]any) { body(m)["v"] = 3 }), exp: expect(deed.CodeUnsupportedVersion, 1, "")},
	}
}

// lyingAlg names key id while signing with s: an Ed25519 seal that claims a key of another
// algorithm (attack case Y04).
type lyingAlg struct {
	id string
	s  *deed.FileSigner
}

func (l lyingAlg) KeyID() string                   { return l.id }
func (l lyingAlg) Alg() string                     { return deed.AlgEd25519 }
func (l lyingAlg) PublicKeyBytes() []byte          { return l.s.PublicKeyBytes() }
func (l lyingAlg) Sign(msg []byte) ([]byte, error) { return l.s.Sign(msg) }

// parityFilesV2 is the v2 parity corpus: v2 and mixed chains with seeded tampering, so the browser
// verifier matches Go on the v2 rules (TestParityV2CoversV2Codes keeps it covering them).
func parityFilesV2(t testing.TB) (corpus, expected []byte) {
	t.Helper()
	r := rand.New(rand.NewPCG(2026, 1019))
	aOnly, bOnly, aX := v2Signers()
	g := deedtest.NewChainV2(20, deedtest.V2ChainOpts{SignersFor: func(seq int64) []deed.AlgSigner {
		switch {
		case seq <= 3:
			return nil
		case seq <= 10:
			return aOnly
		}
		return bOnly
	}})
	v2Lines := g.Lines[3:]
	withX := make([][]byte, 0, 6)
	for i := 3; i < 9; i++ {
		withX = append(withX, deedtest.Canonical(deedtest.ResealV2(g.Deeds[i], aX, nil)))
	}
	ks := deedtest.KeySetV2()
	pick := func(lines [][]byte) []byte { return lines[r.IntN(len(lines))] }
	seals := func(m map[string]any) []any { return m["seals"].([]any) }
	type gen func() (kind string, chain, strict bool, deeds [][]byte)
	gens := []struct {
		weight int
		g      gen
	}{
		{30, func() (string, bool, bool, [][]byte) { return "valid", false, r.IntN(3) == 0, [][]byte{pick(v2Lines)} }},
		{15, func() (string, bool, bool, [][]byte) {
			return "valid mixed chain", true, false, append([][]byte(nil), g.Lines[:4+r.IntN(16)]...)
		}},
		{40, func() (string, bool, bool, [][]byte) {
			b := bytes.Clone(pick(v2Lines))
			for range 1 + r.IntN(2) {
				i := r.IntN(len(b))
				b[i] ^= byte(1 << r.IntN(8))
			}
			return "bit flip", false, r.IntN(4) == 0, [][]byte{b}
		}},
		{20, func() (string, bool, bool, [][]byte) { return "unknown algorithm", false, false, [][]byte{pick(withX)} }},
		{20, func() (string, bool, bool, [][]byte) {
			return "seal stripped", false, false, [][]byte{edit(t, pick(withX), func(m map[string]any) {
				s := seals(m)
				m["seals"] = []any{s[r.IntN(2)]}
			})}
		}},
		{20, func() (string, bool, bool, [][]byte) {
			return "seal entry edit", false, false, [][]byte{edit(t, pick(withX), func(m map[string]any) {
				s := seals(m)
				e := s[r.IntN(2)].(map[string]any)
				switch r.IntN(3) {
				case 0:
					s[0], s[1] = s[1], s[0]
				case 1:
					e["seal_key_id"] = deedtest.ID("sk", 2+r.IntN(2))
				default:
					e["alg"] = []string{"Ed448", "ML-DSA-44", "ed25519"}[r.IntN(3)]
				}
			})}
		}},
		{20, func() (string, bool, bool, [][]byte) {
			raw := pick(v2Lines)
			return "seal_keys edit", false, false, [][]byte{edit(t, raw, func(m map[string]any) {
				b := body(m)
				switch r.IntN(4) {
				case 0:
					delete(b, "seal_keys")
				case 1:
					b["seal_keys"] = []any{}
				case 2:
					b["seal_key_id"] = deedtest.TestKey("a").ID
				default:
					b["seal_keys"].([]any)[0].(map[string]any)["alg"] = "Ed 25519"
				}
			})}
		}},
		{20, func() (string, bool, bool, [][]byte) {
			at := deed.Ed25519AloneUntil.Add(time.Duration(r.IntN(3)-1) * time.Millisecond)
			d := g.Deeds[3+r.IntN(len(v2Lines))]
			if r.IntN(2) == 0 {
				return "the 2035 boundary, v1", false, false, [][]byte{deedtest.Canonical(deedtest.Reseal(d, deedtest.TestKey("b"), func(b *deed.Body) {
					b.V, b.SealKeys, b.IssuedAt = deed.Version, nil, deed.FormatTime(at)
				}))}
			}
			return "the 2035 boundary, v2", false, false, [][]byte{deedtest.Canonical(deedtest.ResealV2(d, bOnly, func(b *deed.Body) { b.IssuedAt = deed.FormatTime(at) }))}
		}},
		{15, func() (string, bool, bool, [][]byte) {
			raw := pick(v2Lines)
			var ind bytes.Buffer
			_ = json.Indent(&ind, raw, "", strings.Repeat(" ", 1+r.IntN(3)))
			return "pretty", false, r.IntN(2) == 0, [][]byte{ind.Bytes()}
		}},
	}
	total := 0
	for _, x := range gens {
		total += x.weight
	}
	if total != parityCasesV2 {
		t.Fatalf("weights sum to %d", total)
	}
	var cbuf bytes.Buffer
	var exp []ParityExpected
	n := 0
	for _, x := range gens {
		for range x.weight {
			n++
			kind, chain, strict, deeds := x.g()
			pc := ParityCase{Case: n, Kind: kind, Chain: chain, Strict: strict}
			for _, d := range deeds {
				pc.Deeds = append(pc.Deeds, base64.StdEncoding.EncodeToString(d))
			}
			line, _ := json.Marshal(pc)
			cbuf.Write(line)
			cbuf.WriteByte('\n')
			exp = append(exp, runParity(t, pc, deeds, ks))
		}
	}
	return cbuf.Bytes(), pretty(t, exp)
}

func TestParityV2CoversV2Codes(t *testing.T) {
	_, expJSON := parityFilesV2(t)
	var exp []ParityExpected
	if err := json.Unmarshal(expJSON, &exp); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	valid := 0
	for _, e := range exp {
		if e.Valid {
			valid++
			continue
		}
		seen[*e.Code]++
	}
	t.Logf("%d valid, codes %v", valid, seen)
	for _, c := range []deed.Code{deed.CodeUnsupportedAlgorithm, deed.CodeSealMissing, deed.CodeSealKeyMismatch, deed.CodeSealInvalid,
		deed.CodeSchemaInvalid, deed.CodeBodyHashMismatch, deed.CodeMalformed} {
		if seen[string(c)] == 0 {
			t.Errorf("no v2 parity case produces %s", c)
		}
	}
	if valid < 40 || len(exp) != parityCasesV2 {
		t.Fatalf("%d valid of %d", valid, len(exp))
	}
}

// checkV2Vectors runs every v2 vector against v2/keys.json (or its own keys.json).
func checkV2Vectors(t *testing.T, files map[string][]byte) {
	ks, err := deed.ParseKeySet(files["v2/keys.json"])
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]Expected
	if err := json.Unmarshal(files["v2/expected.json"], &all); err != nil {
		t.Fatal(err)
	}
	for p, want := range all {
		if got := runVector(t, files[p], strings.HasSuffix(p, ".jsonl"), false, ks); !sameExpected(got, want) {
			t.Errorf("%s: got %s, want %s", p, pretty(t, got), pretty(t, want))
		}
	}
	dirs := map[string]bool{}
	for p := range files {
		if strings.HasPrefix(p, "v2/invalid/") {
			dirs[strings.Join(strings.Split(p, "/")[:3], "/")] = true
		}
	}
	for d := range dirs {
		var want Expected
		if err := json.Unmarshal(files[d+"/expected.json"], &want); err != nil {
			t.Fatal(err)
		}
		vks := ks
		if kb, ok := files[d+"/keys.json"]; ok {
			if vks, err = deed.ParseKeySet(kb); err != nil {
				if got := toExpected(err, nil); !sameExpected(got, want) {
					t.Errorf("%s: key set: got %s, want %s", d, pretty(t, got), pretty(t, want))
				}
				continue
			}
		}
		data, chain := files[d+"/deed.json"], false
		if data == nil {
			data, chain = files[d+"/deeds.jsonl"], true
		}
		if got := runVector(t, data, chain, false, vks); !sameExpected(got, want) {
			t.Errorf("%s: got %s, want %s", d, pretty(t, got), pretty(t, want))
		}
	}
}
