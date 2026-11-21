package approve

import (
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

func TestBreakGlassQueue(t *testing.T) {
	q := NewBreakGlassQueue()
	orgID := ids.New[ids.OrgKind]()

	// 1. Enqueue without ActionID must fail
	if err := q.Enqueue(&BreakGlassReview{}); err == nil {
		t.Fatal("expected error on empty ActionID")
	}

	// 2. Enqueue emergency break-glass action
	item1 := &BreakGlassReview{
		ActionID:         "act_01jkm99trauma",
		OrgID:            orgID,
		EmergencyReason:  "Acute polytrauma resuscitation in ED Bay 2; immediate sedative required",
		Ward:             "Emergency Medicine",
		ClinicianSubject: "sha256:salted_doc_digest",
	}
	if err := q.Enqueue(item1); err != nil {
		t.Fatalf("failed to enqueue breakglass item: %v", err)
	}

	// Verify defaults
	if item1.RuleID != "break-glass" {
		t.Fatalf("expected rule_id 'break-glass', got %q", item1.RuleID)
	}
	if item1.Status != ReviewStatusPending {
		t.Fatalf("expected status 'pending_review', got %q", item1.Status)
	}

	// 3. ListPending returns item1
	pending := q.ListPending(orgID)
	if len(pending) != 1 || pending[0].ActionID != "act_01jkm99trauma" {
		t.Fatalf("expected 1 pending item, got %d", len(pending))
	}

	// 4. Closing without reason must fail (HC-P08)
	_, err := q.CloseReview(CloseReviewInput{
		ReviewID: item1.ID,
		Reviewer: "dr.rao@kmc.edu",
		Reason:   "",
	})
	if err == nil {
		t.Fatal("expected error when closing review without reason")
	}

	// 5. Normal close (latency >= 1,000 ms)
	closed1, err := q.CloseReview(CloseReviewInput{
		ReviewID:        item1.ID,
		Reviewer:        "dr.rao@kmc.edu",
		Reason:          "Reviewed ED nursing chart; resuscitation protocol appropriately triggered under verbal order",
		ReviewLatencyMs: 8500, // 8.5 seconds
	})
	if err != nil {
		t.Fatalf("failed to close review: %v", err)
	}
	if closed1.Status != ReviewStatusClosed {
		t.Fatalf("expected status 'closed', got %q", closed1.Status)
	}
	if closed1.FlaggedRubberStamping {
		t.Fatal("normal review should not be flagged for rubber-stamping")
	}
	if closed1.ClosedAt == nil {
		t.Fatal("closed_at must be populated")
	}

	// 6. Test CH-13 rubber-stamping detection: review latency < 1,000 ms
	item2 := &BreakGlassReview{
		ActionID:        "act_01jkm100fast",
		OrgID:           orgID,
		EmergencyReason: "Cardiogenic shock rapid intubation",
		Ward:            "ICU",
	}
	if err := q.Enqueue(item2); err != nil {
		t.Fatalf("failed to enqueue item2: %v", err)
	}

	closed2, err := q.CloseReview(CloseReviewInput{
		ReviewID:        item2.ID,
		Reviewer:        "dr.sharma@hospital.test",
		Reason:          "Approved rapidly during shift change",
		ReviewLatencyMs: 340, // 340 ms < 1,000 ms threshold
	})
	if err != nil {
		t.Fatalf("failed to close item2: %v", err)
	}
	if !closed2.FlaggedRubberStamping {
		t.Fatal("expected FlaggedRubberStamping = true for 340 ms review latency (CH-13)")
	}
	if closed2.Status != ReviewStatusFlagged {
		t.Fatalf("expected status 'flagged', got %q", closed2.Status)
	}
	if !strings.Contains(closed2.AuditNotice, "CH-13") {
		t.Fatalf("expected CH-13 audit notice, got %q", closed2.AuditNotice)
	}

	// 7. Verify Weekly Digest (CH-07)
	now := time.Now().UTC()
	digest := q.GenerateWeeklyDigest(orgID, now.Add(-24*time.Hour), now.Add(24*time.Hour))
	if digest.TotalBreakGlass != 2 {
		t.Fatalf("expected 2 total break-glass, got %d", digest.TotalBreakGlass)
	}
	if digest.PendingReviews != 0 {
		t.Fatalf("expected 0 pending reviews, got %d", digest.PendingReviews)
	}
	if digest.ClosedReviews != 2 {
		t.Fatalf("expected 2 closed reviews, got %d", digest.ClosedReviews)
	}
	if digest.FlaggedRubberStamping != 1 {
		t.Fatalf("expected 1 flagged rubber-stamping, got %d", digest.FlaggedRubberStamping)
	}

	report := FormatWeeklyDigest(digest)
	if !strings.Contains(report, "Emergency Break-Glass Weekly Clinical Audit Digest (CH-07)") {
		t.Fatal("digest missing header")
	}
	if !strings.Contains(report, "FLAGGED CH-13") {
		t.Fatal("digest missing FLAGGED CH-13 marker")
	}
	if !strings.Contains(report, "dr.rao@kmc.edu") {
		t.Fatal("digest missing reviewer notes")
	}
}
