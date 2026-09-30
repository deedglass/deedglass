package httpx

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLogCanaries(t *testing.T) {
	h := newHarness(t, harnessOpts{keys: fakeKeys{}})
	canaryKey := "dg_live_CANARY00_canarycanarycanarycanarycanaryca"
	dsn := "postgres://deedglass_app:pw-canary-9f2@db.internal:5432/deedglass"
	h.post(t, "/v1/_test/echo", `{"a":1,"b":"`+canaryKey+`"}`, bearer(canaryKey), hdr("Cookie", "__Host-deedglass_session=cookie-canary"))
	h.post(t, "/v1/_test/echo", `{"a":1}`, bearer(goodKey), idem("K"), hdr("X-Request-Id", "ok"))
	h.post(t, "/v1/_test/secret", `{}`, bearer(goodKey), idem("S"))
	h.post(t, "/v1/_test/echo", `{"fail":500}`, bearer(goodKey))

	// Direct logging through the redacting handler, as later sessions will.
	h.srv.log.Info("connecting", "dsn", dsn, "database_url", dsn, "url", dsn, "Authorization", "Bearer "+canaryKey,
		"note", "key "+canaryKey+" used", "err", errors.New("dial "+dsn+" failed"),
		slog.Group("req", "token", "tok-canary", "password", "pw-canary-9f2", "keep", "visible"))
	h.srv.log.With("secret", "with-canary", "api_key", canaryKey).Warn("with attrs " + canaryKey)

	logs := h.log.String()
	for _, c := range []string{canaryKey, "CANARY00", "pw-canary-9f2", "cookie-canary", "tok-canary", "with-canary", canarySecret} {
		if strings.Contains(logs, c) {
			t.Errorf("log contains %q", c)
		}
	}
	for _, want := range []string{"visible", "dg_live_[redacted]", "://[redacted]@db.internal:5432", `"route":"/v1/_test/echo"`, `"org_id":"` + testOrg.String(), `"agent_id":"` + testAgent.String(), `"idempotent_replay":false`} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q", want)
		}
	}
	// One JSON object per line, each with the access-log fields.
	var requests int
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON: %s", line)
		}
		if m["msg"] != "request" {
			continue
		}
		requests++
		for _, f := range []string{"request_id", "method", "route", "status", "bytes", "duration_ms", "ip", "org_id", "agent_id", "action_id", "api_version", "idempotent_replay", "client_request_id", "user_agent"} {
			if _, ok := m[f]; !ok {
				t.Errorf("access log lacks %s: %s", f, line)
			}
		}
		for _, f := range []string{"body", "query", "authorization"} {
			if _, ok := m[f]; ok {
				t.Errorf("access log has %s", f)
			}
		}
	}
	if requests != 4 {
		t.Errorf("%d access-log lines, want 4", requests)
	}
}

