package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/ids"
)

var update = flag.Bool("update", false, "rewrite testdata/vault/envelope_v1.json")

// sentinel is the canary every redaction test looks for (S16 §6).
const sentinel = "dgSENTINEL7Q2vK9xT4mB8nR3"

func ring(t testing.TB, keys ...byte) *Ring {
	t.Helper()
	var parts []string
	for _, k := range keys {
		parts = append(parts, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{k}, 32)))
	}
	r, err := NewRing(strings.Join(parts, ","))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var (
	orgA = ids.MustParse[ids.OrgKind]("org_01k6c9kb2zcjsyn7b8g2k1vqvp")
	orgB = ids.MustParse[ids.OrgKind]("org_01k6c9kb2zcjsyn7b8g2k1vqvq")
	conA = ids.MustParse[ids.ConnectorKind]("con_01j9zq7c4m8x2v6t0r3n5k1h7d")
	conB = ids.MustParse[ids.ConnectorKind]("con_01j9zq7c4m8x2v6t0r3n5k1h7e")
)

func fields(kv ...string) map[string][]byte {
	m := map[string][]byte{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = []byte(kv[i+1])
	}
	return m
}

func TestRoundTrip(t *testing.T) {
	r := ring(t, 1)
	ctx := context.Background()
	ref := Ref{Org: orgA, Connector: conA}
	cases := []struct {
		kind string
		f    map[string][]byte
	}{
		{KindBearer, fields("token", sentinel)},
		{KindBasic, fields("username", "orders-bot", "password", sentinel)},
		{KindDSN, fields("dsn", "postgres://agent:"+sentinel+"@db.example.com:5432/orders", "witness_dsn", "postgres://w:"+sentinel+"x@db.example.com/orders")},
		{KindFields, fields("api_key", sentinel, "account", "acct_123456")},
		{KindHeader, fields("value", sentinel)},
		{KindQuery, fields("value", sentinel)},
	}
	for _, tc := range cases {
		s, err := Seal(ctx, r, nil, ref, 3, tc.kind, tc.f)
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		if len(s.WrappedDEK) != WrappedDEKLen || s.KEKID != r.ActiveID() {
			t.Fatalf("%s: wrapped %d bytes with %s", tc.kind, len(s.WrappedDEK), s.KEKID)
		}
		if bytes.Contains(s.Ciphertext, []byte(sentinel)) || bytes.Contains(s.WrappedDEK, []byte(sentinel)) {
			t.Fatalf("%s: the plaintext is visible", tc.kind)
		}
		sec, err := Open(ctx, r, ref, s)
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		for k, v := range tc.f {
			if sec.Field(k) != string(v) {
				t.Errorf("%s.%s: got %q", tc.kind, k, sec.Field(k))
			}
		}
		if sec.Kind() != tc.kind {
			t.Errorf("kind %s", sec.Kind())
		}
		sec.Destroy()
		if sec.Field(fieldNames(tc.f)[0]) != "" {
			t.Errorf("%s: a destroyed secret still reads", tc.kind)
		}
		m := Meta{Object: "credential", Kind: tc.kind, Fields: fieldNames(tc.f), Hint: Hint(tc.kind, tc.f)}
		b, _ := json.Marshal(m)
		if bytes.Contains(b, []byte(sentinel)) {
			t.Errorf("%s: Meta carries a value: %s", tc.kind, b)
		}
	}
}

func fieldNames(f map[string][]byte) []string {
	var out []string
	for k := range f {
		out = append(out, k)
	}
	return out
}

