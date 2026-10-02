//go:build integration

package webhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
	"github.com/deedglass/deedglass/internal/vault"
	"github.com/deedglass/deedglass/pkg/deedglass/webhook"
)

var bg = context.Background()

type hit struct {
	header http.Header
	body   []byte
}

type receiver struct {
	mu     sync.Mutex
	hits   []hit
	status atomic.Int32 // the next answer's status (0: 200)
	srv    *httptest.Server
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.hits = append(r.hits, hit{header: req.Header.Clone(), body: b})
		r.mu.Unlock()
		st := int(r.status.Load())
		if st == 0 {
			st = 200
		}
		if st >= 300 && st < 400 {
			w.Header().Set("Location", "http://169.254.169.254/latest/meta-data/")
		}
		w.WriteHeader(st)
		_, _ = w.Write([]byte(strings.Repeat("x", 100_000)))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) all() []hit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hit(nil), r.hits...)
}

type env struct {
	e    *storetest.Env
	led  *ledger.Ledger
	s    *Service
	org  ids.OrgID
	user ids.UserID
	skew atomic.Int64
	logs *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := storetest.New(t, storetest.Options{})
	seed, _ := deed.NewSeed(rand.Reader)
	signer, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.DB.RegisterSealKey(bg, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	x := &env{e: e, logs: &bytes.Buffer{}}
	log := slog.New(slog.NewTextHandler(x.logs, nil))
	x.led = ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true))
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	ring, err := vault.NewRing(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	x.s = New(Options{DB: e.DB, Ledger: x.led, KEK: ring, Logger: log,
		Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true},
		Clock:  func() time.Time { return time.Now().Add(time.Duration(x.skew.Load())) }})
	o, u, _, err := e.DB.CreateOrg(bg, store.NewOrg{Name: "Acme", Slug: "acme", OwnerEmail: "owner@acme.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	x.org, x.user = o.ID, u.ID
	return x
}

func (x *env) actor() Actor {
	return Actor{Ref: deed.Ref{Type: "user", ID: x.user.String()}, By: x.user.String()}
}

// publish sends one event in its own transaction (rollback: then rolls it back).
func (x *env) publish(t *testing.T, typ string, rollback bool) string {
	t.Helper()
	evt := ids.New[ids.ActionEventKind]().String()
	st := "approved"
	err := x.e.DB.OrgTx(bg, x.org, func(ctx context.Context, tx pgx.Tx) error {
		if err := x.led.Publish(ctx, tx, ledger.Event{Event: evt, Type: typ, Org: x.org.String(), ID: ids.New[ids.ApprovalKind]().String(),
			Status: &st}); err != nil {
			return err
		}
		if rollback {
			return errRollback
		}
		return nil
	})
	if err != nil && err != errRollback {
		t.Fatal(err)
	}
	return evt
}

var errRollback = io.ErrUnexpectedEOF

func (x *env) deliveries(t *testing.T, wh string) []struct {
	id, status string
	attempt    int
} {
	t.Helper()
	var out []struct {
		id, status string
		attempt    int
	}
	err := x.e.DB.OrgReadTx(bg, x.org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, status, attempt FROM deedglass.webhook_deliveries WHERE webhook_id = $1 ORDER BY created_at`, wh)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d struct {
				id, status string
				attempt    int
			}
			if err := rows.Scan(&d.id, &d.status, &d.attempt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (x *env) deliverAll(t *testing.T, wh string) {
	t.Helper()
	for _, d := range x.deliveries(t, wh) {
		if d.status != "pending" {
			continue
		}
		id, _ := ids.Parse[ids.WebhookDeliveryKind](d.id)
		if _, err := x.s.deliver(bg, x.org, id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWebhookDelivery(t *testing.T) {
	x := newEnv(t)
	r := newReceiver(t)
	w, err := x.s.Create(bg, x.actor(), x.org, CreateInput{URL: r.srv.URL + "/hook", Events: []string{"approval.decided", "approval.requested"}})
	if err != nil {
		t.Fatal(err)
	}
	if w.Secret == nil || !strings.HasPrefix(*w.Secret, "whsec_") || w.Deed == nil {
		t.Fatalf("create: %+v", w)
	}
	secret := *w.Secret
	// Reading it back never shows the secret.
	g, _ := x.s.Get(bg, x.org, w.ID)
	if g.Secret != nil || *g.SecretHint != secret[len(secret)-4:] {
		t.Errorf("get: %+v", g)
	}
	// An event of a subscribed type queues one delivery; another type, and a rolled-back one, none.
	evt := x.publish(t, "approval.decided", false)
	x.publish(t, "deed.sealed", false)
	x.publish(t, "approval.requested", true)
	ds := x.deliveries(t, w.ID)
	if len(ds) != 1 {
		t.Fatalf("%d deliveries, want 1", len(ds))
	}
	// 500, then 200: two attempts, the same event id and body bytes, both signed.
	r.status.Store(500)
	x.deliverAll(t, w.ID)
	if d := x.deliveries(t, w.ID)[0]; d.status != "pending" || d.attempt != 2 {
		t.Fatalf("after a 500: %+v", d)
	}
	r.status.Store(200)
	x.deliverAll(t, w.ID)
	if d := x.deliveries(t, w.ID)[0]; d.status != "succeeded" {
		t.Fatalf("after a 200: %+v", d)
	}
	hits := r.all()
	if len(hits) != 2 || !bytes.Equal(hits[0].body, hits[1].body) {
		t.Fatalf("%d hits, bodies equal %v", len(hits), len(hits) == 2 && bytes.Equal(hits[0].body, hits[1].body))
	}
	for _, h := range hits {
		if h.header.Get("Deedglass-Event-Id") != evt || h.header.Get("Deedglass-Event-Type") != "approval.decided" ||
			h.header.Get("Deedglass-Webhook-Id") != w.ID || !strings.HasPrefix(h.header.Get("User-Agent"), "Deedglass-Webhooks/1.0") {
			t.Errorf("headers %v", h.header)
		}
		if err := webhook.Verify(secret, h.header.Get(webhook.Header), h.body, 0); err != nil {
			t.Errorf("signature: %v", err)
		}
		if err := webhook.Verify("whsec_wrong", h.header.Get(webhook.Header), h.body, 0); err == nil {
			t.Error("a wrong secret verified")
		}
		flipped := append([]byte(nil), h.body...)
		flipped[10] ^= 1
		if err := webhook.Verify(secret, h.header.Get(webhook.Header), flipped, 0); err == nil {
			t.Error("a flipped byte verified")
		}
		var env ledger.WebhookEnvelope
		if err := json.Unmarshal(h.body, &env); err != nil || env.Object != "event" || env.ID != evt || env.Type != "approval.decided" ||
			env.APIVersion != "2026-10-01" || env.Data.Object["object"] != "approval" {
			t.Errorf("envelope %s: %v", h.body, err)
		}
		for _, bad := range []string{`"args"`, `"sql"`, `"context"`, "@"} {
			if bytes.Contains(h.body, []byte(bad)) {
				t.Errorf("payload has %s: %s", bad, h.body)
			}
		}
	}
	// webhook.test: sent now, answered with the outcome.
	tr, err := x.s.Test(bg, x.org, w.ID)
	if err != nil || !tr.Delivered || tr.ResponseStatus != 200 {
		t.Fatalf("test: %+v %v", tr, err)
	}
	if last := r.all()[len(r.all())-1]; webhook.Verify(secret, last.header.Get(webhook.Header), last.body, 0) != nil ||
		last.header.Get("Deedglass-Event-Type") != "webhook.test" {
		t.Error("the test delivery isn't signed")
	}
	// Redirects are failures, never followed (the target is a metadata address).
	r.status.Store(302)
	x.publish(t, "approval.decided", false)
	before := len(r.all())
	x.deliverAll(t, w.ID)
	if len(r.all()) != before+1 {
		t.Errorf("a redirect was followed: %d hits", len(r.all())-before)
	}
	vs, _ := x.s.Deliveries(bg, x.org, w.ID, 10)
	if len(vs) < 3 || vs[0].Status != "pending" || vs[0].LastError == nil || !strings.Contains(*vs[0].LastError, "redirect") {
		t.Errorf("deliveries %+v", vs)
	}
	// Failing for 72 h disables the endpoint with a deed.
	r.status.Store(500)
	if err := x.e.DB.OrgTx(bg, x.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.webhooks SET failing_since = pg_catalog.now() - interval '73 hours' WHERE id = $1`, w.ID)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE deedglass.webhook_deliveries SET next_attempt_at = pg_catalog.now() WHERE webhook_id = $1`, w.ID)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	x.deliverAll(t, w.ID)
	if g, _ := x.s.Get(bg, x.org, w.ID); g.Status != "disabled" || g.DisabledReason == nil || *g.DisabledReason != "failing" {
		t.Errorf("after 72 h of failures: %+v", g)
	}
	var n int
	_ = x.e.DB.OrgReadTx(bg, x.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.admin_events WHERE action = 'webhook.disabled'`).Scan(&n)
	})
	if n != 1 {
		t.Errorf("%d webhook.disabled deeds", n)
	}
	// Delete seals webhook.deleted and removes the rows.
	if d, err := x.s.Delete(bg, x.actor(), x.org, w.ID); err != nil || !d.Deleted {
		t.Fatalf("delete %+v %v", d, err)
	}
	if strings.Contains(x.logs.String(), secret) || strings.Contains(x.logs.String(), "/hook") {
		t.Error("the secret or the URL's path reached the log")
	}
}

