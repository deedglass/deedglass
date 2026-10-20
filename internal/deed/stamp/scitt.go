package stamp

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/big"
	"time"

	"github.com/veraison/go-cose"

	"github.com/deedglass/deedglass/internal/deed"
)

// The transparency-service half (TY-03, ADR-112), following the IETF SCITT architecture
// (RFC 9943) and COSE inclusion proofs (RFC 9942):
//
//   - The Signed Statement is a tagged COSE_Sign1 whose payload is JCS(head), sealed by the issuer's
//     Ed25519 seal key: protected header 1 alg −8 (EdDSA, ADR-008), 3 content type
//     application/vnd.deedglass.chain-head.v1+json, 4 kid (the seal key id), 15 CWT claims
//     {1 iss: the issuer URL, 2 sub: the org, 6 iat}.
//   - The service's inclusion proof is a COSE_Sign1 with a detached payload: protected header 395
//     (verifiable data structure) = 1 (RFC9162_SHA256), unprotected header 396 (verifiable data
//     structure proofs) = {−1: [bstr .cbor [tree_size, leaf_index, [hash…]]]}. Its signature covers
//     the Merkle tree root the inclusion path leads to.
//   - The leaf this package checks is the RFC 9162 leaf hash of the Signed Statement's bytes:
//     SHA-256(0x00 ‖ statement). A service that defines its leaf another way (over a countersigned
//     statement, say) gives inclusion proofs this verifier reports as not matching; LIMITATIONS.md
//     says so.

// ContentTypeHead is the protected content type of a head Signed Statement.
const ContentTypeHead = "application/vnd.deedglass.chain-head.v1+json"

// COSE header labels RFC 9942 registers.
const (
	labelVDS    = 395 // verifiable data structure
	labelVDP    = 396 // verifiable data structure proofs
	vdsRFC9162  = 1   // RFC9162_SHA256
	proofsIncl  = -1  // inclusion proofs
	maxPathHash = 64  // a path longer than 64 hashes means a tree past 2^64 leaves
)

// JWK is a transparency service's public key: EC P-256 (ES256) or OKP Ed25519 (EdDSA). The members
// are closed: kty, crv, x, and y for EC.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y,omitempty"`
}

func (k JWK) validate() error {
	_, _, err := k.public()
	return err
}

// Validate reports whether k is a usable service key.
func (k JWK) Validate() error { return k.validate() }

// public returns the key and its COSE algorithm.
func (k JWK) public() (any, cose.Algorithm, error) {
	switch {
	case k.Kty == "OKP" && k.Crv == "Ed25519" && k.Y == "":
		x, err := b64.DecodeString(k.X)
		if err != nil || !reJWKCoord.MatchString(k.X) || len(x) != ed25519.PublicKeySize {
			return nil, 0, errors.New("an Ed25519 x of 32 bytes")
		}
		return ed25519.PublicKey(x), cose.AlgorithmEdDSA, nil
	case k.Kty == "EC" && k.Crv == "P-256":
		x, errX := b64.DecodeString(k.X)
		y, errY := b64.DecodeString(k.Y)
		if errX != nil || errY != nil || !reJWKCoord.MatchString(k.X) || !reJWKCoord.MatchString(k.Y) {
			return nil, 0, errors.New("P-256 x and y of 32 bytes")
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if _, err := pub.ECDH(); err != nil { // the point must be on the curve
			return nil, 0, errors.New("a P-256 point on the curve")
		}
		return pub, cose.AlgorithmES256, nil
	}
	return nil, 0, errors.New("kty OKP with crv Ed25519, or kty EC with crv P-256")
}

// JWKOf returns the JWK of an Ed25519 or P-256 public key.
func JWKOf(pub any) (JWK, error) {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return JWK{Kty: "OKP", Crv: "Ed25519", X: b64.EncodeToString(k)}, nil
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			break
		}
		var x, y [32]byte
		k.X.FillBytes(x[:])
		k.Y.FillBytes(y[:])
		return JWK{Kty: "EC", Crv: "P-256", X: b64.EncodeToString(x[:]), Y: b64.EncodeToString(y[:])}, nil
	}
	return JWK{}, errors.New("stamp: a service key must be Ed25519 or P-256")
}

// edSigner adapts a deed.Signer to go-cose (EdDSA over the Sig_structure).
type edSigner struct{ s deed.Signer }

func (edSigner) Algorithm() cose.Algorithm { return cose.AlgorithmEdDSA }
func (e edSigner) Sign(_ io.Reader, tbs []byte) ([]byte, error) {
	return e.s.Sign(tbs)
}