func TestIntegrity(t *testing.T) {
	r := ring(t, 1)
	ctx := context.Background()
	ref := Ref{Org: orgA, Connector: conA}
	s, err := Seal(ctx, r, nil, ref, 1, KindBearer, fields("token", sentinel))
	if err != nil {
		t.Fatal(err)
	}
	flip := func(b []byte, i int) []byte { c := bytes.Clone(b); c[i] ^= 1; return c }
	mutations := map[string]func(Sealed) (Ref, Sealed){
		"flipped ciphertext byte": func(s Sealed) (Ref, Sealed) { s.Ciphertext = flip(s.Ciphertext, 20); return ref, s },
		"flipped nonce byte":      func(s Sealed) (Ref, Sealed) { s.Ciphertext = flip(s.Ciphertext, 0); return ref, s },
		"flipped wrapped key":     func(s Sealed) (Ref, Sealed) { s.WrappedDEK = flip(s.WrappedDEK, 30); return ref, s },
		"copied into org B":       func(s Sealed) (Ref, Sealed) { return Ref{Org: orgB, Connector: conA}, s },
		"another connector":       func(s Sealed) (Ref, Sealed) { return Ref{Org: orgA, Connector: conB}, s },
		"version edited":          func(s Sealed) (Ref, Sealed) { s.Version = 2; return ref, s },
		"kind edited":             func(s Sealed) (Ref, Sealed) { s.Kind = KindHeader; return ref, s },
		"truncated":               func(s Sealed) (Ref, Sealed) { s.Ciphertext = s.Ciphertext[:20]; return ref, s },
		"short wrapped key":       func(s Sealed) (Ref, Sealed) { s.WrappedDEK = s.WrappedDEK[:59]; return ref, s },
	}
	for name, mut := range mutations {
		rf, ms := mut(s)
		sec, err := Open(ctx, r, rf, ms)
		if !errors.Is(err, ErrIntegrity) || sec != nil {
			t.Errorf("%s: %v %v", name, sec, err)
		}
	}
	// A key that isn't in the ring.
	other := ring(t, 2)
	_, err = Open(ctx, other, ref, s)
	var uk *UnknownKEKError
	if !errors.As(err, &uk) || uk.KEKID != r.ActiveID() {
		t.Fatalf("unknown kek: %v", err)
	}
	if !strings.Contains(uk.Error(), r.ActiveID()+", which isn't in DEEDGLASS_VAULT_KEY") {
		t.Errorf("message: %s", uk.Error())
	}
}

func TestRewrap(t *testing.T) {
	ctx := context.Background()
	old := ring(t, 1)
	both := ring(t, 2, 1)
	ref := Ref{Org: orgA, Connector: conA}
	s, _ := Seal(ctx, old, nil, ref, 1, KindBearer, fields("token", sentinel))
	nw, err := Rewrap(ctx, both, ref, s)
	if err != nil {
		t.Fatal(err)
	}
	if nw.KEKID != both.ActiveID() || !bytes.Equal(nw.Ciphertext, s.Ciphertext) {
		t.Fatalf("rewrap changed the ciphertext or kept the key")
	}
	if sec, err := Open(ctx, ring(t, 2), ref, nw); err != nil || sec.Field("token") != sentinel {
		t.Fatalf("open after rewrap: %v", err)
	}
}

// TestSecretPrints: no formatting path shows a value.
func TestSecretPrints(t *testing.T) {
	sec := NewSecretForTest(KindBearer, map[string]string{"token": sentinel})
	type holder struct {
		S *Secret
		s *Secret
	}
	var logs bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&logs, nil))
	l.Info("x", "secret", sec, "any", slog.AnyValue(sec))
	b, _ := json.Marshal(map[string]any{"s": sec})
	outs := []string{fmt.Sprintf("%v %+v %#v %s %q %x", sec, sec, sec, sec, sec, sec), string(b), logs.String(),
		fmt.Sprintf("%v %+v", holder{sec, sec}, &holder{sec, sec})}
	for _, o := range outs {
		if strings.Contains(o, sentinel) {
			t.Fatalf("a value printed: %s", o)
		}
	}
	if !strings.Contains(outs[0], RedactedText) || !strings.Contains(string(b), RedactedText) {
		t.Fatalf("missing marker: %s %s", outs[0], b)
	}
}

