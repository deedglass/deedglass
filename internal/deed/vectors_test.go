package deed_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
	"github.com/deedglass/deedglass/internal/keys"
)

var update = flag.Bool("update", false, "rewrite testdata/deeds and its SHA256SUMS lines")

const vecRoot = "../../testdata/deeds"

// Expected is one vector's expected.json (S05's CLI and TypeScript verifier read it).
type Expected struct {
	Valid      bool    `json:"valid"`
	Code       *string `json:"code"`
	Seq        *int64  `json:"seq"`
	Field      *string `json:"field"`
	StrictCode *string `json:"strict_code"`
	Exit       int     `json:"exit"`
}

func pretty(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func jsonl(lines ...[]byte) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		b.Write(l)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// vectorFiles builds every file under testdata/deeds, deterministically, keyed by relative path.
func vectorFiles(t testing.TB) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	ks := deedtest.KeySet()
	files["keys/test-keys.json"] = pretty(t, ks)

	type so struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
		Hex       string `json:"hex"`
		Refused   string `json:"refused"`
	}
	var small []so
	for _, k := range deedtest.SmallOrderKeys {
		_, err := keys.ParsePublicKey(k.PublicKey())
		if err == nil {
			t.Fatalf("%s accepted", k.Name)
		}
		small = append(small, so{k.Name, base64.RawURLEncoding.EncodeToString(k.PublicKey()), k.Hex, deed.UnsafeReason(err)})
	}
	files["keys/small-order-keys.json"] = pretty(t, map[string]any{"object": "list", "note": "Ed25519 public key encodings every Deedglass verifier must refuse (attack case A13).", "data": small})

	// valid/: one chain of six deeds in org 1, sealed by key a.
	org1 := deedtest.Org(1)
	a := deedtest.TestKey("a")
	// Deed 4 amends deed 1; NewChain gives deed n of org 1 the id deedtest.ID("deed", 100000+n).
	orig := deedtest.ActionVerified(org1, 1)
	orig.DeedID = deedtest.ID("deed", 100001)
	bodies := []func(seq int64) deed.Body{
		func(seq int64) deed.Body { return deedtest.ActionVerified(org1, int(seq)) },
		func(seq int64) deed.Body { return deedtest.ActionDenied(org1, int(seq)) },
		func(seq int64) deed.Body { return deedtest.AdminStopped(org1, int(seq)) },
		func(seq int64) deed.Body { return deedtest.Amendment(&orig) },
		func(seq int64) deed.Body { return deedtest.ActionApproved(org1, int(seq)) },
		func(seq int64) deed.Body { return deedtest.UnicodeAgent(org1, int(seq)) },
	}
	valid := deedtest.NewChain(6, deedtest.ChainOpts{Org: org1, KeyFor: func(int64) deedtest.Key { return a },
		Body: func(_ string, seq int64) deed.Body { return bodies[seq-1](seq) }})
	if valid.Deeds[0].ID != orig.DeedID {
		t.Fatal("deed 1's id changed; fix the amendment's original")
	}
	names := []string{"01-action-verified-l2", "02-action-denied", "03-admin-agent-stopped", "04-amendment-mismatch", "05-action-approved-then-executed", "06-unicode-agent-name"}
	expectedAll := map[string]Expected{}
	for i, n := range names {
		files["valid/"+n+".json"] = valid.Lines[i]
		expectedAll["valid/"+n+".json"] = Expected{Valid: true}
	}
	files["valid/chain-01-06.jsonl"] = jsonl(valid.Lines...)
	expectedAll["valid/chain-01-06.jsonl"] = Expected{Valid: true}

	g := deedtest.NewChain(20, deedtest.ChainOpts{})
	files["chains/genesis-20.jsonl"] = jsonl(g.Lines...)
	expectedAll["chains/genesis-20.jsonl"] = Expected{Valid: true}

	raw := valid.Lines[0]
	var ind bytes.Buffer
	_ = json.Indent(&ind, raw, "", "  ")
	files["lenient/whitespace.json"] = append(ind.Bytes(), '\n')
	files["lenient/key-order.json"] = shuffleKeys(t, raw)
	nc := string(deed.CodeNonCanonical)
	expectedAll["lenient/whitespace.json"] = Expected{Valid: true, StrictCode: &nc}
	expectedAll["lenient/key-order.json"] = Expected{Valid: true, StrictCode: &nc}

	// COSE.
	cose, err := deed.EncodeCOSE(valid.Deeds[0], deedtest.TestKey("b").Signer, deedtest.Issuer, deedtest.T0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	files["cose/01-action-verified-l2.cose"] = cose
	expectedAll["cose/01-action-verified-l2.cose"] = Expected{Valid: true}
	files["expected.json"] = pretty(t, expectedAll)

	// invalid/: one directory per row of the S04 §6.1 table.
	for _, iv := range invalidVectors(t, valid, g) {
		dir := "invalid/" + iv.name + "/"
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
	files["worked-example.txt"] = workedExample(t, valid.Deeds[0], valid.Lines[0])
	files["messages.json"] = deed.MessageCatalogJSON()
	files["parity/corpus-500.jsonl"], files["parity/corpus-500.expected.json"] = parityFiles(t)
	files["README.md"] = []byte(vectorsReadme)
	addV2Vectors(t, files)
	return files
}

type invalidVector struct {
	name  string
	data  []byte
	chain bool
	keys  []byte
	exp   Expected
}

func expect(code deed.Code, seq int64, field string) Expected {
	c := string(code)
	e := Expected{Code: &c, Exit: 1}
	if seq > 0 {
		e.Seq = &seq
	}
	if field != "" {
		e.Field = &field
	}
	if code == deed.CodeUnsupportedVersion || code == deed.CodeUnsupportedAlgorithm {
		e.Exit = 2 // cannot determine (contract §10 exit codes)
	}
	// seal_key_unknown is 1 since S05 (§3.3): a deed naming a key outside the key set the verifier
	// was given fails; only "no key set at all" (keys_missing) cannot be determined.
	return e
}

func invalidVectors(t testing.TB, valid, g *deedtest.Chain) []invalidVector {
	env, raw := valid.Deeds[0], valid.Lines[0]
	a, b := deedtest.TestKey("a"), deedtest.TestKey("b")
	s := string(raw)
	flip := edit(t, raw, func(m map[string]any) {
		v, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(m["seal"].(string), "ed25519:"))
		v[0] ^= 1
		m["seal"] = "ed25519:" + base64.RawURLEncoding.EncodeToString(v)
	})
	noTag := edit(t, raw, func(m map[string]any) {
		v := ed25519Sign(a.Seed, []byte(env.BodyHash))
		m["seal"] = "ed25519:" + base64.RawURLEncoding.EncodeToString(v)
	})
	wrongKey := deedtest.Canonical(deedtest.Reseal(env, deedtest.Key{ID: a.ID}, nil, lying{id: a.ID, s: b.Signer}))
	unknown := deedtest.Canonical(deedtest.Reseal(env, deedtest.Key{ID: deedtest.ID("sk", 9), Signer: mustSigner(t, deedtest.ID("sk", 9), 9)}, nil))
	notYet := deedtest.Canonical(deedtest.Reseal(env, b, nil))
	late := deedtest.Canonical(deedtest.Reseal(env, a, func(x *deed.Body) { x.IssuedAt = deed.FormatTime(deedtest.ARetired) }))
	revoked := deedtest.Canonical(deedtest.Reseal(env, deedtest.TestKey("r"), nil))
	admin := valid.Lines[2]

	l := g.Lines
	gap := jsonl(l[0], l[1], l[3])
	dup := jsonl(l[0], l[1], l[1])
	prevBreak := jsonl(l[0], l[1], deedtest.Canonical(deedtest.Reseal(g.Deeds[2], a, func(x *deed.Body) { x.PrevHash = g.Deeds[0].BodyHash })))
	wrongGen := jsonl(deedtest.Canonical(deedtest.Reseal(g.Deeds[0], a, func(x *deed.Body) { x.PrevHash = deed.Genesis(deedtest.Org(3)) })))
	other := deedtest.NewChain(2, deedtest.ChainOpts{Org: deedtest.Org(3)})
	mixed := jsonl(l[0], other.Lines[1])
	regress := jsonl(l[0], l[1], l[2], l[3], deedtest.Canonical(deedtest.Reseal(g.Deeds[4], a, func(x *deed.Body) { x.IssuedAt = deed.FormatTime(deedtest.T0.Add(2 * time.Second)) })))

	ksSmall := func() []byte {
		ks := deedtest.KeySet()
		out := keySetJSON(t, ks)
		id := deedtest.SmallOrderKeys[0]
		x := base64.RawURLEncoding.EncodeToString(id.PublicKey())
		out = bytes.ReplaceAll(out, []byte(ks.Keys[1].PublicKey), []byte(x))
		out = bytes.Replace(out, []byte(ks.Keys[1].Thumbprint), []byte(deed.Thumbprint(id.PublicKey())), 1)
		var v any
		_ = json.Unmarshal(out, &v)
		return pretty(t, v)
	}()

	return []invalidVector{
		{name: "body-edit", data: edit(t, raw, func(m map[string]any) { body(m)["execution"].(map[string]any)["latency_ms"] = 42 }), exp: expect(deed.CodeBodyHashMismatch, 1, "")},
		{name: "seal-bit-flip", data: flip, exp: expect(deed.CodeSealInvalid, 1, "")},
		{name: "wrong-domain-tag", data: noTag, exp: expect(deed.CodeSealInvalid, 1, "")},
		{name: "wrong-key-known-id", data: wrongKey, exp: expect(deed.CodeSealInvalid, 1, "")},
		{name: "unknown-key-id", data: unknown, exp: expect(deed.CodeSealKeyUnknown, 1, "")},
		{name: "envelope-body-key-differ", data: edit(t, raw, func(m map[string]any) { m["seal_key_id"] = b.ID }), exp: expect(deed.CodeSealKeyMismatch, 1, "")},
		{name: "id-differs", data: edit(t, raw, func(m map[string]any) { m["id"] = deedtest.ID("deed", 7) }), exp: expect(deed.CodeIDMismatch, 1, "")},
		{name: "key-not-yet-active", data: notYet, exp: expect(deed.CodeSealKeyNotValidAt, 1, "")},
		{name: "key-retired", data: late, exp: expect(deed.CodeSealKeyNotValidAt, 1, "")},
		{name: "revoked-key", data: revoked, exp: expect(deed.CodeSealKeyRevoked, 1, "")},
		{name: "duplicate-key", data: []byte(strings.Replace(s, `"seq":1,`, `"seq":1,"seq":2,`, 1)), exp: expect(deed.CodeMalformed, 0, "")},
		{name: "fraction", data: []byte(strings.Replace(s, `"latency_ms":41`, `"latency_ms":41.0`, 1)), exp: expect(deed.CodeMalformed, 0, "")},
		{name: "big-integer", data: []byte(strings.Replace(s, `"seq":1,`, `"seq":9007199254740993,`, 1)), exp: expect(deed.CodeMalformed, 0, "")},
		{name: "negative-zero", data: []byte(strings.Replace(s, `"amount":120`, `"amount":-0`, 1)), exp: expect(deed.CodeMalformed, 0, "")},
		{name: "lone-surrogate", data: []byte(strings.Replace(s, `"name":"billing-agent"`, `"name":"`+bs("d800")+`"`, 1)), exp: expect(deed.CodeMalformed, 0, "")},
		{name: "unknown-field", data: edit(t, raw, func(m map[string]any) { body(m)["note"] = "x" }), exp: expect(deed.CodeSchemaInvalid, 1, "body.note")},
		{name: "missing-field", data: edit(t, raw, func(m map[string]any) { delete(body(m), "amends") }), exp: expect(deed.CodeSchemaInvalid, 1, "body.amends")},
		{name: "wrong-kind-presence", data: edit(t, admin, func(m map[string]any) {
			body(m)["policy"] = map[string]any{"id": nil, "version_id": nil, "version_hash": nil, "mode": "enforce", "decision": "allow", "rule_id": nil}
		}), exp: expect(deed.CodeSchemaInvalid, 3, "body.policy")},
		{name: "version-3", data: edit(t, raw, func(m map[string]any) { body(m)["v"] = 3 }), exp: expect(deed.CodeUnsupportedVersion, 1, "")},
		{name: "chain-seq-gap", data: gap, chain: true, exp: expect(deed.CodeChainSeqGap, 4, "")},
		{name: "chain-duplicate-seq", data: dup, chain: true, exp: expect(deed.CodeChainSeqOutOfOrder, 2, "")},
		{name: "chain-prev-break", data: prevBreak, chain: true, exp: expect(deed.CodeChainPrevHashMismatch, 3, "")},
		{name: "chain-wrong-genesis", data: wrongGen, chain: true, exp: expect(deed.CodeChainGenesisMismatch, 1, "")},
		{name: "chain-mixed-orgs", data: mixed, chain: true, exp: expect(deed.CodeChainOrgMismatch, 2, "")},
		{name: "chain-time-regression", data: regress, chain: true, exp: expect(deed.CodeChainTimeRegression, 5, "")},
		{name: "small-order-key-in-set", data: raw, keys: ksSmall, exp: expect(deed.CodeSealKeyRejected, 0, "")},
	}
}

