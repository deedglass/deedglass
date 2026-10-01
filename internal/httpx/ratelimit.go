package httpx

import (
	"hash/maphash"
	"math"
	"sync"
	"time"
)

// Limiter is an in-process token-bucket rate limiter keyed by string (a client IP, a hashed email,
// a key id). Each key may spend Burst tokens at once and earns them back at Burst per Window. It
// has no dependency and no goroutine: the map is sharded to keep lock contention low, and each
// shard drops buckets that have refilled completely (they are the same as no bucket) at most once
// per Window.
//
// Buckets live in one process. Behind several instances each instance counts on its own, so the
// effective budget is up to N times larger; callers that need a global cap also count in the
// database (S07 caps sign-in tokens per user there).
type Limiter struct {
	burst  float64
	rate   float64 // tokens per second
	window time.Duration
	now    func() time.Time
	seed   maphash.Seed
	shards [limiterShards]limiterShard
}

const limiterShards = 32

type limiterShard struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// NewLimiter allows burst events per window for each key. now may be nil (time.Now).
func NewLimiter(burst int, window time.Duration, now func() time.Time) *Limiter {
	if burst < 1 || window <= 0 {
		panic("httpx: a limiter needs burst ≥ 1 and a positive window")
	}
	if now == nil {
		now = time.Now
	}
	l := &Limiter{burst: float64(burst), rate: float64(burst) / window.Seconds(), window: window, now: now, seed: maphash.MakeSeed()}
	for i := range l.shards {
		l.shards[i].buckets = map[string]*bucket{}
	}
	return l
}

// Allow spends one token for key. When none is left it spends nothing and returns false with how
// long until one is back.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	return l.take(key, true)
}

// Check reports whether key has a token, without spending it (for budgets charged only on
// failure: check first, then Allow when the attempt fails).
func (l *Limiter) Check(key string) (ok bool, retryAfter time.Duration) {
	return l.take(key, false)
}

func (l *Limiter) take(key string, spend bool) (bool, time.Duration) {
	sh := &l.shards[maphash.String(l.seed, key)%limiterShards]
	now := l.now()
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if now.Sub(sh.lastSweep) >= l.window {
		l.sweep(sh, now)
	}
	b := sh.buckets[key]
	tokens := l.burst
	if b != nil {
		tokens = math.Min(l.burst, b.tokens+now.Sub(b.at).Seconds()*l.rate)
	}
	if tokens < 1 {
		wait := time.Duration((1 - tokens) / l.rate * float64(time.Second))
		return false, max(wait, time.Second)
	}
	if spend {
		if b == nil {
			b = &bucket{}
			sh.buckets[key] = b
		}
		b.tokens, b.at = tokens-1, now
	}
	return true, 0
}

// sweep drops buckets that have refilled completely. The caller holds sh.mu.
func (l *Limiter) sweep(sh *limiterShard, now time.Time) {
	for k, b := range sh.buckets {
		if b.tokens+now.Sub(b.at).Seconds()*l.rate >= l.burst {
			delete(sh.buckets, k)
		}
	}
	sh.lastSweep = now
}

// Len is the number of keys holding a bucket (tests and metrics).
func (l *Limiter) Len() int {
	n := 0
	for i := range l.shards {
		l.shards[i].mu.Lock()
		n += len(l.shards[i].buckets)
		l.shards[i].mu.Unlock()
	}
	return n
}

// RetryMinutes rounds a wait up to whole minutes, at least 1, for messages ("Try again in 14
// minutes.").
func RetryMinutes(d time.Duration) int {
	return max(1, int(math.Ceil(d.Minutes())))
}
