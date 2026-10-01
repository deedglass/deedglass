package gate

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Worker runs inside deedglass serve (S13 §3.8): it claims witness jobs across orgs through
// store.ClaimWitnessJobs (FOR UPDATE SKIP LOCKED), runs each in an org transaction, and runs the
// sweepers every 5 s. It wakes on deedglass_jobs and polls every 2 s ±20% as a fallback.
type Worker struct {
	p    *Pipeline
	id   string // locked_by: the instance and a random suffix
	wake chan struct{}

	mu   sync.Mutex
	held map[int64]ids.OrgID // jobs claimed and not finished

	// Timings; tests shorten them.
	PollEvery  time.Duration
	SweepEvery time.Duration
	Lease      time.Duration
	Drain      time.Duration
}

// Worker timings (S13 §3.8).
const (
	DefaultPoll  = 2 * time.Second
	DefaultSweep = 5 * time.Second
	DefaultLease = 30 * time.Second
	DefaultDrain = 10 * time.Second
	claimBatch   = 16
	staleBatch   = 100
	keyBatch     = 1000
)

// NewWorker returns a worker named id (the instance's name; it appears in locked_by).
func (p *Pipeline) NewWorker(id string) *Worker {
	if id == "" {
		id = "deedglass"
	}
	return &Worker{p: p, id: id + "-" + ids.New[ids.RequestKind]().String()[4:12], wake: make(chan struct{}, 1), held: map[int64]ids.OrgID{},
		PollEvery: DefaultPoll, SweepEvery: DefaultSweep, Lease: DefaultLease, Drain: DefaultDrain}
}

// Wake is the deedglass_jobs handler: claim now instead of at the next poll.
func (w *Worker) Wake(string) {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func jitter(d time.Duration) time.Duration {
	return d + time.Duration((unitFloat()*0.4-0.2)*float64(d))
}

// unitFloat is a uniform float in [0, 1) from crypto/rand (lint G5: no math/rand).
func unitFloat() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0.5
	}
	return float64(binary.LittleEndian.Uint64(b[:])>>11) / (1 << 53)
}

// Run claims and sweeps until ctx ends, then stops claiming, lets in-flight jobs finish for up to
// Drain, and releases the leases it still holds.
func (w *Worker) Run(ctx context.Context) error {
	sem := make(chan struct{}, w.p.cfg.WorkerConcurrency)
	var wg sync.WaitGroup
	poll := time.NewTimer(0)
	defer poll.Stop()
	sweep := time.NewTicker(w.SweepEvery)
	defer sweep.Stop()
	jobCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()
	for {
		select {
		case <-ctx.Done():
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(w.Drain):
				cancelJobs()
			}
			w.releaseHeld()
			return nil
		case <-sweep.C:
			w.Sweep(ctx)
			continue
		case <-w.wake:
		case <-poll.C:
		}
		free := cap(sem) - len(sem)
		if free > 0 {
			jobs, err := w.p.o.DB.ClaimWitnessJobs(ctx, w.id, min(free, claimBatch), w.Lease)
			if err != nil && ctx.Err() == nil {
				w.p.log.Warn("gate: claiming witness jobs failed", "err", err)
			}
			for _, j := range jobs {
				w.hold(j)
				sem <- struct{}{}
				wg.Add(1)
				go func(j store.ClaimedJob) {
					defer func() { <-sem; wg.Done(); w.drop(j.ID) }()
					w.handle(jobCtx, j)
				}(j)
			}
		}
		poll.Reset(jitter(w.PollEvery))
	}
}

func (w *Worker) hold(j store.ClaimedJob) {
	w.mu.Lock()
	w.held[j.ID] = j.OrgID
	w.mu.Unlock()
	w.p.m.inflight.Add(1)
}

func (w *Worker) drop(id int64) {
	w.mu.Lock()
	delete(w.held, id)
	w.mu.Unlock()
	w.p.m.inflight.Add(-1)
}

// releaseHeld ends the leases of jobs this worker won't finish (graceful shutdown).
func (w *Worker) releaseHeld() {
	w.mu.Lock()
	held := make(map[int64]ids.OrgID, len(w.held))
	for k, v := range w.held {
		held[k] = v
	}
	w.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for id, org := range held {
		_ = w.p.o.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return store.ReleaseJobLease(ctx, tx, org, id, w.id)
		})
	}
}

// backoff is min(2^attempts s, 5 min) ±20%.
func backoff(attempts int) time.Duration {
	d := 5 * time.Minute
	if attempts < 9 {
		d = min(time.Duration(1<<attempts)*time.Second, d)
	}
	return jitter(d)
}

// handle runs one witness job: the deferred witness, then the verdict and the seal in one org
// transaction. A failure retries with backoff; at max_attempts the action is sealed UNVERIFIABLE
// (witness_gave_up).
func (w *Worker) handle(ctx context.Context, j store.ClaimedJob) {
	p := w.p
	var sub Subject
	err := p.o.DB.OrgReadTx(ctx, j.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		ga, err := store.GetGateAction(ctx, tx, j.OrgID, j.ActionID, false)
		if err != nil {
			return err
		}
		sub = subjectOf(ga)
		return nil
	})
	if err != nil {
		p.m.job(j.Kind, "error")
		return
	}
	out, werr := p.o.Witness.Deferred(ctx, sub)
	err = p.o.Ledger.OrgTxRetry(ctx, j.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		job, err := store.GetJob(ctx, tx, j.OrgID, j.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil // another worker finished it
		}
		if err != nil {
			return err
		}
		if werr != nil && job.Attempts < job.MaxAttempts {
			return store.RetryJob(ctx, tx, j.OrgID, j.ID, p.now().Add(backoff(job.Attempts)), redact(werr))
		}
		if werr != nil {
			method := MethodReported
			if sub.Mode == ModeProxy {
				method = MethodEdgeReported
			}
			out = gaveUp(method)
		}
		if err := p.recordVerdict(ctx, tx, j.ActionID, out, "system", ""); err != nil && !errors.Is(err, ledger.ErrStateConflict) {
			return err
		}
		if err := p.seal(ctx, tx, j.ActionID); err != nil && !errors.Is(err, ledger.ErrNotSealable) {
			return err
		}
		return store.CompleteJob(ctx, tx, j.OrgID, j.ID)
	})
	switch {
	case err != nil:
		p.m.job(j.Kind, "error")
		p.log.Warn("gate: a witness job failed", "org_id", j.OrgID.String(), "action_id", j.ActionID.String(), "err", redact(err))
	case werr != nil:
		p.m.job(j.Kind, "retry")
	default:
		p.m.job(j.Kind, "done")
	}
	p.broker.Notify(j.ActionID.String())
}

// redact keeps an error's message short for last_error and logs; errors here never carry payloads,
// but a connector's might, so the cap is hard.
func redact(err error) string { return clipMessage(err.Error()) }
