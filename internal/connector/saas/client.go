// Package saas is the shared HTTP plumbing of the SaaS connector types (S29: github and slack; S30:
// stripe, gmail and notion). Every request goes through an egress client (TestSingleEgressDoor), the
// path is built only from escaped segments, the credential is added last, bodies are capped, and a
// call is retried at most once and only when the caller says it is idempotent.
package saas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
)

// DefaultMaxBody is the response cap when Client.MaxBody is zero.
const DefaultMaxBody = 4 << 20

// MaxRetryWait is the longest Retry-After an idempotent call waits before its one retry.
const MaxRetryWait = 5 * time.Second

// Client is one service's HTTP client. Base is fixed per service (https only outside dev); HTTP
// always comes from egress.Policy.Client.
type Client struct {
	Base    *url.URL
	HTTP    *http.Client
	Headers http.Header // Accept, version headers; the egress client adds User-Agent
	// Auth adds the credential to r. It runs last, after every other header, so nothing a caller
	// sets can replace it. A nil Auth sends no credential.
	Auth func(ctx context.Context, r *http.Request) error
	// Classify maps a non-2xx answer (or a 2xx with an error body, Slack's ok:false) to a typed
	// error. Nil: connector.FromStatus for statuses ≥ 400.
	Classify func(*Response) *connector.Error
	// IdempotencyHeader is the service's header for Request.IdempotencyKey ("Idempotency-Key").
	IdempotencyHeader string
	MaxBody           int64 // default DefaultMaxBody
	// RetryIf, when set, decides whether an Idempotent request's answer is retried (S30: Stripe's
	// Stripe-Should-Retry header and its 409 idempotency_key_in_use), and after how long. It
	// replaces the default 502/503/504 and 429 rule for answers; a connect error is still retried
	// once. It runs only for Idempotent requests, so a call without an idempotency guarantee is
	// never sent twice.
	RetryIf func(*Response) (retry bool, after time.Duration)
	// Sleep waits between attempts (tests replace it); nil waits on a timer or ctx.
	Sleep func(ctx context.Context, d time.Duration) bool
}

// Request is one call.
type Request struct {
	Method string
	Path   string // built only by Segments; joined under Base's path
	Query  url.Values
	JSON   any        // an application/json body, or
	Form   url.Values // an application/x-www-form-urlencoded body (Slack, Stripe)
	// Idempotent allows ONE retry on a connect error, 502/503/504, or a 429 whose Retry-After is
	// at most MaxRetryWait. Writes leave it false: a non-idempotent call is never sent twice.
	Idempotent     bool
	IdempotencyKey string
	Header         http.Header // per-call headers (never auth)
}

// Response is an answer. Body is at most MaxBody bytes; a larger body is an error, never a
// silent truncation.
type Response struct {
	Status   int
	Header   http.Header
	Body     []byte
	Attempts int
	Latency  time.Duration
}

// JSON decodes the body into v (numbers as json.Number when v is an any).
func (r *Response) JSON(v any) error {
	dec := json.NewDecoder(bytes.NewReader(r.Body))
	dec.UseNumber()
	return dec.Decode(v)
}

// RetryAfter reads Retry-After in seconds (an HTTP date is ignored), capped at max.
func (r *Response) RetryAfter(max time.Duration) time.Duration {
	if r == nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(r.Header.Get("Retry-After")))
	if err != nil || n < 0 {
		return 0
	}
	d := time.Duration(n) * time.Second
	if max > 0 && d > max {
		d = max
	}
	return d
}

// SentError is a failure after the request may have reached the service (a timeout or a broken
// connection while waiting for the answer): the call may have run. Callers that create things
// reconcile instead of retrying.
type SentError struct{ Err error }

func (e *SentError) Error() string {
	return "saas: the request was sent but no answer arrived: " + e.Err.Error()
}
func (e *SentError) Unwrap() error { return e.Err }

// Do sends r and returns the answer. A non-2xx answer (or what Classify calls an error) returns
// the Response and a *connector.Error. A request that never left (a refused destination, a
// connect error) is a *connector.Error with a kind that says nothing was sent; one that left but
// got no answer is a *connector.Error of KindTimeout or KindUpstream wrapping *SentError.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	if c.HTTP == nil || c.Base == nil {
		return nil, errors.New("saas: the client has no HTTP client or base URL")
	}
	attempts := 1
	if r.Idempotent {
		attempts = 2
	}
	var (
		resp *Response
		err  error
	)
	for i := 1; i <= attempts; i++ {
		var retry bool
		var wait time.Duration
		resp, err, retry, wait = c.once(ctx, r)
		if resp != nil {
			resp.Attempts = i
		}
		if !retry || i == attempts {
			break
		}
		if !c.sleep(ctx, wait) {
			break
		}
	}
	return resp, err
}

