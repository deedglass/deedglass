package deed

import (
	"crypto/ed25519"
	"fmt"
	"sort"
	"time"

	"github.com/deedglass/deedglass/internal/intent"
	"github.com/deedglass/deedglass/internal/keys"
)

// Seal key statuses.
const (
	KeyActive  = "active"
	KeyRetired = "retired" // retired_at is set; deeds issued before it stay valid
	KeyRevoked = "revoked" // every deed it sealed fails verification
)

// AlgEd25519 is the only seal key algorithm in v1.
const AlgEd25519 = "Ed25519"

// SealKey is one public seal key as keys.json publishes it (contract §7).
type SealKey struct {
	Object      string  `json:"object"` // "seal_key"
	ID          string  `json:"id"`
	Alg         string  `json:"alg"`        // "Ed25519"
	PublicKey   string  `json:"public_key"` // base64url, 32 bytes
	Thumbprint  string  `json:"thumbprint"` // sha256:<hex of the raw key>
	Status      string  `json:"status"`
	ActivatedAt string  `json:"activated_at"`
	RetiredAt   *string `json:"retired_at"`
	RevokedAt   *string `json:"revoked_at"`
	JWK         JWK     `json:"jwk"`
}

// JWK is the key as an RFC 8037 OKP JSON Web Key. It omits alg: RFC 9864 deprecates "EdDSA" for
// "Ed25519", and many JOSE libraries don't know the new name yet.
type JWK struct {
	Kty string `json:"kty"` // "OKP"
	Crv string `json:"crv"` // "Ed25519"
	X   string `json:"x"`   // = public_key
	Kid string `json:"kid"` // = id
	Use string `json:"use"` // "sig"
}

// KeySet is GET /.well-known/deedglass/keys.json: every seal key ever registered, sorted by
// activated_at.
type KeySet struct {
	Object    string    `json:"object"` // "seal_key_set"
	Issuer    string    `json:"issuer"`
	UpdatedAt string    `json:"updated_at"`
	Keys      []SealKey `json:"keys"`

	byID map[string]*keyEntry
}

type keyEntry struct {
	key       *SealKey
	pub       ed25519.PublicKey
	activated time.Time
	retired   *time.Time
	revoked   *time.Time
}

// KeyRecord is a seal key as the server stores it; NewKeySet turns records into a KeySet.
type KeyRecord struct {
	ID          string
	PublicKey   ed25519.PublicKey
	Status      string
	ActivatedAt time.Time
	RetiredAt   *time.Time
	RevokedAt   *time.Time
}

// NewSealKey renders one record in the published shape.
func NewSealKey(r KeyRecord) SealKey {
	x := b64.EncodeToString(r.PublicKey)
	k := SealKey{
		Object: "seal_key", ID: r.ID, Alg: AlgEd25519, PublicKey: x, Thumbprint: Thumbprint(r.PublicKey),
		Status: r.Status, ActivatedAt: FormatTime(r.ActivatedAt),
		JWK: JWK{Kty: "OKP", Crv: "Ed25519", X: x, Kid: r.ID, Use: "sig"},
	}
	if r.RetiredAt != nil {
		s := FormatTime(*r.RetiredAt)
		k.RetiredAt = &s
	}
	if r.RevokedAt != nil {
		s := FormatTime(*r.RevokedAt)
		k.RevokedAt = &s
	}
	return k
}

// NewKeySet builds the published key set from stored records. It applies the same checks as
// ParseKeySet, so a server never publishes a key a verifier would refuse.
func NewKeySet(issuer string, recs []KeyRecord) (*KeySet, error) {
	ks := &KeySet{Object: "seal_key_set", Issuer: issuer, Keys: make([]SealKey, 0, len(recs))}
	var updated time.Time
	for _, r := range recs {
		ks.Keys = append(ks.Keys, NewSealKey(r))
		for _, t := range []*time.Time{&r.ActivatedAt, r.RetiredAt, r.RevokedAt} {
			if t != nil && t.After(updated) {
				updated = *t
			}
		}
	}
	ks.UpdatedAt = FormatTime(updated)
	if err := ks.index(); err != nil {
		return nil, err
	}
	return ks, nil
}

