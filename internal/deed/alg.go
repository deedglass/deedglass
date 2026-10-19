package deed

import (
	"crypto/ed25519"
	"regexp"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/keys"
)

// The seal algorithm registry (DEED_SPEC.md §7.4, ADR-109). Every seal of a v2 deed names its
// algorithm, and every algorithm has its own domain tag, seal prefix and accept window. An
// algorithm that is not in this table is never used to check anything: a deed that needs one is
// unsupported_algorithm (the verifier cannot decide).

// Seal key algorithms.
const (
	AlgEd25519 = "Ed25519" // RFC 8032 pure Ed25519, 32-byte keys, 64-byte signatures
)

// Ed25519AloneUntil is the accept policy's date for Ed25519: a deed issued at or after it needs a
// seal of another accepted algorithm too (NIST IR 8547 ipd lists EdDSA as disallowed after 2035).
// The policy reads issued_at only, never the verifier's clock, so the 2046 test gives the same
// answer every year. Moving it is a spec revision with new vectors.
var Ed25519AloneUntil = time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)

// MaxSeals bounds seal_keys and seals in a v2 deed.
const MaxSeals = 4

// reAlg is the pattern every alg name matches, known or not.
var reAlg = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,31}$`)

// reUnknownSeal is the shape of a seal whose algorithm this verifier doesn't implement: the
// lower-case alg name, a colon, base64url. It is only checked for shape, never verified.
var reUnknownSeal = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}:[A-Za-z0-9_-]+$`)

// algSpec is one implemented seal algorithm.
type algSpec struct {
	name       string
	prefix     string    // "ed25519:"
	tag        string    // the v2 domain tag, "deedglass/deed/v2/ed25519\n"
	keySize    int       // raw public key bytes
	sigSize    int       // raw signature bytes
	aloneUntil time.Time // zero: accepted alone for any issued_at
	// parseKey checks a raw public key and returns what verify needs; a non-nil error refuses the
	// whole key set (seal_key_rejected).
	parseKey func(raw []byte) (any, error)
	// verify checks sig over msg with a key parseKey returned.
	verify func(pub any, msg, sig []byte) bool
	// jwkOK reports whether a published jwk matches the key (DEED_SPEC.md §10.1).
	jwkOK func(j JWK, k *SealKey) bool
}

// algs is the registry, keyed by the alg name.
var algs = map[string]*algSpec{
	AlgEd25519: {
		name: AlgEd25519, prefix: SealPrefix, tag: TagDeedV2Ed25519,
		keySize: ed25519.PublicKeySize, sigSize: ed25519.SignatureSize,
		aloneUntil: Ed25519AloneUntil,
		parseKey:   parseEd25519Key,
		verify: func(pub any, msg, sig []byte) bool {
			return ed25519.Verify(pub.(ed25519.PublicKey), msg, sig)
		},
		jwkOK: func(j JWK, k *SealKey) bool {
			return j.Kty == "OKP" && j.Crv == "Ed25519" && j.X == k.PublicKey && j.Alg == "" && j.Pub == "" &&
				j.Kid == k.ID && j.Use == "sig"
		},
	},
}

// Algs lists the implemented algorithms, Ed25519 first.
func Algs() []string {
	out := []string{AlgEd25519}
	for n := range algs {
		if n != AlgEd25519 {
			out = append(out, n)
		}
	}
	return out
}

// AlgSupported reports whether this build can check seals of alg.
func AlgSupported(alg string) bool { _, ok := algs[alg]; return ok }

// sealShapeOK reports whether seal has the shape alg needs: the exact pattern for an implemented
// algorithm, the lower-case name and base64url for any other.
func sealShapeOK(alg, seal string) bool {
	if a, ok := algs[alg]; ok {
		return a.sealShapeOK(seal)
	}
	return reUnknownSeal.MatchString(seal) && strings.HasPrefix(seal, strings.ToLower(alg)+":")
}

// acceptedAlone reports whether a verified seal of alg is enough on its own for a deed issued at t.
func acceptedAlone(alg string, t time.Time) bool {
	a, ok := algs[alg]
	return ok && (a.aloneUntil.IsZero() || t.Before(a.aloneUntil))
}

// openAlgSeal checks one seal of an implemented algorithm over tag + body_hash.
func openAlgSeal(a *algSpec, pub any, tag, bodyHash, seal string) bool {
	if !a.sealShapeOK(seal) {
		return false
	}
	sig, err := b64.DecodeString(seal[len(a.prefix):])
	if err != nil || len(sig) != a.sigSize {
		return false
	}
	return a.verify(pub, []byte(tag+bodyHash), sig)
}

// parseEd25519Key refuses small-order and non-canonical points (attack case A13).
func parseEd25519Key(raw []byte) (any, error) {
	pub, err := keys.ParsePublicKey(raw)
	if err != nil {
		return nil, err
	}
	return pub, nil
}

// sealChars is the length of base64url without padding of n bytes.
func sealChars(n int) int { return (n*8 + 5) / 6 }

// sealShapeOK reports whether seal is prefix + exactly the base64url characters of a signature of
// this algorithm (Go's regexp can't count past 1,000).
func (a *algSpec) sealShapeOK(seal string) bool {
	if !strings.HasPrefix(seal, a.prefix) || len(seal) != len(a.prefix)+sealChars(a.sigSize) {
		return false
	}
	for i := len(a.prefix); i < len(seal); i++ {
		c := seal[i]
		if !('A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
