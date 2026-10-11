package licence

import (
	"bytes"
	"crypto/ed25519"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/keys"
)

// The public lk_ keys this build trusts. The founder generates the key pair offline
// (deedglass keys gen --purpose licence), puts the private JWK in the site's Pages secret and
// commits only this file. The same file is served at /.well-known/deedglass/licence-keys.json.
// An empty set means no licence verifies: Pro stays off, everything free works.
//
//go:embed licence-keys.json
var embeddedKeys []byte

// ObjectKeySet is the key set's object field.
const ObjectKeySet = "licence_key_set"

// KeySet is the set of lk_ public keys a verifier trusts.
type KeySet struct {
	keys map[string]ed25519.PublicKey
}

// KeyRecord is one public key in licence-keys.json.
type KeyRecord struct {
	ID         string `json:"id"`
	Alg        string `json:"alg"`
	PublicKey  string `json:"public_key"` // base64url, 32 bytes
	Thumbprint string `json:"thumbprint"` // sha256 of the raw key
}

type keySetFile struct {
	Object string      `json:"object"`
	Keys   []KeyRecord `json:"keys"`
}

// ParseKeySet reads licence-keys.json strictly. Any unsafe or inconsistent key refuses the whole set.
func ParseKeySet(raw []byte) (KeySet, error) {
	var f keySetFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return KeySet{}, fmt.Errorf("licence: the key set isn't valid JSON: %w", err)
	}
	if f.Object != ObjectKeySet {
		return KeySet{}, errors.New(`licence: the key set's object must be "licence_key_set"`)
	}
	ks := KeySet{keys: map[string]ed25519.PublicKey{}}
	for _, k := range f.Keys {
		if !reLK.MatchString(k.ID) {
			return KeySet{}, fmt.Errorf("licence: key %q is not an lk_ id", k.ID)
		}
		if k.Alg != "Ed25519" {
			return KeySet{}, fmt.Errorf("licence: key %s has alg %q; only Ed25519", k.ID, k.Alg)
		}
		raw, err := b64.DecodeString(k.PublicKey)
		if err != nil {
			return KeySet{}, fmt.Errorf("licence: key %s isn't base64url", k.ID)
		}
		pub, err := keys.ParsePublicKey(raw)
		if err != nil {
			return KeySet{}, fmt.Errorf("licence: key %s: %w", k.ID, err)
		}
		if deed.Thumbprint(pub) != k.Thumbprint {
			return KeySet{}, fmt.Errorf("licence: key %s: the thumbprint doesn't match the key", k.ID)
		}
		if _, dup := ks.keys[k.ID]; dup {
			return KeySet{}, fmt.Errorf("licence: key %s is listed twice", k.ID)
		}
		ks.keys[k.ID] = pub
	}
	return ks, nil
}

// Embedded returns the key set built into this binary.
func Embedded() (KeySet, error) { return ParseKeySet(embeddedKeys) }

// EmbeddedJSON is licence-keys.json as built in (served by the site at the well-known path).
func EmbeddedJSON() []byte { return bytes.Clone(embeddedKeys) }

// Len is the number of keys.
func (ks KeySet) Len() int { return len(ks.keys) }

func (ks KeySet) lookup(id string) (ed25519.PublicKey, bool) {
	if !strings.HasPrefix(id, "lk_") {
		return nil, false
	}
	k, ok := ks.keys[id]
	return k, ok
}

// Record describes pub as a licence-keys.json entry.
func Record(id string, pub ed25519.PublicKey) KeyRecord {
	return KeyRecord{ID: id, Alg: "Ed25519", PublicKey: b64.EncodeToString(pub), Thumbprint: deed.Thumbprint(pub)}
}

// KeySetJSON renders records as licence-keys.json.
func KeySetJSON(recs []KeyRecord) ([]byte, error) {
	if recs == nil {
		recs = []KeyRecord{}
	}
	b, err := json.MarshalIndent(keySetFile{Object: ObjectKeySet, Keys: recs}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
