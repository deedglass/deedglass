package webhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/pkg/deedglass/webhook"
)

// The delivery worker (S22 §3.9). It claims due outbox rows across orgs through
// store.ClaimWebhookDeliveries (ids only, at most 4 in flight per endpoint), then delivers each in
// its own org transactions: read, open the secret, POST with no transaction open, record the
// outcome. Redirects are never followed (a 3xx is a failure); the destination is checked again by
// the egress door's dialer at every delivery (DNS rebinding can't reach a private address).

// Worker delivers the outbox.
type Worker struct {
	s    *Service
	id   string
	wake chan struct{}
	// PollEvery is the fallback poll (the Ledger's events wake it sooner).
	PollEvery time.Duration
}

// NewWorker returns a delivery worker named id (the instance; it appears in locked_by).
func (s *Service) NewWorker(id string) *Worker {
	return &Worker{s: s, id: id + "-whk", wake: make(chan struct{}, 1), PollEvery: time.Second}
}

// Wake makes the worker claim now (wired to deedglass_events: every event may have queued rows).
func (w *Worker) Wake(string) {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run claims and delivers until ctx ends.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTimer(0)
	defer t.Stop()
	sem := make(chan struct{}, 8)
	for {
		select {
		case <-ctx.Done():
			for range cap(sem) {
				sem <- struct{}{}
			}
			return nil
		case <-w.wake:
		case <-t.C:
		}
		free := cap(sem) - len(sem)
		if free > 0 && w.s.o.Egress != nil {
			ds, err := w.s.o.DB.ClaimWebhookDeliveries(ctx, w.id, free, inFlightLease)
			if err != nil && ctx.Err() == nil {
				w.s.log.Warn("webhook: claiming deliveries failed", "err", err)
			}
			for _, d := range ds {
				sem <- struct{}{}
				go func(d store.ClaimedDelivery) {
					defer func() { <-sem }()
					if _, err := w.s.deliver(context.WithoutCancel(ctx), d.OrgID, d.ID); err != nil {
						w.s.log.Warn("webhook: a delivery failed", "org_id", d.OrgID.String(), "webhook_id", d.WebhookID.String(),
							"delivery_id", d.ID.String(), "err", err)
					}
				}(d)
			}
		}
		t.Reset(w.PollEvery)
	}
}

// Result is one delivery attempt's outcome.
type Result struct {
	Delivered  bool
	Status     int
	Took       time.Duration
	Error      string
	EventID    string
	EventType  string
	WebhookID  string
	DeliveryID string
}

type pending struct {
	webhook   ids.WebhookID
	url       string
	status    string
	envelope  []byte
	eventID   string
	eventType string
	payload   []byte
	attempt   int
	failing   *time.Time
}

// deliver makes one attempt at a delivery and records it.
func (s *Service) deliver(ctx context.Context, org ids.OrgID, id ids.WebhookDeliveryID) (Result, error) {
	var p pending
	err := s.o.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT d.webhook_id, w.url, w.status, w.secret_envelope, d.event_id, d.event_type, d.payload::text,
		    d.attempt, w.failing_since
		    FROM deedglass.webhook_deliveries AS d JOIN deedglass.webhooks AS w ON w.org_id = d.org_id AND w.id = d.webhook_id
		    WHERE d.org_id = $1 AND d.id = $2 AND d.status = 'pending'`, org, id).
			Scan(&p.webhook, &p.url, &p.status, &p.envelope, &p.eventID, &p.eventType, &p.payload, &p.attempt, &p.failing)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, nil // delivered, deleted or dead meanwhile
	}
	if err != nil {
		return Result{}, err
	}
	r := Result{EventID: p.eventID, EventType: p.eventType, WebhookID: p.webhook.String(), DeliveryID: id.String()}
	if p.status != "active" {
		return r, s.record(ctx, org, id, p, r, "dead")
	}
	secret, err := s.openSecret(ctx, org, p.webhook, p.envelope)
	if err != nil {
		r.Error = "the endpoint's signing secret can't be opened"
		return r, s.record(ctx, org, id, p, r, "")
	}
	r = s.post(ctx, p, secret, r)
	return r, s.record(ctx, org, id, p, r, "")
}

// post sends the payload, signed. No transaction is open.
func (s *Service) post(ctx context.Context, p pending, secret string, r Result) Result {
	start := s.now()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(p.payload))
	if err != nil {
		r.Error = "the request can't be built"
		return r
	}
	h := req.Header
	h.Set("Content-Type", "application/json")
	h.Set("Deedglass-Event-Id", p.eventID)
	h.Set("Deedglass-Event-Type", p.eventType)
	h.Set("Deedglass-Webhook-Id", p.webhook.String())
	h.Set(webhook.Header, webhook.Sign(secret, start, p.payload))
	client := s.o.Egress.Client(egress.ClientOptions{Timeout: s.cfg.Timeout, MaxResponseBytes: MaxResponseRead,
		Redirects: egress.RedirectNone, UserAgent: UserAgent})
	res, err := client.Do(req)
	r.Took = s.now().Sub(start)
	if err != nil {
		if be, ok := egress.IsBlocked(err); ok {
			r.Error = "destination blocked: " + be.Reason
		} else if ctx.Err() != nil {
			r.Error = "no answer within " + s.cfg.Timeout.String()
		} else {
			r.Error = "the endpoint couldn't be reached"
		}
		return r
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, MaxResponseRead))
	_ = res.Body.Close()
	r.Status = res.StatusCode
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		r.Delivered = true
	case res.StatusCode >= 300 && res.StatusCode < 400:
		r.Error = "a redirect (" + strconv.Itoa(res.StatusCode) + "); Deedglass doesn't follow webhook redirects"
	default:
		r.Error = "HTTP " + strconv.Itoa(res.StatusCode)
	}
	return r
}

// record writes an attempt's outcome: succeeded, a retry on the schedule, or dead; the endpoint's
// health; and, after DisableAfter of failures, disables it with a webhook.disabled deed.
func (s *Service) record(ctx context.Context, org ids.OrgID, id ids.WebhookDeliveryID, p pending, r Result, force string) error {
	now := s.now()
	status := "succeeded"
	var next *time.Time
	if !r.Delivered {
		status = "pending"
		if p.attempt > s.cfg.MaxAttempts || force == "dead" {
			status = "dead"
		} else {
			t := now.Add(jitter(Schedule[p.attempt-1]))
			next = &t
		}
	}
	var respStatus, respMS *int
	if r.Status > 0 {
		v := r.Status
		respStatus = &v
	}
	if r.Took > 0 || r.Status > 0 {
		v := int(r.Took.Milliseconds())
		respMS = &v
	}
	return s.o.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		attempt := p.attempt
		if status == "pending" {
			attempt++
		}
		var delivered *time.Time
		if r.Delivered {
			delivered = &now
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.webhook_deliveries SET status = $3, attempt = $4, response_status = $5, response_ms = $6,
		    last_error = $7, next_attempt_at = $8, delivered_at = $9, locked_by = NULL, locked_until = NULL
		    WHERE org_id = $1 AND id = $2`, org, id, status, attempt, respStatus, respMS, nonEmpty(r.Error), next, delivered); err != nil {
			return err
		}
		if r.Delivered {
			_, err := tx.Exec(ctx, `UPDATE deedglass.webhooks SET last_success_at = $3, failing_since = NULL WHERE org_id = $1 AND id = $2`,
				org, p.webhook, now)
			return err
		}
		if force == "dead" {
			return nil
		}
		var failing *time.Time
		if err := tx.QueryRow(ctx, `UPDATE deedglass.webhooks SET failing_since = COALESCE(failing_since, $3) WHERE org_id = $1 AND id = $2
		    RETURNING failing_since`, org, p.webhook, now).Scan(&failing); err != nil {
			return err
		}
		if failing != nil && now.Sub(*failing) >= s.cfg.DisableAfter {
			return s.disable(ctx, tx, org, p.webhook, now)
		}
		return nil
	})
}

