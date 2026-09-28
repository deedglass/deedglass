package keys

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"regexp"
	"strings"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKID(t *testing.T) {
	// RFC 8032 §7.1 TEST 1 public key; expected value computed independently
	// with `xxd -r -p | shasum -a 256 | cut -c1-32`.
	pub := mustHex(t, "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a")
	kid, err := KID(pub)
	if err != nil {
		t.Fatal(err)
	}
	if kid != "21fe31dfa154a261626bf854046fd227" {
		t.Fatalf("kid = %s", kid)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(kid) {
		t.Fatalf("kid format: %s", kid)
	}
	for _, n := range []int{0, 31, 33, 64} {
		if _, err := KID(make([]byte, n)); !errors.Is(err, ErrKeySize) {
			t.Fatalf("KID(%d bytes): %v", n, err)
		}
	}
}

// encode returns the RFC 8032 encoding of (x, y) where y may be >= p (to
// build non-canonical encodings) and sign is written as given.
func encode(y *big.Int, sign uint) []byte {
	be := y.FillBytes(make([]byte, 32))
	le := make([]byte, 32)
	for i := range 32 {
		le[i] = be[31-i]
	}
	le[31] |= byte(sign << 7)
	return le
}

// torsionPoints derives the 8 points of order dividing 8 from first
// principles, independently of the package's decode/add code:
//   - x = 0: y = 1 (identity) and y = -1 (order 2);
//   - y = 0: x = ±sqrt(-1) (order 4);
//   - order 8: doubling to y = 0 needs x^2 = -y^2, which with the curve
//     equation gives d y^4 + 2 y^2 - 1 = 0, so y^2 = (-1 ± sqrt(1+d)) / d.
func torsionPoints(t *testing.T) map[string][]byte {
	t.Helper()
	P := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	D := new(big.Int).Mul(big.NewInt(-121665), new(big.Int).ModInverse(big.NewInt(121666), P))
	D.Mod(D, P)
	if D.Cmp(d) != 0 {
		t.Fatalf("curve constant d mismatch")
	}
	pts := map[string][]byte{
		"identity (order 1)":    encode(big.NewInt(1), 0),
		"y=-1 (order 2)":        encode(new(big.Int).Sub(P, big.NewInt(1)), 0),
		"y=0, x even (order 4)": encode(big.NewInt(0), 0),
		"y=0, x odd (order 4)":  encode(big.NewInt(0), 1),
	}
	s := new(big.Int).ModSqrt(new(big.Int).Mod(new(big.Int).Add(D, big.NewInt(1)), P), P)
	if s == nil {
		t.Fatal("1+d is not a square")
	}
	dInv := new(big.Int).ModInverse(D, P)
	found := 0
	for _, sgn := range []int64{1, -1} {
		y2 := new(big.Int).Add(big.NewInt(-1), new(big.Int).Mul(big.NewInt(sgn), s))
		y2.Mul(y2, dInv)
		y2.Mod(y2, P)
		y := new(big.Int).ModSqrt(y2, P)
		if y == nil {
			continue // this root of the quadratic has no y
		}
		for _, yy := range []*big.Int{y, new(big.Int).Sub(P, y)} {
			for _, sign := range []uint{0, 1} {
				pts[fmt.Sprintf("order 8 #%d", found)] = encode(yy, sign)
				found++
			}
		}
	}
	if found != 4 || len(pts) != 8 {
		t.Fatalf("derived %d order-8 points, %d total; want 4 and 8", found, len(pts))
	}
	return pts
}

// acceptsForgery reports whether crypto/ed25519 accepts the signature
// (R = identity, S = 0) for at least one of 256 messages. For a small-order
// key A this happens whenever [k]A = identity (probability >= 1/8 per
// message); for a prime-order key it does not happen.
func acceptsForgery(pub []byte) bool {
	sig := make([]byte, 64)
	sig[0] = 1 // R = encoding of the identity; S = 0
	for i := range 256 {
		if ed25519.Verify(ed25519.PublicKey(pub), []byte(fmt.Sprintf("pay mallory %d", i)), sig) {
			return true
		}
	}
	return false
}

func TestSmallOrderKeysRejected(t *testing.T) {
	pts := torsionPoints(t)
	for name, enc := range pts {
		if _, err := ParsePublicKey(enc); !errors.Is(err, ErrSmallOrder) {
			t.Errorf("%s %x: got %v, want ErrSmallOrder", name, enc, err)
		}
		// Independent evidence from the standard library that the key is
		// dangerous: a fixed signature verifies unrelated messages.
		if !acceptsForgery(enc) {
			t.Errorf("%s %x: stdlib did not accept the (identity, 0) signature; not small order?", name, enc)
		}
	}
	// The spike S4 probe exactly: identity key, two unrelated messages.
	id := pts["identity (order 1)"]
	sig := make([]byte, 64)
	sig[0] = 1
	if !ed25519.Verify(id, []byte("pay mallory 1000000"), sig) || !ed25519.Verify(id, []byte("any other message"), sig) {
		t.Fatal("spike S4 probe no longer reproduces; re-check the stdlib behaviour")
	}
}

// TestSmallOrderMatchesPublishedList cross-checks the derived points against
// the widely used blocklist of small-order Ed25519 encodings (y values of the
// 8-torsion subgroup, as in libsodium's ge25519_has_small_order).
func TestSmallOrderMatchesPublishedList(t *testing.T) {
	published := []string{
		"0100000000000000000000000000000000000000000000000000000000000000", // y = 1
		"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // y = p-1
		"0000000000000000000000000000000000000000000000000000000000000000", // y = 0
		"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05", // order 8
		"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a", // order 8
	}
	ys := map[string]bool{}
	for _, enc := range torsionPoints(t) {
		c := append([]byte(nil), enc...)
		c[31] &= 0x7f
		ys[hex.EncodeToString(c)] = true
	}
	if len(ys) != len(published) {
		t.Fatalf("derived %d distinct y encodings, published list has %d", len(ys), len(published))
	}
	for _, h := range published {
		if !ys[h] {
			t.Errorf("published small-order y %s not derived", h)
		}
	}
}

func TestNonCanonicalRejected(t *testing.T) {
	P := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	cases := map[string][]byte{
		"y = p (alias of y = 0)":          encode(P, 0),
		"y = p, sign set":                 encode(P, 1),
		"y = p+1 (alias of identity)":     encode(new(big.Int).Add(P, big.NewInt(1)), 0),
		"y = p+1, sign set":               encode(new(big.Int).Add(P, big.NewInt(1)), 1),
		"y = 2^255-1":                     encode(new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1)), 0),
		"identity with sign bit (x = -0)": encode(big.NewInt(1), 1),
		"order-2 with sign bit (x = -0)":  encode(new(big.Int).Sub(P, big.NewInt(1)), 1),
	}
	for name, enc := range cases {
		if _, err := ParsePublicKey(enc); !errors.Is(err, ErrNonCanonical) {
			t.Errorf("%s: got %v, want ErrNonCanonical", name, err)
		}
	}
	// Aliases of small-order points are also forgeable in the stdlib, which
	// accepts non-canonical encodings.
	if !acceptsForgery(encode(new(big.Int).Add(P, big.NewInt(1)), 0)) {
		t.Error("stdlib rejected the y = p+1 alias; revisit the note in ParsePublicKey")
	}
}

