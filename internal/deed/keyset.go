package deed

import (
	"crypto/ed25519"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/deedglass/deedglass/internal/intent"
)

// Seal key statuses.
const (
	KeyActive  = "active"
	KeyRetired = "retired" // retired_at is set; deeds issued before it stay valid
	KeyRevoked = "revoked" // every deed it sealed fails verification
)

// SealKey is one public seal key as keys.json publishes it (contract §7).
type SealKey struct {
	Object      string  `json:"object"` // "seal_key"
	ID          string  `json:"id"`
	Alg         string  `json:"alg"`        // "Ed25519" (alg.go lists the others)
	PublicKey   string  `json:"public_key"` // base64url of the raw key (Ed25519: 32 bytes)
	Thumbprint  string  `json:"thumbprint"` // sha256:<hex of the raw key>
	Status      string  `json:"status"`
	ActivatedAt string  `json:"activated_at"`
	RetiredAt   *string `json:"retired_at"`
	RevokedAt   *string `json:"revoked_at"`
	JWK         JWK     `json:"jwk"`
}

// JWK is the key as a JSON Web Key. An Ed25519 key is an RFC 8037 OKP key {kty, crv, x, kid, use};
// it omits alg because RFC 9864 deprecates "EdDSA" for "Ed25519" and many JOSE libraries don't know
// the new name yet. An ML-DSA key is an RFC 9964 AKP key {kty, alg, pub, kid, use}, where alg is
// required.
type JWK struct {
	Kty string `json:"kty"`           // "OKP" | "AKP"
	Crv string `json:"crv,omitempty"` // OKP: "Ed25519"
	X   string `json:"x,omitempty"`   // OKP: = public_key
	Alg string `json:"alg,omitempty"` // AKP: = the key's alg
	Pub string `json:"pub,omitempty"` // AKP: = public_key
	Kid string `json:"kid"`           // = id
	Use string `json:"use"`           // "sig"
}

// jwkKeys is the closed member list of a key's jwk, by algorithm: OKP for Ed25519, AKP for the
// RFC 9964 algorithms, and for an algorithm this verifier doesn't implement any of the known
// members with kty, kid and use present.
func jwkKeys(alg string) (required, optional []string) {
	switch {
	case alg == AlgEd25519:
		return []string{"kty", "crv", "x", "kid", "use"}, nil
	case AlgSupported(alg):
		return []string{"kty", "alg", "pub", "kid", "use"}, nil
	}
	return []string{"kty", "kid", "use"}, []string{"crv", "x", "alg", "pub"}
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
	alg       *algSpec // nil: an algorithm this verifier doesn't implement (kept, never used)
	pub       any      // what alg.verify takes (Ed25519: ed25519.PublicKey)
	activated time.Time
	retired   *time.Time
	revoked   *time.Time
}

// KeyRecord is a seal key as the server stores it; NewKeySet turns records into a KeySet.
type KeyRecord struct {
	ID          string
	Alg         string            // "" means Ed25519
	PublicKey   ed25519.PublicKey // the raw key bytes (Ed25519: 32 bytes)
	Status      string
	ActivatedAt time.Time
	RetiredAt   *time.Time
	RevokedAt   *time.Time
}

