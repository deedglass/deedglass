package deed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gowebpki/jcs"

	"github.com/deedglass/deedglass/internal/intent"
)

// Hashes, domain tags and the canonical form (DEED_SPEC.md §6–§7). Every byte that is signed is
// named here.
const (
	// DigestPrefix starts every digest: "sha256:" + 64 lowercase hex characters.
	DigestPrefix = "sha256:"
	// TagDeed is prepended to body_hash to form a v1 deed's seal input.
	TagDeed = "deedglass/deed/v1\n"
	// TagDeedV2Ed25519 is a v2 deed's Ed25519 seal input tag. Every v2 algorithm has its own tag
	// (alg.go), so a signature made for one algorithm or version is never accepted as another.
	TagDeedV2Ed25519 = "deedglass/deed/v2/ed25519\n"
	// TagBundle is the export-bundle manifest's domain tag (S05).
	TagBundle = "deedglass/bundle/v1\n"
	// TagIntent is the signed-intent domain tag (S09).
	TagIntent = "deedglass/intent/v1\n"
	// TagGenesis is hashed with the org id to form the prev_hash of seq 1.
	TagGenesis = "deedglass/genesis\n"
)

// MaxSafeInteger is 2^53-1, the largest integer a deed body may hold.
const MaxSafeInteger = intent.MaxSafeInteger

// Digest returns "sha256:" + lowercase hex(SHA-256(b)).
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return DigestPrefix + hex.EncodeToString(sum[:])
}

// StrictCanonical returns JCS(v) (RFC 8785) for a deed body or envelope. v is marshalled, the bytes
// pass the strict profile (valid UTF-8, no duplicate keys, integers only in 0..2^53-1, no -0, no
// lone surrogates, depth ≤ 16), and only then are they canonicalized. Anything ambiguous is refused,
// never fixed up.
func StrictCanonical(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("deed: marshal: %w", err)
	}
	return canonicalBytes(b)
}

// canonicalBytes runs the strict profile over b, then JCS.
func canonicalBytes(b []byte) ([]byte, error) {
	if _, err := intent.ParseStrictNonNegative(b); err != nil {
		return nil, malformedFrom(err)
	}
	c, err := jcs.Transform(b)
	if err != nil {
		return nil, errMalformed("canonicalization failed: " + err.Error())
	}
	return c, nil
}

// DigestJSON is the payload digest (request_digest, result_digest, detail_digest, data_digest):
// "sha256:" + hex(SHA-256(JCS(raw))) with plain RFC 8785. Payloads may hold fractions and negative
// numbers, so the strict integer rule does not apply; JCS still refuses invalid JSON.
func DigestJSON(raw []byte) (string, error) {
	c, err := jcs.Transform(raw)
	if err != nil {
		return "", fmt.Errorf("deed: payload is not valid JSON: %w", err)
	}
	return Digest(c), nil
}

// DigestValue marshals v and returns DigestJSON of it.
func DigestValue(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("deed: marshal: %w", err)
	}
	return DigestJSON(b)
}

// malformedFrom turns a strict-parser error into CodeMalformed with a short detail.
func malformedFrom(err error) *VerifyError {
	var ie *intent.Error
	if errors.As(err, &ie) {
		d := ie.Msg
		if ie.Path != "" {
			d += " at " + ie.Path
		}
		return errMalformed(d)
	}
	return errMalformed(err.Error())
}

// parseStrictTree runs the deed strict profile and returns the tree.
func parseStrictTree(b []byte) (any, *VerifyError) {
	v, err := intent.ParseStrictNonNegative(b)
	if err != nil {
		return nil, malformedFrom(err)
	}
	return v, nil
}