// TestRedactForms: the sentinel in every form it travels in.
func TestRedactForms(t *testing.T) {
	r := NewRedactor(sentinel)
	r.AddBasic("orders-bot", sentinel)
	weird := sentinel + "/+ &=%"
	r2 := NewRedactor(weird)
	forms := []string{
		sentinel,
		url.QueryEscape(sentinel),
		base64.StdEncoding.EncodeToString([]byte(sentinel)),
		base64.RawURLEncoding.EncodeToString([]byte(sentinel)),
		base64.StdEncoding.EncodeToString([]byte("orders-bot:" + sentinel)),
		"postgres://agent:" + sentinel + "@db:5432/x",
	}
	for _, f := range forms {
		in := "before " + f + " after"
		out := r.Redact(in)
		if strings.Contains(out, sentinel) || strings.Contains(out, f) {
			t.Errorf("%q survived: %q", f, out)
		}
	}
	for _, f := range []string{weird, url.QueryEscape(weird), url.PathEscape(weird), lowerHex(url.QueryEscape(weird)),
		mustJSON(weird), strings.TrimPrefix(url.UserPassword("u", weird).String(), "u:")} {
		if out := r2.Redact("x " + f + " y"); strings.Contains(out, f) {
			t.Errorf("%q survived: %q", f, out)
		}
	}
	if NewRedactor("short").Redact("a short b") != "a short b" {
		t.Error("a value under 6 bytes was redacted")
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// TestRedactThroughErrorsAndLogs pushes the sentinel through an error, an upstream body, a panic
// value and slog groups, with the redactor in the context.
func TestRedactThroughErrorsAndLogs(t *testing.T) {
	r := NewRedactor(sentinel)
	ctx := WithRedactor(context.Background(), r)
	var buf bytes.Buffer
	log := slog.New(NewRedactingHandler(slog.NewJSONHandler(&buf, nil)))
	err := fmt.Errorf("upstream said: %w", errors.New(`{"error":"bad token `+sentinel+`"}`))
	func() {
		defer func() {
			if p := recover(); p != nil {
				log.ErrorContext(ctx, "panic", "value", fmt.Sprint(p))
			}
		}()
		panic("boom " + sentinel)
	}()
	log.InfoContext(ctx, "call failed: "+sentinel, "err", err, slog.Group("upstream", slog.String("body", base64.StdEncoding.EncodeToString([]byte(sentinel))),
		slog.Group("deeper", slog.Any("bytes", []byte(sentinel)))), "auth", "Bearer "+sentinel)
	log.With("fixed", "Bearer abcdefghijk12345").InfoContext(context.Background(), "with attrs")
	out := buf.String()
	if strings.Count(out, sentinel) != 0 || strings.Contains(out, base64.StdEncoding.EncodeToString([]byte(sentinel))) || strings.Contains(out, "abcdefghijk12345") {
		t.Fatalf("the sentinel reached the log:\n%s", out)
	}
	if !strings.Contains(out, RedactedText) {
		t.Fatalf("no marker:\n%s", out)
	}
}

func TestScrub(t *testing.T) {
	positives := []string{
		"dg_live_AbCdEf12_0123456789abcdefABCDEF0123456789",
		"dg_test_AbCdEf12_0123456789abcdefABCDEF0123456789",
		"sk_live_51HxYzAbCdEfGhIj",
		"rk_test_AbCdEfGh12345678",
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.e30.abc",
		"bearer abcdefgh12345678",
		"postgres://app:hunter2hunter@db:5432/x",
		"AKIAIOSFODNN7EXAMPLE",
		"ghp_" + strings.Repeat("a1B2", 9),
		"github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyz",
		"xoxb-123456789012-abcdefghij",
		"xoxp-1234-5678-abcdefghijkl",
		// S29
		"ghs_" + strings.Repeat("Ab12", 9),
		"xapp-1-A0123ABCD-1234567890-abcdef",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkq",
		"eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJJdjIzbGkifQ.c2lnbmF0dXJlc2lnbmF0dXJlc2ln",
	}
	for _, p := range positives {
		in := "x " + p + " y"
		out := Scrub(in)
		if out == in || !strings.Contains(out, RedactedText) {
			t.Errorf("not scrubbed: %q → %q", in, out)
		}
	}
	negatives := []string{
		"dg_ is our prefix",
		"sk_live_short",
		"the bearer of bad news",
		"https://example.com/path@v2",
		"AKIA is how AWS key ids start",
		"ghp_short",
		// S29
		"ghs_short",
		"xapp- is the app-level prefix",
		"-----BEGIN PUBLIC KEY-----",
		"eyJ is how a JSON object starts in base64",
		"eyJhbGciOiJIUzI1NiJ9.e30.abc",
	}
	for _, n := range negatives {
		if out := Scrub(n); out != n {
			t.Errorf("near miss scrubbed: %q → %q", n, out)
		}
	}
}

// FuzzRedactor: a random secret at a random offset in random text never survives.
func FuzzRedactor(f *testing.F) {
	f.Add("hunter2hunter2", "prefix ", " suffix", uint8(0))
	f.Add(sentinel, "", "", uint8(3))
	f.Add("]]]]]]]]", "[redacted", "", uint8(1))
	f.Add("abc/+=&?", "x=", "&y", uint8(2))
	f.Fuzz(func(t *testing.T, secret, pre, post string, form uint8) {
		if len(secret) < MinFieldLength || strings.Contains(RedactedText, secret) {
			return
		}
		r := NewRedactor(secret)
		var enc string
		switch form % 5 {
		case 0:
			enc = secret
		case 1:
			enc = url.QueryEscape(secret)
		case 2:
			enc = base64.StdEncoding.EncodeToString([]byte(secret))
		case 3:
			enc = mustJSON(secret)
		case 4:
			enc = url.PathEscape(secret)
		}
		out := r.Redact(pre + enc + post)
		if strings.Contains(out, secret) || len(enc) >= MinFieldLength && strings.Contains(out, enc) {
			t.Fatalf("survived: secret %q form %d in %q", secret, form%5, out)
		}
	})
}

// TestGoldenEnvelope pins the envelope format with a fixed random source.
func TestGoldenEnvelope(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "vault", "envelope_v1.json")
	keyB64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	r, err := NewRing(keyB64)
	if err != nil {
		t.Fatal(err)
	}
	rd := detRand{}
	r.WithRand(&rd)
	ref := Ref{Org: orgA, Connector: conA}
	s, err := Seal(context.Background(), r, &rd, ref, 1, KindBearer, fields("token", "dg-test-token-not-a-real-secret-0001"))
	if err != nil {
		t.Fatal(err)
	}
	type golden struct {
		Alg, KEK, KEKID, Org, Connector, Kind string
		Version                               int
		AADDEK, AADSecret                     string
		WrappedDEK, Ciphertext                string
		Plaintext                             string
	}
	g := golden{Alg: Alg, KEK: keyB64, KEKID: s.KEKID, Org: orgA.String(), Connector: conA.String(), Kind: s.Kind, Version: s.Version,
		AADDEK: string(aadDEK(ref, s.KEKID)), AADSecret: string(aadSecret(ref, 1, KindBearer)),
		WrappedDEK: hex.EncodeToString(s.WrappedDEK), Ciphertext: hex.EncodeToString(s.Ciphertext),
		Plaintext: `{"v":1,"kind":"bearer","fields":{"token":"dg-test-token-not-a-real-secret-0001"}}`}
	b, _ := json.MarshalIndent(g, "", "  ")
	b = append(b, '\n')
	if *update {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/vault -run TestGoldenEnvelope -update)", err)
	}
	if !bytes.Equal(want, b) {
		t.Fatalf("the envelope differs from %s", path)
	}
	// The pinned bytes open with the production path (NewGCMWithRandomNonce).
	var pinned golden
	_ = json.Unmarshal(want, &pinned)
	wd, _ := hex.DecodeString(pinned.WrappedDEK)
	ct, _ := hex.DecodeString(pinned.Ciphertext)
	r2, _ := NewRing(pinned.KEK)
	sec, err := Open(context.Background(), r2, ref, Sealed{Version: 1, Kind: KindBearer, KEKID: pinned.KEKID, WrappedDEK: wd, Ciphertext: ct})
	if err != nil || sec.Field("token") != "dg-test-token-not-a-real-secret-0001" {
		t.Fatalf("the golden doesn't open: %v", err)
	}
}

