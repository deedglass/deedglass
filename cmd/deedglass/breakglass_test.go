package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

func TestCLIBreakGlassList(t *testing.T) {
	orgID := ids.New[ids.OrgKind]()
	reviews := []*approve.BreakGlassReview{
		{
			ID:              "bgr_01jkm99trauma",
			ActionID:        "act_01jkm99trauma",
			OrgID:           orgID,
			RuleID:          "break-glass",
			EmergencyReason: "Acute polytrauma resuscitation",
			Ward:            "Emergency Medicine",
			Status:          approve.ReviewStatusPending,
			CreatedAt:       time.Now().UTC(),
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/break-glass" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reviews": reviews,
			"count":   len(reviews),
		})
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	env := &Env{
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: func(string) string { return "" },
		Globals: Globals{
			Server: srv.URL,
			APIKey: "k_test_admin_key",
		},
		Style: ui.Style{Color: false},
	}

	// 1. Tabular output
	if err := runApprovalsBreakGlassList(context.Background(), env, nil); err != nil {
		t.Fatalf("runApprovalsBreakGlassList failed: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "bgr_01jkm99trauma") {
		t.Fatalf("expected bgr_01jkm99trauma in output, got: %s", out)
	}
	if !strings.Contains(out, "Emergency Medicine") {
		t.Fatalf("expected ward in output, got: %s", out)
	}

	// 2. JSON output
	stdout.Reset()
	env.Globals.JSON = true
	if err := runApprovalsBreakGlassList(context.Background(), env, nil); err != nil {
		t.Fatalf("runApprovalsBreakGlassList JSON failed: %v", err)
	}
	var jsonResp map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &jsonResp); err != nil {
		t.Fatalf("failed to unmarshal JSON output: %v", err)
	}
	if jsonResp["count"].(float64) != 1 {
		t.Fatalf("expected count 1, got %v", jsonResp["count"])
	}
}

func TestCLIBreakGlassReview(t *testing.T) {
	orgID := ids.New[ids.OrgKind]()
	closedReview := approve.BreakGlassReview{
		ID:                    "bgr_01jkm99trauma",
		ActionID:              "act_01jkm99trauma",
		OrgID:                 orgID,
		RuleID:                "break-glass",
		EmergencyReason:       "Acute polytrauma resuscitation",
		Ward:                  "Emergency Medicine",
		Status:                approve.ReviewStatusFlagged,
		ReviewedBy:            "clinician-reviewer",
		ReviewReason:          "Post-hoc attending verification",
		ReviewLatencyMs:       550,
		FlaggedRubberStamping: true,
		AuditNotice:           "QUALITY ALERT: Clinician review completed in 550 ms (< 1,000 ms threshold); flagged for rubber-stamping audit (CH-13).",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/break-glass/bgr_01jkm99trauma/review" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(closedReview)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	env := &Env{
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: func(string) string { return "" },
		Globals: Globals{
			Server: srv.URL,
			APIKey: "k_test_admin_key",
		},
		Style: ui.Style{Color: false},
	}

	// 1. Missing review ID
	err := runApprovalsBreakGlassReview(context.Background(), env, []string{"--reason", "Valid reason"})
	if err == nil {
		t.Fatal("expected error when review ID is omitted")
	}

	// 2. Missing reason
	err = runApprovalsBreakGlassReview(context.Background(), env, []string{"bgr_01jkm99trauma"})
	if err == nil {
		t.Fatal("expected error when --reason is omitted")
	}

	// 3. Successful review submission with CH-13 warning in output
	stdout.Reset()
	args := []string{"bgr_01jkm99trauma", "--reason", "Post-hoc attending verification", "--latency-ms", "550"}
	if err := runApprovalsBreakGlassReview(context.Background(), env, args); err != nil {
		t.Fatalf("runApprovalsBreakGlassReview failed: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "Closed review bgr_01jkm99trauma with status flagged") {
		t.Fatalf("expected flagged status line, got: %s", out)
	}
	if !strings.Contains(out, "CH-13") {
		t.Fatalf("expected CH-13 audit notice in output, got: %s", out)
	}
}

func TestCLIBreakGlassDigest(t *testing.T) {
	sampleMD := "# Emergency Break-Glass Weekly Clinical Audit Digest (CH-07)\n\n**Total Emergency Overrides:** 1\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/break-glass/digest" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("format") == "json" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_break_glass": 1,
				"closed_reviews":    1,
			})
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(sampleMD))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	env := &Env{
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: func(string) string { return "" },
		Globals: Globals{
			Server: srv.URL,
			APIKey: "k_test_admin_key",
		},
		Style: ui.Style{Color: false},
	}

	// 1. Markdown digest output
	if err := runApprovalsBreakGlassDigest(context.Background(), env, nil); err != nil {
		t.Fatalf("runApprovalsBreakGlassDigest failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "Emergency Break-Glass Weekly Clinical Audit Digest (CH-07)") {
		t.Fatalf("expected markdown digest, got: %s", stdout.String())
	}

	// 2. JSON digest output
	stdout.Reset()
	env.Globals.JSON = true
	if err := runApprovalsBreakGlassDigest(context.Background(), env, nil); err != nil {
		t.Fatalf("runApprovalsBreakGlassDigest JSON failed: %v", err)
	}
	var jsonResp map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &jsonResp); err != nil {
		t.Fatalf("failed to unmarshal JSON digest: %v", err)
	}
	if jsonResp["total_break_glass"].(float64) != 1 {
		t.Fatalf("expected total 1, got %v", jsonResp["total_break_glass"])
	}
}

// Silence unused import in case
var _ = deedglass.Client{}
