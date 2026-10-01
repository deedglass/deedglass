package egress

import (
	"cmp"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/deedglass/deedglass/internal/buildinfo"
)

// Redirects is a client's redirect rule.
type Redirects string

const (
	// RedirectNone never follows a redirect: the 3xx answer is the response. Writes always use it.
	RedirectNone Redirects = "none"
	// RedirectSameHost follows at most MaxRedirects hops to the same scheme, host and port.
	RedirectSameHost Redirects = "same_host"
)

// MaxRedirects is the most hops RedirectSameHost follows.
const MaxRedirects = 3

// Timeouts of every egress client (S16 §3.2). The action deadline (the request's context) bounds
// the whole call on top of them.
const (
	TLSHandshakeTimeout   = 5 * time.Second
	ResponseHeaderTimeout = 10 * time.Second
)

// UserAgent is the User-Agent every egress client sends unless the caller sets one.
func UserAgent() string {
	return "deedglass/" + buildinfo.Get().Version + " (+https://docs.deedglass.com/connectors)"
}

// ClientOptions configures Client.
type ClientOptions struct {
	Timeout          time.Duration // the whole request, headers and body; 0 = only the context's deadline
	MaxResponseBytes int64         // the body cap after content decoding; 0 = no cap (callers should set one)
	Redirects        Redirects     // default none
	UserAgent        string        // default UserAgent()
	HeaderTimeout    time.Duration // time to the response headers; default ResponseHeaderTimeout (10 s)
}

// ErrResponseTooLarge is returned by a body read that passes MaxResponseBytes. It is never a
// silent truncation.
var ErrResponseTooLarge = errors.New("egress: the response is larger than the connector's max_response_bytes")

// RedirectError is a refused redirect.
type RedirectError struct{ Reason string }

func (e *RedirectError) Error() string { return "egress: redirect refused: " + e.Reason }

// Client returns an *http.Client whose every connection goes through p. Environment proxies are
// ignored (proxy support is v2), TLS is at least 1.2 and always verified, and the response body is
// capped after gzip decoding, which stops decompression bombs.
func (p *Policy) Client(o ClientOptions) *http.Client {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           p.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   TLSHandshakeTimeout,
		ResponseHeaderTimeout: cmp.Or(o.HeaderTimeout, ResponseHeaderTimeout),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return newClient(tr, o)
}

func newClient(tr http.RoundTripper, o ClientOptions) *http.Client {
	ua := o.UserAgent
	if ua == "" {
		ua = UserAgent()
	}
	c := &http.Client{Timeout: o.Timeout, Transport: &transport{next: tr, ua: ua, max: o.MaxResponseBytes}}
	switch o.Redirects {
	case RedirectSameHost:
		c.CheckRedirect = sameHost
	default:
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c
}

func sameHost(req *http.Request, via []*http.Request) error {
	if len(via) > MaxRedirects {
		return &RedirectError{Reason: fmt.Sprintf("more than %d redirects", MaxRedirects)}
	}
	first := via[0].URL
	switch {
	case first.Scheme == "https" && req.URL.Scheme != "https":
		return &RedirectError{Reason: "https to http"}
	case req.URL.Scheme != first.Scheme:
		return &RedirectError{Reason: "a different scheme"}
	case req.URL.Host != first.Host:
		return &RedirectError{Reason: "a different host"}
	}
	return nil
}

// transport sets the User-Agent and caps the body.
type transport struct {
	next http.RoundTripper
	ua   string
	max  int64
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("User-Agent") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("User-Agent", t.ua)
	}
	res, err := t.next.RoundTrip(r)
	if err != nil || t.max <= 0 {
		return res, err
	}
	res.Body = &capped{rc: res.Body, left: t.max}
	return res, nil
}

type capped struct {
	rc   io.ReadCloser
	left int64
}

func (c *capped) Read(b []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		if n, _ := c.rc.Read(one[:]); n > 0 {
			return 0, ErrResponseTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(b)) > c.left {
		b = b[:c.left]
	}
	n, err := c.rc.Read(b)
	c.left -= int64(n)
	return n, err
}

func (c *capped) Close() error { return c.rc.Close() }

// ReadBody reads at most max bytes; one byte more is ErrResponseTooLarge. It reads at most
// max + 1 bytes from r.
func ReadBody(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrResponseTooLarge
	}
	return b, nil
}

// FirstPartyOptions configures FirstParty.
type FirstPartyOptions struct {
	Timeout   time.Duration // the whole request; 0 = only the context's deadline
	Proxy     bool          // honour HTTPS_PROXY/HTTP_PROXY/NO_PROXY (a CLI behind a corporate proxy)
	MaxIdle   int           // MaxIdleConnsPerHost (default 2)
	Redirects Redirects     // default none
	UserAgent string        // default UserAgent()
}

// FirstParty is the client for destinations an operator or a person at a terminal named on
// purpose, never an agent: the Deedglass server the CLI, mcp serve and the SDK talk to, and the
// fixed Resend API. It has the same TLS and timeout floors but no address classification, because
// talking to Deedglass on localhost is the normal dev case. Anything an org configures uses
// Policy.Client instead.
func FirstParty(o FirstPartyOptions) *http.Client {
	tr := &http.Transport{
		DialContext:           (&netDialer).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   TLSHandshakeTimeout,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   max(o.MaxIdle, 2),
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if o.Proxy {
		tr.Proxy = http.ProxyFromEnvironment
	}
	return newClient(tr, ClientOptions{Timeout: o.Timeout, Redirects: o.Redirects, UserAgent: o.UserAgent})
}

// Wrap returns a client over a caller-supplied transport with the egress client's redirect rule,
// User-Agent and body cap. Tests use it to point a client at an httptest TLS server's transport;
// production code uses Client or FirstParty.
func Wrap(tr http.RoundTripper, o ClientOptions) *http.Client { return newClient(tr, o) }
