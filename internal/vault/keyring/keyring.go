// Package keyring parses DEEDGLASS_VAULT_KEY: a ring of 1–4 comma-separated base64 keys of exactly
// 32 bytes. The first key is active (it wraps every new data key); the rest only unwrap. Each key
// is named by a derived id, kek:<12 hex>, which is the only thing about it ever printed or stored.
//
// It is a leaf package (stdlib only) so internal/config can validate the ring and print its ids
// without importing the vault.
package keyring

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// MaxKeys is the most keys a ring holds (an active key plus three being retired).
const MaxKeys = 4

// KeyLen is the length of every key-encryption key: AES-256.
const KeyLen = 32

// idDomain separates the id derivation from every other use of the key bytes.
const idDomain = "deedglass/vault/kek-id/v1\n"

// ID returns a key's id: "kek:" + the first 12 hex digits of sha256(domain ‖ key).
func ID(key []byte) string {
	h := sha256.New()
	h.Write([]byte(idDomain))
	h.Write(key)
	return "kek:" + hex.EncodeToString(h.Sum(nil))[:12]
}

// Key is one key of the ring.
type Key struct {
	ID    string
	Bytes []byte
}

// Problem is a ring the config refuses, with the next step.
type Problem struct {
	Message string // ends with a period
	Next    string
}

func (p *Problem) Error() string { return strings.TrimSpace(p.Message + " " + p.Next) }

const genHint = "Generate a key with `openssl rand -base64 32`."

// Parse reads a ring. Whitespace around each key is ignored; empty entries are not.
func Parse(s string) ([]Key, error) {
	parts := strings.Split(s, ",")
	if len(parts) > MaxKeys {
		return nil, &Problem{fmt.Sprintf("holds %d keys; a ring holds at most %d.", len(parts), MaxKeys),
			"Run deedglass vault rotate, then remove the keys no credential uses any more."}
	}
	out := make([]Key, 0, len(parts))
	seen := map[string]int{}
	for i, p := range parts {
		p = strings.TrimSpace(p)
		pos := ordinal(i + 1)
		if p == "" {
			return nil, &Problem{fmt.Sprintf("has an empty %s key.", pos), "Separate keys with single commas, the active key first."}
		}
		b, err := decode(p)
		if err != nil {
			return nil, &Problem{fmt.Sprintf("has a %s key that isn't base64.", pos), genHint}
		}
		if len(b) != KeyLen {
			return nil, &Problem{fmt.Sprintf("has a %s key of %d bytes; each key must be exactly %d.", pos, len(b), KeyLen), genHint}
		}
		id := ID(b)
		if j, dup := seen[id]; dup {
			return nil, &Problem{fmt.Sprintf("lists the same key twice (the %s and the %s, %s).", ordinal(j+1), pos, id),
				"Remove the duplicate."}
		}
		seen[id] = i
		out = append(out, Key{ID: id, Bytes: b})
	}
	return out, nil
}

// IDs lists a ring's key ids, active first.
func IDs(keys []Key) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.ID
	}
	return out
}

func decode(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.Strict().DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("not base64")
}

func ordinal(n int) string {
	switch n {
	case 1:
		return "first"
	case 2:
		return "second"
	case 3:
		return "third"
	case 4:
		return "fourth"
	}
	return fmt.Sprintf("%dth", n)
}
