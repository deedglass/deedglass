package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// The S03 §3.13 budget. Run: make bench-http. Raw output goes to bench/results/.

func benchServer(b *testing.B, health HealthSource) *Server {
	b.Helper()
	// The real logger (JSON, info, redacting) writing to io.Discard: the access log is part of the cost.
	s := New(Options{Logger: NewLogger(io.Discard, true, slog.LevelInfo)})
	s.Mount(s.SystemRoutes(SystemDeps{Health: health, Spec: []byte("openapi: 3.1.1\n")})...)
	return s
}

// reportPercentiles adds p50 and p99 (µs) from per-op samples.
func reportPercentiles(b *testing.B, d []time.Duration) {
	if len(d) == 0 {
		return
	}
	slices.Sort(d)
	b.ReportMetric(float64(d[len(d)/2].Nanoseconds())/1e3, "p50-µs")
	b.ReportMetric(float64(d[len(d)*99/100].Nanoseconds())/1e3, "p99-µs")
}

type nopWriter struct{ h http.Header }

func (w *nopWriter) Header() http.Header         { return w.h }
func (w *nopWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *nopWriter) WriteHeader(int)             {}

// BenchmarkPipelineVersion: GET /v1/version in process, through every global and per-route stage.
func BenchmarkPipelineVersion(b *testing.B) {
	s := benchServer(b, nil)
	req := httptest.NewRequest("GET", "/v1/version", nil)
	w := &nopWriter{h: http.Header{}}
	samples := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		clear(w.h)
		t0 := time.Now()
		s.ServeHTTP(w, req)
		samples = append(samples, time.Since(t0))
	}
	b.StopTimer()
	reportPercentiles(b, samples)
}

// BenchmarkPipelineNotFound: the 404 path with did-you-mean (probes every method).
func BenchmarkPipelineNotFound(b *testing.B) {
	s := benchServer(b, nil)
	req := httptest.NewRequest("GET", "/v1/helth", nil)
	w := &nopWriter{h: http.Header{}}
	b.ReportAllocs()
	for b.Loop() {
		clear(w.h)
		s.ServeHTTP(w, req)
	}
}

// BenchmarkPipelineLoad: 50 goroutines × 2,000 requests to /v1/version over a loopback TCP
// server (httptest.NewServer). Reported once; b.N only repeats the whole run.
func BenchmarkPipelineLoad(b *testing.B) {
	s := benchServer(b, nil)
	ts := httptest.NewServer(s)
	defer ts.Close()
	tr := &http.Transport{MaxIdleConnsPerHost: 64, MaxConnsPerHost: 64}
	client := &http.Client{Transport: tr}
	defer tr.CloseIdleConnections()
	const workers, per = 50, 2000
	for b.Loop() {
		var (
			mu      sync.Mutex
			all     = make([]time.Duration, 0, workers*per)
			errs    int
			wg      sync.WaitGroup
			started = time.Now()
		)
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				local := make([]time.Duration, 0, per)
				bad := 0
				for range per {
					t0 := time.Now()
					res, err := client.Get(ts.URL + "/v1/version")
					if err != nil {
						bad++
						continue
					}
					_, _ = io.Copy(io.Discard, res.Body)
					res.Body.Close()
					if res.StatusCode != 200 {
						bad++
					}
					local = append(local, time.Since(t0))
				}
				mu.Lock()
				all = append(all, local...)
				errs += bad
				mu.Unlock()
			}()
		}
		wg.Wait()
		elapsed := time.Since(started)
		reportPercentiles(b, all)
		b.ReportMetric(float64(errs), "errors")
		b.ReportMetric(float64(len(all))/elapsed.Seconds(), "req/s")
	}
}

// BenchmarkHealthCached: /v1/health answered from the 1 s cache.
func BenchmarkHealthCached(b *testing.B) {
	s := benchServer(b, &fakeHealth{state: MigrationState{Applied: 9, Expected: 9}})
	req := httptest.NewRequest("GET", "/v1/health", nil)
	w := &nopWriter{h: http.Header{}}
	s.ServeHTTP(w, req) // fill the cache
	samples := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	for b.Loop() {
		clear(w.h)
		t0 := time.Now()
		s.ServeHTTP(w, req)
		samples = append(samples, time.Since(t0))
	}
	b.StopTimer()
	reportPercentiles(b, samples)
}
