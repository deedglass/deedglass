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
	"github.com/deedglass/deedglass/internal/witness"
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
					switch {
					case j.Kind == JobL2Match:
						w.handleL2Job(jobCtx, j)
					case j.Kind == JobApprovalResume:
						w.handleOther(jobCtx, j, w.p.resumeJob)
					case w.p.jobHandler(j.Kind) != nil:
						w.handleOther(jobCtx, j, w.p.jobHandler(j.Kind))
					default:
						w.handle(jobCtx, j)
					}
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

// handle runs one witness job (S19: kind l1_readback, and S13's kind witness): the read-back with
// no transaction open, then the verdict, the deed and the job's end in one org transaction (B2).
// A job that can't run (the connector can't be resolved, the database fails) retries with backoff;
// at max_attempts the Witness decides without L1, with witness_gave_up listed as an L1 gap.
func (w *Worker) handle(ctx context.Context, j store.ClaimedJob) {
	p := w.p
	var (
		ga  *store.GateAction
		job store.JobRow
	)
	err := p.o.DB.OrgReadTx(ctx, j.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if ga, err = store.GetGateAction(ctx, tx, j.OrgID, j.ActionID, false); err != nil {
			return err
		}
		job, err = store.PeekJob(ctx, tx, j.OrgID, j.ID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		p.m.job(j.Kind, "done") // another worker, or B2, finished it
		return
	}
	if err != nil {
		p.m.job(j.Kind, "error")
		return
	}
	var (
		out  witness.Outcome
		werr error
		wc   witnessCase
	)
	if State(ga.State) == Witnessing {
		wc, werr = p.jobCase(ctx, ga, job.Payload)
		if werr == nil {
			rctx, cancel := witness.WithBudget(ctx, p.wcfg.ReadbackTimeout)
			out, _, werr = p.observe(rctx, wc, true)
			cancel()
		}
	}
	partial := false // S21: L2 is waiting too, so the read-back is kept and finalizeL2 decides
	err = p.o.Ledger.OrgTxRetry(ctx, j.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		partial = false
		job, err := store.GetJob(ctx, tx, j.OrgID, j.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil // another worker finished it
		}
		if err != nil {
			return err
		}
		cur, err := store.GetGateAction(ctx, tx, j.OrgID, j.ActionID, true)
		if err != nil {
			return err
		}
		if State(cur.State) != Witnessing {
			return store.CompleteJob(ctx, tx, j.OrgID, j.ID) // already decided (B2 won the race)
		}
		if werr != nil && job.Attempts < job.MaxAttempts {
			return store.RetryJob(ctx, tx, j.OrgID, j.ID, p.now().Add(backoff(job.Attempts)), redact(werr))
		}
		if werr != nil {
			if wc.sub.ActionID == "" {
				wc = p.storedCase(cur)
			}
			if out, err = p.decide(wc, witness.GaveUp(witness.L1, job.Attempts, redact(werr))); err != nil {
				return err
			}
		}
		if waiting, err := l2Waiting(ctx, tx, j.OrgID, j.ActionID); err != nil {
			return err
		} else if waiting {
			partial = true
			return nil // stored below, outside this transaction's verdict path
		}
		if err := p.recordVerdict(ctx, tx, j.ActionID, out, "system", ""); err != nil && !errors.Is(err, ledger.ErrStateConflict) {
			return err
		}
		if err := p.seal(ctx, tx, j.ActionID); err != nil && !errors.Is(err, ledger.ErrNotSealable) {
			return err
		}
		return store.CompleteJob(ctx, tx, j.OrgID, j.ID)
	})
	if err == nil && partial {
		if err = p.storeL1Partial(ctx, j.OrgID, j.ActionID, out); err == nil || errors.Is(err, ErrStateConflict) {
			err = p.finalizeL2(ctx, j.OrgID, j.ActionID, l2Check, "")
			if errors.Is(err, ErrStateConflict) {
				err = nil
			}
		}
	}
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

// Approval job kinds (S22). approval_resume is the Gate's own (Resume); approval_notify belongs to
// internal/approve, which registers its handler with SetJobHandler.
const (
	JobApprovalNotify = "approval_notify"
	JobApprovalResume = "approval_resume"
)

// JobHandler runs one job of a kind the Gate doesn't own. An error retries the job with backoff,
// up to its max_attempts; the handler must be idempotent.
type JobHandler func(ctx context.Context, org ids.OrgID, action ids.ActionID, attempts int) error

// SetJobHandler registers the handler of a job kind before the worker runs (serve wires
// approve.Service's approval_notify).
func (p *Pipeline) SetJobHandler(kind string, h JobHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.jobs == nil {
		p.jobs = map[string]JobHandler{}
	}
	p.jobs[kind] = h
}

func (p *Pipeline) jobHandler(kind string) JobHandler {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jobs[kind]
}

// resumeJob is approval_resume: an approved action runs (gateway) or becomes authorized at the edge
// (proxy) through Resume, whose compare-and-set makes a second worker or a retry a no-op.
func (p *Pipeline) resumeJob(ctx context.Context, org ids.OrgID, action ids.ActionID, _ int) error {
	_, _, err := p.resume(ctx, org, action)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// handleOther runs a job through h, then ends it in an org transaction: done, retried with backoff,
// or (at max_attempts) dropped with a warning.
func (w *Worker) handleOther(ctx context.Context, j store.ClaimedJob, h JobHandler) {
	p := w.p
	herr := h(ctx, j.OrgID, j.ActionID, j.Attempts)
	err := p.o.DB.OrgTx(ctx, j.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		job, err := store.GetJob(ctx, tx, j.OrgID, j.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if herr != nil && job.Attempts < job.MaxAttempts {
			return store.RetryJob(ctx, tx, j.OrgID, j.ID, p.now().Add(backoff(job.Attempts)), redact(herr))
		}
		return store.CompleteJob(ctx, tx, j.OrgID, j.ID)
	})
	switch {
	case err != nil:
		p.m.job(j.Kind, "error")
		p.log.Warn("gate: ending a job failed", "kind", j.Kind, "org_id", j.OrgID.String(), "action_id", j.ActionID.String(), "err", redact(err))
	case herr != nil:
		p.m.job(j.Kind, "retry")
		p.log.Warn("gate: a job failed and will retry", "kind", j.Kind, "org_id", j.OrgID.String(), "action_id", j.ActionID.String(),
			"attempt", j.Attempts, "err", redact(herr))
	default:
		p.m.job(j.Kind, "done")
	}
	p.broker.Notify(j.ActionID.String())
}