func TestScrub(t *testing.T) {
	for in, want := range map[string]string{
		"dg_test_abc_def and dg_live_X":        "dg_test_[redacted] and dg_live_[redacted]",
		"postgres://u:p@h/db":                  "postgres://[redacted]@h/db",
		"https://user:@host/x":                 "https://[redacted]@host/x",
		"https://host/x":                       "https://host/x",
		"postgres://token@h/db (a bare token)": "postgres://[redacted]@h/db (a bare token)",
		"plain text":                           "plain text",
	} {
		if got := Scrub(in); got != want {
			t.Errorf("Scrub(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTextLoggerInDev(t *testing.T) {
	var b logBuf
	NewLogger(&b, false, slog.LevelInfo).Info("hello", "password", "x")
	if got := b.String(); !strings.Contains(got, "msg=hello") || strings.Contains(got, "password") {
		t.Fatalf("got %q", got)
	}
}

func TestMetrics(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.do(t, "GET", "/v1/version", "")
	h.do(t, "GET", "/v1/version", "")
	h.do(t, "GET", "/v1/helth", "")
	h.do(t, "BREW", "/v1/version", "")
	w := httptest.NewRecorder()
	h.srv.Metrics().Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	out := w.Body.String()
	for _, want := range []string{
		`deedglass_http_requests_total{route="/v1/version",method="GET",status="200"} 2`,
		`deedglass_http_requests_total{route="unmatched",method="GET",status="404"} 1`,
		`deedglass_http_requests_total{route="unmatched",method="OTHER",status="405"} 1`,
		`deedglass_http_request_duration_seconds_bucket{route="/v1/version",le="+Inf"} 2`,
		`deedglass_http_request_duration_seconds_count{route="/v1/version"} 2`,
		`deedglass_http_in_flight 0`,
		`deedglass_build_info{version="`,
		"# TYPE deedglass_http_request_duration_seconds histogram",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition lacks %q", want)
		}
	}
	if strings.Contains(out, "/v1/helth") {
		t.Error("a raw path became a label")
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type %q", ct)
	}
	w = httptest.NewRecorder()
	h.srv.Metrics().Handler().ServeHTTP(w, httptest.NewRequest("GET", "/other", nil))
	if w.Code != 404 {
		t.Errorf("/other: %d", w.Code)
	}
}

type fakeHealth struct {
	pingErr error
	state   MigrationState
	migErr  error
	calls   atomic.Int64
	delay   time.Duration
}

func (f *fakeHealth) PingDatabase(ctx context.Context) error {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.pingErr
}
func (f *fakeHealth) Migrations(context.Context) (MigrationState, error) { return f.state, f.migErr }

func TestHealth(t *testing.T) {
	cases := []struct {
		name   string
		src    *fakeHealth
		status int
		want   string
		body   []string
	}{
		{"ok", &fakeHealth{state: MigrationState{Applied: 9, Expected: 9}}, 200, StatusOK,
			[]string{`"database": {`, `"latency_ms": 0`, `"applied": 9`, `"expected": 9`, `"seal_key": {`, `"not_configured"`}},
		{"pending", &fakeHealth{state: MigrationState{Applied: 8, Expected: 9}}, 200, StatusDegraded, []string{`"status": "pending"`}},
		{"changed", &fakeHealth{state: MigrationState{Applied: 9, Expected: 9, Changed: true}}, 200, StatusDegraded, []string{`"status": "mismatch"`}},
		{"down", &fakeHealth{pingErr: errors.New("dial tcp 10.0.0.9:5432: connection refused")}, 503, StatusDown, []string{`"status": "down"`}},
		{"migration read failed", &fakeHealth{migErr: errors.New("permission denied")}, 200, StatusDegraded, []string{`"status": "unknown"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{health: c.src})
			r := h.do(t, "GET", "/v1/health", "")
			if r.status != c.status {
				t.Fatalf("status %d, want %d: %s", r.status, c.status, r.body)
			}
			var b HealthBody
			if err := json.Unmarshal(r.body, &b); err != nil {
				t.Fatal(err)
			}
			if b.Status != c.want || b.Object != "health" || b.APIVersion != "2026-10-01" {
				t.Fatalf("body %+v", b)
			}
			for _, s := range c.body {
				if !strings.Contains(string(r.body), s) {
					t.Errorf("body lacks %s:\n%s", s, r.body)
				}
			}
			for _, leak := range []string{"10.0.0.9", "refused", "permission"} {
				if strings.Contains(string(r.body), leak) {
					t.Errorf("body leaks %q", leak)
				}
			}
			if r.header.Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control %q", r.header.Get("Cache-Control"))
			}
		})
	}
}

func TestHealthCacheAndDrain(t *testing.T) {
	src := &fakeHealth{state: MigrationState{Applied: 9, Expected: 9}, delay: 20 * time.Millisecond}
	h := newHarness(t, harnessOpts{health: src})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); h.do(t, "GET", "/v1/health", "") }()
	}
	wg.Wait()
	if n := src.calls.Load(); n != 1 {
		t.Fatalf("20 concurrent probes pinged the database %d times, want 1", n)
	}
	h.do(t, "GET", "/v1/health", "")
	if src.calls.Load() != 1 {
		t.Fatal("a probe within 1 s must use the cache")
	}
	h.advance(1100 * time.Millisecond)
	h.do(t, "GET", "/v1/health", "")
	if src.calls.Load() != 2 {
		t.Fatalf("after 1 s the checks must run again (%d)", src.calls.Load())
	}
	h.srv.StartDraining()
	r := h.do(t, "GET", "/v1/health", "")
	if r.status != 503 || !strings.Contains(string(r.body), `"status": "draining"`) {
		t.Fatalf("draining: %d %s", r.status, r.body)
	}
}

func TestHealthWithoutDatabase(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	r := h.do(t, "GET", "/v1/health", "")
	if r.status != 200 || !strings.Contains(string(r.body), `"database": {
      "status": "not_configured"`) {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

func TestVersionAndSpec(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	r := h.do(t, "GET", "/v1/version", "")
	var v map[string]any
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"object", "version", "commit", "built_at", "go", "api_versions", "default_api_version"} {
		if _, ok := v[k]; !ok {
			t.Errorf("version lacks %s", k)
		}
	}
	if v["object"] != "version" || v["default_api_version"] != "2026-10-01" {
		t.Errorf("version body %v", v)
	}
	r = h.do(t, "GET", "/api/openapi.yaml", "")
	if r.status != 200 || string(r.body) != "openapi: 3.1.1\n" || r.header.Get("Content-Type") != "application/yaml" ||
		r.header.Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("spec: %d %q %q %q", r.status, r.body, r.header.Get("Content-Type"), r.header.Get("Cache-Control"))
	}
}
