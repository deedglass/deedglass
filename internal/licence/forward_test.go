package licence

import (
	"crypto/ed25519"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// signRaw signs b without Sign's shape check, so a test can hand Parse and Verify a token whose
// signature is genuine but whose body is malformed.
func signRaw(t *testing.T, priv ed25519.PrivateKey, b Body) Token {
	t.Helper()
	b.KeyID = testKeyID
	h, err := BodyHash(b)
	if err != nil {
		t.Fatal(err)
	}
	return Token{Body: b, BodyHash: h, Sig: SigPrefix + b64.EncodeToString(ed25519.Sign(priv, SigningInput(h)))}
}

// TestForwardCompatibleFeatures: a token from a newer site may name features this build predates.
// It verifies, the unknown names grant nothing, the known ones keep working, and every other check
// (shape, signature) stays strict (ADR-079, forward compatibility).
func TestForwardCompatibleFeatures(t *testing.T) {
	priv, ks := testSigner(t)
	many := make([]string, maxFeatures+1)
	for i := range many {
		many[i] = "feature_" + strings.Repeat("x", i%20+1)
	}
	for _, tc := range []struct {
		name     string
		features []string
		tamper   func(*Token)
		wantErr  error // nil: accepted; errBody: refused shape; ErrSignature
		granted  []string
	}{
		{name: "known and unknown", features: []string{FeatureScanFull, "undo_session", FeaturePacksPro, "security_pack", "agency"},
			granted: []string{FeatureScanFull, FeaturePacksPro}},
		{name: "unknown only", features: []string{"undo_session", "security_pack", "agency"}, granted: []string{}},
		{name: "known only", features: PlanFeatures[PlanPro], granted: PlanFeatures[PlanPro]},
		{name: "duplicate known", features: []string{FeatureScanFix, FeatureScanFix, "agency"}, granted: []string{FeatureScanFix}},
		{name: "at the cap", features: many[:maxFeatures], granted: []string{}},
		{name: "malformed: uppercase", features: []string{FeatureScanFull, "Undo_Session"}, wantErr: errBody},
		{name: "malformed: space", features: []string{"undo session"}, wantErr: errBody},
		{name: "malformed: empty name", features: []string{FeatureScanFull, ""}, wantErr: errBody},
		{name: "malformed: too short", features: []string{"ab"}, wantErr: errBody},
		{name: "malformed: too long", features: []string{"a" + strings.Repeat("b", 32)}, wantErr: errBody},
		{name: "malformed: leading digit", features: []string{"1agency"}, wantErr: errBody},
		{name: "malformed: dash", features: []string{"security-pack"}, wantErr: errBody},
		{name: "malformed: empty list", features: []string{}, wantErr: errBody},
		{name: "malformed: over the cap", features: many, wantErr: errBody},
		{name: "bad signature", features: []string{FeatureScanFull, "undo_session"},
			tamper: func(x *Token) { x.Sig = x.Sig[:20] + flip(x.Sig[20]) + x.Sig[21:] }, wantErr: ErrSignature},
		{name: "unknown feature added after signing", features: []string{FeatureScanFull},
			tamper: func(x *Token) { x.Features = append(x.Features, "agency") }, wantErr: ErrSignature},
		{name: "known feature added after signing", features: []string{FeatureScanFull, "agency"},
			tamper: func(x *Token) { x.Features = append(x.Features, FeatureActionPrivate) }, wantErr: ErrSignature},
		{name: "unknown key id", features: []string{FeatureScanFull, "agency"},
			tamper: func(x *Token) { x.KeyID = "lk_01jdtestkey00000000000000b" }, wantErr: ErrSignature},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := bodies()[2]
			b.Features = append([]string(nil), tc.features...)
			tok := signRaw(t, priv, b)
			if tc.tamper != nil {
				tc.tamper(&tok)
			}
			raw, err := tok.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			back, perr := Parse(raw)
			verr := tok.Verify(ks)
			if tc.wantErr != nil {
				if !errors.Is(verr, tc.wantErr) {
					t.Fatalf("Verify: got %v, want %v", verr, tc.wantErr)
				}
				if errors.Is(tc.wantErr, errBody) && perr == nil {
					t.Fatal("Parse accepted a malformed features list")
				}
				if _, err := Sign(testKeyID, priv, b); errors.Is(tc.wantErr, errBody) && err == nil {
					t.Fatal("Sign accepted a malformed features list")
				}
				return
			}
			if perr != nil || verr != nil {
				t.Fatalf("refused: parse %v, verify %v", perr, verr)
			}
			if err := back.Verify(ks); err != nil {
				t.Fatalf("round trip: %v", err)
			}
			if got := back.Granted(); !slices.Equal(got, tc.granted) {
				t.Fatalf("Granted = %v, want %v", got, tc.granted)
			}
			for _, f := range knownFeatures {
				if back.Has(f) != slices.Contains(tc.granted, f) {
					t.Errorf("Has(%q) = %v", f, back.Has(f))
				}
			}
			for _, f := range back.Features {
				if !slices.Contains(knownFeatures, f) && back.Has(f) {
					t.Errorf("an unknown feature was granted: %q", f)
				}
			}
			// The body keeps every name, so the signature still covers the exact signed bytes.
			if !slices.Equal(back.Features, tc.features) {
				t.Fatalf("features rewritten: %v", back.Features)
			}
		})
	}
}

// TestUnknownFeatureNeverRuns: through the manager, a token naming a future feature keeps its known
// features running and never lets the unknown one run; expiry still applies.
func TestUnknownFeatureNeverRuns(t *testing.T) {
	priv, ks := testSigner(t)
	b := bodies()[0] // report: 30 days, test mode
	b.Features = append(append([]string(nil), b.Features...), "undo_session")
	tok, err := Sign(testKeyID, priv, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := tok.Verify(ks); err != nil {
		t.Fatal(err)
	}
	if tok.Has("undo_session") {
		t.Fatal("an unknown feature was granted")
	}
	v, _ := time.Parse(TimeLayout, b.ValidatedAt)
	if s := Evaluate(tok, v.Add(time.Hour), false); !s.Active || !tok.Has(FeatureScanFull) {
		t.Fatalf("known feature inside the grace: %+v", s)
	}
	pe, _ := time.Parse(TimeLayout, *b.PeriodEnd)
	if s := Evaluate(tok, pe.Add(15*24*time.Hour), false); s.Active || !s.Expired {
		t.Fatalf("expired token still active: %+v", s)
	}
	if s := Evaluate(tok, v.Add(time.Hour), true); s.Active || !s.ModeMismatch {
		t.Fatalf("test token active on a live build: %+v", s)
	}
}
