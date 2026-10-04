package saas_test

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/vault"
)

var update = flag.Bool("update", false, "rewrite the goldens")

func devClient(t *testing.T, srv *saastest.Server) *saas.Client {
	t.Helper()
	pol := &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}
	u, _ := url.Parse(srv.URL + "/api")
	return &saas.Client{Base: u, HTTP: pol.Client(egress.ClientOptions{Timeout: 5 * time.Second, MaxResponseBytes: 1 << 20}),
		Headers: http.Header{"Accept": {"application/json"}},
		Sleep:   func(context.Context, time.Duration) bool { return true }}
}

func TestDoSendsAuthLastAndEscapesSegments(t *testing.T) {
	srv := saastest.New(t, saastest.Exchange{Name: "get", Method: "GET", Path: "/api/repos/a b/c%2Fd",
		RequireHeaders: map[string]string{"Authorization": "Bearer tok", "Accept": "application/json", "Idem": "k1"},
		Query:          map[string]string{"x": "1"}, Status: 200, Body: json.RawMessage(`{"ok":true}`)})
	c := devClient(t, srv)
	c.IdempotencyHeader = "Idem"
	c.Auth = func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer tok")
		return nil
	}
	p, err := saas.Segments("repos", "a b", "c%2Fd")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(context.Background(), saas.Request{Method: "GET", Path: p, Query: url.Values{"x": {"1"}}, IdempotencyKey: "k1",
		Header: http.Header{"Authorization": {"Bearer smuggled"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || string(resp.Body) != `{"ok":true}` || resp.Attempts != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	if got := srv.Requests()[0].Header.Get("User-Agent"); !strings.HasPrefix(got, "deedglass/") {
		t.Fatalf("user agent %q", got)
	}
}

func TestSegmentsRefusals(t *testing.T) {
	for _, bad := range [][]string{{""}, {"."}, {".."}, {"a/b"}, {"a\x00"}, {strings.Repeat("x", 1025)}, {"\xff"}} {
		if _, err := saas.Segments(bad...); !errors.Is(err, saas.ErrSegment) {
			t.Errorf("Segments(%q) err = %v", bad, err)
		}
	}
	if p, err := saas.Segments("repos", "o", "r", strings.Repeat("x", 1024)); err != nil || !strings.HasPrefix(p, "/repos/o/r/") {
		t.Fatalf("p=%q err=%v", p, err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustSegments didn't panic")
		}
	}()
	saas.MustSegments("..")
}

func TestRetryOnlyWhenIdempotent(t *testing.T) {
	cases := []struct {
		name       string
		idempotent bool
		first      saastest.Exchange
		calls      int
		wantErr    string
	}{
		{"503 read retried", true, saastest.Exchange{Status: 503}, 2, ""},
		{"503 write not retried", false, saastest.Exchange{Status: 503}, 1, connector.CodeUpstreamError},
		{"429 short wait retried", true, saastest.Exchange{Status: 429, Headers: map[string]string{"Retry-After": "3"}}, 2, ""},
		{"429 long wait not retried", true, saastest.Exchange{Status: 429, Headers: map[string]string{"Retry-After": "30"}}, 1, connector.CodeUpstreamRateLimited},
		{"404 not retried", true, saastest.Exchange{Status: 404}, 1, connector.CodeUpstreamNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := tc.first
			first.Name, first.Method, first.Path = "first", "GET", "/api/x"
			second := saastest.Exchange{Name: "second", Method: "GET", Path: "/api/x", Status: 200, Body: json.RawMessage(`{}`), Optional: true}
			srv := saastest.New(t, first, second)
			c := devClient(t, srv)
			var waited time.Duration
			c.Sleep = func(_ context.Context, d time.Duration) bool { waited = d; return true }
			resp, err := c.Do(context.Background(), saas.Request{Path: "/x", Idempotent: tc.idempotent})
			if n := len(srv.Requests()); n != tc.calls {
				t.Fatalf("calls = %d, want %d", n, tc.calls)
			}
			if tc.wantErr == "" {
				if err != nil || resp.Attempts != 2 {
					t.Fatalf("err=%v resp=%+v", err, resp)
				}
				if first.Status == 429 && waited != 3*time.Second {
					t.Fatalf("waited %v", waited)
				}
				return
			}
			ce := connector.AsError(err)
			if ce == nil || ce.Code != tc.wantErr {
				t.Fatalf("err = %v, want %s", err, tc.wantErr)
			}
		})
	}
}

func TestClassifierAndBodyCap(t *testing.T) {
	srv := saastest.New(t,
		saastest.Exchange{Name: "okfalse", Method: "POST", Path: "/api/chat.postMessage", Status: 200, Body: json.RawMessage(`{"ok":false,"error":"not_in_channel"}`)},
		saastest.Exchange{Name: "big", Method: "GET", Path: "/api/big", Status: 200, Body: json.RawMessage(`"` + strings.Repeat("a", 2048) + `"`)})
	c := devClient(t, srv)
	c.MaxBody = 1024
	c.Classify = func(r *saas.Response) *connector.Error {
		var b struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if r.JSON(&b) == nil && !b.OK {
			return &connector.Error{Kind: connector.KindToolError, Code: "upstream_error", AgentMessage: b.Error}
		}
		return nil
	}
	_, err := c.Do(context.Background(), saas.Request{Method: "POST", Path: "/chat.postMessage", Form: url.Values{"channel": {"C1"}}})
	if ce := connector.AsError(err); ce == nil || ce.AgentMessage != "not_in_channel" || ce.HTTPStatus != 200 {
		t.Fatalf("err = %v", err)
	}
	_, err = c.Do(context.Background(), saas.Request{Path: "/big"})
	if ce := connector.AsError(err); ce == nil || ce.Code != connector.CodeResponseTooLarge {
		t.Fatalf("err = %v", err)
	}
	if got := srv.Requests()[0].Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Fatalf("content type %q", got)
	}
}

func TestBlockedAndSent(t *testing.T) {
	// prod refuses loopback: nothing is sent.
	srv := saastest.New(t)
	u, _ := url.Parse(srv.URL)
	prod := &egress.Policy{Env: egress.EnvProd}
	c := &saas.Client{Base: u, HTTP: prod.Client(egress.ClientOptions{Timeout: time.Second})}
	_, err := c.Do(context.Background(), saas.Request{Path: "/x"})
	if ce := connector.AsError(err); ce == nil || ce.Kind != connector.KindDestinationBlocked {
		t.Fatalf("err = %v", err)
	}
	if saas.IsSent(err) {
		t.Fatal("a blocked request counts as sent")
	}
	// A deadline while waiting for the answer: sent, timeout.
	slow := saastest.New(t, saastest.Exchange{Name: "slow", Method: "POST", Path: "/api/x", Status: 201, DelayMS: 500})
	c2 := devClient(t, slow)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = c2.Do(ctx, saas.Request{Method: "POST", Path: "/x", JSON: map[string]any{"a": 1}})
	ce := connector.AsError(err)
	if ce == nil || ce.Kind != connector.KindTimeout || !saas.IsSent(err) {
		t.Fatalf("err = %v", err)
	}
	if n := slow.Count("slow"); n != 1 {
		t.Fatalf("POST sent %d times", n)
	}
	// Bodies: one only.
	if _, err := c2.Do(context.Background(), saas.Request{Path: "/x", JSON: 1, Form: url.Values{}}); err == nil {
		t.Fatal("two bodies accepted")
	}
	if _, err := c2.Do(context.Background(), saas.Request{Path: "x"}); err == nil {
		t.Fatal("a path without a leading slash was accepted")
	}
}

func TestRetryAfter(t *testing.T) {
	r := &saas.Response{Header: http.Header{"Retry-After": {"7200"}}}
	if d := r.RetryAfter(time.Hour); d != time.Hour {
		t.Fatal(d)
	}
	r.Header.Set("Retry-After", "Wed, 21 Oct 2015 07:28:00 GMT")
	if d := r.RetryAfter(time.Hour); d != 0 {
		t.Fatal(d)
	}
	if (*saas.Response)(nil).RetryAfter(0) != 0 {
		t.Fatal("nil")
	}
}

func TestTokenCacheSingleFlight(t *testing.T) {
	var tc saas.TokenCache
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	tc.Now = func() time.Time { return now }
	var mints atomic.Int32
	mint := func(context.Context) (*vault.Secret, time.Time, error) {
		mints.Add(1)
		time.Sleep(20 * time.Millisecond)
		return vault.NewSecretForTest("bearer", map[string]string{"token": "ghs_x"}), now.Add(time.Hour), nil
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := tc.Get(context.Background(), "con_1/v1", 5*time.Minute, mint)
			if err != nil || s.Field("token") != "ghs_x" {
				t.Errorf("err=%v", err)
			}
		}()
	}
	wg.Wait()
	if mints.Load() != 1 || tc.Mints("con_1/v1") != 1 {
		t.Fatalf("minted %d times", mints.Load())
	}
	// Within the margin: a new mint.
	now = now.Add(56 * time.Minute)
	if _, err := tc.Get(context.Background(), "con_1/v1", 5*time.Minute, mint); err != nil || mints.Load() != 2 {
		t.Fatalf("mints=%d err=%v", mints.Load(), err)
	}
	// A failed mint isn't cached.
	boom := errors.New("boom")
	tc.Forget("con_1/v1")
	if _, err := tc.Get(context.Background(), "con_1/v1", 0, func(context.Context) (*vault.Secret, time.Time, error) { return nil, time.Time{}, boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := tc.Get(context.Background(), "con_1/v1", 0, mint); err != nil || mints.Load() != 3 {
		t.Fatalf("mints=%d", mints.Load())
	}
}

func TestSignRS256Golden(t *testing.T) {
	key := saastest.TestRSAKey()
	claims := map[string]any{"iat": 1791345540, "exp": 1791346140, "iss": "Iv23liAbC123"}
	jwt, err := saas.SignRS256(key, claims)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", jwt)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature doesn't verify: %v", err)
	}
	h, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if string(h) != `{"alg":"RS256","typ":"JWT"}` {
		t.Fatalf("header %s", h)
	}
	golden := "testdata/jwt_rs256.golden"
	if *update {
		if err := os.WriteFile(golden, []byte(jwt+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(want)) != jwt {
		t.Fatalf("jwt differs from the golden:\n%s\n%s", jwt, want)
	}
	if _, err := saas.SignRS256(nil, claims); err == nil {
		t.Fatal("nil key accepted")
	}
}

func TestParseRSAPrivateKey(t *testing.T) {
	k, err := saas.ParseRSAPrivateKey(saastest.TestRSAKeyPEM())
	if err != nil || k.N.Cmp(saastest.TestRSAKey().N) != 0 {
		t.Fatalf("err=%v", err)
	}
	for _, bad := range []string{"", "not pem", "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n",
		"-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n", "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"} {
		if _, err := saas.ParseRSAPrivateKey([]byte(bad)); err == nil || strings.Contains(err.Error(), "AAAA") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	_ = fmt.Sprint()
}

func TestSleepAndTransportEdges(t *testing.T) {
	// The real sleep: waits, honours ctx, and never waits past the deadline.
	srv := saastest.New(t,
		saastest.Exchange{Name: "busy", Method: "GET", Path: "/api/x", Status: 503},
		saastest.Exchange{Name: "ok", Method: "GET", Path: "/api/x", Status: 200, Body: json.RawMessage(`{}`)})
	c := devClient(t, srv)
	c.Sleep = nil
	if resp, err := c.Do(context.Background(), saas.Request{Path: "/x", Idempotent: true}); err != nil || resp.Attempts != 2 {
		t.Fatalf("err %v resp %+v", err, resp)
	}
	srv2 := saastest.New(t, saastest.Exchange{Name: "busy", Method: "GET", Path: "/api/x", Status: 503})
	c2 := devClient(t, srv2)
	c2.Sleep = nil
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c2.Do(ctx, saas.Request{Path: "/x", Idempotent: true}); err == nil {
		t.Fatal("a 503 passed")
	}
	// A refused connection sent nothing; it is retried once when idempotent.
	u, _ := url.Parse("http://127.0.0.1:1")
	pol := &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}
	c3 := &saas.Client{Base: u, HTTP: pol.Client(egress.ClientOptions{Timeout: time.Second}), Sleep: func(context.Context, time.Duration) bool { return true }}
	_, err := c3.Do(context.Background(), saas.Request{Path: "/x", Idempotent: true})
	if ce := connector.AsError(err); ce == nil || saas.IsSent(err) || ce.Code != connector.CodeConnectorUnavailable {
		t.Fatalf("err %v", err)
	}
	var se *saas.SentError
	if !errors.As(&connector.Error{Err: &saas.SentError{Err: errors.New("eof")}}, &se) || se.Error() == "" || se.Unwrap() == nil {
		t.Fatal("SentError")
	}
	// No client is an error, not a panic.
	if _, err := (&saas.Client{}).Do(context.Background(), saas.Request{}); err == nil {
		t.Fatal("an empty client sent")
	}
	// A broken connection after sending: upstream, sent.
	srv4 := saastest.New(t)
	srv4.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		conn.Close()
	})
	c4 := devClient(t, srv4)
	_, err = c4.Do(context.Background(), saas.Request{Method: "POST", Path: "/x", JSON: map[string]int{"a": 1}})
	if !saas.IsSent(err) {
		t.Fatalf("a dropped connection: %v", err)
	}
	// Segments survive escaping on the wire.
	srv5 := saastest.New(t, saastest.Exchange{Name: "pct", Method: "GET", Path: "/api/a%b c", Status: 200, Body: json.RawMessage(`{}`)})
	if _, err := devClient(t, srv5).Do(context.Background(), saas.Request{Path: saas.MustSegments("a%b c")}); err != nil {
		t.Fatal(err)
	}
	var tc saas.TokenCache
	if _, err := tc.Get(context.Background(), "k", 0, func(context.Context) (*vault.Secret, time.Time, error) {
		return vault.NewSecretForTest("bearer", map[string]string{"token": "x"}), time.Now().Add(time.Hour), nil
	}); err != nil {
		t.Fatal(err)
	}
}