func TestWebhookDestinations(t *testing.T) {
	x := newEnv(t)
	prod := New(Options{DB: x.e.DB, Ledger: x.led, KEK: x.s.o.KEK, Egress: &egress.Policy{Env: egress.EnvProd}})
	for _, u := range []string{"http://169.254.169.254/latest/meta-data/", "https://10.0.0.5/hook", "http://127.0.0.1:9/hook",
		"https://localhost/hook", "ftp://example.com/x", "https://user:pw@example.com/"} {
		if _, err := prod.Create(bg, x.actor(), x.org, CreateInput{URL: u, Events: []string{"deed.sealed"}}); err == nil {
			t.Errorf("prod accepted %s", u)
		} else if c := httpx.AsError(err); c == nil || (c.Code != httpx.CodeDestinationBlocked && c.Code != httpx.CodeValidationFailed) {
			t.Errorf("%s: %v", u, err)
		}
	}
	// Dev refuses metadata even with the private-network override.
	if _, err := x.s.Create(bg, x.actor(), x.org, CreateInput{URL: "http://169.254.169.254/x", Events: []string{"deed.sealed"}}); err == nil {
		t.Error("dev accepted the metadata address")
	}
	if _, err := x.s.Create(bg, x.actor(), x.org, CreateInput{URL: "https://hooks.example.test/x", Events: []string{"nope.event"}}); httpx.AsError(err) == nil ||
		httpx.AsError(err).Param != "events" {
		t.Errorf("unknown event: %v", err)
	}
	// At most 16 endpoints.
	r := newReceiver(t)
	for i := range 16 {
		if _, err := x.s.Create(bg, x.actor(), x.org, CreateInput{URL: r.srv.URL, Events: []string{"deed.sealed"}}); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if _, err := x.s.Create(bg, x.actor(), x.org, CreateInput{URL: r.srv.URL, Events: []string{"deed.sealed"}}); httpx.AsError(err) == nil ||
		httpx.AsError(err).Code != httpx.CodeConflict {
		t.Errorf("the 17th: %v", err)
	}
}