// SignStatement returns the tagged COSE_Sign1 Signed Statement for head, sealed by s.
func SignStatement(head Head, s deed.Signer, issuer string, now time.Time) ([]byte, error) {
	payload, err := head.Bytes()
	if err != nil {
		return nil, err
	}
	if issuer == "" {
		return nil, errors.New("stamp: a Signed Statement needs an issuer")
	}
	msg := cose.NewSign1Message()
	msg.Headers.Protected = cose.ProtectedHeader{
		cose.HeaderLabelAlgorithm:   cose.AlgorithmEdDSA,
		cose.HeaderLabelContentType: ContentTypeHead,
		cose.HeaderLabelKeyID:       []byte(s.KeyID()),
		cose.HeaderLabelCWTClaims: cose.CWTClaims{
			cose.CWTClaimIssuer:   issuer,
			cose.CWTClaimSubject:  head.Org,
			cose.CWTClaimIssuedAt: now.Unix(),
		},
	}
	msg.Payload = payload
	if err := msg.Sign(nil, nil, edSigner{s}); err != nil {
		return nil, fmt.Errorf("stamp: sign the statement: %w", err)
	}
	return msg.MarshalCBOR()
}

// edVerifier checks an EdDSA signature with an Ed25519 key.
type edVerifier struct{ pub ed25519.PublicKey }

func (edVerifier) Algorithm() cose.Algorithm { return cose.AlgorithmEdDSA }
func (v edVerifier) Verify(content, sig []byte) error {
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(v.pub, content, sig) {
		return errors.New("signature")
	}
	return nil
}

// StatementInfo is what a verified Signed Statement says.
type StatementInfo struct {
	KeyID    string
	Issuer   string
	IssuedAt time.Time
}

// VerifyStatement checks a head Signed Statement: its payload is exactly JCS(head), its header is
// the one SignStatement writes, and its EdDSA signature verifies with the issuer's seal key from
// ks (a key that is in the set and not revoked; ADR-109 keeps Ed25519 usable for statements made
// before 2035).
func VerifyStatement(stmt []byte, head Head, ks *deed.KeySet) (*StatementInfo, error) {
	want, err := head.Bytes()
	if err != nil {
		return nil, err
	}
	var msg cose.Sign1Message
	if err := msg.UnmarshalCBOR(stmt); err != nil {
		return nil, errors.New("the Signed Statement is not a tagged COSE_Sign1")
	}
	if !bytes.Equal(msg.Payload, want) {
		return nil, errors.New("the Signed Statement is about another head")
	}
	p := msg.Headers.Protected
	if alg, err := p.Algorithm(); err != nil || alg != cose.AlgorithmEdDSA {
		return nil, errors.New("the Signed Statement is not EdDSA")
	}
	if ct, _ := p[cose.HeaderLabelContentType].(string); ct != ContentTypeHead {
		return nil, errors.New("the Signed Statement's content type is not a chain head")
	}
	kid, _ := p[cose.HeaderLabelKeyID].([]byte)
	claims, _ := lookup(map[any]any(p), cose.HeaderLabelCWTClaims)
	cm, _ := claims.(map[any]any)
	iss, _ := lookupString(cm, cose.CWTClaimIssuer)
	sub, _ := lookupString(cm, cose.CWTClaimSubject)
	if len(kid) == 0 || iss == "" || sub != head.Org {
		return nil, errors.New("the Signed Statement's kid, issuer or subject is missing or wrong")
	}
	info := &StatementInfo{KeyID: string(kid), Issuer: iss}
	if iat, ok := lookup(cm, cose.CWTClaimIssuedAt); ok {
		if n, ok := asInt64(iat); ok {
			info.IssuedAt = time.Unix(n, 0).UTC()
		}
	}
	key, pub, ok := ks.Lookup(info.KeyID)
	switch {
	case !ok || pub == nil:
		return nil, fmt.Errorf("the Signed Statement is sealed by %s, which is not an Ed25519 key in the key set", info.KeyID)
	case key.RevokedAt != nil:
		return nil, fmt.Errorf("the Signed Statement is sealed by %s, which is revoked", info.KeyID)
	}
	if err := msg.Verify(nil, edVerifier{pub}); err != nil {
		return nil, errors.New("the Signed Statement's seal doesn't verify")
	}
	return info, nil
}

// InclusionInfo is what a verified inclusion proof says.
type InclusionInfo struct {
	TreeSize  uint64
	LeafIndex uint64
	Pinned    bool // the service key is one the reader passed (verify --scitt-key)
}

