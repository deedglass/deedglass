package gate

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/internal/witness"
)

// Metrics are the Gate's Prometheus families (S13 §3.8), hand-written like S03's: bounded labels
// only (mode, status, kind, result, sweeper), never ids.
type Metrics struct {
	mu       sync.Mutex
	actions  map[[2]string]*atomic.Uint64 // {mode, status}
	jobs     map[[2]string]*atomic.Uint64 // {kind, result}
	sweeps   map[[2]string]*atomic.Uint64 // {sweeper, result}
	verdicts map[[2]string]*atomic.Uint64 // {level, verdict}: L2 verdicts sealed (S21; S27 reads mismatch rates)
	overhead histogram
	inflight atomic.Int64 // jobs this instance holds now (deedglass_worker_queue_depth)
	// DL-025: every evaluation stopped by the 50 ms time limit (it fails closed), by mode.
	timeLimitEnforce atomic.Uint64
	timeLimitShadow  atomic.Uint64
}

// overheadBuckets are deedglass_gate_overhead_seconds' buckets: 1, 2, 5, 10, 25, 50, 100 ms.
var overheadBuckets = []float64{0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1}

type histogram struct {
	counts [8]atomic.Uint64 // the last is +Inf
	sumNS  atomic.Int64
	n      atomic.Uint64
}

func (h *histogram) observe(d time.Duration) {
	s := d.Seconds()
	i := sort.SearchFloat64s(overheadBuckets, s)
	h.counts[i].Add(1)
	h.sumNS.Add(d.Nanoseconds())
	h.n.Add(1)
}

// NewMetrics returns empty metrics.
func NewMetrics() *Metrics {
	return &Metrics{actions: map[[2]string]*atomic.Uint64{}, jobs: map[[2]string]*atomic.Uint64{}, sweeps: map[[2]string]*atomic.Uint64{},
		verdicts: map[[2]string]*atomic.Uint64{}}
}

func (m *Metrics) inc(set map[[2]string]*atomic.Uint64, a, b string) { m.add(set, a, b, 1) }

func (m *Metrics) add(set map[[2]string]*atomic.Uint64, a, b string, n uint64) {
	if m == nil {
		return
	}
	k := [2]string{a, b}
	m.mu.Lock()
	c, ok := set[k]
	if !ok {
		c = new(atomic.Uint64)
		set[k] = c
	}
	m.mu.Unlock()
	c.Add(n)
}

func (m *Metrics) action(mode Mode, status string) {
	if m != nil {
		m.inc(m.actions, string(mode), status)
	}
}

func (m *Metrics) job(kind, result string) {
	if m != nil {
		m.inc(m.jobs, kind, result)
	}
}

func (m *Metrics) sweep(sweeper, result string) {
	if m != nil {
		m.inc(m.sweeps, sweeper, result)
	}
}

// l2 counts one verdict sealed after an L2 wait.
func (m *Metrics) l2(d witness.Decision) {
	if m != nil {
		m.inc(m.verdicts, string(d.Level), string(d.Verdict))
	}
}

func (m *Metrics) observeOverhead(d time.Duration) {
	if m != nil {
		m.overhead.observe(d)
	}
}

// TimeLimitTrips is the number of evaluations the 50 ms time limit stopped (enforced, shadow).
func (m *Metrics) TimeLimitTrips() (enforce, shadow uint64) {
	return m.timeLimitEnforce.Load(), m.timeLimitShadow.Load()
}

// ActionsTotal is deedglass_gate_actions_total for one mode and status (tests).
func (m *Metrics) ActionsTotal(mode Mode, status string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.actions[[2]string{string(mode), status}]; ok {
		return c.Load()
	}
	return 0
}

// Collect writes the exposition (an httpx.Collector).
func (m *Metrics) Collect(w io.Writer) {
	var b strings.Builder
	family := func(name, help, typ string, set map[[2]string]*atomic.Uint64, l1, l2 string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
		m.mu.Lock()
		keys := make([][2]string, 0, len(set))
		for k := range set {
			keys = append(keys, k)
		}
		m.mu.Unlock()
		sort.Slice(keys, func(i, j int) bool { return keys[i][0]+"\x00"+keys[i][1] < keys[j][0]+"\x00"+keys[j][1] })
		for _, k := range keys {
			m.mu.Lock()
			v := set[k].Load()
			m.mu.Unlock()
			fmt.Fprintf(&b, "%s{%s=%q,%s=%q} %d\n", name, l1, k[0], l2, k[1], v)
		}
	}
	family("deedglass_gate_actions_total", "Actions the Gate answered, by mode and status.", "counter", m.actions, "mode", "status")
	b.WriteString("# HELP deedglass_gate_overhead_seconds Time the Gate adds to an action, excluding the tool's own run time.\n# TYPE deedglass_gate_overhead_seconds histogram\n")
	var cum uint64
	for i, le := range overheadBuckets {
		cum += m.overhead.counts[i].Load()
		fmt.Fprintf(&b, "deedglass_gate_overhead_seconds_bucket{le=%q} %d\n", strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", le), "0"), "."), cum)
	}
	cum += m.overhead.counts[len(overheadBuckets)].Load()
	fmt.Fprintf(&b, "deedglass_gate_overhead_seconds_bucket{le=\"+Inf\"} %d\n", cum)
	fmt.Fprintf(&b, "deedglass_gate_overhead_seconds_sum %g\ndeedglass_gate_overhead_seconds_count %d\n",
		float64(m.overhead.sumNS.Load())/1e9, m.overhead.n.Load())
	family("deedglass_worker_jobs_total", "Witness jobs the worker finished, by kind and result.", "counter", m.jobs, "kind", "result")
	fmt.Fprintf(&b, "# HELP deedglass_worker_queue_depth Witness jobs this instance holds now.\n# TYPE deedglass_worker_queue_depth gauge\ndeedglass_worker_queue_depth %d\n", m.inflight.Load())
	family("deedglass_gate_sweeps_total", "Actions the sweepers handled, by sweeper and result.", "counter", m.sweeps, "sweeper", "result")
	family("deedglass_witness_verdicts_total", "Verdicts sealed after an L2 wait, by claimed level and verdict.", "counter", m.verdicts, "level", "verdict")
	fmt.Fprintf(&b, "# HELP deedglass_policy_eval_time_limit_total Evaluations stopped by the 50 ms time limit, which denies with policy_unavailable (DL-025).\n# TYPE deedglass_policy_eval_time_limit_total counter\n")
	fmt.Fprintf(&b, "deedglass_policy_eval_time_limit_total{mode=\"enforce\"} %d\ndeedglass_policy_eval_time_limit_total{mode=\"shadow\"} %d\n",
		m.timeLimitEnforce.Load(), m.timeLimitShadow.Load())
	_, _ = io.WriteString(w, b.String())
}