// detRand is a deterministic byte stream (sha256 counter mode) for the golden vector.
type detRand struct{ n uint64 }

func (d *detRand) Read(p []byte) (int, error) {
	for i := range p {
		if i%32 == 0 {
			d.n++
		}
		sum := sha256.Sum256(fmt.Appendf(nil, "deedglass/vault/golden/%d", d.n))
		p[i] = sum[i%32]
	}
	return len(p), nil
}

func TestCheckFieldsAndHint(t *testing.T) {
	bad := []struct {
		kind  string
		f     map[string][]byte
		prod  bool
		param string
	}{
		{"oauth", fields("token", sentinel), false, "kind"},
		{KindBearer, fields("value", sentinel), false, "secret.token"},
		{KindBearer, fields("token", sentinel, "extra", "x"), false, "secret.extra"},
		{KindBasic, fields("username", "u"), false, "secret.password"},
		{KindBearer, fields("token", "ab\ncd"), false, "secret.token"},
		{KindBearer, fields("token", "short"), true, "secret.token"},
		{KindFields, fields("Bad-Name", "x"), false, "secret"},
		{KindFields, map[string][]byte{}, false, "secret"},
	}
	for _, tc := range bad {
		err := CheckFields(tc.kind, tc.f, tc.prod)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Param != tc.param {
			t.Errorf("%s %v: %v", tc.kind, fieldNames(tc.f), err)
		} else if strings.Contains(fe.Message, sentinel) {
			t.Errorf("the message names a value: %s", fe.Message)
		}
	}
	if err := CheckFields(KindBasic, fields("username", "u", "password", "longenough"), true); err != nil {
		t.Errorf("a short username is fine: %v", err)
	}
	hints := map[string]string{
		Hint(KindBearer, fields("token", "abcdefghijklmnopqrstu9f2c")):            "…9f2c",
		Hint(KindBearer, fields("token", "shorttoken")):                           "",
		Hint(KindBasic, fields("username", "orders-bot")):                         "orders-bot",
		Hint(KindDSN, fields("dsn", "postgres://agent:pw@db.example.com/orders")): "agent@db.example.com:5432/orders",
	}
	for got, want := range hints {
		if got != want {
			t.Errorf("hint %q, want %q", got, want)
		}
	}
}

func BenchmarkOpen(b *testing.B) {
	r := ring(b, 1)
	ctx := context.Background()
	ref := Ref{Org: orgA, Connector: conA}
	s, _ := Seal(ctx, r, nil, ref, 1, KindFields, fields("blob", strings.Repeat("x", 900)))
	b.ReportAllocs()
	for b.Loop() {
		sec, err := Open(ctx, r, ref, s)
		if err != nil {
			b.Fatal(err)
		}
		sec.Destroy()
	}
}

func BenchmarkRedact4KiB(b *testing.B) {
	r := NewRedactor(sentinel, "second-secret-value-1234")
	rng := rand.New(rand.NewPCG(1, 2))
	buf := make([]byte, 4096)
	for i := range buf {
		buf[i] = byte('a' + rng.IntN(26))
	}
	s := string(buf)
	b.ReportAllocs()
	for b.Loop() {
		_ = r.Redact(s)
	}
}
