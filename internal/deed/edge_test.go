package deed_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

// Every field rule, one mutation each, on a complete action deed (Body.Validate).
func TestValidateFieldRules(t *testing.T) {
	c := deedtest.NewChain(7, deedtest.ChainOpts{})
	action := c.Deeds[0].Body
	admin := c.Deeds[6].Body
	approved := deedtest.ActionApproved(c.Org, 3)
	approved.DeedID, approved.Seq, approved.PrevHash, approved.IssuedAt, approved.SealKeyID = action.DeedID, 1, action.PrevHash, action.IssuedAt, action.SealKeyID
	if err := approved.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := "not-valid"
	cases := []struct {
		field string
		base  deed.Body
		f     func(b *deed.Body)
	}{
		{"kind", action, func(b *deed.Body) { b.Kind = "other" }},
		{"deed_id", action, func(b *deed.Body) { b.DeedID = bad }},
		{"seq", action, func(b *deed.Body) { b.Seq = 0 }},
		{"seq", action, func(b *deed.Body) { b.Seq = deed.MaxSafeInteger + 1 }},
		{"prev_hash", action, func(b *deed.Body) { b.PrevHash = bad }},
		{"issued_at", action, func(b *deed.Body) { b.IssuedAt = "2026-02-30T00:00:00.000Z" }},
		{"seal_key_id", action, func(b *deed.Body) { b.SealKeyID = bad }},
		{"org", action, func(b *deed.Body) { b.Org = bad }},
		{"on_behalf_of", action, func(b *deed.Body) { s := "has space"; b.OnBehalfOf = &s }},
		{"action.id", action, func(b *deed.Body) { a := *b.Action; a.ID = bad; b.Action = &a }},
		{"action.tool", action, func(b *deed.Body) { a := *b.Action; a.Tool = ""; b.Action = &a }},
		{"action.connector", action, func(b *deed.Body) { a := *b.Action; a.Connector = &bad; b.Action = &a }},
		{"action.intent_hash", action, func(b *deed.Body) { a := *b.Action; a.IntentHash = &bad; b.Action = &a }},
		{"policy.id", action, func(b *deed.Body) { p := *b.Policy; p.ID = &bad; b.Policy = &p }},
		{"policy.version_id", action, func(b *deed.Body) { p := *b.Policy; p.VersionID = &bad; b.Policy = &p }},
		{"policy.version_hash", action, func(b *deed.Body) { p := *b.Policy; p.VersionHash = &bad; b.Policy = &p }},
		{"policy.rule_id", action, func(b *deed.Body) { p := *b.Policy; s := "has space"; p.RuleID = &s; b.Policy = &p }},
		{"approval.id", approved, func(b *deed.Body) { a := *b.Approval; a.ID = bad; b.Approval = &a }},
		{"approval.decided_by", approved, func(b *deed.Body) { a := *b.Approval; a.DecidedBy = nil; b.Approval = &a }},
		{"approval.decided_by[0]", approved, func(b *deed.Body) { a := *b.Approval; a.DecidedBy = []string{bad}; b.Approval = &a }},
		{"approval.decided_by", approved, func(b *deed.Body) {
			a := *b.Approval
			a.Outcome = "expired"
			b.Approval = &a
		}},
		{"approval.decided_at", approved, func(b *deed.Body) { a := *b.Approval; a.DecidedAt = &bad; b.Approval = &a }},
		{"approval.deed", approved, func(b *deed.Body) { a := *b.Approval; a.Deed = &bad; b.Approval = &a }},
		{"execution.started_at", action, func(b *deed.Body) { e := *b.Execution; e.StartedAt = bad; b.Execution = &e }},
		{"execution.finished_at", action, func(b *deed.Body) { e := *b.Execution; e.FinishedAt = bad; b.Execution = &e }},
		{"execution.latency_ms", action, func(b *deed.Body) { e := *b.Execution; e.LatencyMS = -1; b.Execution = &e }},
		{"execution.result_digest", action, func(b *deed.Body) { e := *b.Execution; e.ResultDigest = &bad; b.Execution = &e }},
		{"witness.verdict", action, func(b *deed.Body) { w := *b.Witness; w.Verdict = "OK"; b.Witness = &w }},
		{"witness.detail_digest", action, func(b *deed.Body) { w := *b.Witness; w.DetailDigest = &bad; b.Witness = &w }},
		{"admin.target.type", admin, func(b *deed.Body) { a := *b.Admin; a.Target = &deed.Ref{Type: "Agent", ID: "x"}; b.Admin = &a }},
		{"admin.data_digest", admin, func(b *deed.Body) { a := *b.Admin; a.DataDigest = &bad; b.Admin = &a }},
		{"agent.id", admin, func(b *deed.Body) { a := *b.Agent; a.ID = bad; b.Agent = &a }},
		{"amends", deedtest.Amendment(&action), func(b *deed.Body) {
			b.DeedID, b.Seq, b.PrevHash, b.IssuedAt, b.SealKeyID = action.DeedID, 2, action.PrevHash, action.IssuedAt, action.SealKeyID
			b.Amends = &bad
		}},
	}
	for _, tc := range cases {
		b := tc.base
		tc.f(&b)
		var ve *deed.VerifyError
		if err := b.Validate(); !errors.As(err, &ve) || ve.Code != deed.CodeSchemaInvalid || ve.Field != tc.field {
			t.Errorf("%s: got %v", tc.field, err)
		}
	}
	v2 := action
	v2.V = 3
	if err := v2.Validate(); deed.CodeOf(err) != deed.CodeUnsupportedVersion {
		t.Errorf("v2: %v", err)
	}
}

