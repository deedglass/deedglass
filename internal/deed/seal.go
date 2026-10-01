package deed

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"

	"github.com/deedglass/deedglass/internal/keys"
)

// SealPrefix starts every seal value.
const SealPrefix = "ed25519:"

var reSeal = regexp.MustCompile(`^ed25519:[A-Za-z0-9_-]{86}$`)

// b64 is base64url without padding, refusing non-zero trailing bits so each value has one spelling.
var b64 = base64.RawURLEncoding.Strict()

// Signer produces pure Ed25519 signatures (RFC 8032, no pre-hash). FileSigner is v1; a KMS or HSM
// signer can satisfy the same interface later.
type Signer interface {
	KeyID() string
	PublicKey() ed25519.PublicKey
	Sign(msg []byte) ([]byte, error)
}

// FileSigner holds a seal key in memory. It never prints its seed: String, GoString and the
// slog value show the key id only.
type FileSigner struct {
	id   string
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// NewFileSigner builds a signer from a 32-byte seed. The derived public key must pass
// keys.ParsePublicKey (a safe Ed25519 point), and id must be an sk_ id.
func NewFileSigner(id string, seed []byte) (*FileSigner, error) {
	if !isID(pSealKey, id) {
		return nil, fmt.Errorf("deed: seal key id %q is not an sk_ id", id)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("deed: seal key seed must be %d bytes", ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	if _, err := keys.ParsePublicKey(pub); err != nil {
		return nil, fmt.Errorf("deed: %w", err)
	}
	return &FileSigner{id: id, priv: priv, pub: pub}, nil
}

func (s *FileSigner) KeyID() string                   { return s.id }
func (s *FileSigner) PublicKey() ed25519.PublicKey    { return s.pub }
func (s *FileSigner) Sign(msg []byte) ([]byte, error) { return ed25519.Sign(s.priv, msg), nil }
func (s *FileSigner) String() string                  { return "seal key " + s.id }
func (s *FileSigner) GoString() string                { return "deed.FileSigner{" + s.id + "}" }

// Thumbprint returns "sha256:" + hex(SHA-256(raw 32-byte public key)).
func Thumbprint(pub ed25519.PublicKey) string { return Digest(pub) }

// SealInput is the exact byte string a seal signs: "deedglass/deed/v1\n" + body_hash (89 bytes).
func SealInput(bodyHash string) []byte { return []byte(TagDeed + bodyHash) }

// sealed is one sealed deed with the bytes the store keeps.
type sealed struct {
	env     *Envelope
	bodyJCS []byte // the exact bytes hashed into body_hash
	envJCS  []byte // JCS(envelope), stored and served verbatim
}

// SealBody seals a complete body: no chain logic and no clock. b.SealKeyID is set to the signer's
// key id when empty, and must equal it otherwise. Tests, vectors and the Sealer use it.
func SealBody(s Signer, b Body) (*Envelope, error) {
	r, err := sealBody(s, b)
	if err != nil {
		return nil, err
	}
	return r.env, nil
}

func sealBody(s Signer, b Body) (*sealed, error) {
	if b.SealKeyID == "" {
		b.SealKeyID = s.KeyID()
	}
	if b.SealKeyID != s.KeyID() {
		return nil, fmt.Errorf("%w: body names seal key %s but the signer is %s", ErrDraft, b.SealKeyID, s.KeyID())
	}
	if err := b.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDraft, err)
	}
	bodyJCS, err := StrictCanonical(&b)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDraft, err)
	}
	hash := Digest(bodyJCS)
	sig, err := s.Sign(SealInput(hash))
	if err != nil {
		return nil, fmt.Errorf("deed: sign: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, errors.New("deed: the signer returned a signature that is not 64 bytes")
	}
	env := &Envelope{Object: ObjectDeed, ID: b.DeedID, Body: b, BodyHash: hash, Seal: SealPrefix + b64.EncodeToString(sig), SealKeyID: s.KeyID()}
	envJCS := canonicalEnvelope(env, bodyJCS)
	if len(envJCS) > MaxEnvelopeBytes {
		return nil, fmt.Errorf("%w: the deed would be %d bytes; the limit is %d", ErrDraft, len(envJCS), MaxEnvelopeBytes)
	}
	return &sealed{env: env, bodyJCS: bodyJCS, envJCS: envJCS}, nil
}

// openSeal checks seal over body_hash with pub. crypto/ed25519 refuses s ≥ L and non-canonical
// encodings of R as RFC 8032 requires; small-order keys never get here (keys.ParsePublicKey).
func openSeal(pub ed25519.PublicKey, bodyHash, seal string) bool {
	if !reSeal.MatchString(seal) {
		return false
	}
	sig, err := b64.DecodeString(seal[len(SealPrefix):])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, SealInput(bodyHash), sig)
}