func ed25519Sign(seed, msg []byte) []byte { return ed25519.Sign(ed25519.NewKeyFromSeed(seed), msg) }

// workedExample is the byte-level example DEED_SPEC.md §16 quotes.
func workedExample(t testing.TB, env *deed.Envelope, line []byte) []byte {
	t.Helper()
	bodyJCS, err := deed.StrictCanonical(&env.Body)
	if err != nil {
		t.Fatal(err)
	}
	a := deedtest.TestKey("a")
	in := deed.SealInput(env.BodyHash)
	var b strings.Builder
	fmt.Fprintf(&b, "Deed v1 worked example (testdata/deeds/valid/01-action-verified-l2.json). TEST KEY: never trust it.\n\n")
	fmt.Fprintf(&b, "seal key id      %s\n", a.ID)
	fmt.Fprintf(&b, "seed (hex)       %s   = SHA-256(\"deedglass/test-vector-key/a\")\n", hex.EncodeToString(a.Seed))
	fmt.Fprintf(&b, "public key       %s   (base64url)\n", base64.RawURLEncoding.EncodeToString(a.Signer.PublicKey()))
	fmt.Fprintf(&b, "thumbprint       %s\n\n", deed.Thumbprint(a.Signer.PublicKey()))
	fmt.Fprintf(&b, "org              %s\n", env.Body.Org)
	fmt.Fprintf(&b, "genesis input    \"deedglass/genesis\\n%s\"\n", env.Body.Org)
	fmt.Fprintf(&b, "prev_hash (seq 1) %s\n\n", deed.Genesis(env.Body.Org))
	fmt.Fprintf(&b, "JCS(body), %d bytes:\n%s\n\n", len(bodyJCS), bodyJCS)
	fmt.Fprintf(&b, "body_hash        %s\n\n", env.BodyHash)
	fmt.Fprintf(&b, "seal input, %d bytes (\"deedglass/deed/v1\\n\" + body_hash), hex:\n%s\n\n", len(in), hex.EncodeToString(in))
	fmt.Fprintf(&b, "seal             %s\n\n", env.Seal)
	fmt.Fprintf(&b, "JCS(envelope), %d bytes, sha256 %x:\n%s\n", len(line), sha256.Sum256(line), line)
	return []byte(b.String())
}

