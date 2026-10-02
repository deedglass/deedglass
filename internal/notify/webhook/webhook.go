// Package webhook delivers Deedglass events to customer endpoints (S22 §3.9): endpoints with a
// vault-held signing secret, the outbox the Ledger fills in the same transaction as every event,
// a delivery worker that signs each POST (pkg/deedglass/webhook), retries on a fixed schedule and
// disables an endpoint that has failed for 72 hours. Payloads are thin: ids and states, never
// arguments, results, context or email addresses.
package webhook

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

// Config is the webhooks.* section of deedglass.yaml, with defaults for zero values.
type Config struct {
	Timeout      time.Duration // 10s: a 2xx within this is a success
	MaxAttempts  int           // 8: retries after the first attempt (the schedule below), then dead
	DisableAfter time.Duration // 72h: an endpoint failing this long is disabled
	MaxEndpoints int           // 16 per org
}

func (c Config) withDefaults() Config {
	if c.Timeout <= 0 || c.Timeout > 30*time.Second {
		c.Timeout = 10 * time.Second
	}
	if c.MaxAttempts <= 0 || c.MaxAttempts > len(Schedule) {
		c.MaxAttempts = len(Schedule)
	}
	if c.DisableAfter <= 0 {
		c.DisableAfter = 72 * time.Hour
	}
	if c.MaxEndpoints <= 0 {
		c.MaxEndpoints = 16
	}
	return c
}

// Schedule is when a failed delivery is retried, after each failed attempt (±20% jitter).
var Schedule = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour,
	3 * time.Hour, 6 * time.Hour, 12 * time.Hour}

// Delivery limits.
const (
	MaxResponseRead = 64 << 10 // response bodies are read up to this, then discarded
	UserAgent       = "Deedglass-Webhooks/1.0 (+https://docs.deedglass.com/webhooks)"
	secretPrefix    = "whsec_"
	inFlightLease   = 2 * time.Minute
)

// Options are the Service's dependencies.
type Options struct {
	DB     *store.DB
	Ledger *ledger.Ledger
	Egress *egress.Policy    // the only door out (SSRF-safe); required to deliver
	KEK    vault.KEKProvider // the vault's key ring; nil: webhooks can't be created or delivered
	Config Config
	Logger *slog.Logger
	Clock  func() time.Time
}

// Service is webhooks.
type Service struct {
	o   Options
	cfg Config
	log *slog.Logger
	now func() time.Time
}

// New returns a Service.
func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return &Service{o: o, cfg: o.Config.withDefaults(), log: o.Logger, now: o.Clock}
}

// Webhook is the webhook object. Secret appears only in the create response.
type Webhook struct {
	Object         string       `json:"object"` // "webhook"
	ID             string       `json:"id"`
	URL            string       `json:"url"`
	Events         []string     `json:"events"`
	Description    *string      `json:"description"`
	Status         string       `json:"status"` // active | disabled
	DisabledReason *string      `json:"disabled_reason"`
	APIVersion     string       `json:"api_version"`
	SecretHint     *string      `json:"secret_hint"`
	Secret         *string      `json:"secret,omitempty"`
	FailingSince   *ledger.Time `json:"failing_since"`
	LastSuccessAt  *ledger.Time `json:"last_success_at"`
	CreatedAt      ledger.Time  `json:"created_at"`
	Deed           *string      `json:"deed,omitempty"`
}

// CreateInput is POST /v1/webhooks.
type CreateInput struct {
	URL         string   `json:"url"`
	Events      []string `json:"events"`
	Description string   `json:"description"`
}

// Actor is who changes webhooks: a person or an org admin key.
type Actor struct {
	Ref deed.Ref
	By  string // usr_… or key_…, for created_by
}

// ActorOf is the actor behind a request's principal.
func ActorOf(p httpx.Principal) Actor {
	if p.Kind == httpx.KindAdminKey {
		return Actor{Ref: deed.Ref{Type: "api_key", ID: p.KeyID.String()}, By: p.KeyID.String()}
	}
	return Actor{Ref: deed.Ref{Type: "user", ID: p.UserID.String()}, By: p.UserID.String()}
}

