package deed

import (
	"bytes"
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

// COSEContentType is the protected content_type (label 3) of a v1 deed; COSEContentTypeV2 of a v2
// deed (ADR-109).
const (
	COSEContentType   = "application/vnd.deedglass.deed.v1+json"
	COSEContentTypeV2 = "application/vnd.deedglass.deed.v2+json"
)

// coseAlg is −8 (EdDSA, with crv Ed25519 documented). RFC 9864 registers −19 for Ed25519;
// go-cose v1.3.0 has no −19, and the signature bytes are the same (ADR-008, ADR-026).
const coseAlg = cose.AlgorithmEdDSA

// coseAlgOf maps a seal algorithm to its COSE value: −8 for Ed25519 (ADR-008), −49 for ML-DSA-65
// (RFC 9964).
func coseAlgOf(alg string) (cose.Algorithm, bool) {
	switch alg {
	case AlgEd25519:
		return coseAlg, true
	case AlgMLDSA65:
		return cose.Algorithm(COSEAlgMLDSA65), true
	}
	return 0, false
}

// coseSigner adapts an AlgSigner to go-cose: its algorithm over the COSE Sig_structure.
type coseSigner struct {
	s   AlgSigner
	alg cose.Algorithm
}

func (c coseSigner) Algorithm() cose.Algorithm { return c.alg }
func (c coseSigner) Sign(_ io.Reader, toBeSigned []byte) ([]byte, error) {
	return c.s.Sign(toBeSigned)
}

// coseVerifier checks a COSE signature with a key of the registry (alg.go).
type coseVerifier struct {
	a   *algSpec
	pub any
	alg cose.Algorithm
}

func (c coseVerifier) Algorithm() cose.Algorithm { return c.alg }
func (c coseVerifier) Verify(content, sig []byte) error {
	if len(sig) != c.a.sigSize || !c.a.verify(c.pub, content, sig) {
		return errors.New("deed: COSE signature")
	}
	return nil
}

// edAlgSigner presents an Ed25519 Signer as an AlgSigner.
type edAlgSigner struct{ Signer }

func (edAlgSigner) Alg() string              { return AlgEd25519 }
func (e edAlgSigner) PublicKeyBytes() []byte { return e.PublicKey() }

// EncodeCOSE returns a tagged COSE_Sign1 whose payload is JCS(env.Body), signed by the Ed25519 key s
// at now. Protected header: 1 alg −8, 3 content_type, 4 kid (the seal key id as bytes),
// 15 CWT claims {1 iss: issuer, 2 sub: "<org>/<deed_id>", 6 iat: now in Unix seconds}.
func EncodeCOSE(env *Envelope, s Signer, issuer string, now time.Time) ([]byte, error) {
	return EncodeCOSEAlg(env, edAlgSigner{s}, issuer, now)
}

// EncodeCOSEAlg is EncodeCOSE with a signer of any registry algorithm: an issuer signs a v2 deed's
// COSE export with its ML-DSA-65 key (alg −49, RFC 9964) and the v2 content type (ADR-109).
func EncodeCOSEAlg(env *Envelope, s AlgSigner, issuer string, now time.Time) ([]byte, error) {
	alg, ok := coseAlgOf(s.Alg())
	if !ok {
		return nil, fmt.Errorf("deed: COSE has no algorithm value for %s", s.Alg())
	}
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
	ct := COSEContentType
	if env.Body.V == VersionV2 {
		ct = COSEContentTypeV2
	}
	msg := cose.NewSign1Message()
	msg.Headers.Protected = cose.ProtectedHeader{
		cose.HeaderLabelAlgorithm:   alg,
		cose.HeaderLabelContentType: ct,
		cose.HeaderLabelKeyID:       []byte(s.KeyID()),
		cose.HeaderLabelCWTClaims: cose.CWTClaims{
			cose.CWTClaimIssuer:   issuer,
			cose.CWTClaimSubject:  env.Body.Org + "/" + env.Body.DeedID,
			cose.CWTClaimIssuedAt: now.Unix(),
		},
	}
	msg.Payload = payload
	if err := msg.Sign(nil, nil, coseSigner{s: s, alg: alg}); err != nil {
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

// VerifyCOSE checks a COSE deed: the tag (18), alg −8 (Ed25519) or −49 (ML-DSA-65), the content
// type, a kid in the key set whose algorithm is the COSE alg, the key's validity at iat (revoked keys
// always fail), the signature, CWT claims iss and sub, and that the payload is canonical JCS of a
// valid deed body of the content type's version whose org and deed_id match sub.
func VerifyCOSE(b []byte, ks *KeySet) (*COSEResult, error) {
	var msg cose.Sign1Message
	if err := msg.UnmarshalCBOR(b); err != nil {
		return nil, errMalformed("not a tagged COSE_Sign1: " + err.Error())
	}
	h := msg.Headers.Protected
	alg, err := h.Algorithm()
	sealAlg := ""
	switch {
	case err != nil:
	case alg == coseAlg:
		sealAlg = AlgEd25519
	case alg == cose.Algorithm(COSEAlgMLDSA65):
		sealAlg = AlgMLDSA65
	}
	if sealAlg == "" {
		return nil, errSchemaField("cose.alg", "must be -8 (EdDSA, Ed25519) or -49 (ML-DSA-65)")
	}
	ct, _ := h[cose.HeaderLabelContentType].(string)
	if ct != COSEContentType && ct != COSEContentTypeV2 {
		return nil, errSchemaField("cose.content_type", "must be "+COSEContentType+" or "+COSEContentTypeV2)
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
	e, ok := ks.entry(string(kid))
	if !ok {
		return nil, &VerifyError{Code: CodeSealKeyUnknown, Key: string(kid)}
	}
	if e.alg == nil || e.alg.name != sealAlg {
		return nil, &VerifyError{Code: CodeSealInvalid, Key: string(kid), Detail: "the COSE alg is not the key's algorithm"}
	}
	at := time.Unix(iat, 0).UTC()
	if c := ks.validAt(string(kid), at); c != "" {
		return nil, &VerifyError{Code: c, Key: string(kid), IssuedAt: FormatTime(at)}
	}
	if err := msg.Verify(nil, coseVerifier{a: e.alg, pub: e.pub, alg: alg}); err != nil {
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
	if (ct == COSEContentTypeV2) != (p.V == VersionV2) {
		return nil, errSchemaField("cose.content_type", "must name the payload's deed version")
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
