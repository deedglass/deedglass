package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Resend defaults and limits.
const (
	// DefaultResendBaseURL is Resend's API origin.
	DefaultResendBaseURL = "https://api.resend.com"
	// resendAttempts is the most requests one Send makes.
	resendAttempts = 3
	// resendTimeout is the default per-attempt client timeout.
	resendTimeout = 10 * time.Second
	// maxRetryAfter caps a 429's Retry-After.
	maxRetryAfter = 10 * time.Second
	// maxTag is Resend's limit on a tag name or value.
	maxTag = 256
	// maxResponse bounds how much of a Resend response is read.
	maxResponse = 64 << 10
	// userAgent identifies the mailer to Resend.
	userAgent = "deedglass-mailer"
)

// backoff is the wait after a failed attempt n (index n-1) that ended in a 5xx or a network error.
var backoff = [resendAttempts - 1]time.Duration{500 * time.Millisecond, time.Second}

// ResendOptions configures NewResend.
type ResendOptions struct {
	// APIKey is the Resend API key (re_…). It is sent only in the Authorization header and is
	// never logged or put in an error.
	APIKey string
	// From is the sender, for example "Deedglass <signin@deedglass.com>".
	From string
	// BaseURL is Resend's origin; empty means DefaultResendBaseURL. Tests point it at httptest.
	BaseURL string
	// Client sends the requests; nil means &http.Client{Timeout: 10 * time.Second}. Its Timeout
	// bounds each attempt.
	Client *http.Client
	// Logger receives "mail.sent" and "mail.failed"; nil means slog.Default().
	Logger *slog.Logger
	// Sleep waits between attempts; nil means a timer that returns ctx.Err() when ctx ends
	// first. Tests stub it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Resend sends mail through Resend's HTTP API (POST /emails).
type Resend struct {
	key, from, endpoint string
	client              *http.Client
	log                 *slog.Logger
	sleep               func(ctx context.Context, d time.Duration) error
}

// NewResend returns a Resend mailer. It returns an error when APIKey or From is empty or BaseURL
// is not an http(s) URL.
func NewResend(o ResendOptions) (*Resend, error) {
	if strings.TrimSpace(o.APIKey) == "" {
		return nil, errors.New("mailer: Resend needs an API key; set DEEDGLASS_RESEND_API_KEY")
	}
	if strings.TrimSpace(o.From) == "" {
		return nil, errors.New(`mailer: Resend needs a From address, for example "Deedglass <signin@deedglass.com>"`)
	}
	base := o.BaseURL
	if base == "" {
		base = DefaultResendBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("mailer: the Resend base URL must be an http or https origin; leave it empty for the default")
	}
	r := &Resend{
		key:      o.APIKey,
		from:     o.From,
		endpoint: strings.TrimRight(base, "/") + "/emails",
		client:   o.Client,
		log:      o.Logger,
		sleep:    o.Sleep,
	}
	if r.client == nil {
		r.client = &http.Client{Timeout: resendTimeout}
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	if r.sleep == nil {
		r.sleep = sleepCtx
	}
	return r, nil
}

// sleepCtx waits for d or until ctx ends, whichever is first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// resendTag is one entry of the request's "tags".
type resendTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// resendEmail is the POST /emails request body.
type resendEmail struct {
	From    string            `json:"from"`
	To      []string          `json:"to"`
	Subject string            `json:"subject"`
	HTML    string            `json:"html"`
	Text    string            `json:"text"`
	Tags    []resendTag       `json:"tags"`
	Headers map[string]string `json:"headers"`
}

// attempt is the outcome of one request.
type attempt struct {
	status     int           // 0 for a network error
	id         string        // Resend's email id on success
	name       string        // Resend's error name, sanitized
	retryAfter time.Duration // from a 429
	err        error         // network error
}

// Send implements Mailer. It makes at most 3 attempts: a 429 waits for Retry-After (capped at
// 10 s; missing or unreadable means 1 s), a 5xx or network error waits 0.5 s then 1 s, and any
// other 4xx returns an error wrapping ErrRejected without retrying.
func (r *Resend) Send(ctx context.Context, m Message) error {
	if err := m.validate(); err != nil {
		return err
	}
	ref, err := entityRef()
	if err != nil {
		return err
	}
	body, err := json.Marshal(resendEmail{
		From:    r.from,
		To:      []string{m.To},
		Subject: m.Subject,
		HTML:    m.HTML,
		Text:    m.Text,
		Tags:    tags(m),
		Headers: map[string]string{"X-Entity-Ref-ID": ref},
	})
	if err != nil {
		return fmt.Errorf("mailer: encode the Resend request: %w", err)
	}

	var last attempt
	for n := 1; n <= resendAttempts; n++ {
		last = r.do(ctx, body, m.IdempotencyKey)
		switch {
		case last.err == nil && last.status >= 200 && last.status < 300:
			r.log.LogAttrs(ctx, slog.LevelInfo, "mail.sent",
				slog.String("kind", m.Kind), slog.String("user_id", m.UserID), slog.String("request_id", m.RequestID),
				slog.Int("status", last.status), slog.String("resend_id", last.id), slog.Int("attempts", n))
			return nil
		case last.err != nil && ctx.Err() != nil:
			r.failed(ctx, m, last, n, "canceled")
			return fmt.Errorf("mailer: the Resend send stopped: %w", ctx.Err())
		case last.err == nil && last.status != http.StatusTooManyRequests && last.status < 500:
			r.failed(ctx, m, last, n, "rejected")
			if last.name != "" {
				return fmt.Errorf("%w: status %d, %s", ErrRejected, last.status, last.name)
			}
			return fmt.Errorf("%w: status %d", ErrRejected, last.status)
		}
		if n == resendAttempts {
			break
		}
		wait := backoff[n-1]
		if last.status == http.StatusTooManyRequests {
			wait = last.retryAfter
		}
		if err := r.sleep(ctx, wait); err != nil {
			r.failed(ctx, m, last, n, "canceled")
			return fmt.Errorf("mailer: the Resend send stopped: %w", err)
		}
	}
	r.failed(ctx, m, last, resendAttempts, "unavailable")
	if last.err != nil {
		return fmt.Errorf("mailer: Resend unavailable after %d attempts: %w", resendAttempts, last.err)
	}
	return fmt.Errorf("mailer: Resend unavailable after %d attempts: status %d", resendAttempts, last.status)
}

// failed logs "mail.failed" with ids and the status only.
func (r *Resend) failed(ctx context.Context, m Message, a attempt, n int, reason string) {
	attrs := []slog.Attr{
		slog.String("kind", m.Kind), slog.String("user_id", m.UserID), slog.String("request_id", m.RequestID),
		slog.Int("status", a.status), slog.Int("attempts", n), slog.String("reason", reason),
	}
	if a.name != "" {
		attrs = append(attrs, slog.String("resend_error", a.name))
	}
	r.log.LogAttrs(ctx, slog.LevelWarn, "mail.failed", attrs...)
}

// do makes one request.
func (r *Resend) do(ctx context.Context, body []byte, idem string) attempt {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return attempt{err: errors.New("mailer: build the Resend request")}
	}
	req.Header.Set("Authorization", "Bearer "+r.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return attempt{err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	a := attempt{status: resp.StatusCode}
	var parsed struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &parsed)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		a.id = sanitizeTag(parsed.ID, 64)
	} else {
		a.name = sanitizeTag(parsed.Name, 64)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		a.retryAfter = retryAfter(resp.Header.Get("Retry-After"))
	}
	return a
}

// retryAfter reads a Retry-After value in seconds, capped at 10 s; missing or unreadable is 1 s.
func retryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return time.Second
	}
	if n > int(maxRetryAfter/time.Second) {
		return maxRetryAfter
	}
	return time.Duration(n) * time.Second
}

// tags builds the request's tags: "kind" from m.Kind, then m.Tags sorted by name.
func tags(m Message) []resendTag {
	out := []resendTag{{Name: "kind", Value: sanitizeTag(m.Kind, maxTag)}}
	names := make([]string, 0, len(m.Tags))
	for k := range m.Tags {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		name, value := sanitizeTag(k, maxTag), sanitizeTag(m.Tags[k], maxTag)
		if name == "" || value == "" || name == "kind" {
			continue
		}
		out = append(out, resendTag{Name: name, Value: value})
	}
	return out
}

// sanitizeTag replaces every byte outside [A-Za-z0-9_-] with '_' and truncates to max bytes.
func sanitizeTag(s string, max int) string {
	b := make([]byte, 0, min(len(s), max))
	for i := 0; i < len(s) && len(b) < max; i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	return string(b)
}

// entityRef returns a fresh 16-byte hex id for X-Entity-Ref-ID, so that mail clients never
// thread two sign-in emails together.
func entityRef() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mailer: read random bytes: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