// Wrong JSON types for every kind of member, through the verifier.
func TestSchemaTypes(t *testing.T) {
	ks := deedtest.KeySet()
	c := deedtest.NewChain(7, deedtest.ChainOpts{})
	raw, admin := c.Lines[0], c.Lines[6]
	approvedEnv, _ := deed.SealBody(deedtest.TestKey("a").Signer, func() deed.Body {
		b := deedtest.ActionApproved(c.Org, 1)
		b.DeedID, b.Seq, b.PrevHash, b.IssuedAt = deedtest.ID("deed", 77), 1, deed.Genesis(c.Org), deed.FormatTime(deedtest.T0)
		return b
	}())
	appr := deedtest.Canonical(approvedEnv)
	cases := []struct {
		name, field string
		src         []byte
		f           func(m map[string]any)
	}{
		{"body not an object", "body", raw, func(m map[string]any) { m["body"] = []any{} }},
		{"agent not an object", "body.agent", raw, func(m map[string]any) { body(m)["agent"] = "x" }},
		{"nullable string wrong type", "body.session", raw, func(m map[string]any) { body(m)["session"] = 1 }},
		{"boolean wrong type", "body.action.intent_signed", raw, func(m map[string]any) { body(m)["action"].(map[string]any)["intent_signed"] = "no" }},
		{"array wrong type", "body.approval.decided_by", appr, func(m map[string]any) { body(m)["approval"].(map[string]any)["decided_by"] = "x" }},
		{"array element wrong type", "body.approval.decided_by[0]", appr, func(m map[string]any) { body(m)["approval"].(map[string]any)["decided_by"] = []any{1} }},
		{"actor not an object", "body.admin.actor", admin, func(m map[string]any) { body(m)["admin"].(map[string]any)["actor"] = "x" }},
		{"actor extra member", "body.admin.actor.x", admin, func(m map[string]any) { body(m)["admin"].(map[string]any)["actor"].(map[string]any)["x"] = 1 }},
		{"envelope member wrong type", "seal", raw, func(m map[string]any) { m["seal"] = 1 }},
		{"body_hash pattern", "body_hash", raw, func(m map[string]any) { m["body_hash"] = "sha256:X" }},
		{"seal_key_id pattern", "seal_key_id", raw, func(m map[string]any) { m["seal_key_id"] = "sk_x" }},
	}
	for _, tc := range cases {
		_, err := deed.VerifyEnvelope(edit(t, tc.src, tc.f), ks, deed.VerifyOptions{})
		var ve *deed.VerifyError
		if !errors.As(err, &ve) || ve.Code != deed.CodeSchemaInvalid || ve.Field != tc.field {
			t.Errorf("%s: got %v", tc.name, err)
		}
	}
	// ParseEnvelope is the same reader without crypto.
	env, err := deed.ParseEnvelope(raw)
	if err != nil || env.ID != c.Deeds[0].ID {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if _, err := deed.ParseEnvelope([]byte(`{}`)); deed.CodeOf(err) != deed.CodeSchemaInvalid {
		t.Fatalf("ParseEnvelope({}): %v", err)
	}
	// A canonical form over 16 KiB, written loosely so the input is under 64 KiB.
	big := edit(t, raw, func(m map[string]any) {
		body(m)["agent"].(map[string]any)["name"] = strings.Repeat("é", 64)
		body(m)["action"].(map[string]any)["tool"] = strings.Repeat("t", 200)
	})
	if _, err := deed.VerifyEnvelope(big, ks, deed.VerifyOptions{}); err == nil {
		t.Fatal("changed deed verified")
	}
}

func TestKeyFileErrorsAndSeeds(t *testing.T) {
	for _, e := range []*deed.KeyFileError{
		{Problem: deed.KeyFileTooOpen, Path: "p", Mode: 0o644}, {Problem: deed.KeyFileUnsafe, ID: "sk_x", Detail: "small-order point"},
		{Problem: deed.KeyFileMismatch, Path: "p"}, {Problem: deed.KeyFileTestKey}, {Problem: deed.KeyFileUnreadable, Path: "p", Detail: "d"},
	} {
		if e.Error() == "" || strings.Contains(e.Error(), "seed\":") {
			t.Errorf("%d: %q", e.Problem, e.Error())
		}
	}
	if _, err := deed.NewSeed(bytes.NewReader(make([]byte, 5))); err == nil {
		t.Fatal("short randomness")
	}
	s, err := deed.NewSeed(bytes.NewReader(make([]byte, 32)))
	if err != nil || len(s) != 32 {
		t.Fatal(err)
	}
	if _, _, err := deed.NewKeyFile("sk_bad", s, time.Now()); err == nil {
		t.Fatal("bad id")
	}
	// A directory we can't create.
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	kf, _, _ := deed.NewKeyFile(deedtest.ID("sk", 60), s, time.Now())
	if _, err := deed.WriteKeyFile(filepath.Join(ro, "keys"), kf); err == nil {
		t.Fatal("wrote into a read-only directory")
	}
	if deed.UnsafeReason(errors.New("x")) != "x" {
		t.Fatal("UnsafeReason passthrough")
	}
	big := filepath.Join(t.TempDir(), "big.json")
	_ = os.WriteFile(big, bytes.Repeat([]byte(" "), 5000), 0o600)
	var kfe *deed.KeyFileError
	if _, err := deed.LoadKeyFile(big, deed.LoadOptions{}); !errors.As(err, &kfe) || kfe.Problem != deed.KeyFileUnreadable {
		t.Fatalf("oversized file: %v", err)
	}
}

func TestKeySetShapes(t *testing.T) {
	for name, raw := range map[string]string{
		"not an object":     `[1]`,
		"keys not array":    `{"object":"seal_key_set","issuer":"x","updated_at":"x","keys":{}}`,
		"key not object":    `{"object":"seal_key_set","issuer":"x","updated_at":"x","keys":[1]}`,
		"key missing jwk":   `{"object":"seal_key_set","issuer":"x","updated_at":"x","keys":[{"object":"seal_key","id":"x","alg":"Ed25519","public_key":"x","thumbprint":"x","status":"active","activated_at":"x","retired_at":null,"revoked_at":null,"jwk":null}]}`,
		"issuer not string": `{"object":"seal_key_set","issuer":1,"updated_at":"x","keys":[]}`,
	} {
		if _, err := deed.ParseKeySet([]byte(raw)); err == nil || deed.CodeOf(err) == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
	ks := deedtest.KeySet()
	if _, _, ok := ks.Lookup("sk_nope"); ok {
		t.Fatal("lookup of an unknown id")
	}
	var nilSet *deed.KeySet
	if _, _, ok := nilSet.Lookup("x"); ok {
		t.Fatal("nil set")
	}
	raw := string(keySetJSON(t, ks))
	for name, s := range map[string]string{
		"bad id":         strings.Replace(raw, ks.Keys[0].ID, "sk_x", 1),
		"bad object":     strings.Replace(raw, `"object":"seal_key",`, `"object":"key",`, 1),
		"bad retired_at": strings.Replace(raw, *ks.Keys[1].RetiredAt, "soon", 1),
		"bad revoked_at": strings.Replace(raw, *ks.Keys[0].RevokedAt, "soon", 1),
	} {
		if _, err := deed.ParseKeySet([]byte(s)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSealBodyRefusals(t *testing.T) {
	env, _ := base(t)
	b := env.Body
	k := deedtest.TestKey("b")
	if _, err := deed.SealBody(k.Signer, b); !errors.Is(err, deed.ErrDraft) {
		t.Fatalf("key id differs from the signer: %v", err)
	}
	b.SealKeyID = ""
	if e, err := deed.SealBody(k.Signer, b); err != nil || e.SealKeyID != k.ID {
		t.Fatalf("empty key id is filled: %v", err)
	}
	b.Kind = "x"
	b.SealKeyID = ""
	if _, err := deed.SealBody(k.Signer, b); !errors.Is(err, deed.ErrDraft) {
		t.Fatalf("invalid body: %v", err)
	}
	if _, err := deed.SealBody(shortSigner{k.Signer}, env.Body); err == nil {
		t.Fatal("a 63-byte signature")
	}
	if _, err := deed.EncodeCOSE(&deed.Envelope{Body: deed.Body{V: 1}}, k.Signer, "x", time.Now()); err == nil {
		t.Fatal("COSE of an invalid body")
	}
}

type shortSigner struct{ deed.Signer }

func (s shortSigner) KeyID() string                   { return deedtest.TestKey("a").ID }
func (s shortSigner) Sign(msg []byte) ([]byte, error) { b, _ := s.Signer.Sign(msg); return b[:63], nil }
