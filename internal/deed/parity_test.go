package deed_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

// The parity corpus (S05 §3.8): 500 cases from deedtest with seeded tampering. Go writes the
// expected first failure of each; the browser verifier's Vitest suite must return the same code,
// seq and field for every case. Deed bytes are base64 (some cases are not valid UTF-8).

// ParityCase is one line of testdata/deeds/parity/corpus-500.jsonl.
type ParityCase struct {
	Case   int      `json:"case"`
	Kind   string   `json:"kind"`   // what was done, for people reading a failure
	Chain  bool     `json:"chain"`  // verify the deeds as one chain (else one deed on its own)
	Strict bool     `json:"strict"` // --strict
	Deeds  []string `json:"deeds"`  // base64 of each deed's bytes
}

// ParityExpected is one entry of corpus-500.expected.json.
type ParityExpected struct {
	Case  int     `json:"case"`
	Valid bool    `json:"valid"`
	Code  *string `json:"code"`
	Seq   *int64  `json:"seq"`
	Field *string `json:"field"`
	Deeds int64   `json:"deeds"` // deeds read before the verifier stopped
}

const parityCases = 500

func parityFiles(t testing.TB) (corpus, expected []byte) {
	t.Helper()
	r := rand.New(rand.NewPCG(2026, 1002))
	g := deedtest.NewChain(30, deedtest.ChainOpts{})
	rn := func(c rune) string { return string(c) }
	names := []string{"Ελένη", "日本語のエージェント", "😀 refund bot", "e" + rn(0x301) + "clair", rn(0x202e) + "evil", "a" + rn(0x2028) + "b",
		rn(0xfeff) + "bom", "𝔘𝔫𝔦𝔠𝔬𝔡𝔢", "Zoë & “quotes”", `back\slash "q"`, "tab-less", rn(0x10ffff) + " max", "<script>", rn(0xfb00) + " ligature"}
	org9 := deedtest.Org(9)
	uni := deedtest.NewChain(len(names), deedtest.ChainOpts{Org: org9, Body: func(org string, seq int64) deed.Body {
		b := deedtest.ActionVerified(org, int(seq))
		b.Agent.Name = names[seq-1]
		return b
	}})
	other := deedtest.NewChain(6, deedtest.ChainOpts{Org: deedtest.Org(3)})
	ks := deedtest.KeySet()

	type gen func() (kind string, chain, strict bool, deeds [][]byte)
	pick := func(lines [][]byte) []byte { return lines[r.IntN(len(lines))] }
	clone := func(b []byte) []byte { return bytes.Clone(b) }
	window := func(n int) [][]byte { // n consecutive deeds of g from seq 1 (a chain must start at genesis)
		return append([][]byte(nil), g.Lines[:n]...)
	}
	valueEdit := func(raw []byte) []byte { // change one body value, keep the seal
		return edit(t, raw, func(m map[string]any) {
			b := body(m)
			switch r.IntN(4) {
			case 0:
				if a, ok := b["agent"].(map[string]any); ok {
					a["name"] = a["name"].(string) + "x"
					return
				}
				b["session"] = "ses-x"
			case 1:
				b["issued_at"] = "2026-10-02T09:00:00.001Z"
			case 2:
				if a, ok := b["action"].(map[string]any); ok {
					a["tool"] = "postgres.execute_all"
					return
				}
				b["org"] = deedtest.Org(4)
			default:
				b["prev_hash"] = deed.Digest([]byte("x"))
			}
		})
	}
	gens := []struct {
		weight int
		g      gen
	}{
		{50, func() (string, bool, bool, [][]byte) { return "valid", false, false, [][]byte{pick(g.Lines)} }},
		{30, func() (string, bool, bool, [][]byte) {
			return "unicode name", false, r.IntN(2) == 0, [][]byte{pick(uni.Lines)}
		}},
		{20, func() (string, bool, bool, [][]byte) {
			n := 2 + r.IntN(8)
			return "valid chain", true, false, window(n)
		}},
		{40, func() (string, bool, bool, [][]byte) {
			return "value edit", false, false, [][]byte{valueEdit(pick(g.Lines))}
		}},
		{110, func() (string, bool, bool, [][]byte) {
			b := clone(pick(append(g.Lines, uni.Lines...)))
			for range 1 + r.IntN(2) {
				i := r.IntN(len(b))
				b[i] ^= byte(1 << r.IntN(8))
			}
			return "bit flip", false, r.IntN(4) == 0, [][]byte{b}
		}},
		{25, func() (string, bool, bool, [][]byte) {
			raw := pick(g.Lines)
			return "key swap", false, false, [][]byte{edit(t, raw, func(m map[string]any) {
				ids := []string{ks.Keys[0].ID, ks.Keys[1].ID, ks.Keys[2].ID, deedtest.ID("sk", 9)}
				id := ids[r.IntN(len(ids))]
				switch r.IntN(3) {
				case 0:
					m["seal_key_id"] = id
				case 1:
					m["seal_key_id"], body(m)["seal_key_id"] = id, id
				default:
					body(m)["seal_key_id"] = id
				}
			})}
		}},
		{20, func() (string, bool, bool, [][]byte) {
			// Re-sealed by the other key or a revoked one: validity windows.
			env := g.Deeds[r.IntN(len(g.Deeds))]
			k := []deedtest.Key{deedtest.TestKey("a"), deedtest.TestKey("b"), deedtest.TestKey("r")}[r.IntN(3)]
			return "reseal", false, false, [][]byte{deedtest.Canonical(deedtest.Reseal(env, k, nil))}
		}},
		{8, func() (string, bool, bool, [][]byte) {
			env := g.Deeds[r.IntN(len(g.Deeds))]
			id := deedtest.ID("sk", 20+r.IntN(5))
			s, _ := deed.NewFileSigner(id, bytes.Repeat([]byte{byte(r.IntN(200) + 1)}, 32))
			return "unknown key", false, false, [][]byte{deedtest.Canonical(deedtest.Reseal(env, deedtest.Key{ID: id, Signer: s}, nil))}
		}},
		{6, func() (string, bool, bool, [][]byte) {
			n := 3 + r.IntN(7)
			c := window(n)
			i := 1 + r.IntN(n-1)
			c[i] = deedtest.Canonical(deedtest.Reseal(g.Deeds[i], deedtest.TestKey("a"), func(b *deed.Body) { b.PrevHash = deed.Genesis(g.Org) }))
			return "prev break", true, false, c
		}},
		{6, func() (string, bool, bool, [][]byte) {
			return "seal flip", false, false, [][]byte{edit(t, pick(g.Lines), func(m map[string]any) {
				sl := []byte(m["seal"].(string))
				i := len("ed25519:") + r.IntN(80)
				if sl[i] == 'A' {
					sl[i] = 'B'
				} else {
					sl[i] = 'A'
				}
				m["seal"] = string(sl)
			})}
		}},
		{25, func() (string, bool, bool, [][]byte) {
			n := 4 + r.IntN(8)
			c := window(n)
			i := 1 + r.IntN(n-2)
			return "gap", true, false, append(c[:i:i], c[i+1:]...)
		}},
		{30, func() (string, bool, bool, [][]byte) {
			n := 3 + r.IntN(8)
			c := window(n)
			i := r.IntN(n - 1)
			c[i], c[i+1] = c[i+1], c[i]
			return "reorder", true, false, c
		}},
		{15, func() (string, bool, bool, [][]byte) {
			n := 2 + r.IntN(8)
			c := window(n)
			i := r.IntN(n)
			return "duplicate", true, false, append(c[:i+1:i+1], c[i:]...)
		}},
		{15, func() (string, bool, bool, [][]byte) {
			n := 2 + r.IntN(3)
			c := window(n)
			return "mixed orgs", true, false, append(c, other.Lines[n])
		}},
		{20, func() (string, bool, bool, [][]byte) {
			s := string(pick(g.Lines))
			muts := []func(string) string{
				func(s string) string { return strings.Replace(s, `"seq":`, `"seq":1,"seq":`, 1) },
				func(s string) string { return strings.Replace(s, `"v":1`, `"v":1.0`, 1) },
				func(s string) string { return strings.Replace(s, `"v":1`, `"v":1e0`, 1) },
				func(s string) string { return strings.Replace(s, `"v":1`, `"v":9007199254740993`, 1) },
				func(s string) string { return strings.Replace(s, `"v":1`, `"v":-0`, 1) },
				func(s string) string { return strings.Replace(s, `"v":1`, `"v":01`, 1) },
				func(s string) string { return strings.Replace(s, `"object":"deed"`, `"object":"`+bs("dc00")+`"`, 1) },
				func(s string) string { return s + " x" },
				func(s string) string { return strings.Repeat("[", 17) + s + strings.Repeat("]", 17) },
				func(s string) string { return strings.Replace(s, `"object"`, `"obj`+bs("0065")+`ct"`, 1) },
				func(s string) string { return strings.Replace(s, `{`, "{\x7f", 1) },
				func(s string) string { return strings.Replace(s, `"deed"`, `"de`+"\t"+`ed"`, 1) },
				func(s string) string { return strings.TrimSuffix(s, "}") },
				func(s string) string { return "" },
				func(s string) string { return strings.Replace(s, `"v":1`, `"v":2`, 1) },
				func(s string) string { return strings.Replace(s, `"v":1`, `"v":3`, 1) },
			}
			return "strict json", false, false, [][]byte{[]byte(muts[r.IntN(len(muts))](s))}
		}},
		{25, func() (string, bool, bool, [][]byte) {
			raw := pick(g.Lines)
			// Unknown keys whose sort order differs between UTF-8 bytes and UTF-16 units.
			keySets := [][]string{{"note"}, {string(rune(0xe000)), "😀"}, {"😀", "zzz"}, {"ä", "b"}, {"B", "a"}}
			ks := keySets[r.IntN(len(keySets))]
			where := r.IntN(2)
			return "unknown field", false, false, [][]byte{edit(t, raw, func(m map[string]any) {
				target := m
				if where == 1 {
					target = body(m)
				}
				for _, k := range ks {
					target[k] = 1
				}
			})}
		}},
		{25, func() (string, bool, bool, [][]byte) {
			raw := pick(g.Lines)
			return "missing or wrong field", false, false, [][]byte{edit(t, raw, func(m map[string]any) {
				b := body(m)
				fields := []string{"v", "kind", "org", "seq", "agent", "action", "policy", "execution", "witness", "cost", "seal_key_id", "amends"}
				f := fields[r.IntN(len(fields))]
				switch r.IntN(3) {
				case 0:
					delete(b, f)
				case 1:
					b[f] = "x"
				default:
					b[f] = nil
				}
			})}
		}},
		{20, func() (string, bool, bool, [][]byte) {
			raw := pick(g.Lines)
			var ind bytes.Buffer
			_ = json.Indent(&ind, raw, "", strings.Repeat(" ", 1+r.IntN(3)))
			return "pretty", false, r.IntN(2) == 0, [][]byte{ind.Bytes()}
		}},
		{10, func() (string, bool, bool, [][]byte) {
			return "id mismatch", false, false, [][]byte{edit(t, pick(g.Lines), func(m map[string]any) { m["id"] = deedtest.ID("deed", r.IntN(50)) })}
		}},
	}
	total := 0
	for _, x := range gens {
		total += x.weight
	}
	if total != parityCases {
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

// runParity verifies one case the way deedglass verify does.
func runParity(t testing.TB, pc ParityCase, deeds [][]byte, ks *deed.KeySet) ParityExpected {
	t.Helper()
	v := deed.NewVerifier(deed.StreamOptions{Keys: ks, KeysPinned: true, Strict: pc.Strict})
	if pc.Chain {
		var b bytes.Buffer
		for _, d := range deeds {
			b.Write(d)
			b.WriteByte('\n')
		}
		if err := v.Stream(context.Background(), &b, "corpus"); err != nil {
			t.Fatal(err)
		}
	} else {
		v.Loose(deeds[0], "corpus")
	}
	rep := v.Report()
	e := ParityExpected{Case: pc.Case, Valid: rep.Result == deed.ResultValid, Deeds: rep.Deeds}
	if f := rep.FirstFailure(); f != nil {
		e.Code, e.Seq, e.Field = &f.Code, f.Seq, f.Field
	}
	return e
}

func TestParityCorpusCoversEveryDeedCode(t *testing.T) {
	_, expJSON := parityFiles(t)
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
	for _, c := range deed.Codes {
		switch c {
		case deed.CodeSealKeyRejected, deed.CodeKeysMissing, deed.CodeBundleInvalid, deed.CodeBundleSealInvalid, deed.CodeChainTimeRegression, deed.CodeChainGenesisMismatch:
			continue // key sets, missing keys and bundles are not deed cases; the last two are in the vectors
		}
		if seen[string(c)] == 0 {
			t.Errorf("no parity case produces %s", c)
		}
	}
	if valid < 60 || len(exp) != parityCases {
		t.Fatalf("%d valid of %d", valid, len(exp))
	}
}
