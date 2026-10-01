package deed

import (
	"crypto/ed25519"
	"fmt"
	"time"
)

// BundleSeal is manifest.seal.json in an export bundle v1 (docs/BUNDLE_FORMAT.md): the exporter's
// seal over the exact bytes of manifest.json. It makes tail truncation detectable, because the
// sealed manifest names the chain head and the count.
type BundleSeal struct {
	Object       string `json:"object"`        // "deed_bundle_seal"
	ManifestHash string `json:"manifest_hash"` // sha256:<hex of SHA-256(manifest.json bytes)>
	Seal         string `json:"seal"`          // ed25519:<base64url>
	SealKeyID    string `json:"seal_key_id"`
}

// BundleSealInput is the byte string a manifest seal signs: "deedglass/bundle/v1\n" + manifest_hash.
func BundleSealInput(manifestHash string) []byte { return []byte(TagBundle + manifestHash) }

// SealManifest seals manifest.json's bytes with s.
func SealManifest(s Signer, manifest []byte) (*BundleSeal, error) {
	h := Digest(manifest)
	sig, err := s.Sign(BundleSealInput(h))
	if err != nil {
		return nil, fmt.Errorf("deed: sign the bundle manifest: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("deed: the signer returned a signature that is not 64 bytes")
	}
	return &BundleSeal{Object: "deed_bundle_seal", ManifestHash: h, Seal: SealPrefix + b64.EncodeToString(sig), SealKeyID: s.KeyID()}, nil
}

// ParseBundleSeal reads manifest.seal.json strictly (closed schema).
func ParseBundleSeal(raw []byte) (*BundleSeal, error) {
	tree, err := parseStrictTree(raw)
	if err != nil {
		return nil, err
	}
	o, verr := asObj(tree, "")
	if verr != nil {
		return nil, verr
	}
	if verr := o.closed([]string{"object", "manifest_hash", "seal", "seal_key_id"}); verr != nil {
		return nil, verr
	}
	d := &decoder{}
	bs := &BundleSeal{Object: d.s(o, "object"), ManifestHash: d.s(o, "manifest_hash"), Seal: d.s(o, "seal"), SealKeyID: d.s(o, "seal_key_id")}
	if d.err != nil {
		return nil, d.err
	}
	switch {
	case bs.Object != "deed_bundle_seal":
		return nil, errSchemaField("object", `must be "deed_bundle_seal"`)
	case !reDigest.MatchString(bs.ManifestHash):
		return nil, errSchemaField("manifest_hash", "must be sha256:<64 hex>")
	case !reSeal.MatchString(bs.Seal):
		return nil, errSchemaField("seal", "must be ed25519:<86 base64url characters>")
	case !isID(pSealKey, bs.SealKeyID):
		return nil, errSchemaField("seal_key_id", "must be an sk_ id")
	}
	return bs, nil
}

// VerifyBundleSeal checks bs over the exact manifest bytes: the hash, the key (in ks, not revoked,
// active at createdAt) and the Ed25519 seal. Every failure is bundle_seal_invalid with a detail.
func VerifyBundleSeal(bs *BundleSeal, manifest []byte, ks *KeySet, createdAt time.Time) error {
	fail := func(detail string) error {
		return &VerifyError{Code: CodeBundleSealInvalid, Key: bs.SealKeyID, Detail: detail}
	}
	if Digest(manifest) != bs.ManifestHash {
		return fail("manifest_hash does not match manifest.json")
	}
	_, pub, ok := ks.Lookup(bs.SealKeyID)
	if !ok {
		return fail("seal key " + bs.SealKeyID + " is not in the key set")
	}
	if c := ks.validAt(bs.SealKeyID, createdAt); c != "" {
		return fail("seal key " + bs.SealKeyID + " was not active when the bundle was created")
	}
	sig, err := b64.DecodeString(bs.Seal[len(SealPrefix):])
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, BundleSealInput(bs.ManifestHash), sig) {
		return fail("the seal does not verify")
	}
	return nil
}
