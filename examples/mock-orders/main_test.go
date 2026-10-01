package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func do(t *testing.T, h http.Handler, method, path, body, token string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Idempotency-Key", "")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestMockOrders(t *testing.T) {
	const tok = "test-token-0123456789"
	for _, lying := range []bool{false, true} {
		s := New(tok, lying)
		if c, _ := do(t, s, "GET", "/v1/orders/42", "", ""); c != 401 {
			t.Fatalf("no token: %d", c)
		}
		if c, _ := do(t, s, "GET", "/v1/orders/51", "", tok); c != 404 {
			t.Fatalf("51: %d", c)
		}
		c, m := do(t, s, "PATCH", "/v1/orders/42", `{"status":"shipped"}`, tok)
		if c != 200 || m["status"] != "shipped" {
			t.Fatalf("patch: %d %v", c, m)
		}
		_, m = do(t, s, "GET", "/v1/orders/42", "", tok)
		if saved := m["status"] == "shipped"; saved == lying {
			t.Fatalf("lying=%v: read back %v", lying, m["status"])
		}
		if c, _ := do(t, s, "PATCH", "/v1/orders/42", `{"status":"lost"}`, tok); c != 422 {
			t.Fatalf("bad status: %d", c)
		}
	}
}
