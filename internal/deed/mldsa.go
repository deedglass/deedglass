package deed

import (
	"crypto/mldsa"
	"fmt"
)

const mldsaPublicKeySize = mldsa.MLDSA65PublicKeySize

// MLDSASigner holds an ML-DSA-65 seal key in memory (ADR-109). It never prints its seed: String and
// GoString show the key id only. It seals beside an Ed25519 key (the Sealer's extra signer), and
// signs COSE exports of v2 deeds (alg −49).
type MLDSASigner struct {
	id            string
	sk            *mldsa.PrivateKey
	pub           []byte
	deterministic bool
}

// NewMLDSASigner builds an ML-DSA-65 signer from a 32-byte FIPS 204 seed (ξ). id must be an sk_ id.
func NewMLDSASigner(id string, seed []byte) (*MLDSASigner, error) {
	if !isID(pSealKey, id) {
		return nil, fmt.Errorf("deed: seal key id %q is not an sk_ id", id)
	}
	if len(seed) != mldsa.PrivateKeySize {
		return nil, fmt.Errorf("deed: an ML-DSA-65 seed must be %d bytes", mldsa.PrivateKeySize)
	}
	sk, err := mldsa.NewPrivateKey(mldsa.MLDSA65(), seed)
	if err != nil {
		return nil, fmt.Errorf("deed: %w", err)
	}
	return &MLDSASigner{id: id, sk: sk, pub: sk.PublicKey().Bytes()}, nil
}

// Deterministic returns a copy that signs with FIPS 204's deterministic variant, so test vectors
// are reproducible. Production signers sign hedged (the default); both verify the same way.
func (s *MLDSASigner) Deterministic() *MLDSASigner {
	c := *s
	c.deterministic = true
	return &c
}

func (s *MLDSASigner) KeyID() string          { return s.id }
func (s *MLDSASigner) Alg() string            { return AlgMLDSA65 }
func (s *MLDSASigner) PublicKeyBytes() []byte { return s.pub }
func (s *MLDSASigner) String() string         { return "seal key " + s.id }
func (s *MLDSASigner) GoString() string       { return "deed.MLDSASigner{" + s.id + "}" }

// Sign is pure ML-DSA-65 over msg with the empty context string.
func (s *MLDSASigner) Sign(msg []byte) ([]byte, error) {
	if s.deterministic {
		return s.sk.SignDeterministic(msg, nil)
	}
	return s.sk.Sign(nil, msg, nil)
}
