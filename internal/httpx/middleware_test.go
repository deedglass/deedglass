package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }

var ridRE = regexp.MustCompile(`^req_[0-7][0-9a-hjkmnp-tv-z]{25}$`)

func TestRequestIDOnEveryResponse(t *testing.T) {
	h := newHarness(t, harnessOpts{keys: fakeKeys{}})
	cases := []struct{ method, path, body string }{
		{"GET", "/v1/version", ""},
		{"GET", "/v1/helth", ""},
		{"DELETE", "/v1/health", ""},
		{"GET", "/v1/_test/panic", ""},
		{"POST", "/v1/_test/echo", `{"a":1}`},
	}
	for _, c := range cases {
		r := h.do(t, c.method, c.path, c.body, hdr("Deedglass-Request-Id", "req_spoofed"), hdr("Content-Type", "application/json"))
		rid := r.header.Get("Deedglass-Request-Id")
		if !ridRE.MatchString(rid) {
			t.Errorf("%s %s: request id %q", c.method, c.path, rid)
		}
		if r.header.Get("Deedglass-Version") != "2026-10-01" {
			t.Errorf("%s %s: Deedglass-Version %q", c.method, c.path, r.header.Get("Deedglass-Version"))
		}
		if r.status >= 400 {
			if got := r.env(t).RequestID; got != rid {
				t.Errorf("%s %s: body request_id %q != header %q", c.method, c.path, got, rid)
			}
		}
	}
}

