package httpx

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/internal/buildinfo"
)

// Metrics is a small Prometheus registry (text exposition format 0.0.4) with no dependency. Labels
// are bounded: route is the pattern or "unmatched", never a raw path.
type Metrics struct {
	mu       sync.Mutex
	requests map[reqKey]*atomic.Uint64
	latency  map[string]*histogram
	inFlight atomic.Int64
}

type reqKey struct {
	route, method string
	status        int
}

// Buckets for deedglass_http_request_duration_seconds.
var durationBuckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type histogram struct {
	counts []atomic.Uint64 // one per bucket, not cumulative
	inf    atomic.Uint64
	sumNS  atomic.Int64
	count  atomic.Uint64
}

// NewMetrics returns an empty registry.
func NewMetrics() *Metrics {
	return &Metrics{requests: map[reqKey]*atomic.Uint64{}, latency: map[string]*histogram{}}
}

// methods outside this set are counted as OTHER, so a client can't grow the label set.
func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return "OTHER"
}

func (m *Metrics) observe(route, method string, status int, d time.Duration) {
	k := reqKey{route, methodLabel(method), status}
	m.mu.Lock()
	c, ok := m.requests[k]
	if !ok {
		c = new(atomic.Uint64)
		m.requests[k] = c
	}
	h, ok := m.latency[route]
	if !ok {
		h = &histogram{counts: make([]atomic.Uint64, len(durationBuckets))}
		m.latency[route] = h
	}
	m.mu.Unlock()
	c.Add(1)
	sec := d.Seconds()
	i := sort.SearchFloat64s(durationBuckets, sec)
	if i < len(durationBuckets) {
		h.counts[i].Add(1)
	} else {
		h.inf.Add(1)
	}
	h.sumNS.Add(d.Nanoseconds())
	h.count.Add(1)
}

// WriteTo writes the exposition.
func (m *Metrics) WriteTo(w io.Writer) (int64, error) {
	var b strings.Builder
	bi := buildinfo.Get()
	b.WriteString("# HELP deedglass_build_info The version and commit of this build.\n# TYPE deedglass_build_info gauge\n")
	fmt.Fprintf(&b, "deedglass_build_info{version=%s,commit=%s} 1\n", q(bi.Version), q(bi.Commit))

	b.WriteString("# HELP deedglass_http_in_flight Requests being served now.\n# TYPE deedglass_http_in_flight gauge\n")
	fmt.Fprintf(&b, "deedglass_http_in_flight %d\n", m.inFlight.Load())

	m.mu.Lock()
	keys := make([]reqKey, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	routes := make([]string, 0, len(m.latency))
	for r := range m.latency {
		routes = append(routes, r)
	}
	m.mu.Unlock()
	sort.Slice(keys, func(i, j int) bool {
		a, c := keys[i], keys[j]
		if a.route != c.route {
			return a.route < c.route
		}
		if a.method != c.method {
			return a.method < c.method
		}
		return a.status < c.status
	})
	sort.Strings(routes)

	b.WriteString("# HELP deedglass_http_requests_total HTTP requests by route pattern, method and status.\n# TYPE deedglass_http_requests_total counter\n")
	for _, k := range keys {
		m.mu.Lock()
		v := m.requests[k].Load()
		m.mu.Unlock()
		fmt.Fprintf(&b, "deedglass_http_requests_total{route=%s,method=%s,status=\"%d\"} %d\n", q(k.route), q(k.method), k.status, v)
	}

	b.WriteString("# HELP deedglass_http_request_duration_seconds Time to serve a request, by route pattern.\n# TYPE deedglass_http_request_duration_seconds histogram\n")
	for _, r := range routes {
		m.mu.Lock()
		h := m.latency[r]
		m.mu.Unlock()
		var cum uint64
		for i, le := range durationBuckets {
			cum += h.counts[i].Load()
			fmt.Fprintf(&b, "deedglass_http_request_duration_seconds_bucket{route=%s,le=%s} %d\n", q(r), q(fmtFloat(le)), cum)
		}
		cum += h.inf.Load()
		fmt.Fprintf(&b, "deedglass_http_request_duration_seconds_bucket{route=%s,le=\"+Inf\"} %d\n", q(r), cum)
		fmt.Fprintf(&b, "deedglass_http_request_duration_seconds_sum{route=%s} %s\n", q(r), fmtFloat(float64(h.sumNS.Load())/1e9))
		fmt.Fprintf(&b, "deedglass_http_request_duration_seconds_count{route=%s} %d\n", q(r), h.count.Load())
	}
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

// Handler serves GET /metrics (on the separate DEEDGLASS_METRICS_LISTEN listener only).
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = m.WriteTo(w)
	})
}

// q quotes a label value per the exposition format (backslash, quote and newline escaped).
func q(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
	return `"` + s + `"`
}

func fmtFloat(f float64) string {
	if math.IsInf(f, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
