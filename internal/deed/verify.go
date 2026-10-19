package deed

import (
	"bytes"
	"fmt"
	"slices"
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
// failure wins (DEED_SPEC.md §11; ADR-109 for v2 and the accept policy):
//
//  1. size and strict JSON                          malformed
//  2. object is "deed"                              schema_invalid (object)
//  3. envelope id equals body.deed_id               id_mismatch
//  4. body.v is 1 or 2                              unsupported_version
//  5. closed schema, enums, patterns, lengths       schema_invalid
//  6. body_hash = sha256(JCS(body))                 body_hash_mismatch
//  7. v2: every seal_keys entry has a seal          seal_missing
//  8. the seals are the keys the body names         seal_key_mismatch
//  9. per seal: its alg is implemented              unsupported_algorithm
//  10. the key is in the key set                    seal_key_unknown
//  11. the key's alg is the seal's alg              seal_invalid
//  12. the key is not revoked                       seal_key_revoked
//  13. the key was active at issued_at              seal_key_not_valid_at
//  14. the seal verifies                            seal_invalid
//  15. the accept policy                            seal_missing
//  16. with Strict: bytes are canonical             non_canonical
//
// Every seal must pass; one good seal never covers a bad one. Every failure is a *VerifyError. A
// nil error means every check passed; no path turns an error into a valid result.
func VerifyEnvelope(raw []byte, ks *KeySet, opt VerifyOptions) (*Result, error) {
	p, verr := parseEnvelope(raw)
	if verr != nil {
		return nil, verr
	}
	env := p.env
	b := &env.Body
	fail := func(c Code, key, detail string) *VerifyError {
		return &VerifyError{Code: c, Seq: b.Seq, DeedID: b.DeedID, Key: key, IssuedAt: b.IssuedAt, Detail: detail}
	}
	if Digest(p.bodyJCS) != env.BodyHash { //deedglass:allow-compare a deed's public body hash
		return nil, fail(CodeBodyHashMismatch, env.SealKeyID, "")
	}
	seals := env.Seals
	tag := func(a *algSpec) string { return a.tag }
	if b.V == Version {
		if env.SealKeyID != b.SealKeyID {
			return nil, fail(CodeSealKeyMismatch, env.SealKeyID, "")
		}
		seals = []SealValue{{Alg: AlgEd25519, Seal: env.Seal, SealKeyID: env.SealKeyID}}
		tag = func(*algSpec) string { return TagDeed }
	} else {
		if c, key, detail := matchSeals(b.SealKeys, seals); c != "" {
			return nil, fail(c, key, detail)
		}
	}
	issued, _ := ParseTime(b.IssuedAt) // Validate checked it
	for _, sv := range seals {
		a := algs[sv.Alg]
		if a == nil {
			return nil, fail(CodeUnsupportedAlgorithm, sv.SealKeyID, sv.Alg)
		}
		e, ok := ks.entry(sv.SealKeyID)
		if !ok {
			return nil, fail(CodeSealKeyUnknown, sv.SealKeyID, "")
		}
		if e.alg != a {
			// A seal is checked only with a key of its own algorithm (attack case Y04).
			return nil, fail(CodeSealInvalid, sv.SealKeyID, "the key set lists "+sv.SealKeyID+" as "+e.key.Alg+", not "+sv.Alg)
		}
		if c := ks.validAt(sv.SealKeyID, issued); c != "" {
			return nil, fail(c, sv.SealKeyID, "")
		}
		if !openAlgSeal(a, e.pub, tag(a), env.BodyHash, sv.Seal) {
			return nil, fail(CodeSealInvalid, sv.SealKeyID, "")
		}
	}
	if detail := acceptPolicy(seals, ks, issued); detail != "" {
		return nil, fail(CodeSealMissing, env.SealKeyID, detail)
	}
	canonical := bytes.Equal(raw, p.envJCS)
	if opt.Strict && !canonical {
		return nil, fail(CodeNonCanonical, env.SealKeyID, "")
	}
	return &Result{Envelope: env, BodyHash: env.BodyHash, Seq: b.Seq, DeedID: b.DeedID, Org: b.Org,
		IssuedAt: issued, SealKeyID: env.SealKeyID, Canonical: canonical}, nil
}

// matchSeals checks a v2 envelope's seals against the body's seal_keys: every key the body names
// has a seal (else seal_missing, attack case Y02), and the seals are exactly those keys in that
// order with the same algorithms (else seal_key_mismatch, attack case Y05).
func matchSeals(keys []SealKeyRef, seals []SealValue) (Code, string, string) {
	for _, k := range keys {
		if !slices.ContainsFunc(seals, func(s SealValue) bool { return s.SealKeyID == k.ID }) {
			return CodeSealMissing, k.ID, "no " + k.Alg + " seal by " + k.ID
		}
	}
	if len(seals) != len(keys) {
		for _, s := range seals {
			if !slices.ContainsFunc(keys, func(k SealKeyRef) bool { return k.ID == s.SealKeyID }) {
				return CodeSealKeyMismatch, s.SealKeyID, "a seal by " + s.SealKeyID + ", which the body does not name"
			}
		}
		return CodeSealKeyMismatch, seals[len(seals)-1].SealKeyID, "two seals by the same key"
	}
	for i, k := range keys {
		if seals[i].SealKeyID != k.ID || seals[i].Alg != k.Alg {
			return CodeSealKeyMismatch, seals[i].SealKeyID, fmt.Sprintf("seal %d is %s by %s; the body names %s by %s", i, seals[i].Alg, seals[i].SealKeyID, k.Alg, k.ID)
		}
	}
	return "", "", ""
}

// acceptPolicy applies DEED_SPEC.md §11.4 to seals that all verified, by issued_at only:
// (a) at least one seal's algorithm is accepted alone at issued_at (Ed25519 only before
// Ed25519AloneUntil); (b) the key-set rule: every algorithm of which the set holds a key that is
// not revoked and was active at issued_at has a seal. It returns why the deed fails, or "".
func acceptPolicy(seals []SealValue, ks *KeySet, issued time.Time) string {
	has := func(alg string) bool {
		return slices.ContainsFunc(seals, func(s SealValue) bool { return s.Alg == alg })
	}
	if !slices.ContainsFunc(seals, func(s SealValue) bool { return acceptedAlone(s.Alg, issued) }) {
		return "Ed25519 alone is accepted only for deeds issued before " + FormatTime(Ed25519AloneUntil)
	}
	for _, alg := range ks.activeAlgs(issued) {
		if !has(alg) {
			return "the key set has a " + alg + " key active at " + FormatTime(issued) + ", and this deed has no " + alg + " seal"
		}
	}
	return ""
}
