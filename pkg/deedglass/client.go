package deedglass

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector/egress"
)

// APIVersion is the Deedglass-Version this client sends.
const APIVersion = "2026-10-01"

// DefaultTimeout bounds every request when the caller's HTTP client has none.
const DefaultTimeout = 30 * time.Second

// Client calls the Deedglass API. The zero value isn't usable; set BaseURL and APIKey (or use
// NewClient). It is safe for concurrent use.
type Client struct {
	BaseURL   string       // https://api.deedglass.com, or http://127.0.0.1:8080
	APIKey    string       // dg_live_… or dg_test_…; never logged or included in errors
	HTTP      *http.Client // nil: a client with a 30 s timeout
	UserAgent string       // default "deedglass-go"
}

// NewClient returns a client for server with apiKey.
func NewClient(apiKey, server string) *Client { return &Client{BaseURL: server, APIKey: apiKey} }

// Error is the API's error envelope, plus the HTTP status and request id.
type Error struct {
	Status     int    `json:"-"`
	Type       string `json:"type"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Param      string `json:"param"`
	RequestID  string `json:"request_id"`
	DocURL     string `json:"doc_url"`
	RetryAfter string `json:"-"` // the Retry-After header, if any
	// Action is the recorded, sealed action on a POST /v1/actions refusal (S13): a policy deny, a
	// budget, a Stop or an unknown tool. Nil on every other error.
	Action json.RawMessage `json:"action,omitempty"`
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("deedglass: HTTP %d", e.Status)
	}
	return e.Code + ": " + e.Message
}

// Response describes a completed call (headers the caller may want).
type Response struct {
	Status   int
	Replayed bool   // Deedglass-Idempotent-Replayed: true
	ID       string // Deedglass-Request-Id
}

// Option changes one request.
type Option func(*http.Request)

// WithIdempotencyKey sets the Idempotency-Key of a POST (by default each POST gets a fresh one).
func WithIdempotencyKey(k string) Option {
	return func(r *http.Request) { r.Header.Set("Idempotency-Key", k) }
}

// Do sends method path (with an optional query) and decodes the JSON response into out. in, when
// not nil, is sent as the JSON body (a json.RawMessage as is). POSTs get an Idempotency-Key unless one is set. A non-2xx
// response is an *Error.
func (c *Client) Do(ctx context.Context, method, path string, in, out any, opts ...Option) (*Response, error) {
	if c == nil || c.BaseURL == "" {
		return nil, errors.New("deedglass: the client has no server URL")
	}
	base, err := url.Parse(strings.TrimSuffix(c.BaseURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, errors.New("deedglass: the server URL must be http(s)://host[:port]")
	}
	var body io.Reader
	switch v := in.(type) {
	case nil:
	case json.RawMessage: // already encoded: sent as is (the caller vouches it is valid JSON)
		body = bytes.NewReader(v)
	default:
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base.String()+path, body)
	if err != nil {
		return nil, err
	}
	h := req.Header
	h.Set("Accept", "application/json")
	h.Set("Deedglass-Version", APIVersion)
	ua := c.UserAgent
	if ua == "" {
		ua = "deedglass-go"
	}
	h.Set("User-Agent", ua)
	if in != nil {
		h.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		h.Set("Authorization", "Bearer "+c.APIKey)
	}
	if method == http.MethodPost {
		h.Set("Idempotency-Key", newIdempotencyKey())
	}
	for _, o := range opts {
		o(req)
	}
	hc := c.HTTP
	if hc == nil {
		hc = egress.FirstParty(egress.FirstPartyOptions{Timeout: DefaultTimeout, Proxy: true})
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, redactKey(err, c.APIKey)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	r := &Response{Status: resp.StatusCode, Replayed: resp.Header.Get("Deedglass-Idempotent-Replayed") == "true",
		ID: resp.Header.Get("Deedglass-Request-Id")}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var env struct {
			Error *Error `json:"error"`
		}
		e := &Error{}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil {
			e = env.Error
		}
		e.Status, e.RetryAfter = resp.StatusCode, resp.Header.Get("Retry-After")
		if e.RequestID == "" {
			e.RequestID = r.ID
		}
		return r, e
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return r, fmt.Errorf("deedglass: the response isn't the JSON this client expects: %w", err)
		}
	}
	return r, nil
}

// newIdempotencyKey is 128 random bits, hex.
func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// redactKey makes sure a transport error never carries the key.
func redactKey(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "dg_…"))
}