// NewSealKey renders one record in the published shape.
func NewSealKey(r KeyRecord) SealKey {
	x := b64.EncodeToString(r.PublicKey)
	alg := r.Alg
	if alg == "" {
		alg = AlgEd25519
	}
	k := SealKey{
		Object: "seal_key", ID: r.ID, Alg: alg, PublicKey: x, Thumbprint: Thumbprint(r.PublicKey),
		Status: r.Status, ActivatedAt: FormatTime(r.ActivatedAt),
		JWK: JWK{Kty: "OKP", Crv: "Ed25519", X: x, Kid: r.ID, Use: "sig"},
	}
	if alg != AlgEd25519 {
		k.JWK = JWK{Kty: "AKP", Alg: alg, Pub: x, Kid: r.ID, Use: "sig"}
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
		req, opt := jwkKeys(k.Alg)
		jkeys := req
		if jm, ok := o.m["jwk"].(map[string]any); ok {
			for _, f := range opt {
				if _, present := jm[f]; present {
					jkeys = append(jkeys, f)
				}
			}
		}
		if j, ok := d.obj(o, "jwk", jkeys); ok {
			k.JWK = JWK{Kty: d.s(j, "kty"), Kid: d.s(j, "kid"), Use: d.s(j, "use")}
			for _, f := range jkeys {
				switch f {
				case "crv":
					k.JWK.Crv = d.s(j, f)
				case "x":
					k.JWK.X = d.s(j, f)
				case "alg":
					k.JWK.Alg = d.s(j, f)
				case "pub":
					k.JWK.Pub = d.s(j, f)
				}
			}
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
		if !reAlg.MatchString(k.Alg) {
			return errKeyRejected(k.ID, "unknown alg "+k.Alg)
		}
		e := &keyEntry{key: k, alg: algs[k.Alg]}
		raw, err := b64.DecodeString(k.PublicKey)
		if e.alg != nil {
			if err != nil || len(raw) != e.alg.keySize {
				return errKeyRejected(k.ID, fmt.Sprintf("public_key is not %d bytes of base64url", e.alg.keySize))
			}
			if e.pub, err = e.alg.parseKey(raw); err != nil {
				return errKeyRejected(k.ID, err.Error())
			}
			if !e.alg.jwkOK(k.JWK, k) {
				return errKeyRejected(k.ID, "jwk does not match the key")
			}
		} else {
			// An algorithm this verifier doesn't implement: kept so deeds of the set's other keys
			// verify, checked for what can be checked without the algorithm, and never used.
			if err != nil || len(raw) == 0 || len(raw) > maxUnknownKeyBytes {
				return errKeyRejected(k.ID, "public_key is not base64url")
			}
			if k.JWK.Kid != k.ID || k.JWK.Use != "sig" || k.JWK.Kty == "" {
				return errKeyRejected(k.ID, "jwk does not match the key")
			}
		}
		if k.Thumbprint != Digest(raw) {
			return errKeyRejected(k.ID, "thumbprint does not match public_key")
		}
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

// maxUnknownKeyBytes bounds the public key of an algorithm this verifier doesn't implement.
const maxUnknownKeyBytes = 8 << 10

// Lookup returns the key with this id. The public key is set for Ed25519 keys only.
func (ks *KeySet) Lookup(id string) (*SealKey, ed25519.PublicKey, bool) {
	if ks == nil {
		return nil, nil, false
	}
	e, ok := ks.byID[id]
	if !ok {
		return nil, nil, false
	}
	pub, _ := e.pub.(ed25519.PublicKey)
	return e.key, pub, true
}

// entry returns the indexed key with this id.
func (ks *KeySet) entry(id string) (*keyEntry, bool) {
	if ks == nil {
		return nil, false
	}
	e, ok := ks.byID[id]
	return e, ok
}

// activeAlgs lists the implemented algorithms of the keys that are not revoked and were active at t
// (activated_at ≤ t < retired_at): the key-set rule says a deed issued at t carries a seal of each
// (DEED_SPEC.md §11.4).
func (ks *KeySet) activeAlgs(t time.Time) []string {
	var out []string
	for i := range ks.Keys {
		e := ks.byID[ks.Keys[i].ID]
		if e.alg == nil || e.revoked != nil || t.Before(e.activated) || (e.retired != nil && !t.Before(*e.retired)) {
			continue // an algorithm this verifier doesn't implement can't be required of a deed it can't check
		}
		if !slices.Contains(out, e.key.Alg) {
			out = append(out, e.key.Alg)
		}
	}
	return out
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
