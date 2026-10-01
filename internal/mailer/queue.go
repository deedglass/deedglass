package mailer

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// Queue defaults.
const (
	// DefaultQueueSize is the default number of jobs a Queue holds.
	DefaultQueueSize = 1024
	// DefaultQueueWorkers is the default number of workers.
	DefaultQueueWorkers = 2
	// DefaultQueueDrain is how long Close waits for queued jobs by default.
	DefaultQueueDrain = 10 * time.Second
	// JobTimeout bounds each job's context.
	JobTimeout = 30 * time.Second
	// closeGrace is how long Close waits for workers after cancelling their context.
	closeGrace = time.Second
	// maxStack bounds the stack logged for a job panic.
	maxStack = 4 << 10
)

// QueueOptions configures NewQueue. Zero values take the defaults.
type QueueOptions struct {
	// Size is how many jobs the queue holds; default 1024.
	Size int
	// Workers is how many jobs run at once; default 2.
	Workers int
	// Drain is how long Close waits for queued jobs before cancelling them; default 10 s.
	Drain time.Duration
	// Logger receives "mail.dropped", "mail.job_failed" and "mail.job_panicked"; nil means
	// slog.Default().
	Logger *slog.Logger
}

// Job is one unit of mail work. It runs on a worker with a 30 s context, does any lookups it
// needs, and sends through m (the queue's mailer). A job's error is logged as text, so it must
// never contain an email address, a link or a code.
type Job func(ctx context.Context, m Mailer) error

// queued is a Job with the ids used in its logs.
type queued struct {
	kind, userID, requestID string
	run                     Job
}

// Queue runs mail jobs on a small pool of workers, off the request path and outside any
// database transaction. Submit never blocks: a full or closed queue drops the job, logs
// "mail.dropped" and counts it in Dropped.
type Queue struct {
	m   Mailer
	log *slog.Logger
	n   int

	drain time.Duration

	mu      sync.RWMutex // guards closed and sends on jobs
	closed  bool
	started bool
	jobs    chan queued

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	dropped   atomic.Uint64
	closeOnce sync.Once
}

// NewQueue returns a Queue that sends through m. Call Start to run its workers and Close to stop
// them.
func NewQueue(m Mailer, o QueueOptions) *Queue {
	if o.Size <= 0 {
		o.Size = DefaultQueueSize
	}
	if o.Workers <= 0 {
		o.Workers = DefaultQueueWorkers
	}
	if o.Drain <= 0 {
		o.Drain = DefaultQueueDrain
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Queue{m: m, log: o.Logger, n: o.Workers, drain: o.Drain, jobs: make(chan queued, o.Size)}
}

// Start starts the workers. The workers keep ctx's values but not its cancellation: Close ends
// them, after draining. A second call, or a call after Close, does nothing.
func (q *Queue) Start(ctx context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.started || q.closed {
		return
	}
	q.started = true
	wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	q.cancel = cancel
	for range q.n {
		q.wg.Add(1)
		go q.work(wctx)
	}
}

// Submit queues j without blocking. It returns false, logs "mail.dropped" and counts the job in
// Dropped when the queue is full or closed. kind, userID and requestID are used only in logs.
func (q *Queue) Submit(kind, userID, requestID string, j Job) bool {
	q.mu.RLock()
	ok := false
	if !q.closed {
		select {
		case q.jobs <- queued{kind: kind, userID: userID, requestID: requestID, run: j}:
			ok = true
		default:
		}
	}
	closed := q.closed
	q.mu.RUnlock()
	if !ok {
		reason := "full"
		if closed {
			reason = "closed"
		}
		q.drop(kind, userID, requestID, reason)
	}
	return ok
}

// Enqueue submits a job that sends msg through the queue's mailer.
func (q *Queue) Enqueue(msg Message) bool {
	return q.Submit(msg.Kind, msg.UserID, msg.RequestID, func(ctx context.Context, m Mailer) error {
		return m.Send(ctx, msg)
	})
}

// Close stops accepting jobs, waits up to the Drain duration for queued jobs to finish, then
// cancels the workers' context; jobs still queued at that point are dropped. It is idempotent.
func (q *Queue) Close() {
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		close(q.jobs)
		started := q.started
		q.mu.Unlock()
		if !started {
			for j := range q.jobs {
				q.drop(j.kind, j.userID, j.requestID, "closed")
			}
			return
		}
		done := make(chan struct{})
		go func() { q.wg.Wait(); close(done) }()
		t := time.NewTimer(q.drain)
		defer t.Stop()
		select {
		case <-done:
		case <-t.C:
		}
		q.cancel()
		g := time.NewTimer(closeGrace)
		defer g.Stop()
		select {
		case <-done:
		case <-g.C:
		}
	})
}

// Dropped returns how many jobs were not run because the queue was full or closed.
func (q *Queue) Dropped() uint64 { return q.dropped.Load() }

// Mailer returns the mailer jobs send through.
func (q *Queue) Mailer() Mailer { return q.m }

// drop counts and logs a job that will not run.
func (q *Queue) drop(kind, userID, requestID, reason string) {
	q.dropped.Add(1)
	q.log.LogAttrs(context.Background(), slog.LevelWarn, "mail.dropped",
		slog.String("kind", kind), slog.String("user_id", userID), slog.String("request_id", requestID),
		slog.String("reason", reason))
}

// work runs jobs until the channel is closed and drained.
func (q *Queue) work(ctx context.Context) {
	defer q.wg.Done()
	for j := range q.jobs {
		if ctx.Err() != nil {
			q.drop(j.kind, j.userID, j.requestID, "shutdown")
			continue
		}
		q.run(ctx, j)
	}
}

// run runs one job with a timeout, logging its error and recovering a panic.
func (q *Queue) run(ctx context.Context, j queued) {
	ctx, cancel := context.WithTimeout(ctx, JobTimeout)
	defer cancel()
	ids := []slog.Attr{slog.String("kind", j.kind), slog.String("user_id", j.userID), slog.String("request_id", j.requestID)}
	defer func() {
		if v := recover(); v != nil {
			// The panic value may hold message content, so only its type and the stack are logged.
			stack := debug.Stack()
			if len(stack) > maxStack {
				stack = stack[:maxStack]
			}
			q.log.LogAttrs(ctx, slog.LevelError, "mail.job_panicked",
				append(ids, slog.String("panic_type", fmt.Sprintf("%T", v)), slog.String("stack", string(stack)))...)
		}
	}()
	if err := j.run(ctx, q.m); err != nil {
		q.log.LogAttrs(ctx, slog.LevelWarn, "mail.job_failed", append(ids, slog.String("error", err.Error()))...)
	}
}
