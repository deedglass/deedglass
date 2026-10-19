package deed_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

// base is deed 1 of the vector chain, sealed by key a.
func base(t testing.TB) (*deed.Envelope, []byte) {
	t.Helper()
	c := deedtest.NewChain(1, deedtest.ChainOpts{})
	return c.Deeds[0], c.Lines[0]
}

// edit parses canonical deed bytes loosely, applies f, and re-marshals (not canonical, not
// re-sealed): a tamperer's view of a deed.
func edit(t testing.TB, raw []byte, f func(m map[string]any)) []byte {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var m map[string]any
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

func body(m map[string]any) map[string]any { return m["body"].(map[string]any) }

// bs builds a backslash escape at run time (CLAUDE.md: test strings with \u escapes).
func bs(hex4 string) string { return "\\" + "u" + hex4 }

func TestVerifyTable(t *testing.T) {
	ks := deedtest.KeySet()
	env, raw := base(t)
	a, b := deedtest.TestKey("a"), deedtest.TestKey("b")

	flipSeal := func(m map[string]any) {
		s := m["seal"].(string)
		sig, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, "ed25519:"))
		sig[0] ^= 1
		m["seal"] = "ed25519:" + base64.RawURLEncoding.EncodeToString(sig)
	}
	noTag := func(m map[string]any) {
		sig := ed25519.Sign(ed25519.NewKeyFromSeed(a.Seed), []byte(env.BodyHash))
		m["seal"] = "ed25519:" + base64.RawURLEncoding.EncodeToString(sig)
	}
	// F01: key b's private key seals a body that names key a (a wrong key behind a known id).
	wrongKey := deedtest.Canonical(deedtest.Reseal(env, deedtest.Key{ID: a.ID, Signer: nil}, nil, lying{id: a.ID, s: b.Signer}))
	unknownKey := deedtest.Canonical(deedtest.Reseal(env, deedtest.Key{ID: deedtest.ID("sk", 9), Signer: mustSigner(t, deedtest.ID("sk", 9), 9)}, nil))
	lateA := deedtest.Canonical(deedtest.Reseal(env, a, func(b *deed.Body) { b.IssuedAt = deed.FormatTime(deedtest.ARetired) }))
	earlyB := deedtest.Canonical(deedtest.Reseal(env, b, func(b *deed.Body) {}))
	revoked := deedtest.Canonical(deedtest.Reseal(env, deedtest.TestKey("r"), func(b *deed.Body) {}))
	sNum := func(s string) json.Number { return json.Number(s) }

	cases := []struct {
		name  string
		raw   []byte
		code  deed.Code
		field string
	}{
		{"valid", raw, "", ""},
		{"body edit", edit(t, raw, func(m map[string]any) { body(m)["execution"].(map[string]any)["latency_ms"] = 42 }), deed.CodeBodyHashMismatch, ""},
		{"seal bit flip", edit(t, raw, flipSeal), deed.CodeSealInvalid, ""},
		{"wrong domain tag", edit(t, raw, noTag), deed.CodeSealInvalid, ""},
		{"wrong key, known id (F01)", wrongKey, deed.CodeSealInvalid, ""},
		{"unknown key id", unknownKey, deed.CodeSealKeyUnknown, ""},
		{"envelope and body key differ", edit(t, raw, func(m map[string]any) { m["seal_key_id"] = b.ID }), deed.CodeSealKeyMismatch, ""},
		{"id differs", edit(t, raw, func(m map[string]any) { m["id"] = deedtest.ID("deed", 7) }), deed.CodeIDMismatch, ""},
		{"key not yet active", earlyB, deed.CodeSealKeyNotValidAt, ""},
		{"key already retired", lateA, deed.CodeSealKeyNotValidAt, ""},
		{"revoked key", revoked, deed.CodeSealKeyRevoked, ""},
		{"duplicate key", []byte(strings.Replace(string(raw), `"seq":1,`, `"seq":1,"seq":2,`, 1)), deed.CodeMalformed, ""},
		{"fraction", []byte(strings.Replace(string(raw), `"latency_ms":41`, `"latency_ms":41.0`, 1)), deed.CodeMalformed, ""},
		{"big integer", []byte(strings.Replace(string(raw), `"seq":1,`, `"seq":9007199254740993,`, 1)), deed.CodeMalformed, ""},
		{"-0 in body", []byte(strings.Replace(string(raw), `"amount":120`, `"amount":-0`, 1)), deed.CodeMalformed, ""},
		{"negative", []byte(strings.Replace(string(raw), `"amount":120`, `"amount":-120`, 1)), deed.CodeMalformed, ""},
		{"lone surrogate", []byte(strings.Replace(string(raw), `"name":"billing-agent"`, `"name":"`+bs("d800")+`"`, 1)), deed.CodeMalformed, ""},
		{"invalid utf-8", []byte(strings.Replace(string(raw), `billing-agent`, "billing-\xff", 1)), deed.CodeMalformed, ""},
		{"trailing data", append(append([]byte{}, raw...), " {}"...), deed.CodeMalformed, ""},
		{"not an object", []byte(`[1]`), deed.CodeMalformed, ""},
		{"too large", bytes.Repeat([]byte(" "), deed.MaxInputBytes+1), deed.CodeMalformed, ""},
		{"unknown field", edit(t, raw, func(m map[string]any) { body(m)["note"] = "x" }), deed.CodeSchemaInvalid, "body.note"},
		{"unknown envelope field", edit(t, raw, func(m map[string]any) { m["note"] = "x" }), deed.CodeSchemaInvalid, "note"},
		{"missing field", edit(t, raw, func(m map[string]any) { delete(body(m), "amends") }), deed.CodeSchemaInvalid, "body.amends"},
		{"missing nested field", edit(t, raw, func(m map[string]any) { delete(body(m)["action"].(map[string]any), "mode") }), deed.CodeSchemaInvalid, "body.action.mode"},
		{"wrong kind presence", edit(t, raw, func(m map[string]any) { body(m)["kind"] = "admin" }), deed.CodeSchemaInvalid, "body.on_behalf_of"},
		{"admin with policy", adminWithPolicy(t), deed.CodeSchemaInvalid, "body.policy"},
		{"wrong type", edit(t, raw, func(m map[string]any) { body(m)["seq"] = "1" }), deed.CodeSchemaInvalid, "body.seq"},
		{"bad enum", edit(t, raw, func(m map[string]any) { body(m)["witness"].(map[string]any)["verdict"] = "OK" }), deed.CodeSchemaInvalid, "body.witness.verdict"},
		{"bad time", edit(t, raw, func(m map[string]any) { body(m)["issued_at"] = "2026-10-02T09:00:00Z" }), deed.CodeSchemaInvalid, "body.issued_at"},
		{"object not deed", edit(t, raw, func(m map[string]any) { m["object"] = "statement" }), deed.CodeSchemaInvalid, "object"},
		{"version 3", edit(t, raw, func(m map[string]any) { body(m)["v"] = sNum("3"); body(m)["new_field"] = 1 }), deed.CodeUnsupportedVersion, ""},
		{"bad seal format", edit(t, raw, func(m map[string]any) { m["seal"] = "ed25519:abc" }), deed.CodeSchemaInvalid, "seal"},
		{"oversized tool", edit(t, raw, func(m map[string]any) { body(m)["action"].(map[string]any)["tool"] = strings.Repeat("x", 201) }), deed.CodeSchemaInvalid, "body.action.tool"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := deed.VerifyEnvelope(tc.raw, ks, deed.VerifyOptions{})
			if tc.code == "" {
				if err != nil || r == nil {
					t.Fatalf("got %v, want valid", err)
				}
				return
			}
			var ve *deed.VerifyError
			if !errors.As(err, &ve) {
				t.Fatalf("got %v (%T), want *VerifyError %s", err, err, tc.code)
			}
			if ve.Code != tc.code || ve.Field != tc.field {
				t.Fatalf("got %s field %q (%v), want %s field %q", ve.Code, ve.Field, err, tc.code, tc.field)
			}
			if r != nil {
				t.Fatal("a result came back with an error")
			}
			if !errors.Is(err, &deed.VerifyError{Code: tc.code}) {
				t.Fatal("errors.Is does not match the code")
			}
		})
	}
}