func TestMiddlewareTable(t *testing.T) {
	h := newHarness(t, harnessOpts{keys: fakeKeys{}})
	big := `{"b":"` + strings.Repeat("x", 1<<20) + `"}` // 1 MiB + 8 bytes
	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		opts       []reqOpt
		status     int
		code       Code
		param      string
		msgContain string
		header     [2]string
	}{
		{name: "panic", method: "GET", path: "/v1/_test/panic", status: 500, code: CodeInternalError},
		{name: "body 1 MiB + 1", method: "POST", path: "/v1/_test/echo", body: big, opts: []reqOpt{bearer(goodKey), jsonCT()}, status: 413, code: CodeRequestTooLarge, msgContain: "1 MiB"},
		{name: "no content type", method: "POST", path: "/v1/_test/echo", body: `{"a":1}`, opts: []reqOpt{bearer(goodKey)}, status: 415, code: CodeUnsupportedMediaType},
		{name: "text/plain", method: "POST", path: "/v1/_test/echo", body: `{"a":1}`, opts: []reqOpt{bearer(goodKey), hdr("Content-Type", "text/plain")}, status: 415, code: CodeUnsupportedMediaType},
		{name: "latin1 charset", method: "POST", path: "/v1/_test/echo", body: `{"a":1}`, opts: []reqOpt{bearer(goodKey), hdr("Content-Type", "application/json; charset=latin1")}, status: 415, code: CodeUnsupportedMediaType},
		{name: "charset utf-8 ok", method: "POST", path: "/v1/_test/echo", body: `{"a":1}`, opts: []reqOpt{bearer(goodKey), hdr("Content-Type", "application/json; charset=UTF-8")}, status: 201},
		{name: "truncated json", method: "POST", path: "/v1/_test/echo", body: `{"a":`, opts: []reqOpt{bearer(goodKey), jsonCT()}, status: 400, code: CodeInvalidJSON, msgContain: "line 1, column 6"},
		{name: "duplicate member", method: "POST", path: "/v1/_test/echo", body: `{"a":1,"a":2}`, opts: []reqOpt{bearer(goodKey), jsonCT()}, status: 400, code: CodeInvalidJSON},
		{name: "invalid utf-8", method: "POST", path: "/v1/_test/echo", body: "{\"b\":\"\xff\"}", opts: []reqOpt{bearer(goodKey), jsonCT()}, status: 400, code: CodeInvalidJSON},
		{name: "trailing data", method: "POST", path: "/v1/_test/echo", body: `{"a":1} {}`, opts: []reqOpt{bearer(goodKey), jsonCT()}, status: 400, code: CodeInvalidJSON},
		{name: "unknown member", method: "POST", path: "/v1/_test/echo", body: `{"amout":1}`, opts: []reqOpt{bearer(goodKey), jsonCT()}, status: 400, code: CodeValidationFailed, param: "amout", msgContain: "`amout` isn't a known field"},
		{name: "wrong type", method: "POST", path: "/v1/_test/echo", body: `{"a":"one"}`, opts: []reqOpt{bearer(goodKey), jsonCT()}, status: 400, code: CodeValidationFailed, param: "a", msgContain: "a whole number"},
		{name: "empty body", method: "POST", path: "/v1/_test/echo", body: ` `, opts: []reqOpt{bearer(goodKey), jsonCT()}, status: 400, code: CodeInvalidJSON},
		{name: "did you mean", method: "GET", path: "/v1/helth", status: 404, code: CodeNotFound, msgContain: "There's no endpoint at GET /v1/helth. Did you mean /v1/health?"},
		{name: "no suggestion", method: "GET", path: "/v2/nothing/here", status: 404, code: CodeNotFound, msgContain: "Check the path"},
		{name: "405", method: "DELETE", path: "/v1/health", status: 405, code: CodeMethodNotAllowed, header: [2]string{"Allow", "GET, HEAD"}, msgContain: "DELETE isn't supported on /v1/health. Use GET or HEAD."},
		{name: "trailing slash", method: "GET", path: "/v1/health/", status: 404, code: CodeNotFound},
		{name: "unclean path", method: "GET", path: "/v1//health", status: 404, code: CodeNotFound},
		{name: "dot segment", method: "GET", path: "/v1/x/../health", status: 404, code: CodeNotFound},
		{name: "HEAD follows GET", method: "HEAD", path: "/v1/version", status: 200},
		{name: "bad version", method: "GET", path: "/v1/version", opts: []reqOpt{hdr("Deedglass-Version", "2026-13-01")}, status: 400, code: CodeAPIVersionInvalid, param: "Deedglass-Version"},
		{name: "unknown version", method: "GET", path: "/v1/version", opts: []reqOpt{hdr("Deedglass-Version", "2025-01-01")}, status: 400, code: CodeAPIVersionInvalid, param: "Deedglass-Version"},
		{name: "version ok", method: "GET", path: "/v1/version", opts: []reqOpt{hdr("Deedglass-Version", "2026-10-01")}, status: 200, header: [2]string{"Deedglass-Version", "2026-10-01"}},
		{name: "no key", method: "GET", path: "/v1/_test/scoped", status: 401, code: CodeAuthenticationRequired, header: [2]string{"WWW-Authenticate", `Bearer realm="deedglass"`}},
		{name: "bad scheme", method: "GET", path: "/v1/_test/scoped", opts: []reqOpt{hdr("Authorization", "Basic Zm9vOmJhcg==")}, status: 401, code: CodeInvalidAPIKey},
		{name: "bad key", method: "GET", path: "/v1/_test/scoped", opts: []reqOpt{bearer("dg_test_nope")}, status: 401, code: CodeInvalidAPIKey, header: [2]string{"WWW-Authenticate", `Bearer realm="deedglass", error="invalid_token"`}},
		{name: "revoked key", method: "GET", path: "/v1/_test/scoped", opts: []reqOpt{bearer(oldKey)}, status: 401, code: CodeAPIKeyRevoked},
		{name: "missing scope", method: "GET", path: "/v1/_test/scoped", opts: []reqOpt{bearer(otherKey)}, status: 403, code: CodeInsufficientScope, msgContain: "deeds:read"},
		{name: "scoped ok", method: "GET", path: "/v1/_test/scoped", opts: []reqOpt{bearer(goodKey)}, status: 200},
		{name: "session route, no session authenticator", method: "GET", path: "/v1/_test/session", status: 401, code: CodeAuthenticationRequired},
		{name: "body on GET", method: "GET", path: "/v1/version", body: "x", status: 413, code: CodeRequestTooLarge, msgContain: "doesn't take a request body"},
		{name: "object not found", method: "GET", path: "/v1/_test/things/agt_x", status: 404, code: CodeNotFound, msgContain: "There's no thing with id agt_x."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.do(t, c.method, c.path, c.body, c.opts...)
			if r.status != c.status {
				t.Fatalf("status %d, want %d: %s", r.status, c.status, r.body)
			}
			if c.header[0] != "" {
				if got := r.header.Get(c.header[0]); got != c.header[1] {
					t.Errorf("%s = %q, want %q", c.header[0], got, c.header[1])
				}
			}
			if c.code == "" {
				return
			}
			e := r.env(t)
			if e.Code != c.code {
				t.Errorf("code %q, want %q (%s)", e.Code, c.code, e.Message)
			}
			if want := byCode[c.code]; e.Type != want.Type || r.status != want.Status {
				t.Errorf("type/status %s/%d, catalog says %s/%d", e.Type, r.status, want.Type, want.Status)
			}
			if e.DocURL != DocURLPrefix+string(c.code) {
				t.Errorf("doc_url %q", e.DocURL)
			}
			if (e.Param == nil) != (c.param == "") || (e.Param != nil && *e.Param != c.param) {
				t.Errorf("param %v, want %q", e.Param, c.param)
			}
			if c.msgContain != "" && !strings.Contains(e.Message, c.msgContain) {
				t.Errorf("message %q doesn't contain %q", e.Message, c.msgContain)
			}
			for _, bad := range []string{"goroutine", "/Users/", "hunter2", "panic", "xxxxxxxx", "\xff", "amout\":"} {
				if strings.Contains(string(r.body), bad) {
					t.Errorf("body leaks %q: %s", bad, r.body)
				}
			}
			if r.header.Get("Content-Type") != ContentTypeJSON {
				t.Errorf("content type %q", r.header.Get("Content-Type"))
			}
		})
	}
}