// ParseKeySet reads keys.json strictly. Any key that is unsafe (small-order or non-canonical
// Ed25519, attack case A13) or inconsistent (thumbprint, jwk, duplicate id, unknown alg) refuses
// the whole set with seal_key_rejected: a verifier must never pick and choose.
func ParseKeySet(raw []byte) (*KeySet, error) {
	tree, err := intent.ParseStrictNonNegative(raw)
	if err != nil {
		return nil, malformedFrom(err)
	}
	top, verr := asObj(tree, "")
	if verr != nil {
		return nil, errMalformed("a key set must be a JSON object")
	}
	if verr := top.closed([]string{"object", "issuer", "updated_at", "keys"}); verr != nil {
		return nil, verr
	}
	d := &decoder{}
	ks := &KeySet{Object: d.s(top, "object"), Issuer: d.s(top, "issuer"), UpdatedAt: d.s(top, "updated_at")}
	if d.err != nil {
		return nil, d.err
	}
	if ks.Object != "seal_key_set" {
		return nil, errSchemaField("object", `must be "seal_key_set"`)
	}
	if _, err := ParseTime(ks.UpdatedAt); err != nil {
		return nil, errSchemaField("updated_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ")
	}
	list, ok := top.m["keys"].([]any)
	if !ok {
		return nil, errSchemaField("keys", "must be an array")
	}
	for i, v := range list {
		o, verr := asObj(v, fmt.Sprintf("keys[%d]", i))
		if verr != nil {
			return nil, verr
		}
		if verr := o.closed([]string{"object", "id", "alg", "public_key", "thumbprint", "status", "activated_at", "retired_at", "revoked_at", "jwk"}); verr != nil {
			return nil, verr
		}
		k := SealKey{Object: d.s(o, "object"), ID: d.s(o, "id"), Alg: d.s(o, "alg"), PublicKey: d.s(o, "public_key"),
			Thumbprint: d.s(o, "thumbprint"), Status: d.s(o, "status"), ActivatedAt: d.s(o, "activated_at"),
			RetiredAt: d.ns(o, "retired_at"), RevokedAt: d.ns(o, "revoked_at")}
		if j, ok := d.obj(o, "jwk", []string{"kty", "crv", "x", "kid", "use"}); ok {
			k.JWK = JWK{Kty: d.s(j, "kty"), Crv: d.s(j, "crv"), X: d.s(j, "x"), Kid: d.s(j, "kid"), Use: d.s(j, "use")}
		} else if d.err == nil {
			d.err = errSchemaField(o.at("jwk"), "must be an object")
		}
		if d.err != nil {
			return nil, d.err
		}
		ks.Keys = append(ks.Keys, k)
	}
	if err := ks.index(); err != nil {
		return nil, err
	}
	return ks, nil
}

// index checks every key and builds the lookup table.
func (ks *KeySet) index() error {
	ks.byID = make(map[string]*keyEntry, len(ks.Keys))
	for i := range ks.Keys {
		k := &ks.Keys[i]
		if k.Object != "seal_key" {
			return errSchemaField(fmt.Sprintf("keys[%d].object", i), `must be "seal_key"`)
		}
		if !isID("sk", k.ID) {
			return errSchemaField(fmt.Sprintf("keys[%d].id", i), "must be an sk_ id")
		}
		if _, dup := ks.byID[k.ID]; dup {
			return errKeyRejected(k.ID, "listed twice")
		}
		if k.Alg != AlgEd25519 {
			return errKeyRejected(k.ID, "unknown alg "+k.Alg)
		}
		raw, err := b64.DecodeString(k.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return errKeyRejected(k.ID, "public_key is not 32 bytes of base64url")
		}
		pub, err := keys.ParsePublicKey(raw)
		if err != nil {
			return errKeyRejected(k.ID, err.Error())
		}
		if k.Thumbprint != Thumbprint(pub) {
			return errKeyRejected(k.ID, "thumbprint does not match public_key")
		}
		if k.JWK.Kty != "OKP" || k.JWK.Crv != "Ed25519" || k.JWK.X != k.PublicKey || k.JWK.Kid != k.ID || k.JWK.Use != "sig" {
			return errKeyRejected(k.ID, "jwk does not match the key")
		}
		e := &keyEntry{key: k, pub: pub}
		if e.activated, err = ParseTime(k.ActivatedAt); err != nil {
			return errSchemaField(fmt.Sprintf("keys[%d].activated_at", i), "must be YYYY-MM-DDTHH:MM:SS.mmmZ")
		}
		if k.RetiredAt != nil {
			t, err := ParseTime(*k.RetiredAt)
			if err != nil {
				return errSchemaField(fmt.Sprintf("keys[%d].retired_at", i), "must be YYYY-MM-DDTHH:MM:SS.mmmZ or null")
			}
			e.retired = &t
		}
		if k.RevokedAt != nil {
			t, err := ParseTime(*k.RevokedAt)
			if err != nil {
				return errSchemaField(fmt.Sprintf("keys[%d].revoked_at", i), "must be YYYY-MM-DDTHH:MM:SS.mmmZ or null")
			}
			e.revoked = &t
		}
		switch {
		case k.Status == KeyActive && e.retired == nil && e.revoked == nil:
		case k.Status == KeyRetired && e.retired != nil && e.revoked == nil:
		case k.Status == KeyRevoked && e.revoked != nil:
		default:
			return errKeyRejected(k.ID, "status "+k.Status+" does not match retired_at and revoked_at")
		}
		ks.byID[k.ID] = e
	}
	sort.SliceStable(ks.Keys, func(i, j int) bool { return ks.Keys[i].ActivatedAt < ks.Keys[j].ActivatedAt })
	// Sorting moved the elements; point the index at their new places.
	for i := range ks.Keys {
		ks.byID[ks.Keys[i].ID].key = &ks.Keys[i]
	}
	return nil
}

// Lookup returns the key with this id.
func (ks *KeySet) Lookup(id string) (*SealKey, ed25519.PublicKey, bool) {
	if ks == nil {
		return nil, nil, false
	}
	e, ok := ks.byID[id]
	if !ok {
		return nil, nil, false
	}
	return e.key, e.pub, true
}

// validAt applies the validity rule: activated_at ≤ t < retired_at (open-ended when null), and
// never for a revoked key, because a thief can backdate issued_at.
func (ks *KeySet) validAt(id string, t time.Time) Code {
	e := ks.byID[id]
	if e.revoked != nil {
		return CodeSealKeyRevoked
	}
	if t.Before(e.activated) || (e.retired != nil && !t.Before(*e.retired)) {
		return CodeSealKeyNotValidAt
	}
	return ""
}
