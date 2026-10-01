package identity

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/intent"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// The shared signed-intent vectors (testdata/intents/, docs/INTENTS.md §11). They are generated
// here and pinned in testdata/SHA256SUMS; never edit one by hand:
//
//	go test ./internal/identity -run TestIntentVectors -update-intents
//
// TestIntentVectors runs every file through the checker (checks 1–13, with an in-memory nonce
// ledger), and every valid file with a sign block through the reference signer, which must
// reproduce the header byte for byte.

var updateIntents = flag.Bool("update-intents", false, "rewrite testdata/intents/")

const vectorDir = "../../testdata/intents"

// RFC 8032 §7.1 TEST 1 and TEST 2 seeds: public test keys, never for anything else.
const (
	seedTest1 = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	seedTest2 = "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb"
	vecOrg    = "org_01k6h7kp6msmzrhdzwtcr3jnkp"
	vecAgent  = "agt_01k633rrmh3bcz2p39gg4vp46q"
	vecName   = "support-bot"
)

type vecKeys struct {
	Comment    string          `json:"comment"`
	Org        string          `json:"org"`
	Agent      string          `json:"agent"`
	AgentName  string          `json:"agent_name"`
	IntentMode string          `json:"intent_mode"`
	Registered []vecRegistered `json:"registered"`
	Keys       map[string]any  `json:"keys"`
}

type vecRegistered struct {
	KID       string `json:"kid"`
	PublicKey string `json:"public_key"`
}

type vecSign struct {
	Key        string `json:"key"`
	Org        string `json:"org"`
	Agent      string `json:"agent"`
	Tool       string `json:"tool"`
	Args       string `json:"args"`
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
	Session    string `json:"session,omitempty"`
	Now        string `json:"now"`
	TTLMillis  int64  `json:"ttl_ms"`
	Nonce      string `json:"nonce"`
}

type vecExpected struct {
	OK          bool   `json:"ok"`
	Status      int    `json:"status,omitempty"`
	Code        string `json:"code,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Param       string `json:"param,omitempty"`
	IntentHash  string `json:"intent_hash,omitempty"`
	ArgsDigest  string `json:"args_digest,omitempty"`
	ArgsJCS     string `json:"args_canonical,omitempty"`
	KID         string `json:"kid,omitempty"`
	OnBehalfOf  string `json:"on_behalf_of,omitempty"`
	Session     string `json:"session,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	IntentBytes int    `json:"intent_bytes,omitempty"`
}

type vector struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Header      string            `json:"header"`
	Tool        string            `json:"tool"`
	Args        string            `json:"args"` // the exact JSON bytes sent with the call
	Headers     map[string]string `json:"headers,omitempty"`
	Now         string            `json:"now"`
	Sends       int               `json:"sends"`
	Sign        *vecSign          `json:"sign,omitempty"`
	Expected    vecExpected       `json:"expected"`
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func seedKey(seed string) ed25519.PrivateKey {
	b, err := hex.DecodeString(seed)
	if err != nil {
		panic(err)
	}
	return ed25519.NewKeyFromSeed(b)
}

// rawIntent builds the intent bytes for a field map: sorted keys, compact, which is JCS for these
// ASCII fields. A test can then corrupt the bytes before signing.
func rawIntent(f map[string]any) []byte {
	b, err := json.Marshal(f)
	if err != nil {
		panic(err)
	}
	return b
}

func signRaw(priv ed25519.PrivateKey, raw []byte, domain bool) string {
	in := []byte(intent.Digest(raw))
	if domain {
		in = intent.SigningInput(intent.Digest(raw))
	}
	return intent.JoinHeader(raw, ed25519.Sign(priv, in))
}

func baseFields(nonce string, iat int64, ttl int64, args string) map[string]any {
	d, err := intent.ArgsDigest([]byte(args))
	if err != nil {
		panic(err)
	}
	kid := testKID(seedKey(seedTest1))
	return map[string]any{"v": intent.AgentVersion, "org": vecOrg, "agent": vecAgent, "kid": kid, "tool": "stripe.refund",
		"args_digest": d, "nonce": nonce, "iat": iat, "exp": iat + ttl}
}

