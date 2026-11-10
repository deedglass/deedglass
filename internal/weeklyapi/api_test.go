package weeklyapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/weekly"
	"github.com/deedglass/deedglass/internal/weekly/pgsource"
)

func TestOffPageExpiredToken(t *testing.T) {
	key := weekly.OffKeyFrom([]byte("secret-key-32-bytes-long-for-test"))
	org := ids.New[ids.OrgKind]()
	user := ids.New[ids.UserKind]()

	expiredTime := time.Now().Add(-100 * 24 * time.Hour)
	tok, err := weekly.OffTokenWithExpiry(key, org, user, expiredTime)
	if err != nil {
		t.Fatal(err)
	}

	a := &API{
		OffKey:  key,
		Limiter: httpx.NewLimiter(10, time.Minute, nil),
		Source:  &pgsource.Source{},
	}

	req := httptest.NewRequest("GET", "/weekly/off?t="+tok, nil)
	rec := httptest.NewRecorder()

	if err := a.offPage(rec, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want %d", rec.Code, http.StatusBadRequest)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "This link has expired") {
		t.Fatalf("expected body to contain 'This link has expired', got: %s", body)
	}
}

func TestOffPageInvalidToken(t *testing.T) {
	key := weekly.OffKeyFrom([]byte("secret-key-32-bytes-long-for-test"))

	a := &API{
		OffKey:  key,
		Limiter: httpx.NewLimiter(10, time.Minute, nil),
		Source:  &pgsource.Source{},
	}

	req := httptest.NewRequest("GET", "/weekly/off?t=bad-token", nil)
	rec := httptest.NewRecorder()

	if err := a.offPage(rec, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want %d", rec.Code, http.StatusBadRequest)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "doesn&#39;t work") && !strings.Contains(body, "doesn't work") {
		t.Fatalf("expected body to contain 'doesn't work', got: %s", body)
	}
}

func TestOffPageRateLimit(t *testing.T) {
	key := weekly.OffKeyFrom([]byte("secret-key-32-bytes-long-for-test"))

	a := &API{
		OffKey:  key,
		Limiter: httpx.NewLimiter(2, time.Minute, nil),
		Source:  &pgsource.Source{},
	}

	// 1st request
	req1 := httptest.NewRequest("GET", "/weekly/off?t=x", nil)
	rec1 := httptest.NewRecorder()
	_ = a.offPage(rec1, req1)

	// 2nd request
	req2 := httptest.NewRequest("GET", "/weekly/off?t=x", nil)
	rec2 := httptest.NewRecorder()
	_ = a.offPage(rec2, req2)

	// 3rd request should hit rate limit
	req3 := httptest.NewRequest("GET", "/weekly/off?t=x", nil)
	rec3 := httptest.NewRecorder()
	err := a.offPage(rec3, req3)
	if err == nil {
		t.Fatalf("expected rate limit error, got nil")
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		if he.Code != httpx.CodeRateLimited {
			t.Fatalf("code %s, want %s", he.Code, httpx.CodeRateLimited)
		}
	} else {
		t.Fatalf("expected *httpx.Error, got %T: %v", err, err)
	}
}
