package deed_test

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/veraison/go-cose"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

func TestCOSERoundTrip(t *testing.T) {
	env, _ := base(t)
	b := deedtest.TestKey("b")
	now := deedtest.T0.Add(time.Hour)
	enc, err := deed.EncodeCOSE(env, b.Signer, deedtest.Issuer, now)
	if err != nil {
		t.Fatal(err)
	}
	if enc[0] != 0xd2 {
		t.Fatalf("not tag 18: %x", enc[:2])
	}
	again, _ := deed.EncodeCOSE(env, b.Signer, deedtest.Issuer, now)
	if string(again) != string(enc) {
		t.Fatal("COSE bytes are not deterministic")
	}
	r, err := deed.VerifyCOSE(enc, deedtest.KeySet())
	if err != nil {
		t.Fatal(err)
	}
	if r.Body.DeedID != env.ID || r.Issuer != deedtest.Issuer || r.KeyID != b.ID || !r.IssuedAt.Equal(now) || r.Subject != env.Body.Org+"/"+env.ID {
		t.Fatalf("%+v", r)
	}

	// Tampering: a payload byte, a signature byte, the tag.
	for name, i := range map[string]int{"payload": len(enc) - 80, "signature": len(enc) - 1} {
		bad := append([]byte{}, enc...)
		bad[i] ^= 1
		if _, err := deed.VerifyCOSE(bad, deedtest.KeySet()); err == nil {
			t.Errorf("%s flip verified", name)
		}
	}
	if _, err := deed.VerifyCOSE(enc[1:], deedtest.KeySet()); deed.CodeOf(err) != deed.CodeMalformed {
		t.Errorf("untagged: %v", err)
	}
	// Signed by key a after it retired: not valid at iat.
	if late, _ := deed.EncodeCOSE(env, deedtest.TestKey("a").Signer, deedtest.Issuer, now); late != nil {
		if _, err := deed.VerifyCOSE(late, deedtest.KeySet()); deed.CodeOf(err) != deed.CodeSealKeyNotValidAt {
			t.Errorf("retired key: %v", err)
		}
	}
	// Unknown key.
	if _, err := deed.VerifyCOSE(enc, nil); deed.CodeOf(err) != deed.CodeSealKeyUnknown {
		t.Errorf("no key set: %v", err)
	}
	if _, err := deed.EncodeCOSE(env, b.Signer, "", now); err == nil {
		t.Error("empty issuer")
	}
}

func TestCOSERefusesOtherShapes(t *testing.T) {
	env, _ := base(t)
	b := deedtest.TestKey("b")
	payload, _ := env.MarshalCanonical() // the envelope, not the body
	hdr := func(alg cose.Algorithm, ct string, claims cose.CWTClaims) cose.Headers {
		h := cose.Headers{Protected: cose.ProtectedHeader{cose.HeaderLabelAlgorithm: alg, cose.HeaderLabelContentType: ct, cose.HeaderLabelKeyID: []byte(b.ID)}}
		if claims != nil {
			h.Protected[cose.HeaderLabelCWTClaims] = claims
		}
		return h
	}
	signer, _ := cose.NewSigner(cose.AlgorithmEdDSA, signerOf(b))
	good := cose.CWTClaims{1: deedtest.Issuer, 2: env.Body.Org + "/" + env.ID, 6: deedtest.T0.Add(time.Hour).Unix()}
	cases := map[string]struct {
		h    cose.Headers
		p    []byte
		code deed.Code
	}{
		"wrong content type": {hdr(cose.AlgorithmEdDSA, "application/json", good), payload, deed.CodeSchemaInvalid},
		"no claims":          {hdr(cose.AlgorithmEdDSA, deed.COSEContentType, nil), payload, deed.CodeSchemaInvalid},
		"envelope payload":   {hdr(cose.AlgorithmEdDSA, deed.COSEContentType, good), payload, deed.CodeSchemaInvalid},
		"no sub":             {hdr(cose.AlgorithmEdDSA, deed.COSEContentType, cose.CWTClaims{1: "x", 6: int64(1)}), payload, deed.CodeSchemaInvalid},
	}
	for name, tc := range cases {
		raw, err := cose.Sign1(nil, signer, tc.h, tc.p, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := deed.VerifyCOSE(raw, deedtest.KeySet()); deed.CodeOf(err) != tc.code {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func signerOf(k deedtest.Key) ed25519.PrivateKey { return ed25519.NewKeyFromSeed(k.Seed) }