func testKID(priv ed25519.PrivateKey) string {
	_, kid, err := ParseIntentPublicKey(FormatIntentPublicKey(priv.Public().(ed25519.PublicKey)))
	if err != nil {
		panic(err)
	}
	return kid
}

// buildVectors is the vector set of S09 §6.
func buildVectors(t *testing.T) []vector {
	k1 := seedKey(seedTest1)
	k2 := seedKey(seedTest2)
	at := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	iat := at.UnixMilli()
	nowS := func(d time.Duration) string { return at.Add(d).Format(time.RFC3339Nano) }
	refund := `{"charge":"ch_3QxZ8f2L","amount":1200}`
	nonce := func(i int) string { return fmt.Sprintf("05c18be68a24c056cd6c7c8910dc%04x", i) }
	signer := func(v *vecSign) string {
		pemBytes := pemOf(t, seedKey(map[string]string{"test1": seedTest1, "test2": seedTest2}[v.Key]))
		s, err := deedglass.LoadIntentSigner(pemBytes, v.Org, v.Agent)
		if err != nil {
			t.Fatal(err)
		}
		now, _ := time.Parse(time.RFC3339Nano, v.Now)
		h, _, err := s.Sign(v.Tool, json.RawMessage(v.Args), deedglass.SignOptions{OnBehalfOf: v.OnBehalfOf, Session: v.Session,
			TTL: time.Duration(v.TTLMillis) * time.Millisecond, Now: func() time.Time { return now }, Nonce: v.Nonce})
		if err != nil {
			t.Fatalf("sign %+v: %v", v, err)
		}
		return h
	}
	valid := func(name, desc, args string, obo, ses string, ttl int64, now string, n int) vector {
		sg := &vecSign{Key: "test1", Org: vecOrg, Agent: vecAgent, Tool: "stripe.refund", Args: args, OnBehalfOf: obo, Session: ses,
			Now: at.Format(time.RFC3339), TTLMillis: ttl, Nonce: nonce(n)}
		if name == "v01-refund-obo" {
			sg.Nonce = "05c18be68a24c056cd6c7c8910dc0729"
		}
		hdrs := map[string]string{}
		if obo != "" {
			hdrs[HeaderOnBehalfOf] = obo
		}
		return vector{Name: name, Description: desc, Header: signer(sg), Tool: "stripe.refund", Args: args, Headers: hdrs, Now: now, Sends: 1, Sign: sg,
			Expected: vecExpected{OK: true}}
	}
	vs := []vector{
		valid("v01-refund-obo", "The S09 §3.2 cross-check vector: a refund on behalf of usr_ext_4821 in session conv_9f2c. Deedglass-On-Behalf-Of is sent too, with the same value (allowed).",
			refund, "usr_ext_4821", "conv_9f2c", 60000, nowS(10*time.Second), 1),
		valid("v02-minimal", "No on_behalf_of or session.", refund, "", "", 60000, nowS(10*time.Second), 2),
		valid("v03-nested-unicode", "Nested arguments with non-ASCII text; JCS keeps UTF-8 as is and sorts keys by UTF-16 code units.",
			`{"note":"café 東京","items":[{"sku":"A-1","qty":2},{"sku":"B-7","qty":1}],"meta":{"z":true,"a":null}}`, "", "", 60000, nowS(time.Second), 3),
		valid("v04-floats", "Fractions are allowed in arguments and canonicalize as IEEE-754 doubles: 1e21 → 1e+21, 0.0000001 → 1e-7, -0 → 0.",
			`{"a":12.5,"b":1e21,"c":0.0000001,"d":0.1,"e":-0}`, "", "", 60000, nowS(time.Second), 4),
		valid("v05-empty-args", "Empty arguments: the digest of {}.", `{}`, "", "", 60000, nowS(time.Second), 5),
		valid("v06-big-number-as-string", "An integer above 2^53−1 sent as a string, as the docs recommend.",
			`{"amount":"12345678901234567890","currency":"usd"}`, "", "", 60000, nowS(time.Second), 6),
		valid("v07-ttl-300000", "The longest lifetime: exp − iat = 300000 ms.", refund, "", "", 300000, nowS(time.Minute), 7),
		valid("v08-skew-inside", "now = exp + 59 s: still inside the 60 s skew.", refund, "", "", 60000, nowS(60*time.Second+59*time.Second), 8),
	}
	v01 := vs[0]
	v02 := vs[1]
	// Session header on v01 too (same value, allowed).
	vs[0].Headers[HeaderSession] = "conv_9f2c"

	bad := func(name, desc, header, tool, args string, hdrs map[string]string, now string, sends int, reason string) vector {
		return vector{Name: name, Description: desc, Header: header, Tool: tool, Args: args, Headers: hdrs, Now: now, Sends: sends,
			Expected: vecExpected{Reason: reason}}
	}
	f := func(n int) map[string]any { return baseFields(nonce(n), iat, 60000, refund) }
	now10 := nowS(10 * time.Second)
	good := signRaw(k1, rawIntent(f(20)), true)
	part1, part2, _ := strings.Cut(good, ".")
	_, sigBytes, _ := intent.SplitHeader(good)

	big := f(24)
	big["zzpadding"] = strings.Repeat("x", 2049)
	bigRaw := rawIntent(big)
	bigRaw = append(bigRaw[:0:0], bigRaw...)
	if over := len(bigRaw) - 2049; over > 0 {
		big["zzpadding"] = strings.Repeat("x", 2049-over)
		bigRaw = rawIntent(big)
	}

	dup := strings.Replace(string(rawIntent(f(25))), `{"agent":"`+vecAgent+`"`, `{"agent":"`+vecAgent+`","agent":"`+vecAgent+`"`, 1)
	f26 := f(26)
	f26["iat"] = int64(1) << 53
	f26["exp"] = int64(1)<<53 + 60000
	unsorted := strings.Replace(string(rawIntent(f(27))), `{"agent":"`+vecAgent+`","args_digest"`, `{"args_digest"`, 1)
	unsorted = strings.Replace(unsorted, `,"exp":`, `,"agent":"`+vecAgent+`","exp":`, 1)
	spaced := strings.Replace(string(rawIntent(f(28))), `"tool":"`, `"tool": "`, 1)
	unknown := f(29)
	unknown["amount"] = 1200
	missing := f(30)
	delete(missing, "nonce")
	zz := f(31)
	zz["nonce"] = strings.Repeat("Z", 32)
	v2 := f(32)
	v2["v"] = "deedglass.intent/2"
	otherOrg := f(33)
	otherOrg["org"] = "org_01k6h7kp6msmzrhdzwtcr3jnkq"
	otherAgent := f(34)
	otherAgent["agent"] = "agt_01k633rrmh3bcz2p39gg4vp4a1"
	unknownKid := f(35)
	unknownKid["kid"] = testKID(k2)
	flipped := []byte(sigBytes)
	flipped[10] ^= 0x01
	_ = part2
	expiredF := f(38)
	future := baseFields(nonce(39), at.Add(10*time.Second+61*time.Second).UnixMilli(), 60000, refund)
	ttl301 := baseFields(nonce(40), iat, 300001, refund)
	ttl0 := baseFields(nonce(41), iat, 0, refund)

	vs = append(vs,
		bad("i01-bad-base64", "The intent part isn't base64url.", "!!!!"+"."+strings.SplitN(good, ".", 2)[1], "stripe.refund", refund, nil, now10, 1, "syntax"),
		bad("i02-three-parts", "Three dot-separated parts.", good+".AAAA", "stripe.refund", refund, nil, now10, 1, "syntax"),
		bad("i03-63-byte-signature", "The signature is 63 bytes.", part1+"."+b64(sigBytes[:63]), "stripe.refund", refund, nil, now10, 1, "syntax"),
		bad("i04-2049-bytes", "The intent is 2,049 bytes.", signRaw(k1, bigRaw, true), "stripe.refund", refund, nil, now10, 1, "too_large"),
		bad("i05-duplicate-key", `"agent" appears twice.`, signRaw(k1, []byte(dup), true), "stripe.refund", refund, nil, now10, 1, "strict"),
		bad("i06-integer-2pow53", "iat is 2^53, above the largest exact integer.", signRaw(k1, rawIntent(f26), true), "stripe.refund", refund, nil, now10, 1, "strict"),
		bad("i07-keys-unsorted", "The keys aren't in JCS order.", signRaw(k1, []byte(unsorted), true), "stripe.refund", refund, nil, now10, 1, "not_canonical"),
		bad("i08-whitespace", "A space after a colon.", signRaw(k1, []byte(spaced), true), "stripe.refund", refund, nil, now10, 1, "not_canonical"),
		bad("i09-unknown-field", `An unknown field "amount".`, signRaw(k1, rawIntent(unknown), true), "stripe.refund", refund, nil, now10, 1, "schema"),
		bad("i10-missing-nonce", "No nonce.", signRaw(k1, rawIntent(missing), true), "stripe.refund", refund, nil, now10, 1, "schema"),
		bad("i11-nonce-pattern", "The nonce isn't lowercase hex.", signRaw(k1, rawIntent(zz), true), "stripe.refund", refund, nil, now10, 1, "schema"),
		bad("i12-version-2", "v is deedglass.intent/2.", signRaw(k1, rawIntent(v2), true), "stripe.refund", refund, nil, now10, 1, "version"),
		bad("i13-other-org", "The intent names another org.", signRaw(k1, rawIntent(otherOrg), true), "stripe.refund", refund, nil, now10, 1, "org_mismatch"),
		bad("i14-other-agent", "The intent names another agent.", signRaw(k1, rawIntent(otherAgent), true), "stripe.refund", refund, nil, now10, 1, "agent_mismatch"),
		bad("i15-unknown-kid", "Signed with the TEST 2 key, which isn't registered.", signRaw(k2, rawIntent(unknownKid), true), "stripe.refund", refund, nil, now10, 1, "unknown_kid"),
		bad("i16-flipped-signature-bit", "One bit of the signature flipped.", part1+"."+b64(flipped), "stripe.refund", refund, nil, now10, 1, "bad_signature"),
		bad("i17-no-domain-tag", `Signed over intent_hash without the "deedglass/intent/v1\n" prefix.`, signRaw(k1, rawIntent(f(37)), false), "stripe.refund", refund, nil, now10, 1, "bad_signature"),
		bad("i18-expired", "now = exp + 61 s.", signRaw(k1, rawIntent(expiredF), true), "stripe.refund", refund, nil, nowS(60*time.Second+61*time.Second), 1, "expired"),
		bad("i19-not-yet-valid", "iat = now + 61 s.", signRaw(k1, rawIntent(future), true), "stripe.refund", refund, nil, now10, 1, "not_yet_valid"),
		bad("i20-ttl-300001", "exp − iat = 300001 ms.", signRaw(k1, rawIntent(ttl301), true), "stripe.refund", refund, nil, now10, 1, "ttl"),
		bad("i21-exp-equals-iat", "exp = iat.", signRaw(k1, rawIntent(ttl0), true), "stripe.refund", refund, nil, nowS(0), 1, "ttl"),
		bad("i22-tool-differs", "Signed for stripe.refund; the call is stripe.create_charge.", v02.Header, "stripe.create_charge", refund, nil, now10, 1, "tool_mismatch"),
		bad("i23-amount-changed", "The body says amount 120000; the intent signed 1200.", v02.Header, "stripe.refund", `{"charge":"ch_3QxZ8f2L","amount":120000}`, nil, now10, 1, "args_mismatch"),
		bad("i24-obo-differs", "Deedglass-On-Behalf-Of says usr_ext_9; the intent says usr_ext_4821.", v01.Header, "stripe.refund", refund,
			map[string]string{HeaderOnBehalfOf: "usr_ext_9"}, now10, 1, "obo_mismatch"),
		bad("i25-unsigned-obo", "Deedglass-On-Behalf-Of is sent; the intent has no on_behalf_of.", v02.Header, "stripe.refund", refund,
			map[string]string{HeaderOnBehalfOf: "usr_ext_4821"}, now10, 1, "unsigned_obo"),
		bad("i26-replay", "v01 sent twice: the second send is refused.", v01.Header, "stripe.refund", refund,
			map[string]string{HeaderOnBehalfOf: "usr_ext_4821", HeaderSession: "conv_9f2c"}, now10, 2, "replay"),
	)
	// Fill the expected outcome from the checker itself, then the test re-checks every file; the
	// reasons above are asserted, so a generator bug can't silently change them.
	for i := range vs {
		want := vs[i].Expected.Reason
		got := runVector(t, vs[i])
		if want != "" && got.Reason != want {
			t.Fatalf("%s: reason %q, want %q", vs[i].Name, got.Reason, want)
		}
		if want == "" && !got.OK {
			t.Fatalf("%s: refused: %+v", vs[i].Name, got)
		}
		vs[i].Expected = got
	}
	return vs
}

