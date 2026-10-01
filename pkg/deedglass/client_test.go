package deedglass

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testKey = "dg_test_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8"

func TestDoSendsHeadersAndDecodes(t *testing.T) {
	var got *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Deedglass-Request-Id", "req_1")
		w.Header().Set("Deedglass-Idempotent-Replayed", "true")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"object":"agent","id":"agt_1","name":"support-bot"}`))
	}))
	defer srv.Close()
	c := NewClient(testKey, srv.URL+"/")
	var out struct{ Object, ID, Name string }
	r, err := c.Do(context.Background(), "POST", "/v1/agents", map[string]string{"name": "support-bot"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "support-bot" || r.Status != 201 || !r.Replayed || r.ID != "req_1" {
		t.Fatalf("got %+v %+v", out, r)
	}
	h := got.Header
	if h.Get("Authorization") != "Bearer "+testKey || h.Get("Deedglass-Version") != APIVersion ||
		h.Get("Content-Type") != "application/json" || len(h.Get("Idempotency-Key")) != 32 {
		t.Fatalf("headers: %v", h)
	}
	if got.URL.Path != "/v1/agents" || string(body) != `{"name":"support-bot"}` {
		t.Fatalf("path %s body %s", got.URL.Path, body)
	}
}

func TestPostsGetDistinctIdempotencyKeysUnlessSet(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := NewClient(testKey, srv.URL)
	ctx := context.Background()
	_, _ = c.Do(ctx, "POST", "/x", nil, nil)
	_, _ = c.Do(ctx, "POST", "/x", nil, nil)
	_, _ = c.Do(ctx, "POST", "/x", nil, nil, WithIdempotencyKey("mine"))
	_, _ = c.Do(ctx, "GET", "/x", nil, nil)
	if len(keys) != 4 || keys[0] == keys[1] || keys[2] != "mine" || keys[3] != "" {
		t.Fatalf("keys: %q", keys)
	}
}

func TestErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "4")
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "permission_error", "code": "agent_stopped",
			"message": `Agent "support-bot" is stopped.`, "param": nil, "request_id": "req_2", "doc_url": "https://docs.deedglass.com/errors/agent_stopped"}})
	}))
	defer srv.Close()
	_, err := NewClient(testKey, srv.URL).Do(context.Background(), "GET", "/v1/deeds?limit=1", nil, nil)
	var e *Error
	if !errors.As(err, &e) || e.Status != 403 || e.Code != "agent_stopped" || e.RequestID != "req_2" || e.RetryAfter != "4" {
		t.Fatalf("got %#v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatal("the key leaked into the error")
	}
}

func TestNonJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
	defer srv.Close()
	_, err := NewClient(testKey, srv.URL).Do(context.Background(), "GET", "/", nil, nil)
	var e *Error
	if !errors.As(err, &e) || e.Status != 502 || e.Code != "" {
		t.Fatalf("got %#v", err)
	}
}

func TestBadServerAndTimeout(t *testing.T) {
	if _, err := NewClient(testKey, "ftp://x").Do(context.Background(), "GET", "/", nil, nil); err == nil {
		t.Fatal("ftp must be refused")
	}
	if _, err := (&Client{}).Do(context.Background(), "GET", "/", nil, nil); err == nil {
		t.Fatal("an empty server must be refused")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(200 * time.Millisecond) }))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, APIKey: testKey, HTTP: &http.Client{Timeout: 20 * time.Millisecond}}
	_, err := c.Do(context.Background(), "GET", "/", nil, nil)
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("got %v", err)
	}
}