func TestGoldenVectors(t *testing.T) {
	files := vectorFiles(t)
	if *update {
		if err := os.RemoveAll(vecRoot); err != nil {
			t.Fatal(err)
		}
		for p, b := range files {
			full := filepath.Join(vecRoot, p)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		writeSums(t, files)
	}
	// Byte-identical with what is on disk, and nothing extra on disk.
	onDisk := map[string]bool{}
	_ = filepath.WalkDir(vecRoot, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(vecRoot, p)
			onDisk[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	for p, want := range files {
		got, err := os.ReadFile(filepath.Join(vecRoot, p))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s differs from the generator; run go test ./internal/deed -run TestGoldenVectors -update", p)
		}
		delete(onDisk, p)
	}
	for p := range onDisk {
		t.Errorf("%s is on disk but not generated", p)
	}
	checkSums(t, files)

	// Every vector's expectation is what Go returns.
	ks, err := deed.ParseKeySet(files["keys/test-keys.json"])
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]Expected
	if err := json.Unmarshal(files["expected.json"], &all); err != nil {
		t.Fatal(err)
	}
	for p, want := range all {
		got := runVector(t, files[p], strings.HasSuffix(p, ".jsonl"), strings.HasSuffix(p, ".cose"), ks)
		if !sameExpected(got, want) {
			t.Errorf("%s: got %s, want %s", p, pretty(t, got), pretty(t, want))
		}
	}
	dirs := map[string]bool{}
	for p := range files {
		if strings.HasPrefix(p, "invalid/") {
			dirs[strings.Join(strings.Split(p, "/")[:2], "/")] = true
		}
	}
	if len(dirs) != 26 {
		t.Errorf("%d invalid vectors, want 26", len(dirs))
	}
	checkV2Vectors(t, files)
	for d := range dirs {
		var want Expected
		if err := json.Unmarshal(files[d+"/expected.json"], &want); err != nil {
			t.Fatal(err)
		}
		vks := ks
		if kb, ok := files[d+"/keys.json"]; ok {
			vks, err = deed.ParseKeySet(kb)
			if err != nil {
				got := toExpected(err, nil)
				if !sameExpected(got, want) {
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

// runVector verifies like S05's `deedglass verify` will: each deed, then the chain.
func runVector(t testing.TB, data []byte, chain, isCOSE bool, ks *deed.KeySet) Expected {
	t.Helper()
	if isCOSE {
		_, err := deed.VerifyCOSE(data, ks)
		return toExpected(err, nil)
	}
	lines := [][]byte{data}
	if chain {
		lines = bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	}
	run := func(strict bool) error {
		cc := deed.NewChainChecker(deed.ChainOptions{})
		for _, l := range lines {
			r, err := deed.VerifyEnvelope(l, ks, deed.VerifyOptions{Strict: strict})
			if err != nil {
				return err
			}
			if chain {
				if err := cc.Next(r); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return toExpected(run(false), run(true))
}

func toExpected(err, strictErr error) Expected {
	if err != nil {
		var ve *deed.VerifyError
		if !errors.As(err, &ve) {
			return Expected{Exit: -1}
		}
		return expect(ve.Code, ve.Seq, ve.Field)
	}
	e := Expected{Valid: true}
	if strictErr != nil {
		c := string(deed.CodeOf(strictErr))
		e.StrictCode = &c
	}
	return e
}

func sameExpected(a, b Expected) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

const sumsPath = "../../testdata/SHA256SUMS"

func sumLines(files map[string][]byte) []string {
	var out []string
	for p, b := range files {
		out = append(out, fmt.Sprintf("%x  deeds/%s", sha256.Sum256(b), p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i][66:] < out[j][66:] })
	return out
}

func writeSums(t testing.TB, files map[string][]byte) {
	raw, err := os.ReadFile(sumsPath)
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, l := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if !strings.Contains(l, "  deeds/") {
			keep = append(keep, l)
		}
	}
	keep = append(keep, sumLines(files)...)
	if err := os.WriteFile(sumsPath, []byte(strings.Join(keep, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func checkSums(t testing.TB, files map[string][]byte) {
	raw, err := os.ReadFile(sumsPath)
	if err != nil {
		t.Fatal(err)
	}
	var have []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, "  deeds/") {
			have = append(have, l)
		}
	}
	if !slices.Equal(have, sumLines(files)) {
		t.Error("testdata/SHA256SUMS deeds/ lines are stale; run with -update")
	}
}

const vectorsReadme = `# Deed v1 and v2 test vectors

Generated by ` + "`go test ./internal/deed -run TestGoldenVectors -update`" + ` from ` + "`internal/deed/deedtest`" + `.
Never edit a file here by hand: the test regenerates every file and fails on any difference, and
` + "`testdata/SHA256SUMS`" + ` pins each one (` + "`make testdata-check`" + `).

**Test keys only. Never trust them.** The three seal keys in ` + "`keys/test-keys.json`" + ` have public
seeds (SHA-256 of ` + "`deedglass/test-vector-key/a`" + `, ` + "`/b`" + `, ` + "`/r`" + `). Deedglass refuses to load them
in prod. A deed sealed by one of them means nothing outside these tests.

| Path | What it is |
|---|---|
| ` + "`keys/test-keys.json`" + ` | The key set every vector verifies against: key a (retired at T0+10s), b (active from T0+5s), r (revoked) |
| ` + "`keys/small-order-keys.json`" + ` | Ed25519 encodings every verifier must refuse (attack case A13) |
| ` + "`valid/*.json`" + ` | One deed each, exactly JCS(envelope), no trailing newline; together a chain of six (` + "`valid/chain-01-06.jsonl`" + `) |
| ` + "`chains/genesis-20.jsonl`" + ` | Seq 1–20 of one org; key a seals 1–10, key b seals 11–20 |
| ` + "`lenient/*.json`" + ` | Valid deeds that are not canonical bytes (pretty-printed, keys reordered): valid by default, ` + "`non_canonical`" + ` under ` + "`--strict`" + ` |
| ` + "`cose/*.cose`" + ` | The optional COSE_Sign1 encoding (alg -8, signed by key b at T0+1h) |
| ` + "`invalid/<name>/`" + ` | ` + "`deed.json`" + ` or ` + "`deeds.jsonl`" + `, an optional ` + "`keys.json`" + ` that replaces the test key set, and ` + "`expected.json`" + ` |
| ` + "`expected.json`" + ` | The expectation for every valid, lenient, chain and COSE vector |
| ` + "`worked-example.txt`" + ` | Every intermediate byte of ` + "`valid/01-action-verified-l2.json`" + ` (DEED_SPEC.md §16) |
| ` + "`v2/`" + ` | Deed v2 (ADR-109): ` + "`v2/keys.json`" + ` (a, b and r, plus key x of an algorithm no verifier implements), ` + "`v2/valid/`" + `, ` + "`v2/lenient/`" + `, ` + "`v2/invalid/<name>/`" + `, ` + "`v2/expected.json`" + ` and ` + "`v2/parity/corpus-200.*`" + `, in the same layout as above |

` + "`expected.json`" + ` fields: ` + "`valid`" + `; ` + "`code`" + ` (a DEED_SPEC.md §11 code, the first failure); ` + "`seq`" + ` (the
deed it names, or null when the deed is unreadable); ` + "`field`" + ` (for ` + "`schema_invalid`" + `); ` + "`strict_code`" + `
(what ` + "`--strict`" + ` adds); ` + "`exit`" + ` (the ` + "`deedglass verify`" + ` exit code: 0 valid, 1 invalid, 2 cannot
determine, such as a newer format; a deed whose key is not in the given key set exits 1).

A verifier in another language passes when it returns the same ` + "`code`" + `, ` + "`seq`" + ` and ` + "`field`" + ` for
every vector.
`
