// Package deedtest holds the fixed test-vector keys, deterministic ids and times, an in-memory
// deed.ChainStore, a chain generator and tamper helpers, for tests, golden vectors and benchmarks.
//
// The keys here are published (testdata/deeds/keys/test-keys.json). They must never seal a real
// deed: deed.LoadKeyFile refuses them in prod and outside tests.
package deedtest

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

// T0 is the first deed's time in every vector.
var T0 = time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)

// Issuer is the issuer named in test key sets and COSE vectors.
const Issuer = "https://deedglass.example"

// crockford is the lowercase ULID alphabet (contract §3).
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// ID returns a deterministic, valid id for prefix: a ULID whose time is T0 plus n milliseconds and
// whose random part is derived from SHA-256(prefix/n). The same (prefix, n) always gives the same id.
func ID(prefix string, n int) string {
	ms := uint64(T0.UnixMilli()) + uint64(n)
	h := sha256.Sum256([]byte(fmt.Sprintf("deedglass/test-id/%s/%d", prefix, n)))
	var u [16]byte
	binary.BigEndian.PutUint16(u[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(u[2:6], uint32(ms))
	copy(u[6:], h[:10])
	// 128 bits as 26 base32 characters, the first holding 3 bits.
	out := make([]byte, 26)
	hi := binary.BigEndian.Uint64(u[0:8])
	lo := binary.BigEndian.Uint64(u[8:16])
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return prefix + "_" + string(out)
}

// Key is one test-vector key.
type Key struct {
	Name   string // a, b or r
	ID     string // sk_…
	Seed   []byte
	Signer *deed.FileSigner
}

// Seed returns test key name's seed: SHA-256("deedglass/test-vector-key/" + name).
func Seed(name string) []byte {
	s := sha256.Sum256([]byte(deed.TestKeySeedLabel + name))
	return s[:]
}

// TestKey returns test key a (active, then retired at seq 11), b (active from the rotation) or
// r (revoked).
func TestKey(name string) Key {
	n := map[string]int{"a": 1, "b": 2, "r": 3}[name]
	if n == 0 {
		panic("deedtest: test keys are a, b and r")
	}
	id := ID("sk", n)
	s, err := deed.NewFileSigner(id, Seed(name))
	if err != nil {
		panic(err)
	}
	return Key{Name: name, ID: id, Seed: Seed(name), Signer: s}
}

// The test key lifecycle. Key a seals seq 1–10; the rotation at T0+5s activates b and retires a
// at T0+10s, when seq 11 is sealed by b. Key r was revoked before the chain began.
var (
	AActivated = T0.Add(-time.Hour)
	BActivated = T0.Add(5 * time.Second)
	ARetired   = T0.Add(10 * time.Second)
	RActivated = T0.Add(-2 * time.Hour)
	RRevoked   = T0.Add(-time.Hour)
)

// KeyRecords returns the three keys as the server stores them.
func KeyRecords() []deed.KeyRecord {
	a, b, r := TestKey("a"), TestKey("b"), TestKey("r")
	ar, rr := ARetired, RRevoked
	return []deed.KeyRecord{
		{ID: r.ID, PublicKey: r.Signer.PublicKey(), Status: deed.KeyRevoked, ActivatedAt: RActivated, RevokedAt: &rr},
		{ID: a.ID, PublicKey: a.Signer.PublicKey(), Status: deed.KeyRetired, ActivatedAt: AActivated, RetiredAt: &ar},
		{ID: b.ID, PublicKey: b.Signer.PublicKey(), Status: deed.KeyActive, ActivatedAt: BActivated},
	}
}

// KeySet is the published test key set.
func KeySet() *deed.KeySet {
	ks, err := deed.NewKeySet(Issuer, KeyRecords())
	if err != nil {
		panic(err)
	}
	return ks
}

// Org returns test org n's id.
func Org(n int) string { return ID("org", n) }

// SmallOrderKey is an Ed25519 public key encoding keys.ParsePublicKey must refuse (attack case A13).
type SmallOrderKey struct {
	Name string
	Hex  string
}

// SmallOrderKeys are the 8 points of the 8-torsion subgroup (libsodium's blocklist) and
// non-canonical encodings of them. With any of them, a fixed signature verifies under Go's
// crypto/ed25519 for a large share of messages.
var SmallOrderKeys = []SmallOrderKey{
	{"identity (order 1)", "0100000000000000000000000000000000000000000000000000000000000000"},
	{"y=-1 (order 2)", "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"},
	{"y=0, x even (order 4)", "0000000000000000000000000000000000000000000000000000000000000000"},
	{"y=0, x odd (order 4)", "0000000000000000000000000000000000000000000000000000000000000080"},
	{"order 8, x even", "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05"},
	{"order 8, x odd", "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85"},
	{"order 8, x even (second)", "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a"},
	{"order 8, x odd (second)", "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa"},
	{"identity alias y=p+1 (non-canonical)", "eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"},
	{"identity with sign bit, x=-0 (non-canonical)", "0100000000000000000000000000000000000000000000000000000000000080"},
	{"y=-1 with sign bit, x=-0 (non-canonical)", "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
	{"y=0 alias y=p (non-canonical)", "edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"},
}

// PublicKey decodes k.
func (k SmallOrderKey) PublicKey() ed25519.PublicKey {
	b, err := hex.DecodeString(k.Hex)
	if err != nil {
		panic(err)
	}
	return b
}