// runVector checks one vector with a fresh in-memory nonce ledger and returns the outcome of the
// last send.
func runVector(t *testing.T, v vector) vecExpected {
	t.Helper()
	k1 := seedKey(seedTest1)
	a := intentAgent{org: mustOrg(vecOrg), id: mustAgent(vecAgent), name: vecName, mode: IntentOptional,
		keys: []IntentKey{{KID: testKID(k1), PublicKey: FormatIntentPublicKey(k1.Public().(ed25519.PublicKey)), pub: k1.Public().(ed25519.PublicKey)}}}
	now, err := time.Parse(time.RFC3339Nano, v.Now)
	if err != nil {
		t.Fatal(err)
	}
	var obo OBO
	if v.Headers != nil {
		obo = OBO{OnBehalfOf: v.Headers[HeaderOnBehalfOf], Session: v.Headers[HeaderSession]}
	}
	used := map[string]bool{}
	var out vecExpected
	for range max(v.Sends, 1) {
		res, err := verifyIntent(a, IntentInput{Header: v.Header, Tool: v.Tool, Args: json.RawMessage(v.Args), OBO: obo, Now: now})
		if err == nil && used[res.Nonce] {
			err = refuse("replay", "", "time", "09:30:10")
		}
		if err != nil {
			e := httpx.AsError(err)
			out = vecExpected{Status: e.Status, Code: string(e.Code), Reason: IntentReason(err), Param: e.Param}
			continue
		}
		used[res.Nonce] = true
		canon, _ := intent.CanonicalArgs([]byte(v.Args))
		raw, _, _ := intent.SplitHeader(v.Header)
		out = vecExpected{OK: true, IntentHash: res.IntentHash, ArgsDigest: res.ArgsDigest, ArgsJCS: string(canon), KID: res.KID,
			OnBehalfOf: res.OBO.OnBehalfOf, Session: res.OBO.Session, ExpiresAt: res.ExpiresAt.Format("2006-01-02T15:04:05.000Z"), IntentBytes: len(raw)}
	}
	return out
}

