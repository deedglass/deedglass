package ids

import (
	"crypto/rand"
	"io"
	"sync"
	"time"
)

// ULID layout (github.com/ulid/spec): a 48-bit millisecond timestamp, then 80 random bits, written
// as 26 Crockford base32 characters. Deedglass writes them in lowercase.
const (
	ulidLen  = 26
	alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	maxTime  = 1<<48 - 1
)

// dec maps a lowercase alphabet byte to its value; 0xff marks every other byte.
var dec = func() (d [256]byte) {
	for i := range d {
		d[i] = 0xff
	}
	for i := 0; i < len(alphabet); i++ {
		d[alphabet[i]] = byte(i)
	}
	return d
}()

// generator hands out strictly increasing ULIDs within one process:
//   - in the same millisecond, the 80-bit random part is incremented (big-endian carry);
//   - if that overflows, it waits for the next millisecond;
//   - if the clock moves back, it keeps the last timestamp.
type generator struct {
	mu     sync.Mutex
	lastMs uint64
	rnd    [10]byte
	now    func() time.Time
	rand   io.Reader
	sleep  func(time.Duration)
}

var defaultGen = &generator{now: time.Now, rand: rand.Reader, sleep: time.Sleep}

func (g *generator) next(prefix string) string {
	g.mu.Lock()
	ms := g.millis()
	switch {
	case ms > g.lastMs:
		g.fill()
	case increment(&g.rnd):
	default:
		for ms <= g.lastMs {
			g.sleep(time.Millisecond)
			ms = g.millis()
		}
		g.fill()
	}
	if ms > g.lastMs {
		g.lastMs = ms
	}
	var buf [8 + 1 + ulidLen]byte
	n := copy(buf[:], prefix)
	buf[n] = '_'
	encode(buf[n+1:n+1+ulidLen], g.lastMs, &g.rnd)
	s := string(buf[:n+1+ulidLen])
	g.mu.Unlock()
	return s
}

func (g *generator) millis() uint64 {
	ms := g.now().UnixMilli()
	if ms < 0 {
		ms = 0
	}
	if ms > maxTime {
		panic("ids: clock is past the ULID range (year 10889)")
	}
	return uint64(ms)
}

// fill reads fresh randomness. A read failure panics: a predictable id is worse than a crash.
func (g *generator) fill() {
	if _, err := io.ReadFull(g.rand, g.rnd[:]); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
}

// increment adds one to the 80-bit big-endian value and reports false on overflow.
func increment(b *[10]byte) bool {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return true
		}
	}
	return false
}

// encode writes the 26-character form of (ms, rnd) into dst.
func encode(dst []byte, ms uint64, rnd *[10]byte) {
	// The 128-bit value is hi:lo, with the timestamp in the top 48 bits.
	hi := ms<<16 | uint64(rnd[0])<<8 | uint64(rnd[1])
	var lo uint64
	for _, b := range rnd[2:] {
		lo = lo<<8 | uint64(b)
	}
	for i := 0; i < ulidLen; i++ {
		s := uint(125 - 5*i)
		var v uint64
		switch {
		case s >= 64:
			v = hi >> (s - 64)
		case s == 0:
			v = lo
		default:
			v = lo>>s | hi<<(64-s)
		}
		dst[i] = alphabet[v&31]
	}
}

// decode reverses encode for a string that already passed check.
func decode(u string) (ms uint64, hi, lo uint64) {
	for i := 0; i < ulidLen; i++ {
		v := uint64(dec[u[i]])
		hi = hi<<5 | lo>>59
		lo = lo<<5 | v
	}
	return hi >> 16, hi, lo
}
