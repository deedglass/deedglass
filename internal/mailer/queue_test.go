package mailer

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recorder is a Mailer that keeps what it was asked to send.
type recorder struct {
	mu   sync.Mutex
	sent []Message
}

func (r *recorder) Send(_ context.Context, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, m)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func testQueue(m Mailer, o QueueOptions) (*Queue, *lockedBuffer) {
	var buf lockedBuffer
	o.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	return NewQueue(m, o), &buf
}

func TestQueueRunsJobs(t *testing.T) {
	rec := &recorder{}
	q, logs := testQueue(rec, QueueOptions{})
	if q.Mailer() != rec {
		t.Fatal("Mailer() is not the queue's mailer")
	}
	q.Start(context.Background())
	q.Start(context.Background()) // a second Start does nothing
	for range 3 {
		if !q.Enqueue(testMessage()) {
			t.Fatal("Enqueue refused")
		}
	}
	var deadline atomic.Bool
	ok := q.Submit("invite", "usr_1", "req_1", func(ctx context.Context, m Mailer) error {
		dl, has := ctx.Deadline()
		deadline.Store(has && time.Until(dl) <= JobTimeout && time.Until(dl) > JobTimeout-5*time.Second)
		if m != rec {
			return errors.New("job got another mailer")
		}
		return errors.New("lookup failed for usr_1")
	})
	if !ok {
		t.Fatal("Submit refused")
	}
	q.Close()
	q.Close() // idempotent
	if rec.count() != 3 {
		t.Errorf("sent %d, want 3", rec.count())
	}
	if !deadline.Load() {
		t.Error("the job had no 30 s deadline")
	}
	out := logs.String()
	for _, w := range []string{`"msg":"mail.job_failed"`, `"kind":"invite"`, `"user_id":"usr_1"`, `"request_id":"req_1"`, `"error":"lookup failed for usr_1"`} {
		if !strings.Contains(out, w) {
			t.Errorf("logs are missing %s:\n%s", w, out)
		}
	}
	if q.Dropped() != 0 {
		t.Errorf("Dropped = %d", q.Dropped())
	}
	if q.Enqueue(testMessage()) || q.Dropped() != 1 {
		t.Errorf("Enqueue after Close: dropped %d", q.Dropped())
	}
	if !strings.Contains(logs.String(), `"reason":"closed"`) {
		t.Error("a closed drop is not logged")
	}
}

func TestQueueFull(t *testing.T) {
	q, logs := testQueue(&recorder{}, QueueOptions{Size: 1, Workers: 1})
	q.Start(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	var ran atomic.Int32
	block := func(ctx context.Context, m Mailer) error {
		ran.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return nil
	}
	if !q.Submit("login", "usr_a", "req_a", block) {
		t.Fatal("first Submit refused")
	}
	<-started // the worker holds job a; the buffer is empty
	if !q.Submit("login", "usr_b", "req_b", block) {
		t.Fatal("second Submit refused")
	}
	if q.Submit("login", "usr_c", "req_c", block) {
		t.Fatal("Submit on a full queue accepted")
	}
	if q.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", q.Dropped())
	}
	close(release)
	q.Close()
	if ran.Load() != 2 {
		t.Errorf("%d jobs ran, want 2", ran.Load())
	}
	out := logs.String()
	for _, w := range []string{`"level":"WARN"`, `"msg":"mail.dropped"`, `"kind":"login"`, `"user_id":"usr_c"`, `"request_id":"req_c"`, `"reason":"full"`} {
		if !strings.Contains(out, w) {
			t.Errorf("logs are missing %s:\n%s", w, out)
		}
	}
}

func TestQueueCloseDrains(t *testing.T) {
	rec := &recorder{}
	q, _ := testQueue(rec, QueueOptions{Workers: 1, Drain: 5 * time.Second})
	q.Start(context.Background())
	for range 5 {
		q.Submit("login", "", "", func(ctx context.Context, m Mailer) error {
			time.Sleep(10 * time.Millisecond)
			return m.Send(ctx, testMessage())
		})
	}
	q.Close()
	if rec.count() != 5 {
		t.Errorf("sent %d, want 5 after Close", rec.count())
	}
}

func TestQueueCloseCancelsAfterDrain(t *testing.T) {
	q, logs := testQueue(&recorder{}, QueueOptions{Workers: 1, Drain: 30 * time.Millisecond})
	q.Start(context.Background())
	started := make(chan struct{})
	var cancelled atomic.Bool
	q.Submit("login", "usr_slow", "", func(ctx context.Context, m Mailer) error {
		close(started)
		<-ctx.Done()
		cancelled.Store(true)
		return ctx.Err()
	})
	<-started
	var ranSecond atomic.Bool
	q.Submit("login", "usr_late", "", func(context.Context, Mailer) error { ranSecond.Store(true); return nil })
	begin := time.Now()
	q.Close()
	if d := time.Since(begin); d > 2*time.Second {
		t.Errorf("Close took %v", d)
	}
	if !cancelled.Load() {
		t.Error("the running job's context was not cancelled")
	}
	if ranSecond.Load() {
		t.Error("a job queued past the drain ran")
	}
	if q.Dropped() != 1 || !strings.Contains(logs.String(), `"reason":"shutdown"`) {
		t.Errorf("Dropped = %d; logs:\n%s", q.Dropped(), logs.String())
	}
}

func TestQueuePanic(t *testing.T) {
	rec := &recorder{}
	q, logs := testQueue(rec, QueueOptions{Workers: 1})
	q.Start(context.Background())
	q.Submit("login", "usr_p", "req_p", func(context.Context, Mailer) error {
		panic("cannot send to priya@acme.com")
	})
	q.Enqueue(testMessage())
	q.Close()
	if rec.count() != 1 {
		t.Errorf("the worker did not survive the panic: sent %d", rec.count())
	}
	out := logs.String()
	if !strings.Contains(out, `"msg":"mail.job_panicked"`) || !strings.Contains(out, `"user_id":"usr_p"`) || !strings.Contains(out, `"panic_type":"string"`) {
		t.Errorf("panic not logged:\n%s", out)
	}
	if strings.Contains(out, "priya@acme.com") {
		t.Errorf("the panic value reached the logs:\n%s", out)
	}
}

func TestQueueNotStarted(t *testing.T) {
	q := NewQueue(None{}, QueueOptions{Logger: slog.New(slog.DiscardHandler)})
	if q.n != DefaultQueueWorkers || q.drain != DefaultQueueDrain || cap(q.jobs) != DefaultQueueSize {
		t.Errorf("defaults: workers %d drain %v size %d", q.n, q.drain, cap(q.jobs))
	}
	q.Enqueue(testMessage())
	q.Enqueue(testMessage())
	q.Close()
	if q.Dropped() != 2 {
		t.Errorf("Dropped = %d, want 2 (queued but never started)", q.Dropped())
	}
	q.Start(context.Background()) // after Close: does nothing
	if q.started {
		t.Error("Start after Close started workers")
	}
	if NewQueue(None{}, QueueOptions{}).log == nil {
		t.Error("no default logger")
	}
}
