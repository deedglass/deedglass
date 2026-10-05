// Package load is Deedglass's open-loop load generator and its reporting (S35 §3.1): a fixed
// arrival rate, latency measured from each request's intended send time (so a stalled server shows
// in the tail instead of slowing the generator down), warm-up discarded, several runs, and a
// log-linear histogram whose percentiles are within 1% of the exact ones. Standard library only.
package load

import (
	"math"
	"math/bits"
	"time"
)

// The histogram covers 1 µs to 60 s. Each power-of-two range of microseconds is split into
// subBuckets linear buckets, so a bucket is at most 1/subBuckets of its value wide: a reported
// percentile (the bucket's midpoint) is within 1/(2·subBuckets) ≈ 0.4% of any value in it.
const (
	subBits    = 7
	subBuckets = 1 << subBits // 128
	minValue   = time.Microsecond
	maxValue   = 60 * time.Second
)

// Hist is a log-linear latency histogram. The zero value is not usable; use NewHist.
type Hist struct {
	counts   []uint64
	n        uint64
	sum      time.Duration
	min, max time.Duration
}

var numBuckets = bucketOf(maxValue) + 1

// NewHist returns an empty histogram.
func NewHist() *Hist { return &Hist{counts: make([]uint64, numBuckets), min: math.MaxInt64} }

// bucketOf maps d to its bucket. Values under 1 µs share bucket 0; values over 60 s share the last.
func bucketOf(d time.Duration) int {
	if d < minValue {
		d = minValue
	}
	if d > maxValue {
		d = maxValue
	}
	// Units of 1/subBuckets µs keep the first octave (1–2 µs) as fine as the others.
	v := uint64(d) * subBuckets / uint64(minValue)
	exp := bits.Len64(v) - 1 - subBits // octave above 1 µs
	if exp < 0 {
		return int(v)
	}
	sub := (v >> uint(exp)) - subBuckets // 0..subBuckets-1
	return subBuckets + exp*subBuckets + int(sub)
}

// bounds returns the bucket's [lo, hi) range.
func bounds(b int) (lo, hi time.Duration) {
	unit := float64(minValue) / subBuckets
	if b < subBuckets {
		return time.Duration(float64(b) * unit), time.Duration(float64(b+1) * unit)
	}
	exp := (b - subBuckets) / subBuckets
	sub := (b - subBuckets) % subBuckets
	base := float64(uint64(1)<<uint(exp)) * unit
	return time.Duration(base * float64(subBuckets+sub)), time.Duration(base * float64(subBuckets+sub+1))
}

// Record adds one value.
func (h *Hist) Record(d time.Duration) {
	h.counts[bucketOf(d)]++
	h.n++
	h.sum += d
	if d < h.min {
		h.min = d
	}
	if d > h.max {
		h.max = d
	}
}

// Merge adds o's values to h.
func (h *Hist) Merge(o *Hist) {
	for i, c := range o.counts {
		h.counts[i] += c
	}
	h.n += o.n
	h.sum += o.sum
	h.min = min(h.min, o.min)
	h.max = max(h.max, o.max)
}

// Count is the number of values recorded.
func (h *Hist) Count() uint64 { return h.n }

// Max is the largest value recorded, exactly.
func (h *Hist) Max() time.Duration {
	if h.n == 0 {
		return 0
	}
	return h.max
}

// Mean is the exact mean.
func (h *Hist) Mean() time.Duration {
	if h.n == 0 {
		return 0
	}
	return h.sum / time.Duration(h.n)
}

// Quantile returns the q-quantile (0 < q ≤ 1) by the nearest-rank rule: the bucket holding the
// ceil(q·n)-th smallest value, reported at its midpoint and clamped to the exact min and max.
func (h *Hist) Quantile(q float64) time.Duration {
	if h.n == 0 {
		return 0
	}
	rank := uint64(math.Ceil(q * float64(h.n)))
	rank = max(rank, 1)
	if rank >= h.n {
		return h.max // the largest value is kept exactly (also above the 60 s range)
	}
	var seen uint64
	for b, c := range h.counts {
		seen += c
		if seen >= rank {
			lo, hi := bounds(b)
			mid := lo + (hi-lo)/2
			return min(max(mid, h.min), h.max)
		}
	}
	return h.max
}
