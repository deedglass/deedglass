package deed

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/veraison/go-cose"
)

// COSE_Sign1 encoding (RFC 9052, tag 18), shaped as an RFC 9943 Signed Statement: the protected
// header carries CWT Claims (RFC 9597, label 15) with iss and sub. It is a second, independent
// signed statement about the same body, made at export time with the active seal key; the native
// seal stays the primary record. It is not registered with any transparency service and is not
// an RFC 9942 transparency-service object (DEED_SPEC.md §12).

// COSEContentType is the protected content_type (label 3).
const COSEContentType = "application/vnd.deedglass.deed.v1+json"

// coseAlg is −8 (EdDSA, with crv Ed25519 documented). RFC 9864 registers −19 for Ed25519;
// go-cose v1.3.0 has no −19, and the signature bytes are the same (ADR-008, ADR-026).
const coseAlg = cose.AlgorithmEdDSA

// coseSigner adapts a Signer to go-cose: pure Ed25519 over the COSE Sig_structure.
type coseSigner struct{ s Signer }

func (c coseSigner) Algorithm() cose.Algorithm { return coseAlg }
func (c coseSigner) Sign(_ io.Reader, toBeSigned []byte) ([]byte, error) {
	return c.s.Sign(toBeSigned)
}

// EncodeCOSE returns a tagged COSE_Sign1 whose payload is JCS(env.Body), signed by s at now.
// Protected header: 1 alg −8, 3 content_type, 4 kid (the seal key id as bytes),
// 15 CWT claims {1 iss: issuer, 2 sub: "<org>/<deed_id>", 6 iat: now in Unix seconds}.
func EncodeCOSE(env *Envelope, s Signer, issuer string, now time.Time) ([]byte, error) {
	if issuer == "" {
		return nil, errors.New("deed: COSE needs an issuer")
	}
	if err := env.Body.Validate(); err != nil {
		return nil, err
	}
	payload, err := StrictCanonical(&env.Body)
	if err != nil {
		return nil, err
	}
	msg := cose.NewSign1Message()
	msg.Headers.Protected = cose.ProtectedHeader{
		cose.HeaderLabelAlgorithm:   coseAlg,
		cose.HeaderLabelContentType: COSEContentType,
		cose.HeaderLabelKeyID:       []byte(s.KeyID()),
		cose.HeaderLabelCWTClaims: cose.CWTClaims{
			cose.CWTClaimIssuer:   issuer,
			cose.CWTClaimSubject:  env.Body.Org + "/" + env.Body.DeedID,
			cose.CWTClaimIssuedAt: now.Unix(),
		},
	}
	msg.Payload = payload
	if err := msg.Sign(nil, nil, coseSigner{s}); err != nil {
		return nil, fmt.Errorf("deed: COSE sign: %w", err)
	}
	return msg.MarshalCBOR()
}

// COSEResult is a verified COSE deed.
type COSEResult struct {
	Body     *Body
	Issuer   string
	Subject  string
	IssuedAt time.Time
	KeyID    string
}

// VerifyCOSE checks a COSE deed: the tag (18), alg −8, the content type, a kid in the key set,
// the key's validity at iat (revoked keys always fail), the signature, CWT claims iss and sub, and
// that the payload is canonical JCS of a valid Deed v1 body whose org and deed_id match sub.
func VerifyCOSE(b []byte, ks *KeySet) (*COSEResult, error) {
	var msg cose.Sign1Message
	if err := msg.UnmarshalCBOR(b); err != nil {
		return nil, errMalformed("not a tagged COSE_Sign1: " + err.Error())
	}
	h := msg.Headers.Protected
	if alg, err := h.Algorithm(); err != nil || alg != coseAlg {
		return nil, errSchemaField("cose.alg", "must be -8 (EdDSA, Ed25519)")
	}
	if ct, _ := h[cose.HeaderLabelContentType].(string); ct != COSEContentType {
		return nil, errSchemaField("cose.content_type", "must be "+COSEContentType)
	}
	kid, _ := h[cose.HeaderLabelKeyID].([]byte)
	claims, ok := h[cose.HeaderLabelCWTClaims].(map[any]any)
	if !ok {
		return nil, errSchemaField("cose.cwt_claims", "must be present")
	}
	iss, _ := claim(claims, cose.CWTClaimIssuer).(string)
	sub, _ := claim(claims, cose.CWTClaimSubject).(string)
	iat, okIat := claimInt(claims, cose.CWTClaimIssuedAt)
	if iss == "" || sub == "" || !okIat {
		return nil, errSchemaField("cose.cwt_claims", "must hold iss, sub and iat")
	}
	_, pub, ok := ks.Lookup(string(kid))
	if !ok {
		return nil, &VerifyError{Code: CodeSealKeyUnknown, Key: string(kid)}
	}
	at := time.Unix(iat, 0).UTC()
	if c := ks.validAt(string(kid), at); c != "" {
		return nil, &VerifyError{Code: c, Key: string(kid), IssuedAt: FormatTime(at)}
	}
	v, err := cose.NewVerifier(coseAlg, ed25519.PublicKey(pub))
	if err != nil {
		return nil, fmt.Errorf("deed: COSE verifier: %w", err)
	}
	if err := msg.Verify(nil, v); err != nil {
		return nil, &VerifyError{Code: CodeSealInvalid, Key: string(kid), Detail: "COSE signature"}
	}
	canon, err := canonicalBytes(msg.Payload)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canon, msg.Payload) {
		return nil, &VerifyError{Code: CodeNonCanonical, Detail: "COSE payload"}
	}
	p, verr := parseBodyBytes(msg.Payload)
	if verr != nil {
		return nil, verr
	}
	if sub != p.Org+"/"+p.DeedID {
		return nil, errSchemaField("cose.cwt_claims.sub", "must be <org>/<deed_id> of the payload")
	}
	return &COSEResult{Body: p, Issuer: iss, Subject: sub, IssuedAt: at, KeyID: string(kid)}, nil
}

func parseBodyBytes(b []byte) (*Body, *VerifyError) {
	tree, err := parseStrictTree(b)
	if err != nil {
		return nil, err
	}
	body, verr := bodyFromTree(tree)
	if verr != nil {
		return nil, verr
	}
	if err := body.Validate(); err != nil {
		return nil, err.(*VerifyError)
	}
	return body, nil
}

func claim(m map[any]any, k int64) any {
	if v, ok := m[k]; ok {
		return v
	}
	if k >= 0 {
		return m[uint64(k)]
	}
	return nil
}

func claimInt(m map[any]any, k int64) (int64, bool) {
	switch v := claim(m, k).(type) {
	case int64:
		return v, true
	case uint64:
		if v <= uint64(MaxSafeInteger) {
			return int64(v), true
		}
	}
	return 0, false
}