func TestNotOnCurveRejected(t *testing.T) {
	n := 0
	for y := int64(2); y < 200 && n < 5; y++ {
		enc := encode(big.NewInt(y), 0)
		_, err := ParsePublicKey(enc)
		if errors.Is(err, ErrNotOnCurve) {
			n++
		}
	}
	if n == 0 {
		t.Fatal("no off-curve encodings found among small y values")
	}
	for _, sz := range []int{0, 31, 33} {
		if _, err := ParsePublicKey(make([]byte, sz)); !errors.Is(err, ErrKeySize) {
			t.Fatalf("%d bytes: %v", sz, err)
		}
	}
}

func TestGeneratedKeysAccepted(t *testing.T) {
	for range 16 {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParsePublicKey(pub)
		if err != nil {
			t.Fatalf("%x: %v", []byte(pub), err)
		}
		if !bytes.Equal(got, pub) {
			t.Fatal("ParsePublicKey changed the key")
		}
		if acceptsForgery(pub) {
			t.Fatalf("%x: prime-order key accepted the (identity, 0) signature", []byte(pub))
		}
	}
}

// rfc8032Vectors parses RFC 8032 §7.1 (Ed25519) test vectors from the RFC text.
func rfc8032Vectors(t *testing.T) (out []struct{ sk, pk, msg, sig []byte }) {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/rfc/rfc8032.txt")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	const head = "7.1.  Test Vectors for Ed25519"
	start := strings.Index(s, head)
	start = strings.Index(s[start+len(head):], head) + start + len(head) // skip the table of contents
	end := strings.Index(s[start:], "7.2.  Test Vectors for Ed25519ctx") + start
	hexRe := regexp.MustCompile(`^[0-9a-f]+$`)
	for _, block := range strings.Split(s[start:end], "-----TEST ")[1:] {
		f := map[string]*strings.Builder{}
		label := ""
		for _, line := range strings.Split(block, "\n")[1:] {
			l := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(l, "SECRET KEY:"):
				label = "sk"
			case strings.HasPrefix(l, "PUBLIC KEY:"):
				label = "pk"
			case strings.HasPrefix(l, "MESSAGE"):
				label = "msg"
			case strings.HasPrefix(l, "SIGNATURE:"):
				label = "sig"
			case strings.HasPrefix(l, "ALGORITHM:"):
				label = ""
			case label != "" && hexRe.MatchString(l):
				if f[label] == nil {
					f[label] = &strings.Builder{}
				}
				f[label].WriteString(l)
			}
		}
		get := func(k string) []byte {
			if f[k] == nil {
				return []byte{}
			}
			return mustHex(t, f[k].String())
		}
		out = append(out, struct{ sk, pk, msg, sig []byte }{get("sk"), get("pk"), get("msg"), get("sig")})
	}
	if len(out) != 5 {
		t.Fatalf("parsed %d RFC 8032 §7.1 vectors, want 5", len(out))
	}
	return out
}

