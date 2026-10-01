package deed

import (
	"crypto/rand"
	"time"
)

// crockford is the lowercase ULID alphabet (contract §3).
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// NewDeedID returns a deed_ id whose ULID time is issued (the database clock the Sealer read
// under the chain lock) and whose 80 random bits come from crypto/rand. Deed ids need not be
// monotonic: seq orders a chain.
func NewDeedID(issued time.Time) string {
	var u [16]byte
	ms := uint64(issued.UnixMilli())
	for i := 5; i >= 0; i-- {
		u[i] = byte(ms)
		ms >>= 8
	}
	if _, err := rand.Read(u[6:]); err != nil {
		panic("deed: crypto/rand failed: " + err.Error()) // crypto/rand.Read never fails on supported platforms
	}
	out := make([]byte, 0, len(pDeed)+1+26)
	out = append(out, pDeed...)
	out = append(out, '_')
	// 128 bits as 26 base32 characters; the first holds the top 3 bits.
	var hi, lo uint64
	for i := 0; i < 8; i++ {
		hi = hi<<8 | uint64(u[i])
		lo = lo<<8 | uint64(u[8+i])
	}
	var enc [26]byte
	for i := 25; i >= 0; i-- {
		enc[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(append(out, enc[:]...))
}