func (c *Client) sleep(ctx context.Context, d time.Duration) bool {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d {
		return false
	}
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// once sends one attempt. retry says whether an idempotent call may try again, after wait.
func (c *Client) once(ctx context.Context, r Request) (*Response, error, bool, time.Duration) {
	req, err := c.build(ctx, r)
	if err != nil {
		return nil, err, false, 0
	}
	start := time.Now()
	hr, err := c.HTTP.Do(req)
	if err != nil {
		return nil, classifyTransport(ctx, err), connectFailure(err), 200 * time.Millisecond
	}
	defer hr.Body.Close()
	max := c.MaxBody
	if max <= 0 {
		max = DefaultMaxBody
	}
	body, err := egress.ReadBody(hr.Body, max)
	resp := &Response{Status: hr.StatusCode, Header: hr.Header, Body: body, Latency: time.Since(start)}
	if err != nil {
		if errors.Is(err, egress.ErrResponseTooLarge) {
			return resp, connector.ErrResponseTooLarge(max), false, 0
		}
		return resp, sent(ctx, err), false, 0
	}
	var ce *connector.Error
	if c.Classify != nil {
		ce = c.Classify(resp)
	} else if resp.Status >= 400 {
		ce = connector.FromStatus(resp.Status, resp.RetryAfter(time.Hour))
	}
	if ce == nil {
		return resp, nil, false, 0
	}
	if ce.HTTPStatus == 0 {
		ce.HTTPStatus = resp.Status
	}
	if c.RetryIf != nil {
		retry, after := c.RetryIf(resp)
		if after > MaxRetryWait {
			after = MaxRetryWait
		}
		return resp, ce, retry, after
	}
	switch {
	case resp.Status == 502 || resp.Status == 503 || resp.Status == 504:
		return resp, ce, true, 200 * time.Millisecond
	case ce.Kind == connector.KindRateLimited && ce.RetryAfter <= MaxRetryWait:
		return resp, ce, true, ce.RetryAfter
	}
	return resp, ce, false, 0
}

func (c *Client) build(ctx context.Context, r Request) (*http.Request, error) {
	if r.Path != "" && !strings.HasPrefix(r.Path, "/") {
		return nil, errors.New("saas: a path must come from Segments")
	}
	u := *c.Base
	u.Path = strings.TrimSuffix(c.Base.Path, "/") + r.Path
	u.RawPath = strings.TrimSuffix(c.Base.EscapedPath(), "/") + escapedPath(r.Path)
	if len(r.Query) > 0 {
		u.RawQuery = r.Query.Encode()
	} else {
		u.RawQuery = ""
	}
	u.Fragment = ""
	var (
		body  io.Reader
		ctype string
	)
	switch {
	case r.JSON != nil && r.Form != nil:
		return nil, errors.New("saas: a request has one body")
	case r.JSON != nil:
		b, err := json.Marshal(r.JSON)
		if err != nil {
			return nil, connector.ErrValidation("args", "can't be sent as JSON")
		}
		body, ctype = bytes.NewReader(b), "application/json"
	case r.Form != nil:
		body, ctype = strings.NewReader(r.Form.Encode()), "application/x-www-form-urlencoded"
	}
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("saas: %w", err)
	}
	for k, vs := range c.Headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	for k, vs := range r.Header {
		if strings.EqualFold(k, "Authorization") {
			continue // auth comes only from Auth
		}
		req.Header.Del(k)
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if r.IdempotencyKey != "" && c.IdempotencyHeader != "" {
		req.Header.Set(c.IdempotencyHeader, r.IdempotencyKey)
	}
	req.Header.Del("Authorization")
	if c.Auth != nil {
		if err := c.Auth(ctx, req); err != nil {
			return nil, err
		}
	}
	return req, nil
}

// escapedPath re-escapes a path built by Segments (each segment was escaped, so its "%2F" must
// stay "%2F" on the wire).
func escapedPath(p string) string {
	if p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// connectFailure reports whether err happened before anything was sent (a dial error).
func connectFailure(err error) bool {
	s := err.Error()
	return strings.Contains(s, "connect:") || strings.Contains(s, "dial ") || strings.Contains(s, "connection refused") ||
		strings.Contains(s, "no such host")
}

// classifyTransport maps a transport error: a blocked destination and a connect failure sent
// nothing; anything else may have reached the service.
func classifyTransport(ctx context.Context, err error) error {
	if be, ok := egress.IsBlocked(err); ok {
		return connector.ErrDestinationBlocked(be)
	}
	var re *egress.RedirectError
	if errors.As(err, &re) {
		return &connector.Error{Kind: connector.KindDestinationBlocked, Code: connector.CodeDestinationBlocked,
			AgentMessage: "The service answered with a redirect Deedglass doesn't follow, so nothing more was sent.", AdminDetail: re.Error(), Err: err}
	}
	if connectFailure(err) {
		return connector.ErrUnreachable(err)
	}
	return sent(ctx, err)
}

func sent(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return &connector.Error{Kind: connector.KindTimeout, Code: connector.CodeTimeout,
			AgentMessage: "The service did not answer in time. The call may have run.", Err: &SentError{Err: err}}
	}
	return &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError,
		AgentMessage: "The connection to the service broke before it answered. The call may have run.", Err: &SentError{Err: err}}
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// IsSent reports whether err is a failure after the request may have reached the service.
func IsSent(err error) bool {
	var se *SentError
	return errors.As(err, &se)
}
