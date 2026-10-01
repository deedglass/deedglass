package httpx

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	l := NewLimiter(20, time.Hour, clock)
	for i := 0; i < 20; i++ {
		if ok, _ := l.Allow("203.0.113.7"); !ok {
			t.Fatalf("request %d refused", i+1)
		}
	}
	ok, wait := l.Allow("203.0.113.7")
	if ok || wait != 3*time.Minute {
		t.Fatalf("21st: ok=%v wait=%v, want refused with 3m (one token every 3 minutes)", ok, wait)
	}
	if ok, _ := l.Allow("198.51.100.1"); !ok {
		t.Fatal("another key has its own bucket")
	}
	// Check never spends.
	if ok, _ := l.Check("192.0.2.1"); !ok || l.Len() != 2 {
		t.Fatalf("Check spent a token or made a bucket: len %d", l.Len())
	}
	now = now.Add(3 * time.Minute)
	if ok, _ := l.Allow("203.0.113.7"); !ok {
		t.Fatal("a token comes back after 3 minutes")
	}
	if ok, w := l.Allow("203.0.113.7"); ok || w != 3*time.Minute {
		t.Fatalf("then refused again: %v %v", ok, w)
	}
	// Hours later every bucket has refilled; touching a shard sweeps it.
	now = now.Add(2 * time.Hour)
	l.Check("203.0.113.7")
	l.Check("198.51.100.1")
	if l.Len() != 0 {
		t.Fatalf("sweep left %d buckets", l.Len())
	}
}

func TestLimiterWaitFloor(t *testing.T) {
	now := time.Now()
	l := NewLimiter(1000, time.Second, func() time.Time { return now })
	for i := 0; i < 1000; i++ {
		l.Allow("k")
	}
	if ok, w := l.Allow("k"); ok || w != time.Second {
		t.Fatalf("Retry-After is at least 1 s: %v %v", ok, w)
	}
}

func TestLimiterConcurrent(t *testing.T) {
	l := NewLimiter(50, time.Hour, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if ok, _ := l.Allow("same"); ok {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
				l.Allow(fmt.Sprint(g, i))
			}
		}()
	}
	wg.Wait()
	if allowed != 50 {
		t.Fatalf("allowed %d, want exactly 50", allowed)
	}
}

func TestLimiterPanicsOnBadConfig(t *testing.T) {
	mustPanic(t, "burst", func() { NewLimiter(0, time.Hour, nil) })
}

func TestRetryMinutes(t *testing.T) {
	for d, want := range map[time.Duration]int{0: 1, time.Second: 1, 60 * time.Second: 1, 61 * time.Second: 2, 14 * time.Minute: 14} {
		if got := RetryMinutes(d); got != want {
			t.Errorf("RetryMinutes(%v) = %d, want %d", d, got, want)
		}
	}
}