// VerifyInclusion checks the service's inclusion proof for stmt: protected 395 = 1, one or more
// RFC9162_SHA256 inclusion proofs under 396, and the signature (ES256 or EdDSA, by key) over the
// root each path leads to. Every proof present must verify.
func VerifyInclusion(incl, stmt []byte, key JWK) (*InclusionInfo, error) {
	pub, alg, err := key.public()
	if err != nil {
		return nil, fmt.Errorf("the service key is not usable (%v)", err)
	}
	var msg cose.Sign1Message
	if err := msg.UnmarshalCBOR(incl); err != nil {
		return nil, errors.New("the inclusion proof is not a tagged COSE_Sign1")
	}
	if msg.Payload != nil {
		return nil, errors.New("the inclusion proof must have a detached payload")
	}
	got, err := msg.Headers.Protected.Algorithm()
	if err != nil || got != alg {
		return nil, errors.New("the inclusion proof's algorithm doesn't match the service key")
	}
	vds, ok := lookup(map[any]any(msg.Headers.Protected), labelVDS)
	if n, isInt := asInt64(vds); !ok || !isInt || n != vdsRFC9162 {
		return nil, errors.New("the inclusion proof is not RFC9162_SHA256 (protected header 395 = 1)")
	}
	vdp, ok := lookup(map[any]any(msg.Headers.Unprotected), labelVDP)
	vm, isMap := vdp.(map[any]any)
	if !ok || !isMap {
		return nil, errors.New("the inclusion proof has no proofs (unprotected header 396)")
	}
	raw, ok := lookup(vm, proofsIncl)
	list, isList := raw.([]any)
	if !ok || !isList || len(list) == 0 {
		return nil, errors.New("the inclusion proof has no inclusion path (396, key -1)")
	}
	verifier, err := cose.NewVerifier(alg, pub)
	if err != nil {
		return nil, errors.New("the service key is not usable")
	}
	leaf := LeafHash(stmt)
	var info *InclusionInfo
	for _, item := range list {
		b, ok := item.([]byte)
		if !ok {
			return nil, errors.New("an inclusion path is not a byte string")
		}
		size, index, path, err := decodeInclusion(b)
		if err != nil {
			return nil, err
		}
		root, err := RootFromInclusion(leaf, index, size, path)
		if err != nil {
			return nil, err
		}
		msg.Payload = root
		if err := msg.Verify(nil, verifier); err != nil {
			return nil, errors.New("the service's signature over the tree root doesn't verify")
		}
		msg.Payload = nil
		if info == nil {
			info = &InclusionInfo{TreeSize: size, LeafIndex: index}
		}
	}
	return info, nil
}

// LeafHash is the RFC 9162 §2.1.1 leaf hash: SHA-256(0x00 ‖ data).
func LeafHash(data []byte) []byte {
	h := sha256.New()
	h.Write([]byte{0})
	h.Write(data)
	return h.Sum(nil)
}

// NodeHash is the RFC 9162 §2.1.1 interior hash: SHA-256(0x01 ‖ left ‖ right).
func NodeHash(l, r []byte) []byte {
	h := sha256.New()
	h.Write([]byte{1})
	h.Write(l)
	h.Write(r)
	return h.Sum(nil)
}

// RootFromInclusion runs RFC 9162 §2.1.3.2: the root an inclusion path leads to from leaf at
// index in a tree of size leaves. It fails when the path doesn't fit the tree.
func RootFromInclusion(leaf []byte, index, size uint64, path [][]byte) ([]byte, error) {
	if index >= size {
		return nil, errors.New("the inclusion path's leaf index is outside the tree")
	}
	if len(path) > maxPathHash {
		return nil, errors.New("the inclusion path is too long")
	}
	fn, sn := index, size-1
	r := leaf
	for _, p := range path {
		if len(p) != sha256.Size {
			return nil, errors.New("an inclusion path hash is not 32 bytes")
		}
		if sn == 0 {
			return nil, errors.New("the inclusion path is longer than the tree")
		}
		if fn&1 == 1 || fn == sn {
			r = NodeHash(p, r)
			for fn&1 == 0 && fn != 0 {
				fn >>= 1
				sn >>= 1
			}
		} else {
			r = NodeHash(r, p)
		}
		fn >>= 1
		sn >>= 1
	}
	if sn != 0 {
		return nil, errors.New("the inclusion path is shorter than the tree")
	}
	return r, nil
}

// lookup finds a CBOR map value by integer label, whatever integer type the decoder chose.
func lookup(m map[any]any, label int64) (any, bool) {
	for k, v := range m {
		if n, ok := asInt64(k); ok && n == label {
			return v, true
		}
	}
	return nil, false
}

func lookupString(m map[any]any, label int64) (string, bool) {
	v, ok := lookup(m, label)
	s, isStr := v.(string)
	return s, ok && isStr
}

func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case uint64:
		if n <= 1<<63-1 {
			return int64(n), true
		}
	case cose.Algorithm:
		return int64(n), true
	}
	return 0, false
}
