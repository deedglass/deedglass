package deed

import (
	"bytes"
	"time"
)

// VerifyOptions tunes VerifyEnvelope.
type VerifyOptions struct {
	// Strict also requires the input bytes to equal JCS(envelope) exactly (code non_canonical).
	// Without it, whitespace and key order in transit don't matter: a deed pretty-printed with jq
	// still verifies.
	Strict bool
}

// Result is one deed that passed VerifyEnvelope.
type Result struct {
	Envelope  *Envelope
	BodyHash  string
	Seq       int64
	DeedID    string
	Org       string
	IssuedAt  time.Time
	SealKeyID string
	Canonical bool // the input bytes were exactly JCS(envelope)
}

// VerifyEnvelope checks one deed against a key set. The checks run in this order and the first
// failure wins (DEED_SPEC.md §11):
//
//  1. size and strict JSON                          malformed
//  2. object is "deed"                              schema_invalid (object)
//  3. envelope id equals body.deed_id               id_mismatch
//  4. body.v is 1                                   unsupported_version
//  5. closed schema, enums, patterns, lengths       schema_invalid
//  6. body_hash = sha256(JCS(body))                 body_hash_mismatch
//  7. envelope seal_key_id = body seal_key_id       seal_key_mismatch
//  8. the key is in the key set                     seal_key_unknown
//  9. the key is not revoked                        seal_key_revoked
//  10. the key was active at issued_at              seal_key_not_valid_at
//  11. the Ed25519 seal verifies                    seal_invalid
//  12. with Strict: bytes are canonical             non_canonical
//
// Every failure is a *VerifyError. A nil error means every check passed; no path turns an error
// into a valid result.
func VerifyEnvelope(raw []byte, ks *KeySet, opt VerifyOptions) (*Result, error) {
	p, verr := parseEnvelope(raw)
	if verr != nil {
		return nil, verr
	}
	env := p.env
	b := &env.Body
	fail := func(c Code) *VerifyError {
		return &VerifyError{Code: c, Seq: b.Seq, DeedID: b.DeedID, Key: env.SealKeyID, IssuedAt: b.IssuedAt}
	}
	if Digest(p.bodyJCS) != env.BodyHash {
		return nil, fail(CodeBodyHashMismatch)
	}
	if env.SealKeyID != b.SealKeyID {
		return nil, fail(CodeSealKeyMismatch)
	}
	_, pub, ok := ks.Lookup(env.SealKeyID)
	if !ok {
		return nil, fail(CodeSealKeyUnknown)
	}
	issued, _ := ParseTime(b.IssuedAt) // Validate checked it
	if c := ks.validAt(env.SealKeyID, issued); c != "" {
		return nil, fail(c)
	}
	if !openSeal(pub, env.BodyHash, env.Seal) {
		return nil, fail(CodeSealInvalid)
	}
	canonical := bytes.Equal(raw, p.envJCS)
	if opt.Strict && !canonical {
		return nil, fail(CodeNonCanonical)
	}
	return &Result{Envelope: env, BodyHash: env.BodyHash, Seq: b.Seq, DeedID: b.DeedID, Org: b.Org,
		IssuedAt: issued, SealKeyID: env.SealKeyID, Canonical: canonical}, nil
}