// disable turns off an endpoint that has failed for DisableAfter, and seals webhook.disabled.
func (s *Service) disable(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.WebhookID, now time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.webhooks SET status = 'disabled', disabled_reason = 'failing'
	    WHERE org_id = $1 AND id = $2 AND status = 'active'`, org, id)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = s.o.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminWebhookDisabled,
		Actor: deed.Ref{Type: "system", ID: "deedglass"}, Target: &deed.Ref{Type: "webhook", ID: id.String()},
		Reason: fmt.Sprintf("The endpoint failed every delivery for %s.", s.cfg.DisableAfter), Data: map[string]any{"reason": "failing"}})
	return err
}

// jitter is d ±20% (crypto/rand: lint G5).
func jitter(d time.Duration) time.Duration {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return d
	}
	f := float64(binary.LittleEndian.Uint64(b[:])>>11) / (1 << 53)
	return d + time.Duration((f*0.4-0.2)*float64(d))
}

// Test sends webhook.test to one endpoint now and waits for the answer (POST /v1/webhooks/{id}/test).
// The delivery is written leased to this request, so the worker leaves it alone; a failure is
// retried on the schedule like any other.
func (s *Service) Test(ctx context.Context, org ids.OrgID, id string) (*TestResult, error) {
	wid, err := parseID(id)
	if err != nil {
		return nil, err
	}
	if s.o.KEK == nil {
		return nil, errVault()
	}
	evt := ids.New[ids.ActionEventKind]()
	did := ids.New[ids.WebhookDeliveryKind]()
	now := s.now()
	err = s.o.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		w, err := readWebhook(ctx, tx, org, wid)
		if err != nil {
			return err
		}
		env := ledger.Envelope(ledger.Event{Event: evt.String(), Type: ledger.EventWebhookTest, Org: org.String(), ID: wid.String(),
			At: deed.FormatTime(now)})
		env.APIVersion = w.APIVersion
		body, err := json.Marshal(env)
		if err != nil {
			return err
		}
		digest, err := deed.DigestJSON(body)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO deedglass.webhook_deliveries (id, org_id, webhook_id, event_type, event_id, payload_digest, payload,
		    next_attempt_at, locked_by, locked_until) VALUES ($1, $2, $3, 'webhook.test', $4, $5, $6::jsonb, $7::timestamptz, 'test', $7::timestamptz + interval '1 minute')`,
			did, org, wid, evt, digest, string(body), now)
		return err
	})
	if err != nil {
		return nil, err
	}
	r, err := s.deliver(ctx, org, did)
	if err != nil {
		return nil, err
	}
	return &TestResult{Object: "webhook_test", Webhook: wid.String(), Delivery: did.String(), Event: evt.String(), Delivered: r.Delivered,
		ResponseStatus: r.Status, ResponseMS: r.Took.Milliseconds(), Error: nonEmpty(r.Error)}, nil
}

// TestResult is POST /v1/webhooks/{id}/test's answer.
type TestResult struct {
	Object         string  `json:"object"` // "webhook_test"
	Webhook        string  `json:"webhook"`
	Delivery       string  `json:"delivery"`
	Event          string  `json:"event"`
	Delivered      bool    `json:"delivered"`
	ResponseStatus int     `json:"response_status"`
	ResponseMS     int64   `json:"response_ms"`
	Error          *string `json:"error"`
}