func TestRFC8032Vectors(t *testing.T) {
	for i, v := range rfc8032Vectors(t) {
		priv := ed25519.NewKeyFromSeed(v.sk)
		if !bytes.Equal(priv.Public().(ed25519.PublicKey), v.pk) {
			t.Fatalf("vector %d: derived public key differs", i)
		}
		if _, err := ParsePublicKey(v.pk); err != nil {
			t.Fatalf("vector %d: registration refused a valid key: %v", i, err)
		}
		if !bytes.Equal(ed25519.Sign(priv, v.msg), v.sig) || !ed25519.Verify(v.pk, v.msg, v.sig) {
			t.Fatalf("vector %d: signature mismatch", i)
		}
		if _, err := KID(v.pk); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWycheproofPublicKeys(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/wycheproof/ed25519_test.json")
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		TestGroups []struct {
			PublicKey struct {
				PK string `json:"pk"`
			} `json:"publicKey"`
		} `json:"testGroups"`
	}
	if err := json.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, g := range wf.TestGroups {
		if seen[g.PublicKey.PK] {
			continue
		}
		seen[g.PublicKey.PK] = true
		if _, err := ParsePublicKey(mustHex(t, g.PublicKey.PK)); err != nil {
			t.Errorf("Wycheproof key %s refused: %v", g.PublicKey.PK, err)
		}
	}
	if len(seen) < 10 {
		t.Fatalf("only %d distinct Wycheproof keys", len(seen))
	}
}
