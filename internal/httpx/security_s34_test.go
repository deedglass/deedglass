package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// S34's review rows 6, 7 and 8 as tables over real routes: the security headers every response
// carries, CORS denied by default, and the server limits.

func TestSecurityHeadersRoutes(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{"GET", "/v1/health", ""},                   // public system route
		{"GET", "/v1/version", ""},                  // public, cacheable data
		{"GET", "/api/openapi.yaml", ""},            // the spec (YAML, not JSON)
		{"POST", "/v1/_test/echo", `{"a":1}`},       // an agent route without a key: 401
		{"GET", "/v1/_test/session", ""},            // a session route without a cookie: 401
		{"GET", "/v1/_test/things/thg_unknown", ""}, // an error envelope: 404
	}
	for _, env := range []struct {
		name string
		prod bool
		base string
		hsts bool
	}{{"dev", false, "http://127.0.0.1:8080", false}, {"prod", true, "https://api.deedglass.com", true}} {
		h := newHarness(t, harnessOpts{prod: env.prod, baseURL: env.base})
		for _, rt := range routes {
			r := h.do(t, rt.method, rt.path, rt.body)
			want := map[string]string{
				"X-Content-Type-Options":     "nosniff",
				"Referrer-Policy":            "no-referrer",
				"X-Frame-Options":            "DENY",
				"Cross-Origin-Opener-Policy": "same-origin",
				"Permissions-Policy":         PermissionsPolicy,
			}
			for k, v := range want {
				if got := r.header.Get(k); got != v {
					t.Errorf("%s %s %s: %s = %q, want %q", env.name, rt.method, rt.path, k, got, v)
				}
			}
			if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "default-src 'none'") {
				t.Errorf("%s %s: CSP %q", env.name, rt.path, csp)
			}
			if (r.header.Get("Strict-Transport-Security") != "") != env.hsts {
				t.Errorf("%s %s: HSTS %q", env.name, rt.path, r.header.Get("Strict-Transport-Security"))
			}
		}
	}
}

// TestCORSDenied: a preflight and a request from another origin get no Access-Control-* header on
// any /v1 route, so a browser on evil.test can't read an answer.
func TestCORSDenied(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	for _, rt := range []struct{ method, path string }{
		{"OPTIONS", "/v1/version"}, {"OPTIONS", "/v1/_test/echo"}, {"GET", "/v1/version"}, {"POST", "/v1/_test/public"},
	} {
		req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(`{}`))
		req.Header.Set("Origin", "https://evil.test")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.srv.ServeHTTP(rec, req)
		for k := range rec.Header() {
			if strings.HasPrefix(k, "Access-Control-") {
				t.Errorf("%s %s from evil.test: %s: %s", rt.method, rt.path, k, rec.Header().Get(k))
			}
		}
		if rt.method == "OPTIONS" && rec.Code < 400 {
			t.Errorf("preflight %s answered %d", rt.path, rec.Code)
		}
	}
}

// TestLimits: the body limit (1 MiB + 1 → 413), JSON nesting the decoder refuses or the shape
// refuses (400), and the route timeout (504) are enforced by the pipeline, not by handlers.
func TestLimits(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	big := `{"b":"` + strings.Repeat("a", DefaultMaxBody) + `"}`
	if r := h.do(t, "POST", "/v1/_test/public", big); r.status != http.StatusRequestEntityTooLarge || codeOf(r) != "request_too_large" {
		t.Fatalf("1 MiB + 1: %d %s", r.status, codeOf(r))
	}
	var in echoIn
	req := httptest.NewRequest("POST", "/v1/_test/public", strings.NewReader(strings.Repeat("[", 10_001)+strings.Repeat("]", 10_001)))
	if err := DecodeJSON(req, &in); err == nil || AsError(err) == nil || AsError(err).Status != http.StatusBadRequest {
		t.Fatalf("depth 10,001: %v", err)
	}
	if r := h.do(t, "GET", "/v1/_test/slow", ""); r.status != http.StatusGatewayTimeout || codeOf(r) != "timeout" {
		t.Fatalf("route timeout: %d %s", r.status, codeOf(r))
	}
}

func codeOf(r resp) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Error.Code
}

// TestPublicExtensionNeedsReason (SR-006): Mount refuses a public extension route without a reason.
func TestPublicExtensionNeedsReason(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a public extension route without PublicReason mounted")
		}
	}()
	New(Options{}).Mount(Route{Method: "GET", Pattern: "/extra", OperationID: "extra", Auth: Public, Extension: "ADR-099",
		Handler: func(http.ResponseWriter, *http.Request) error { return nil }})
}