func mustOrg(s string) ids.OrgID {
	id, err := ids.Parse[ids.OrgKind](s)
	if err != nil {
		panic(err)
	}
	return id
}

func mustAgent(s string) ids.AgentID {
	id, err := ids.Parse[ids.AgentKind](s)
	if err != nil {
		panic(err)
	}
	return id
}

func pemOf(t *testing.T, priv ed25519.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func writeVectors(t *testing.T, vs []vector) {
	k1, k2 := seedKey(seedTest1), seedKey(seedTest2)
	for _, d := range []string{"valid", "invalid", "keys"} {
		if err := os.MkdirAll(filepath.Join(vectorDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string, b []byte) {
		if err := os.WriteFile(filepath.Join(vectorDir, p), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("keys/test1.pem", pemOf(t, k1))
	write("keys/test2.pem", pemOf(t, k2))
	kf := vecKeys{
		Comment: "RFC 8032 §7.1 TEST 1 and TEST 2 keys. They are public: test only, never use them for anything else. " +
			"The agent below has intent_mode optional and exactly one registered key, test1.",
		Org: vecOrg, Agent: vecAgent, AgentName: vecName, IntentMode: string(IntentOptional),
		Registered: []vecRegistered{{KID: testKID(k1), PublicKey: FormatIntentPublicKey(k1.Public().(ed25519.PublicKey))}},
		Keys: map[string]any{
			"test1": map[string]string{"seed_hex": seedTest1, "pem": "keys/test1.pem", "kid": testKID(k1), "public_key": FormatIntentPublicKey(k1.Public().(ed25519.PublicKey))},
			"test2": map[string]string{"seed_hex": seedTest2, "pem": "keys/test2.pem", "kid": testKID(k2), "public_key": FormatIntentPublicKey(k2.Public().(ed25519.PublicKey))},
		},
	}
	b, _ := json.MarshalIndent(kf, "", "  ")
	write("keys.json", append(b, '\n'))
	for _, v := range vs {
		dir := "invalid"
		if v.Expected.OK {
			dir = "valid"
		}
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(dir, v.Name+".json"), append(b, '\n'))
	}
}

func readVectors(t *testing.T) []vector {
	var vs []vector
	for _, d := range []string{"valid", "invalid"} {
		files, err := filepath.Glob(filepath.Join(vectorDir, d, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(files)
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var v vector
			if err := json.Unmarshal(b, &v); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			vs = append(vs, v)
		}
	}
	return vs
}

func TestIntentVectors(t *testing.T) {
	if *updateIntents {
		writeVectors(t, buildVectors(t))
	}
	vs := readVectors(t)
	if len(vs) != 34 {
		t.Fatalf("%d vectors, want 34 (v01–v08, i01–i26); regenerate with -update-intents", len(vs))
	}
	for _, v := range vs {
		t.Run(v.Name, func(t *testing.T) {
			got := runVector(t, v)
			if got != v.Expected {
				t.Errorf("checker:\n got  %+v\n want %+v", got, v.Expected)
			}
			if !v.Expected.OK {
				if v.Expected.Status != 403 || !strings.HasPrefix(v.Expected.Code, "intent_") {
					t.Errorf("a refused intent must be 403 intent_*: %+v", v.Expected)
				}
				return
			}
			if v.Sign == nil {
				t.Fatal("a valid vector carries its sign block")
			}
			pemBytes, err := os.ReadFile(filepath.Join(vectorDir, "keys", v.Sign.Key+".pem"))
			if err != nil {
				t.Fatal(err)
			}
			s, err := deedglass.LoadIntentSigner(pemBytes, v.Sign.Org, v.Sign.Agent)
			if err != nil {
				t.Fatal(err)
			}
			now, _ := time.Parse(time.RFC3339Nano, v.Sign.Now)
			h, hash, err := s.Sign(v.Sign.Tool, json.RawMessage(v.Sign.Args), deedglass.SignOptions{OnBehalfOf: v.Sign.OnBehalfOf, Session: v.Sign.Session,
				TTL: time.Duration(v.Sign.TTLMillis) * time.Millisecond, Now: func() time.Time { return now }, Nonce: v.Sign.Nonce})
			if err != nil || h != v.Header || hash != v.Expected.IntentHash {
				t.Errorf("signer: %v\n got  %s\n want %s", err, h, v.Header)
			}
		})
	}
}

// The §3.2 cross-check vector, pinned here as literals too: if the files and the code agree with
// each other but not with the brief, this fails.
func TestCrossCheckVector(t *testing.T) {
	var v vector
	b, err := os.ReadFile(filepath.Join(vectorDir, "valid", "v01-refund-obo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(v.Header, ".TQAgBevx6iwlDM2Z7px4mrRiCjdbrAX3GCacB_gMAQbvPagJqCsJUWYz7xS7VuQqyV2rOK71uyRHvym2nqjJCA") ||
		v.Expected.IntentHash != "sha256:87c73e450a53f0269e33f7adb8430a6f89714606f9aae38bc732536fef7e3089" ||
		v.Expected.ArgsDigest != "sha256:f7411a7cf4706333c9ba80c85aab7d8b92363ad3077a288dadc26bef42d89d36" ||
		v.Expected.KID != "21fe31dfa154a261626bf854046fd227" || v.Expected.IntentBytes != 393 {
		t.Fatalf("v01 drifted from S09 §3.2: %+v %s", v.Expected, v.Header)
	}
}
