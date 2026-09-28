package keys

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
)

// Registration errors.
var (
	ErrKeySize      = errors.New("keys: Ed25519 public key must be 32 bytes")
	ErrNonCanonical = errors.New("keys: non-canonical Ed25519 point encoding")
	ErrNotOnCurve   = errors.New("keys: encoding is not a point on edwards25519")
	ErrSmallOrder   = errors.New("keys: small-order Ed25519 public key")
)

// KID returns the key id of a raw 32-byte Ed25519 public key: lowercase hex
// of the first 16 bytes of SHA-256(pub) (spec §5, §12).
func KID(pub []byte) (string, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", ErrKeySize
	}
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:16]), nil
}

// ParsePublicKey applies the registration rules (spec §12, plan §3.0 #7) and
// returns the key. It refuses keys that are not 32 bytes, non-canonical
// encodings (y >= p, or x = 0 with the sign bit set), encodings that are not
// on the curve, and points of small order (8·A = identity). Go's
// crypto/ed25519 accepts all of these; with the identity key, one signature
// verifies every message (spike S4).
func ParsePublicKey(pub []byte) (ed25519.PublicKey, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, ErrKeySize
	}
	x, y, err := decodePoint(pub)
	if err != nil {
		return nil, err
	}
	for i := 0; i < 3; i++ {
		x, y = add(x, y, x, y)
	}
	if x.Sign() == 0 && y.Cmp(one) == 0 {
		return nil, ErrSmallOrder
	}
	return ed25519.PublicKey(append([]byte(nil), pub...)), nil
}

// Curve arithmetic below uses math/big: public keys are public, so constant
// time is not needed, and it avoids a dependency outside the pinned set
// (spec §3.2). Curve: -x^2 + y^2 = 1 + d x^2 y^2 over GF(2^255-19).
var (
	one   = big.NewInt(1)
	p     = new(big.Int).Sub(new(big.Int).Lsh(one, 255), big.NewInt(19))
	d     = mustInt("37095705934669439343138083508754565189542113879843219016388785533085940283555")
	sqrtM = new(big.Int).Exp(big.NewInt(2), new(big.Int).Div(new(big.Int).Sub(p, one), big.NewInt(4)), p) // sqrt(-1)
)

func mustInt(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("keys: bad constant")
	}
	return n
}

// decodePoint follows RFC 8032 §5.1.3 and additionally refuses y >= p.
func decodePoint(b []byte) (x, y *big.Int, err error) {
	le := make([]byte, 32)
	for i := range 32 {
		le[31-i] = b[i] // little-endian to big-endian
	}
	sign := le[0] >> 7
	le[0] &= 0x7f
	y = new(big.Int).SetBytes(le)
	if y.Cmp(p) >= 0 {
		return nil, nil, ErrNonCanonical
	}
	y2 := new(big.Int).Mul(y, y)
	y2.Mod(y2, p)
	u := new(big.Int).Sub(y2, one) // y^2 - 1
	u.Mod(u, p)
	v := new(big.Int).Mul(d, y2) // d y^2 + 1
	v.Add(v, one)
	v.Mod(v, p)
	vInv := new(big.Int).ModInverse(v, p)
	if vInv == nil {
		return nil, nil, ErrNotOnCurve
	}
	x2 := new(big.Int).Mul(u, vInv)
	x2.Mod(x2, p)
	// p = 5 mod 8: candidate root x = x2^((p+3)/8).
	x = new(big.Int).Exp(x2, new(big.Int).Div(new(big.Int).Add(p, big.NewInt(3)), big.NewInt(8)), p)
	if !isSquareRoot(x, x2) {
		x.Mul(x, sqrtM)
		x.Mod(x, p)
		if !isSquareRoot(x, x2) {
			return nil, nil, ErrNotOnCurve
		}
	}
	if x.Sign() == 0 && sign == 1 {
		return nil, nil, ErrNonCanonical
	}
	if uint8(x.Bit(0)) != sign {
		x.Sub(p, x)
	}
	return x, y, nil
}

func isSquareRoot(x, sq *big.Int) bool {
	t := new(big.Int).Mul(x, x)
	t.Mod(t, p)
	return t.Cmp(sq) == 0
}

// add is the complete twisted Edwards addition law for a = -1.
func add(x1, y1, x2, y2 *big.Int) (*big.Int, *big.Int) {
	x1y2 := new(big.Int).Mul(x1, y2)
	y1x2 := new(big.Int).Mul(y1, x2)
	y1y2 := new(big.Int).Mul(y1, y2)
	x1x2 := new(big.Int).Mul(x1, x2)
	dxy := new(big.Int).Mul(d, x1x2)
	dxy.Mul(dxy, y1y2)
	dxy.Mod(dxy, p)

	xn := new(big.Int).Add(x1y2, y1x2)
	xd := new(big.Int).Add(one, dxy)
	yn := new(big.Int).Add(y1y2, x1x2)
	yd := new(big.Int).Sub(one, dxy)
	yd.Mod(yd, p)

	x3 := xn.Mul(xn, new(big.Int).ModInverse(xd.Mod(xd, p), p))
	x3.Mod(x3, p)
	y3 := yn.Mul(yn, new(big.Int).ModInverse(yd, p))
	y3.Mod(y3, p)
	return x3, y3
}