// lying reports one key id and signs with another key.
type lying struct {
	id string
	s  deed.Signer
}

func (l lying) KeyID() string                   { return l.id }
func (l lying) PublicKey() ed25519.PublicKey    { return l.s.PublicKey() }
func (l lying) Sign(msg []byte) ([]byte, error) { return l.s.Sign(msg) }

func mustSigner(t testing.TB, id string, n byte) *deed.FileSigner {
	t.Helper()
	s, err := deed.NewFileSigner(id, bytes.Repeat([]byte{n}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func adminWithPolicy(t testing.TB) []byte {
	c := deedtest.NewChain(7, deedtest.ChainOpts{}) // seq 7 is an admin deed
	return edit(t, c.Lines[6], func(m map[string]any) {
		body(m)["policy"] = map[string]any{"id": nil, "version_id": nil, "version_hash": nil, "mode": "enforce", "decision": "allow", "rule_id": nil}
	})
}

func TestVerifyLenientAndStrict(t *testing.T) {
	ks := deedtest.KeySet()
	_, raw := base(t)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	shuffled := shuffleKeys(t, raw)
	for name, in := range map[string][]byte{"whitespace": pretty.Bytes(), "key order": shuffled} {
		r, err := deed.VerifyEnvelope(in, ks, deed.VerifyOptions{})
		if err != nil || r.Canonical {
			t.Errorf("%s: lenient got %v canonical=%v, want valid and non-canonical", name, err, r != nil && r.Canonical)
		}
		if _, err := deed.VerifyEnvelope(in, ks, deed.VerifyOptions{Strict: true}); deed.CodeOf(err) != deed.CodeNonCanonical {
			t.Errorf("%s: strict got %v, want non_canonical", name, err)
		}
	}
	r, err := deed.VerifyEnvelope(raw, ks, deed.VerifyOptions{Strict: true})
	if err != nil || !r.Canonical {
		t.Fatalf("canonical input: %v", err)
	}
}

// shuffleKeys writes the deed with every object's keys in reverse order.
func shuffleKeys(t testing.TB, raw []byte) []byte {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	var w func(v any)
	w = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			for i := 0; i < len(keys); i++ {
				for j := i + 1; j < len(keys); j++ {
					if keys[j] > keys[i] {
						keys[i], keys[j] = keys[j], keys[i]
					}
				}
			}
			buf.WriteByte('{')
			for i, k := range keys {
				if i > 0 {
					buf.WriteByte(',')
				}
				kb, _ := json.Marshal(k)
				buf.Write(kb)
				buf.WriteByte(':')
				w(x[k])
			}
			buf.WriteByte('}')
		default:
			b, _ := json.Marshal(x)
			buf.Write(b)
		}
	}
	w(v)
	return buf.Bytes()
}

func TestVerifyNilKeySet(t *testing.T) {
	_, raw := base(t)
	if _, err := deed.VerifyEnvelope(raw, nil, deed.VerifyOptions{}); deed.CodeOf(err) != deed.CodeSealKeyUnknown {
		t.Fatalf("got %v", err)
	}
}

// The first failing check wins, in the documented order: a deed with a changed body and a broken
// seal reports body_hash_mismatch; with also an unknown key, still body_hash_mismatch.
func TestVerifyFirstFailureWins(t *testing.T) {
	ks := deedtest.KeySet()
	_, raw := base(t)
	both := edit(t, raw, func(m map[string]any) {
		body(m)["execution"].(map[string]any)["latency_ms"] = 42
		m["seal"] = "ed25519:" + strings.Repeat("A", 86)
	})
	if _, err := deed.VerifyEnvelope(both, ks, deed.VerifyOptions{}); deed.CodeOf(err) != deed.CodeBodyHashMismatch {
		t.Fatalf("got %v", err)
	}
	idAndVersion := edit(t, raw, func(m map[string]any) { m["id"] = deedtest.ID("deed", 3); body(m)["v"] = 3 })
	if _, err := deed.VerifyEnvelope(idAndVersion, ks, deed.VerifyOptions{}); deed.CodeOf(err) != deed.CodeIDMismatch {
		t.Fatalf("got %v", err)
	}
}

func TestVerifyErrorRefs(t *testing.T) {
	ks := deedtest.KeySet()
	c := deedtest.NewChain(12, deedtest.ChainOpts{})
	bad := edit(t, c.Lines[11], func(m map[string]any) { body(m)["execution"].(map[string]any)["latency_ms"] = 42 })
	_, err := deed.VerifyEnvelope(bad, ks, deed.VerifyOptions{})
	var ve *deed.VerifyError
	if !errors.As(err, &ve) || ve.Seq != 12 || ve.DeedID != c.Deeds[11].ID {
		t.Fatalf("got %+v", err)
	}
	s, next := deed.Message(ve)
	if s != "Deed 12 was changed after it was sealed." || next != "Get a fresh export from the issuer and compare deed 12." {
		t.Fatalf("message %q / %q", s, next)
	}
}
