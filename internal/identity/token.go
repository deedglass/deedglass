package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"time"
)

// Sign-in tokens and codes (S07 §3.4). Only hashes are stored.
const (
	TokenBytes      = 32 // 256-bit; base64url without padding = 43 characters
	TokenLen        = 43
	LoginTTL        = 15 * time.Minute
	InviteTTL       = 7 * 24 * time.Hour
	MaxCodeAttempts = 5
	CodeDigits      = 6
)

// codeDomain separates code hashes from every other sha256 in Deedglass.
const codeDomain = "deedglass/login-code/v1\n"

// ErrMalformedToken: a token that isn't exactly 43 base64url characters.
var ErrMalformedToken = errors.New("identity: a token is 43 base64url characters")

// NewToken returns a fresh 256-bit token (base64url, no padding) and its sha256. It reads
// crypto/rand only.
func NewToken() (raw string, hash [32]byte, err error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", hash, fmt.Errorf("identity: reading random bytes: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, sha256.Sum256([]byte(raw)), nil
}

// HashToken returns the sha256 of a raw token, after checking its shape: exactly 43 characters of
// the base64url alphabet that decode to 32 bytes. Anything else (padding, '+', '/', a newline) is
// ErrMalformedToken, so a malformed token never reaches the database.
func HashToken(raw string) ([32]byte, error) {
	var zero [32]byte
	if len(raw) != TokenLen {
		return zero, ErrMalformedToken
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return zero, ErrMalformedToken
		}
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(b) != TokenBytes {
		return zero, ErrMalformedToken
	}
	return sha256.Sum256([]byte(raw)), nil
}

var codeMax = big.NewInt(1_000_000)

// NewCode returns a uniformly random 6-digit code, leading zeros kept ("004817"). crypto/rand.Int
// samples by rejection, so no value is more likely than another.
func NewCode() (string, error) { return newCodeFrom(rand.Reader) }

// newCodeFrom draws a code from r: crypto/rand in production, a seeded stream in the uniformity
// test, so that test is deterministic (DL-034).
func newCodeFrom(r io.Reader) (string, error) {
	n, err := rand.Int(r, codeMax)
	if err != nil {
		return "", fmt.Errorf("identity: reading random bytes: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// HashCode is sha256(pepper ‖ "deedglass/login-code/v1\n" ‖ tokenID ‖ "\n" ‖ code). The pepper
// (DEEDGLASS_API_KEY_PEPPER) means a leaked code hash can't be brute-forced offline, and the token
// id binds a code to the one email it was sent in.
func HashCode(pepper []byte, tokenID, code string) [32]byte {
	h := sha256.New()
	h.Write(pepper)
	h.Write([]byte(codeDomain))
	h.Write([]byte(tokenID))
	h.Write([]byte{'\n'})
	h.Write([]byte(code))
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// NormalizeCode strips spaces from a typed code ("482 913") and reports whether what is left is
// exactly six digits.
func NormalizeCode(s string) (string, bool) {
	b := make([]byte, 0, CodeDigits)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
		case c >= '0' && c <= '9' && len(b) < CodeDigits:
			b = append(b, c)
		default:
			return "", false
		}
	}
	return string(b), len(b) == CodeDigits
}

// Equal compares two hashes in constant time.
func Equal(a, b [32]byte) bool { return subtle.ConstantTimeCompare(a[:], b[:]) == 1 }

// equalBytes compares a stored hash (bytea) with a computed one in constant time. A stored value
// of the wrong length never matches.
func equalBytes(stored []byte, h [32]byte) bool {
	return len(stored) == 32 && subtle.ConstantTimeCompare(stored, h[:]) == 1
}
