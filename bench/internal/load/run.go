package load

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Target is what a scenario measures.
type Target interface {
	// Do sends request i and reports what the server said. Errors count; they are never dropped.
	Do(ctx context.Context, i int) (Outcome, error)
}

// TargetFunc adapts a function to Target.
type TargetFunc func(ctx context.Context, i int) (Outcome, error)

// Do calls f.
func (f TargetFunc) Do(ctx context.Context, i int) (Outcome, error) { return f(ctx, i) }

// Outcome is one answer.
type Outcome struct {
	Status string        // "sealed", "denied", "pending_approval", "org_stopped", …
	Server time.Duration // server-side time when the target can measure it; 0 otherwise
}

// Plan is one scenario's load.
type Plan struct {
	Name        string
	Rate        float64       // arrivals per second (open loop)
	Warmup      time.Duration // default 10 s
	Duration    time.Duration // default 60 s
	MaxInFlight int           // default 256
	Runs        int           // default 3
	Timeout     time.Duration // per request; default 30 s (a timeout is an error, and its full wait counts)
}

// RunResult is one run's numbers. Durations are client-observed, from the intended send time.
type RunResult struct {
	Count, Errors                       int
	AchievedRate                        float64
	P50, P90, P95, P99, P999, Max, Mean time.Duration
	Statuses                            map[string]int
	Hist                                *Hist `json:"-"`
}

func (p *Plan) defaults() {
	if p.Warmup == 0 {
		p.Warmup = 10 * time.Second
	}
	if p.Warmup < 0 {
		p.Warmup = 0
	}
	if p.Duration <= 0 {
		p.Duration = 60 * time.Second
	}
	if p.MaxInFlight <= 0 {
		p.MaxInFlight = 256
	}
	if p.Runs <= 0 {
		p.Runs = 3
	}
	if p.Timeout <= 0 {
		p.Timeout = 30 * time.Second
	}
}

// Run executes p's runs against t one after another.
func Run(ctx context.Context, p Plan, t Target) ([]RunResult, error) {
	p.defaults()
	if p.Rate <= 0 {
		return nil, errors.New("load: rate must be positive")
	}
	var out []RunResult
	next := 0
	for r := 0; r < p.Runs; r++ {
		res, n, err := runOnce(ctx, p, t, next)
		if err != nil {
			return out, err
		}
		next = n
		out = append(out, res)
	}
	return out, nil
}

// runOnce sends requests at their intended times start + i/rate. The scheduler never waits for an
// answer; when MaxInFlight requests are outstanding it waits for a slot, and that wait is part of
// the next request's latency because latency is measured from the intended time (no coordinated
// omission). Requests intended during the warm-up are sent but not recorded.
func runOnce(ctx context.Context, p Plan, t Target, first int) (RunResult, int, error) {
	total := int((p.Warmup + p.Duration).Seconds() * p.Rate)
	warm := int(p.Warmup.Seconds() * p.Rate)
	interval := time.Duration(float64(time.Second) / p.Rate)
	sem := make(chan struct{}, p.MaxInFlight)
	hist := NewHist()
	statuses := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	count, errs := 0, 0
	var measureStart, lastDone time.Time
	start := time.Now()
	for k := 0; k < total; k++ {
		intended := start.Add(time.Duration(k) * interval)
		if d := time.Until(intended); d > 0 {
			select {
			case <-ctx.Done():
				wg.Wait()
				return RunResult{}, first, ctx.Err()
			case <-time.After(d):
			}
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return RunResult{}, first, ctx.Err()
		}
		if k == warm {
			measureStart = intended
		}
		wg.Add(1)
		go func(k int, intended time.Time) {
			defer wg.Done()
			defer func() { <-sem }()
			rctx, cancel := context.WithTimeout(ctx, p.Timeout)
			o, err := t.Do(rctx, first+k)
			cancel()
			done := time.Now()
			if k < warm {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			hist.Record(done.Sub(intended))
			count++
			if done.After(lastDone) {
				lastDone = done
			}
			if err != nil {
				errs++
				statuses["error"]++
				return
			}
			statuses[o.Status]++
		}(k, intended)
	}
	wg.Wait()
	res := RunResult{Count: count, Errors: errs, Statuses: statuses, Hist: hist,
		P50: hist.Quantile(0.50), P90: hist.Quantile(0.90), P95: hist.Quantile(0.95), P99: hist.Quantile(0.99),
		P999: hist.Quantile(0.999), Max: hist.Max(), Mean: hist.Mean()}
	if span := lastDone.Sub(measureStart); span > 0 && count > 0 {
		res.AchievedRate = float64(count) / span.Seconds()
	}
	return res, first + total, nil
}

// Median returns the median of xs (the middle run of three).
func Median(xs []time.Duration) time.Duration {
	if len(xs) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}
