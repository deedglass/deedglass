package approve

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// ReviewStatus indicates the lifecycle stage of a break-glass post-hoc review.
type ReviewStatus string

const (
	ReviewStatusPending ReviewStatus = "pending_review"
	ReviewStatusClosed  ReviewStatus = "closed"
	ReviewStatusFlagged ReviewStatus = "flagged"
)

// BreakGlassReview tracks an emergency break-glass action requiring post-hoc clinical review (HC-P08).
type BreakGlassReview struct {
	ID                    string       `json:"id"`
	ActionID              string       `json:"action_id"`
	OrgID                 ids.OrgID    `json:"org_id"`
	RuleID                string       `json:"rule_id"`
	EmergencyReason       string       `json:"emergency_reason"`  // from Deedglass-Emergency header (1–280 chars)
	ClinicianSubject      string       `json:"clinician_subject"` // safe salted digest or user ref
	Ward                  string       `json:"ward,omitempty"`    // clinical duty ward (e.g. "Emergency Department")
	Status                ReviewStatus `json:"status"`            // pending_review | closed | flagged
	CreatedAt             time.Time    `json:"created_at"`
	ClosedAt              *time.Time   `json:"closed_at,omitempty"`
	ReviewedBy            string       `json:"reviewed_by,omitempty"`   // reviewer user ID or email
	ReviewReason          string       `json:"review_reason,omitempty"` // mandatory justification to close (1–280 chars)
	ReviewLatencyMs       int64        `json:"review_latency_ms"`       // time in ms between presentation and review submission
	FlaggedRubberStamping bool         `json:"flagged_rubber_stamping"` // CH-13: true if review latency < 1,000 ms
	AuditNotice           string       `json:"audit_notice,omitempty"`
}

// CloseReviewInput is the required input to close a post-hoc break-glass review.
type CloseReviewInput struct {
	ReviewID        string
	Reviewer        string // reviewer user ID or email
	Reason          string // mandatory reason (1–280 chars)
	ReviewLatencyMs int64  // latency in ms
}

// BreakGlassQueue is a review queue for emergency break-glass actions (HC-P08).
type BreakGlassQueue struct {
	mu    sync.RWMutex
	items map[string]*BreakGlassReview
}

// NewBreakGlassQueue creates a new break-glass review queue.
func NewBreakGlassQueue() *BreakGlassQueue {
	return &BreakGlassQueue{
		items: make(map[string]*BreakGlassReview),
	}
}

