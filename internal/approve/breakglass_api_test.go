package approve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

type fakeSessions struct {
	p httpx.Principal
}

func (f fakeSessions) AuthenticateSession(context.Context, *http.Request) (httpx.Principal, error) {
	return f.p, nil
}

func TestBreakGlassAPIRoutes(t *testing.T) {
	orgID := ids.New[ids.OrgKind]()
	otherOrg := ids.New[ids.OrgKind]()
	userID := ids.New[ids.UserKind]()

	p := httpx.Principal{
		Kind:   httpx.KindUser,
		OrgID:  orgID,
		UserID: userID,
		Role:   httpx.RoleAdmin,
		Scopes: []string{httpx.ScopeAdmin, httpx.ScopeActionsRead},
	}

	bg := NewBreakGlassQueue()
	svc := &Service{
		breakGlass: bg,
		now:        time.Now,
	}

	srv := httpx.New(httpx.Options{
		Sessions: fakeSessions{p: p},
	})
	srv.Mount(Routes(svc)...)

	// Pre-populate queue
	item1 := &BreakGlassReview{
		ID:               "bgr_act_01jkm99trauma",
		ActionID:         "act_01jkm99trauma",
		OrgID:            orgID,
		RuleID:           "break-glass",
		EmergencyReason:  "Acute polytrauma resuscitation in ED Bay 2",
		Ward:             "Emergency Medicine",
		ClinicianSubject: "sha256:salted_doc_digest",
		Status:           ReviewStatusPending,
		CreatedAt:        time.Now().UTC().Add(-2 * time.Hour),
	}
	if err := bg.Enqueue(item1); err != nil {
		t.Fatalf("failed to enqueue item1: %v", err)
	}

	itemOther := &BreakGlassReview{
		ID:              "bgr_act_other_org",
		ActionID:        "act_other_org",
		OrgID:           otherOrg,
		RuleID:          "break-glass",
		EmergencyReason: "Other org emergency",
		Status:          ReviewStatusPending,
		CreatedAt:       time.Now().UTC(),
	}
	if err := bg.Enqueue(itemOther); err != nil {
		t.Fatalf("failed to enqueue itemOther: %v", err)
	}

	// 1. GET /v1/break-glass (list pending)
	t.Run("ListPending", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/break-glass", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var resp listBreakGlassResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}
		if resp.Count != 1 || len(resp.Reviews) != 1 {
			t.Fatalf("expected 1 review for org, got count=%d len=%d", resp.Count, len(resp.Reviews))
		}
		if resp.Reviews[0].ID != item1.ID {
			t.Fatalf("expected review %q, got %q", item1.ID, resp.Reviews[0].ID)
		}
	})

	// 2. GET /v1/break-glass/{id}
	t.Run("GetReview", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/break-glass/"+item1.ID, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		var rev BreakGlassReview
		if err := json.Unmarshal(rec.Body.Bytes(), &rev); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}
		if rev.ActionID != item1.ActionID {
			t.Fatalf("expected action %q, got %q", item1.ActionID, rev.ActionID)
		}

		// Tenant isolation: requesting otherOrg's item must 404
		reqIso := httptest.NewRequest("GET", "/v1/break-glass/"+itemOther.ID, nil)
		recIso := httptest.NewRecorder()
		srv.ServeHTTP(recIso, reqIso)
		if recIso.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for cross-org review, got %d", recIso.Code)
		}
	})

	// 3. POST /v1/break-glass/{id}/review - validation errors
	t.Run("ReviewValidation", func(t *testing.T) {
		// Empty reason
		body, _ := json.Marshal(reviewBreakGlassBody{Reason: "", ReviewLatencyMs: 5000})
		req := httptest.NewRequest("POST", "/v1/break-glass/"+item1.ID+"/review", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for empty reason, got %d", rec.Code)
		}

		// Reason exceeds 280 chars
		bodyLong, _ := json.Marshal(reviewBreakGlassBody{Reason: strings.Repeat("A", 281), ReviewLatencyMs: 5000})
		reqLong := httptest.NewRequest("POST", "/v1/break-glass/"+item1.ID+"/review", bytes.NewReader(bodyLong))
		reqLong.Header.Set("Content-Type", "application/json")
		recLong := httptest.NewRecorder()
		srv.ServeHTTP(recLong, reqLong)
		if recLong.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for >280 char reason, got %d", recLong.Code)
		}
	})

	// 4. POST /v1/break-glass/{id}/review - CH-13 rubber-stamping check (< 1,000 ms)
	t.Run("ReviewRapidLatencyCH13", func(t *testing.T) {
		body, _ := json.Marshal(reviewBreakGlassBody{
			Reason:          "Quick sign-off during handover",
			ReviewLatencyMs: 450, // 450 ms < 1,000 ms threshold
		})
		req := httptest.NewRequest("POST", "/v1/break-glass/"+item1.ID+"/review", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var rev BreakGlassReview
		if err := json.Unmarshal(rec.Body.Bytes(), &rev); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if !rev.FlaggedRubberStamping {
			t.Fatal("expected FlaggedRubberStamping=true for 450 ms review (CH-13)")
		}
		if rev.Status != ReviewStatusFlagged {
			t.Fatalf("expected status 'flagged', got %q", rev.Status)
		}
		if !strings.Contains(rev.AuditNotice, "CH-13") {
			t.Fatalf("expected CH-13 audit notice, got %q", rev.AuditNotice)
		}
		if rev.ReviewedBy != userID.String() {
			t.Fatalf("expected reviewer %q, got %q", userID.String(), rev.ReviewedBy)
		}
	})

	// 5. POST /v1/break-glass/{id}/review - Normal latency (>= 1,000 ms)
	t.Run("ReviewNormalLatency", func(t *testing.T) {
		item2 := &BreakGlassReview{
			ID:               "bgr_act_02icu",
			ActionID:         "act_02icu",
			OrgID:            orgID,
			RuleID:           "break-glass",
			EmergencyReason:  "Cardiogenic shock rapid intubation",
			Ward:             "ICU",
			ClinicianSubject: "sha256:doc2",
			Status:           ReviewStatusPending,
			CreatedAt:        time.Now().UTC().Add(-1 * time.Hour),
		}
		if err := bg.Enqueue(item2); err != nil {
			t.Fatalf("enqueue item2 failed: %v", err)
		}

		body, _ := json.Marshal(reviewBreakGlassBody{
			Reason:          "Reviewed patient vitals, intubation medication verified appropriately by attending",
			ReviewLatencyMs: 14200, // 14.2 seconds
		})
		req := httptest.NewRequest("POST", "/v1/break-glass/"+item2.ID+"/review", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var rev BreakGlassReview
		if err := json.Unmarshal(rec.Body.Bytes(), &rev); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if rev.FlaggedRubberStamping {
			t.Fatal("normal latency review should not be flagged")
		}
		if rev.Status != ReviewStatusClosed {
			t.Fatalf("expected status 'closed', got %q", rev.Status)
		}
	})

	// 6. GET /v1/break-glass/digest - Markdown and JSON formats (CH-07)
	t.Run("WeeklyDigest", func(t *testing.T) {
		// Markdown
		reqMD := httptest.NewRequest("GET", "/v1/break-glass/digest?window_days=7", nil)
		recMD := httptest.NewRecorder()
		srv.ServeHTTP(recMD, reqMD)

		if recMD.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", recMD.Code)
		}
		md := recMD.Body.String()
		if !strings.Contains(md, "Emergency Break-Glass Weekly Clinical Audit Digest (CH-07)") {
			t.Fatalf("digest missing CH-07 header: %s", md)
		}
		if !strings.Contains(md, "FLAGGED CH-13") {
			t.Fatalf("digest missing FLAGGED CH-13 marker: %s", md)
		}
		if !strings.Contains(md, "act_01jkm99trauma") {
			t.Fatalf("digest missing action id: %s", md)
		}

		// JSON
		reqJSON := httptest.NewRequest("GET", "/v1/break-glass/digest?format=json&window_days=7", nil)
		recJSON := httptest.NewRecorder()
		srv.ServeHTTP(recJSON, reqJSON)

		if recJSON.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", recJSON.Code)
		}
		var d WeeklyDigest
		if err := json.Unmarshal(recJSON.Body.Bytes(), &d); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if d.TotalBreakGlass != 2 {
			t.Fatalf("expected 2 total break-glass, got %d", d.TotalBreakGlass)
		}
		if d.ClosedReviews != 2 {
			t.Fatalf("expected 2 closed reviews, got %d", d.ClosedReviews)
		}
		if d.FlaggedRubberStamping != 1 {
			t.Fatalf("expected 1 flagged rubber stamping, got %d", d.FlaggedRubberStamping)
		}
	})
}