// Messages (validation).
const (
	msgEvents      = "events must list 1 to 32 event types, for example approval.decided or deed.sealed. GET the docs at docs.deedglass.com/webhooks for the list."
	msgDescription = "description can be at most 200 characters."
	msgVault       = "Webhooks keep their signing secret in the vault, and DEEDGLASS_VAULT_KEY isn't set. An operator sets it, then restarts."
)

func msgEventUnknown(e string) string {
	return fmt.Sprintf("%q isn't an event Deedglass sends. Use one of the types in docs.deedglass.com/webhooks, for example approval.decided.", clip(e, 64))
}

func msgTooMany(n int) string {
	return fmt.Sprintf("This org already has %d webhook endpoints, the limit. Delete one first.", n)
}

// Create checks the URL through the egress door (https in prod; a destination it refuses is
// refused here, and again at every delivery), mints the signing secret, seals it in the vault and
// seals the webhook.created admin deed. The secret is returned once.
func (s *Service) Create(ctx context.Context, a Actor, org ids.OrgID, in CreateInput) (*Webhook, error) {
	if s.o.KEK == nil {
		return nil, httpx.E(httpx.CodeVaultUnavailable, httpx.Msg(msgVault))
	}
	events, err := checkEvents(in.Events)
	if err != nil {
		return nil, err
	}
	desc := strings.TrimSpace(in.Description)
	if len([]rune(desc)) > 200 {
		return nil, httpx.ErrValidation("description", msgDescription)
	}
	u, err := s.checkURL(ctx, in.URL)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	secret := secretPrefix + base64.RawURLEncoding.EncodeToString(raw)
	id := ids.New[ids.WebhookKind]()
	env, kek, err := s.seal(ctx, org, id, secret)
	if err != nil {
		return nil, err
	}
	hint := secret[len(secret)-4:]
	var out *Webhook
	err = s.o.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*)::integer FROM deedglass.webhooks WHERE org_id = $1`, org).Scan(&n); err != nil {
			return err
		}
		if n >= s.cfg.MaxEndpoints {
			return httpx.E(httpx.CodeConflict, httpx.Msg(msgTooMany(n)))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deedglass.webhooks (id, org_id, url, description, events, secret_envelope, secret_hint, kek_id, created_by)
		    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, id, org, u, nonEmpty(desc), events, env, hint, kek, a.By); err != nil {
			return err
		}
		d, err := s.o.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminWebhookCreated, Actor: a.Ref,
			Target: &deed.Ref{Type: "webhook", ID: id.String()}, Data: map[string]any{"url_host": hostOf(u), "events": events},
			RequestID: httpx.RequestID(ctx)})
		if err != nil {
			return err
		}
		w, err := readWebhook(ctx, tx, org, id)
		if err != nil {
			return err
		}
		w.Secret, w.Deed = &secret, &d.ID
		out = w
		return nil
	})
	return out, err
}

func checkEvents(in []string) ([]string, error) {
	if len(in) == 0 || len(in) > 32 {
		return nil, httpx.ErrValidation("events", msgEvents)
	}
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.TrimSpace(e)
		if !slices.Contains(ledger.WebhookEvents, e) {
			return nil, httpx.ErrValidation("events", msgEventUnknown(e))
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	slices.Sort(out)
	return out, nil
}

// checkURL is the egress door's URL check with DNS: the destination must be allowed now.
func (s *Service) checkURL(ctx context.Context, raw string) (string, error) {
	if s.o.Egress == nil {
		return "", errors.New("webhook: no egress policy")
	}
	c, err := s.o.Egress.CheckURL(ctx, strings.TrimSpace(raw))
	if err != nil {
		if be, ok := egress.IsBlocked(err); ok {
			return "", httpx.E(httpx.CodeDestinationBlocked, httpx.Msg(be.Reason), httpx.Param("url"))
		}
		return "", httpx.ErrValidation("url", "Deedglass couldn't resolve the URL's host. Check the URL, then retry.")
	}
	return c.URL.String(), nil
}

// envelope is the stored form of a sealed secret (webhooks.secret_envelope).
type envelope struct {
	V          int    `json:"v"`
	KEK        string `json:"kek"`
	WrappedDEK []byte `json:"dek"`
	Ciphertext []byte `json:"ct"`
}

func (s *Service) seal(ctx context.Context, org ids.OrgID, id ids.WebhookID, secret string) ([]byte, string, error) {
	sl, err := vault.Seal(ctx, s.o.KEK, nil, vault.Ref{Org: org, Webhook: id}, 1, vault.KindFields, map[string][]byte{"secret": []byte(secret)})
	if err != nil {
		return nil, "", err
	}
	b, err := json.Marshal(envelope{V: 1, KEK: sl.KEKID, WrappedDEK: sl.WrappedDEK, Ciphertext: sl.Ciphertext})
	return b, sl.KEKID, err
}

// openSecret is the dispatcher's one vault.Open call site (TestOpenerCallSites allows it).
func (s *Service) openSecret(ctx context.Context, org ids.OrgID, id ids.WebhookID, b []byte) (string, error) {
	if s.o.KEK == nil {
		return "", errors.New("webhook: no vault key ring")
	}
	var e envelope
	if err := json.Unmarshal(b, &e); err != nil || e.V != 1 {
		return "", errors.New("webhook: the stored secret can't be read")
	}
	sec, err := vault.Open(ctx, s.o.KEK, vault.Ref{Org: org, Webhook: id},
		vault.Sealed{Version: 1, Kind: vault.KindFields, KEKID: e.KEK, WrappedDEK: e.WrappedDEK, Ciphertext: e.Ciphertext})
	if err != nil {
		return "", err
	}
	defer sec.Destroy()
	v := sec.Field("secret")
	if v == "" {
		return "", errors.New("webhook: the stored secret has no value")
	}
	return v, nil
}

const webhookCols = `id, url, events, description, status, disabled_reason, api_version, secret_hint, failing_since, last_success_at, created_at`

func scanWebhook(r pgx.Row) (*Webhook, error) {
	var (
		w                   Webhook
		created             time.Time
		failing, lastSucess *time.Time
	)
	if err := r.Scan(&w.ID, &w.URL, &w.Events, &w.Description, &w.Status, &w.DisabledReason, &w.APIVersion, &w.SecretHint,
		&failing, &lastSucess, &created); err != nil {
		return nil, err
	}
	w.Object, w.CreatedAt = "webhook", ledger.Time(created)
	if failing != nil {
		t := ledger.Time(*failing)
		w.FailingSince = &t
	}
	if lastSucess != nil {
		t := ledger.Time(*lastSucess)
		w.LastSuccessAt = &t
	}
	return &w, nil
}

func readWebhook(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.WebhookID) (*Webhook, error) {
	w, err := scanWebhook(tx.QueryRow(ctx, `SELECT `+webhookCols+` FROM deedglass.webhooks WHERE org_id = $1 AND id = $2`, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound(id.String())
	}
	return w, err
}

func notFound(id string) *httpx.Error {
	return httpx.E(httpx.CodeNotFound, httpx.Msg("There's no webhook with id "+clip(id, 40)+". List them with GET /v1/webhooks."))
}

func parseID(id string) (ids.WebhookID, error) {
	w, err := ids.Parse[ids.WebhookKind](id)
	if err != nil {
		return w, notFound(id)
	}
	return w, nil
}

// List is every endpoint of the org (at most MaxEndpoints, so one page).
func (s *Service) List(ctx context.Context, org ids.OrgID) ([]*Webhook, error) {
	out := []*Webhook{}
	err := s.o.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+webhookCols+` FROM deedglass.webhooks WHERE org_id = $1 ORDER BY created_at, id`, org)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			w, err := scanWebhook(rows)
			if err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

// Get is one endpoint.
func (s *Service) Get(ctx context.Context, org ids.OrgID, id string) (*Webhook, error) {
	wid, err := parseID(id)
	if err != nil {
		return nil, err
	}
	var out *Webhook
	err = s.o.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		out, err = readWebhook(ctx, tx, org, wid)
		return err
	})
	return out, err
}

// Deleted is DELETE /v1/webhooks/{id}'s answer.
type Deleted struct {
	Object  string `json:"object"` // "webhook"
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
	Deed    string `json:"deed"`
}

// Delete removes an endpoint and its pending deliveries, and seals webhook.deleted.
func (s *Service) Delete(ctx context.Context, a Actor, org ids.OrgID, id string) (*Deleted, error) {
	wid, err := parseID(id)
	if err != nil {
		return nil, err
	}
	var out *Deleted
	err = s.o.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		w, err := readWebhook(ctx, tx, org, wid)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM deedglass.webhook_deliveries WHERE org_id = $1 AND webhook_id = $2`, org, wid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM deedglass.webhooks WHERE org_id = $1 AND id = $2`, org, wid); err != nil {
			return err
		}
		d, err := s.o.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminWebhookDeleted, Actor: a.Ref,
			Target: &deed.Ref{Type: "webhook", ID: wid.String()}, Data: map[string]any{"url_host": hostOf(w.URL), "events": w.Events},
			RequestID: httpx.RequestID(ctx)})
		if err != nil {
			return err
		}
		out = &Deleted{Object: "webhook", ID: wid.String(), Deleted: true, Deed: d.ID}
		return nil
	})
	return out, err
}

// DeliveryView is one delivery as GET /v1/webhooks/{id}/deliveries shows it (never the payload's
// secret; the payload itself is thin by design).
type DeliveryView struct {
	Object         string       `json:"object"` // "webhook_delivery"
	ID             string       `json:"id"`
	Event          string       `json:"event"`
	Type           string       `json:"type"`
	Status         string       `json:"status"` // pending | succeeded | failed | dead
	Attempt        int          `json:"attempt"`
	ResponseStatus *int         `json:"response_status"`
	ResponseMS     *int         `json:"response_ms"`
	LastError      *string      `json:"last_error"`
	NextAttemptAt  *ledger.Time `json:"next_attempt_at"`
	DeliveredAt    *ledger.Time `json:"delivered_at"`
	CreatedAt      ledger.Time  `json:"created_at"`
}

// Deliveries is an endpoint's last deliveries, newest first (at most 100).
func (s *Service) Deliveries(ctx context.Context, org ids.OrgID, id string, limit int) ([]DeliveryView, error) {
	wid, err := parseID(id)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	out := []DeliveryView{}
	err = s.o.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := readWebhook(ctx, tx, org, wid); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, event_id, event_type, status, attempt, response_status, response_ms, last_error,
		    next_attempt_at, delivered_at, created_at FROM deedglass.webhook_deliveries
		    WHERE org_id = $1 AND webhook_id = $2 ORDER BY created_at DESC, id DESC LIMIT $3`, org, wid, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				d                  DeliveryView
				next, done, create *time.Time
			)
			if err := rows.Scan(&d.ID, &d.Event, &d.Type, &d.Status, &d.Attempt, &d.ResponseStatus, &d.ResponseMS, &d.LastError,
				&next, &done, &create); err != nil {
				return err
			}
			d.Object = "webhook_delivery"
			if next != nil && d.Status == "pending" {
				t := ledger.Time(*next)
				d.NextAttemptAt = &t
			}
			if done != nil {
				t := ledger.Time(*done)
				d.DeliveredAt = &t
			}
			if create != nil {
				d.CreatedAt = ledger.Time(*create)
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// hostOf is the URL's host for deeds and logs (never the path or query, which may hold a token).
func hostOf(raw string) string {
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}