// Enqueue adds an emergency break-glass action to the post-hoc review queue.
func (q *BreakGlassQueue) Enqueue(item *BreakGlassReview) error {
	if item == nil || item.ActionID == "" {
		return errors.New("breakglass: action_id is required")
	}
	if item.RuleID == "" {
		item.RuleID = "break-glass"
	}
	if item.EmergencyReason == "" {
		item.EmergencyReason = "Emergency clinical override"
	}
	if len(item.EmergencyReason) > 280 {
		return errors.New("breakglass: emergency reason exceeds 280 characters")
	}
	if item.Status == "" {
		item.Status = ReviewStatusPending
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	if item.ID == "" {
		item.ID = fmt.Sprintf("bgr_%s", item.ActionID)
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	q.items[item.ID] = item
	return nil
}

// CloseReview closes a pending post-hoc review. It requires a non-empty reviewer reason.
// If review latency is under 1,000 ms, it flags the review under CH-13 (rubber-stamping prevention).
func (q *BreakGlassQueue) CloseReview(in CloseReviewInput) (*BreakGlassReview, error) {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, errors.New("breakglass: reviewer reason is required to close review (HC-P08)")
	}
	if len(reason) > 280 {
		return nil, errors.New("breakglass: reviewer reason exceeds 280 characters")
	}
	reviewer := strings.TrimSpace(in.Reviewer)
	if reviewer == "" {
		return nil, errors.New("breakglass: reviewer identity is required")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	item, ok := q.items[in.ReviewID]
	if !ok {
		return nil, fmt.Errorf("breakglass: review %q not found", in.ReviewID)
	}
	if item.Status == ReviewStatusClosed {
		return nil, fmt.Errorf("breakglass: review %q is already closed", in.ReviewID)
	}

	now := time.Now().UTC()
	item.ClosedAt = &now
	item.ReviewedBy = reviewer
	item.ReviewReason = reason
	item.ReviewLatencyMs = in.ReviewLatencyMs

	// CH-13: Rapid review (< 1,000 ms) is flagged for CMO quality audit
	if in.ReviewLatencyMs > 0 && in.ReviewLatencyMs < 1000 {
		item.FlaggedRubberStamping = true
		item.Status = ReviewStatusFlagged
		item.AuditNotice = fmt.Sprintf("QUALITY ALERT: Clinician review completed in %d ms (< 1,000 ms threshold); flagged for rubber-stamping audit (CH-13).", in.ReviewLatencyMs)
	} else {
		item.Status = ReviewStatusClosed
		item.AuditNotice = "Post-hoc break-glass review closed with verified clinician justification."
	}

	return item, nil
}

// Get returns a review by ID.
func (q *BreakGlassQueue) Get(id string) (*BreakGlassReview, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	item, ok := q.items[id]
	if !ok {
		return nil, fmt.Errorf("breakglass: review %q not found", id)
	}
	cp := *item
	return &cp, nil
}

// List returns reviews for the given org, optionally filtered by status.
// If status is empty or "all", all reviews for the org are returned.
// Results are ordered by CreatedAt descending, then by ID.
func (q *BreakGlassQueue) List(org ids.OrgID, status ReviewStatus) []*BreakGlassReview {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var out []*BreakGlassReview
	for _, it := range q.items {
		if org != (ids.OrgID{}) && it.OrgID != org {
			continue
		}
		if status != "" && status != "all" && it.Status != status {
			continue
		}
		cp := *it
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// ListPending returns all reviews awaiting post-hoc clinician sign-off.
func (q *BreakGlassQueue) ListPending(org ids.OrgID) []*BreakGlassReview {
	return q.List(org, ReviewStatusPending)
}

// WeeklyDigest represents the CMO safety audit digest for break-glass actions (CH-07).
type WeeklyDigest struct {
	OrgID                 ids.OrgID           `json:"org_id"`
	WindowStart           time.Time           `json:"window_start"`
	WindowEnd             time.Time           `json:"window_end"`
	TotalBreakGlass       int                 `json:"total_break_glass"`
	PendingReviews        int                 `json:"pending_reviews"`
	ClosedReviews         int                 `json:"closed_reviews"`
	FlaggedRubberStamping int                 `json:"flagged_rubber_stamping"`
	Reviews               []*BreakGlassReview `json:"reviews"`
}

// GenerateWeeklyDigest aggregates break-glass actions over a review window.
func (q *BreakGlassQueue) GenerateWeeklyDigest(org ids.OrgID, windowStart, windowEnd time.Time) WeeklyDigest {
	q.mu.RLock()
	defer q.mu.RUnlock()

	digest := WeeklyDigest{
		OrgID:       org,
		WindowStart: windowStart,
		WindowEnd:   windowEnd,
	}

	for _, it := range q.items {
		if org != (ids.OrgID{}) && it.OrgID != org {
			continue
		}
		if it.CreatedAt.Before(windowStart) || it.CreatedAt.After(windowEnd) {
			continue
		}
		digest.TotalBreakGlass++
		digest.Reviews = append(digest.Reviews, it)
		switch it.Status {
		case ReviewStatusPending:
			digest.PendingReviews++
		case ReviewStatusClosed:
			digest.ClosedReviews++
		case ReviewStatusFlagged:
			digest.ClosedReviews++
			digest.FlaggedRubberStamping++
		}
	}
	return digest
}

// FormatWeeklyDigest generates a formatted clinical safety audit summary for CMO review.
func FormatWeeklyDigest(d WeeklyDigest) string {
	var b strings.Builder
	b.WriteString("# Emergency Break-Glass Weekly Clinical Audit Digest (CH-07)\n\n")
	b.WriteString(fmt.Sprintf("**Window:** %s to %s (UTC)\n", d.WindowStart.Format("2006-01-02 15:04"), d.WindowEnd.Format("2006-01-02 15:04")))
	b.WriteString(fmt.Sprintf("**Total Emergency Overrides:** %d\n", d.TotalBreakGlass))
	b.WriteString(fmt.Sprintf("**Pending Post-Hoc Reviews:** %d\n", d.PendingReviews))
	b.WriteString(fmt.Sprintf("**Closed Reviews:** %d\n", d.ClosedReviews))
	b.WriteString(fmt.Sprintf("**Flagged Rubber-Stamping Overrides (< 1,000 ms, CH-13):** %d\n\n", d.FlaggedRubberStamping))

	if len(d.Reviews) == 0 {
		b.WriteString("No emergency break-glass actions recorded during this period.\n")
		return b.String()
	}

	b.WriteString("### Detailed Incident Log\n\n")
	b.WriteString("| Action ID | Emergency Reason | Ward | Status | Latency | Reviewer | Justification |\n")
	b.WriteString("|:---|:---|:---|:---|:---|:---|:---|\n")
	for _, r := range d.Reviews {
		latStr := fmt.Sprintf("%d ms", r.ReviewLatencyMs)
		if r.FlaggedRubberStamping {
			latStr += " [FLAGGED CH-13]"
		}
		note := r.ReviewReason
		if note == "" {
			note = "Awaiting review"
		}
		rev := r.ReviewedBy
		if rev == "" {
			rev = "Unassigned"
		}
		b.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s | %s | %s | %s |\n",
			r.ActionID, r.EmergencyReason, r.Ward, r.Status, latStr, rev, note))
	}
	return b.String()
}
