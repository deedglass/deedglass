package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"regexp"
)

// API key secrets (contract §3): dg_<mode>_<prefix 8 base62>_<secret 32 base62>, exactly 49
// characters. The prefix is public (it is the lookup key and is shown in lists); the secret is
// shown once and stored only as sha256(pepper ‖ secret).
var keyRE = regexp.MustCompile(`^dg_(live|test)_([0-9A-Za-z]{8})_([0-9A-Za-z]{32})$`)

// KeyRegexp is the pattern secret scanners match (docs/AGENTS.md).
const KeyRegexp = `dg_(live|test)_[0-9A-Za-z]{8}_[0-9A-Za-z]{32}`

// Key lengths.
const (
	KeyLen       = 49
	KeyPrefixLen = 8
	KeySecretLen = 32
)

// Mode is a key's mode: live or test. A test key carries Livemode=false in the principal, and S13
// records it on every action.
type Mode string

const (
	ModeLive Mode = "live"
	ModeTest Mode = "test"
)

// Valid reports whether m is live or test.
func (m Mode) Valid() bool { return m == ModeLive || m == ModeTest }

// ParsedKey is a bearer token split into its parts.
type ParsedKey struct {
	Mode   Mode
	Prefix string
	Secret string
}

// Display is the key as Deedglass shows it after creation: dg_live_jJbEEMWk_••••.
func (p ParsedKey) Display() string { return DisplayPrefix(p.Mode, p.Prefix) + "_••••" }

// DisplayPrefix is the public part of a key, as the API's "prefix" field: dg_live_jJbEEMWk.
func DisplayPrefix(m Mode, prefix string) string { return "dg_" + string(m) + "_" + prefix }

// ErrMalformedKey is returned for anything that isn't exactly a Deedglass key. It never quotes the
// input: a mistyped key is still mostly a secret.
var ErrMalformedKey = errors.New("identity: not a Deedglass API key")

// ParseAPIKey splits a key strictly: no surrounding space, no other case, no other lengths.
func ParseAPIKey(s string) (ParsedKey, error) {
	if len(s) != KeyLen {
		return ParsedKey{}, ErrMalformedKey
	}
	m := keyRE.FindStringSubmatch(s)
	if m == nil {
		return ParsedKey{}, ErrMalformedKey
	}
	return ParsedKey{Mode: Mode(m[1]), Prefix: m[2], Secret: m[3]}, nil
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewAPIKey generates a key from crypto/rand. Characters are drawn by rejection sampling (bytes ≥
// 248 are discarded), so every base62 character is equally likely: the 32-character secret
// carries 32·log2(62) ≈ 190 bits.
func NewAPIKey(m Mode) (full string, p ParsedKey, err error) {
	if !m.Valid() {
		return "", ParsedKey{}, errors.New("identity: key mode must be live or test")
	}
	chars, err := randomBase62(KeyPrefixLen + KeySecretLen)
	if err != nil {
		return "", ParsedKey{}, err
	}
	p = ParsedKey{Mode: m, Prefix: chars[:KeyPrefixLen], Secret: chars[KeyPrefixLen:]}
	return DisplayPrefix(m, p.Prefix) + "_" + p.Secret, p, nil
}

func randomBase62(n int) (string, error) {
	out := make([]byte, 0, n)
	buf := make([]byte, 64)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b >= 248 { // 248 = 4·62: keep only bytes that map evenly
				continue
			}
			out = append(out, base62[b%62])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

// HashSecret is sha256(pepper ‖ secret), per contract §3. Compare results with
// crypto/subtle.ConstantTimeCompare, never with == or bytes.Equal.
func HashSecret(pepper []byte, secret string) [32]byte {
	h := sha256.New()
	h.Write(pepper)
	h.Write([]byte(secret))
	var out [32]byte
	h.Sum(out[:0])
	return out
}