func TestPanicIsLoggedWithStackAndRequestID(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	r := h.do(t, "GET", "/v1/_test/panic", "")
	rid := r.header.Get("Deedglass-Request-Id")
	logs := h.log.String()
	if !strings.Contains(logs, "goroutine") || !strings.Contains(logs, rid) || !strings.Contains(logs, "panic serving request") {
		t.Fatalf("log lacks the stack or request id:\n%s", logs)
	}
	if strings.Contains(string(r.body), "boom") {
		t.Fatal("panic value leaked into the body")
	}
}

func TestAbortHandlerIsRepanicked(t *testing.T) {
	srv := New(Options{Logger: NewLogger(&logBuf{}, true, nil)})
	srv.Mount(Route{Method: "GET", Pattern: "/v1/_test/abort", OperationID: "x", Auth: Public, Extension: "test",
		Handler: func(http.ResponseWriter, *http.Request) error { panic(http.ErrAbortHandler) }})
	defer func() {
		if p := recover(); p != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", p)
		}
	}()
	srv.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/_test/abort", nil))
	t.Fatal("no panic")
}

func TestTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, harnessOpts{})
		h.srv.o.Now = time.Now // the bubble's fake clock
		start := time.Now()
		r := h.do(t, "GET", "/v1/_test/slow", "")
		if r.status != 504 || r.env(t).Code != CodeTimeout {
			t.Fatalf("got %d %s", r.status, r.body)
		}
		if !strings.Contains(r.env(t).Message, "after 0.05 s") {
			t.Errorf("message %q", r.env(t).Message)
		}
		if d := time.Since(start); d != 50*time.Millisecond {
			t.Errorf("took %v of fake time, want exactly 50ms", d)
		}
	})
}

