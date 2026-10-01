package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

func testKeySet(context.Context) (*deed.KeySet, error) { return deedtest.KeySet(), nil }

func TestKeysJSON(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	src := func(ctx context.Context) (*deed.KeySet, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("database down")
		}
		return testKeySet(ctx)
	}
	h := &harness{log: &logBuf{}}
	h.srv = New(Options{Logger: NewLogger(h.log, true, nil)})
	h.srv.Mount(h.srv.WellKnownRoutes(src, func() time.Time { return now })...)

	get := func(hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/.well-known/deedglass/keys.json", nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.srv.ServeHTTP(w, req)
		return w
	}
	r := get(nil)
	if r.Code != 200 {
		t.Fatalf("status %d: %s", r.Code, r.Body)
	}
	for k, want := range map[string]string{"Cache-Control": "public, max-age=300", "Access-Control-Allow-Origin": "*",
		"X-Content-Type-Options": "nosniff", "Content-Type": "application/json; charset=utf-8", "Cross-Origin-Resource-Policy": "cross-origin"} {
		if got := r.Header().Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	etag := r.Header()["ETag"][0]
	if !strings.HasPrefix(etag, `"sha256:`) || r.Header().Get("Deedglass-Request-Id") == "" {
		t.Fatalf("etag %q", etag)
	}
	ks, err := deed.ParseKeySet(r.Body.Bytes())
	if err != nil || len(ks.Keys) != 3 || ks.Issuer != deedtest.Issuer {
		t.Fatalf("body does not parse as a key set: %v\n%s", err, r.Body)
	}
	for _, inm := range []string{etag, "W/" + etag, `"x", ` + etag, "*"} {
		if r := get(map[string]string{"If-None-Match": inm}); r.Code != 304 || r.Body.Len() != 0 || r.Header()["ETag"][0] != etag {
			t.Errorf("If-None-Match %s: %d", inm, r.Code)
		}
	}
	if r := get(map[string]string{"If-None-Match": `"other"`}); r.Code != 200 {
		t.Errorf("stale etag: %d", r.Code)
	}
	if calls.Load() != 1 {
		t.Fatalf("%d fetches inside the TTL, want 1", calls.Load())
	}
	// After the TTL, a failing source is a 503, never a stale or partial list.
	now = now.Add(61 * time.Second)
	fail.Store(true)
	r = get(nil)
	if r.Code != http.StatusServiceUnavailable || !strings.Contains(r.Body.String(), "Seal keys are temporarily unavailable. Retry in a few seconds.") ||
		r.Header().Get("Retry-After") != "5" || strings.Contains(r.Body.String(), "database down") {
		t.Fatalf("outage: %d %s", r.Code, r.Body)
	}
	fail.Store(false)
	if r := get(nil); r.Code != 200 || calls.Load() != 3 {
		t.Fatalf("recovery: %d after %d calls", r.Code, calls.Load())
	}
	req := httptest.NewRequest("HEAD", "/.well-known/deedglass/keys.json", nil)
	w := httptest.NewRecorder()
	h.srv.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %d bytes", w.Code, w.Body.Len())
	}
}

func TestHealthSealKeyCheck(t *testing.T) {
	for status, want := range map[string]string{StatusOK: StatusOK, StatusDegraded: StatusDegraded, StatusDown: StatusDegraded} {
		s := New(Options{Logger: NewLogger(&logBuf{}, true, nil)})
		h := &health{src: &fakeHealth{state: MigrationState{Applied: 9, Expected: 9}}, now: time.Now, s: s,
			sealKey: func(context.Context) string { return status }}
		b := h.compute(context.Background())
		if b.Checks.SealKey.Status != status || b.Status != want {
			t.Errorf("seal key %s: overall %s, want %s", status, b.Status, want)
		}
	}
}

// S04 §3.11: keys.json from the cache, p99 ≤ 5 ms in process.
func TestKeysJSONCachedBudget(t *testing.T) {
	s := New(Options{Logger: NewLogger(&logBuf{}, true, nil)})
	s.Mount(s.WellKnownRoutes(testKeySet, nil)...)
	var d []time.Duration
	for i := 0; i < 2100; i++ {
		w := httptest.NewRecorder()
		t0 := time.Now()
		s.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/deedglass/keys.json", nil))
		if i >= 100 {
			d = append(d, time.Since(t0))
		}
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	slices.Sort(d)
	p50, p99 := d[len(d)/2], d[len(d)*99/100]
	t.Logf("keys.json from cache: p50 %v · p99 %v (2,000 requests)", p50, p99)
	if p99 > 5*time.Millisecond {
		t.Errorf("p99 %v, budget 5 ms", p99)
	}
}
