package decoder

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Metrics are the decoder's Prometheus families, exposed on DEEDGLASS_METRICS_LISTEN. Labels are
// connector ids (never names a person typed into a query) and fixed words.
type Metrics struct {
	mu       sync.Mutex
	frames   map[[2]string]uint64 // (connector, class) → frames stored
	polls    map[string]uint64
	peekSum  map[string]float64
	peekN    map[string]uint64
	peekBkt  map[string][]uint64
	lag      map[string]int64
	retained map[string]int64
	status   map[string]string
	halts    map[[2]string]uint64 // (connector, reason)
}

var peekBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

// NewMetrics returns empty metrics.
func NewMetrics() *Metrics {
	return &Metrics{frames: map[[2]string]uint64{}, polls: map[string]uint64{}, peekSum: map[string]float64{},
		peekN: map[string]uint64{}, peekBkt: map[string][]uint64{}, lag: map[string]int64{}, retained: map[string]int64{},
		status: map[string]string{}, halts: map[[2]string]uint64{}}
}

func (m *Metrics) frame(con, class string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.frames[[2]string{con, class}]++
	m.mu.Unlock()
}

func (m *Metrics) peek(con string, d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.polls[con]++
	s := d.Seconds()
	m.peekSum[con] += s
	m.peekN[con]++
	b := m.peekBkt[con]
	if b == nil {
		b = make([]uint64, len(peekBuckets))
		m.peekBkt[con] = b
	}
	for i, le := range peekBuckets {
		if s <= le {
			b[i]++
		}
	}
}

func (m *Metrics) slot(con string, lag, retained int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.lag[con], m.retained[con] = lag, retained
	m.mu.Unlock()
}

func (m *Metrics) setStatus(con, status, reason string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.status[con] = status
	if status == StatusHalted {
		m.halts[[2]string{con, reason}]++
	}
	m.mu.Unlock()
}

func (m *Metrics) forget(con string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.lag, con)
	delete(m.retained, con)
	delete(m.status, con)
	m.mu.Unlock()
}

// Collect writes the families (httpx.Collector).
func (m *Metrics) Collect(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	keys2 := func(set map[[2]string]uint64) [][2]string {
		out := make([][2]string, 0, len(set))
		for k := range set {
			out = append(out, k)
		}
		sort.Slice(out, func(i, j int) bool { return out[i][0]+"\x00"+out[i][1] < out[j][0]+"\x00"+out[j][1] })
		return out
	}
	keys1 := func(n int, each func(func(string))) []string {
		out := make([]string, 0, n)
		each(func(k string) { out = append(out, k) })
		sort.Strings(out)
		return out
	}
	b.WriteString("# HELP deedglass_decoder_frames_total Frames the decoder stored, by connector and class.\n# TYPE deedglass_decoder_frames_total counter\n")
	for _, k := range keys2(m.frames) {
		fmt.Fprintf(&b, "deedglass_decoder_frames_total{connector=%q,class=%q} %d\n", k[0], k[1], m.frames[k])
	}
	b.WriteString("# HELP deedglass_decoder_halts_total Times a decoder runner halted, by connector and reason.\n# TYPE deedglass_decoder_halts_total counter\n")
	for _, k := range keys2(m.halts) {
		fmt.Fprintf(&b, "deedglass_decoder_halts_total{connector=%q,reason=%q} %d\n", k[0], k[1], m.halts[k])
	}
	cons := keys1(len(m.peekN), func(f func(string)) {
		for k := range m.peekN {
			f(k)
		}
	})
	b.WriteString("# HELP deedglass_decoder_peek_seconds Time one peek took, by connector.\n# TYPE deedglass_decoder_peek_seconds histogram\n")
	for _, c := range cons {
		for i, le := range peekBuckets {
			fmt.Fprintf(&b, "deedglass_decoder_peek_seconds_bucket{connector=%q,le=%q} %d\n", c, fmt.Sprint(le), m.peekBkt[c][i])
		}
		fmt.Fprintf(&b, "deedglass_decoder_peek_seconds_bucket{connector=%q,le=\"+Inf\"} %d\n", c, m.peekN[c])
		fmt.Fprintf(&b, "deedglass_decoder_peek_seconds_sum{connector=%q} %g\n", c, m.peekSum[c])
		fmt.Fprintf(&b, "deedglass_decoder_peek_seconds_count{connector=%q} %d\n", c, m.peekN[c])
	}
	gauge := func(name, help string, set map[string]int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
		for _, c := range keys1(len(set), func(f func(string)) {
			for k := range set {
				f(k)
			}
		}) {
			fmt.Fprintf(&b, "%s{connector=%q} %d\n", name, c, set[c])
		}
	}
	gauge("deedglass_decoder_lag_bytes", "WAL between the server's position and the slot's confirmed position, in bytes.", m.lag)
	gauge("deedglass_decoder_retained_bytes", "WAL the slot keeps on the watched database (from restart_lsn), in bytes.", m.retained)
	b.WriteString("# HELP deedglass_decoder_running Whether a connector's decoder is running (1) or not (0).\n# TYPE deedglass_decoder_running gauge\n")
	for _, c := range keys1(len(m.status), func(f func(string)) {
		for k := range m.status {
			f(k)
		}
	}) {
		v := 0
		if m.status[c] == StatusRunning {
			v = 1
		}
		fmt.Fprintf(&b, "deedglass_decoder_running{connector=%q} %d\n", c, v)
	}
	_, _ = io.WriteString(w, b.String())
}