func TestRealIP(t *testing.T) {
	cases := []struct {
		name    string
		proxies []string
		remote  string
		xff     string
		want    string
	}{
		{"untrusted remote ignores XFF", nil, "203.0.113.9:5000", "1.2.3.4", "203.0.113.9"},
		{"trusted proxy honours XFF", []string{"10.0.0.0/8"}, "10.1.2.3:5000", "1.2.3.4", "1.2.3.4"},
		{"right to left skipping trusted hops", []string{"10.0.0.0/8"}, "10.1.2.3:5000", "6.6.6.6, 1.2.3.4, 10.9.9.9", "1.2.3.4"},
		{"spoofed leftmost is not used", []string{"10.0.0.0/8"}, "10.1.2.3:5000", "9.9.9.9, 1.2.3.4", "1.2.3.4"},
		{"all trusted uses leftmost", []string{"10.0.0.0/8"}, "10.1.2.3:5000", "10.0.0.1", "10.0.0.1"},
		{"garbage hop stops the walk", []string{"10.0.0.0/8"}, "10.1.2.3:5000", "1.2.3.4, nonsense", "10.1.2.3"},
		{"ipv6", []string{"::1/128"}, "[::1]:5000", "2001:db8::1", "2001:db8::1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{proxies: c.proxies})
			var got string
			h.srv.Mount(Route{Method: "GET", Pattern: "/v1/_test/ip", OperationID: "ip", Auth: Public, Extension: "test",
				Handler: func(w http.ResponseWriter, r *http.Request) error { got = ClientIP(r.Context()); return nil }})
			h.do(t, "GET", "/v1/_test/ip", "", remote(c.remote), hdr("X-Forwarded-For", c.xff))
			if got != c.want {
				t.Fatalf("ip %q, want %q", got, c.want)
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	for _, c := range []struct {
		name string
		prod bool
		base string
		hsts bool
	}{
		{"dev", false, "https://api.example.com", false},
		{"prod http", true, "http://api.example.com", false},
		{"prod https", true, "https://api.example.com", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{prod: c.prod, baseURL: c.base})
			r := h.do(t, "GET", "/v1/version", "")
			want := map[string]string{
				"X-Content-Type-Options":       "nosniff",
				"Referrer-Policy":              "no-referrer",
				"X-Frame-Options":              "DENY",
				"Content-Security-Policy":      "default-src 'none'; frame-ancestors 'none'",
				"Cross-Origin-Resource-Policy": "same-origin",
				"Cache-Control":                "no-store",
			}
			for k, v := range want {
				if r.header.Get(k) != v {
					t.Errorf("%s = %q, want %q", k, r.header.Get(k), v)
				}
			}
			if (r.header.Get("Strict-Transport-Security") != "") != c.hsts {
				t.Errorf("HSTS present = %v, want %v", !c.hsts, c.hsts)
			}
			for k := range r.header {
				if strings.HasPrefix(k, "Access-Control-") || k == "Server" {
					t.Errorf("unexpected header %s", k)
				}
			}
		})
	}
}

func TestPinnedVersionFromPrincipal(t *testing.T) {
	h := newHarness(t, harnessOpts{keys: fakeKeys{}})
	r := h.do(t, "GET", "/v1/_test/scoped", "", bearer(goodKey))
	if r.header.Get("Deedglass-Version") != "2026-10-01" || !strings.Contains(string(r.body), `"version": "2026-10-01"`) {
		t.Fatalf("got %s %s", r.header.Get("Deedglass-Version"), r.body)
	}
	r = h.do(t, "GET", "/v1/version", "", hdr("Deedglass-Version", "2026-10-01"), func(r *http.Request) { r.Header.Add("Deedglass-Version", "2026-10-01") })
	if r.status != 400 || r.env(t).Code != CodeAPIVersionInvalid {
		t.Fatalf("a repeated header must be refused, got %d", r.status)
	}
}

func TestClientRequestIDLogged(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.do(t, "GET", "/v1/version?token=abc", "", hdr("X-Request-Id", "client-123"))
	logs := h.log.String()
	if !strings.Contains(logs, `"client_request_id":"client-123"`) {
		t.Errorf("client_request_id missing:\n%s", logs)
	}
	if strings.Contains(logs, "token=abc") {
		t.Errorf("the query string reached the log:\n%s", logs)
	}
	h.do(t, "GET", "/v1/version", "", hdr("X-Request-Id", strings.Repeat("a", 65)))
	if strings.Contains(h.log.String(), strings.Repeat("a", 65)) {
		t.Error("an over-long X-Request-Id was logged")
	}
}

func TestHandlerErrorsAfterWriteAreNotRendered(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.srv.Mount(Route{Method: "GET", Pattern: "/v1/_test/late", OperationID: "late", Auth: Public, Extension: "test",
		Handler: func(w http.ResponseWriter, r *http.Request) error {
			_ = WriteJSON(w, 200, map[string]string{"object": "ok"})
			return context.Canceled
		}})
	r := h.do(t, "GET", "/v1/_test/late", "")
	if r.status != 200 || strings.Contains(string(r.body), "error") {
		t.Fatalf("got %d %s", r.status, r.body)
	}
}
