package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/ledger"
)

var updateFollow = flag.Bool("update-follow", false, "rewrite testdata/cli/actions-follow.txt")

// TestFollowGolden pins the actions list --follow line format (S15 §5) with a fixed clock.
func TestFollowGolden(t *testing.T) {
	saved := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = saved })
	at := func(s string) ledger.Time {
		tm, _ := time.Parse(time.RFC3339, "2026-10-05T"+s+"Z")
		return ledger.Time(tm)
	}
	sp := func(s string) *string { return &s }
	i64 := func(n int64) *int64 { return &n }
	acts := []ledger.Action{
		{ID: "act_01j9zw3k5m7p9r1t3v5x7z9b1d", CreatedAt: at("10:14:03"), Tool: "orders.execute_sql", State: "sealed", Status: ledger.StatusExecuted,
			Policy: ledger.ActionPolicy{RuleID: sp("allow-order-reads")}, Execution: &ledger.ActionExecution{LatencyMS: i64(41)},
			Witness: &ledger.ActionWitness{Level: "L0", Verdict: "VERIFIED"}},
		{ID: "act_01j9zw4m5m7p9r1t3v5x7z9b1d", CreatedAt: at("10:14:21"), Tool: "orders.execute_sql", State: "sealed", Status: ledger.StatusDenied,
			Policy: ledger.ActionPolicy{RuleID: sp("block-destructive-sql")}, Deed: sp("deed_01j9zw4m5m7p9r1t3v5x7z9b1e")},
		{ID: "act_01j9zw5p5m7p9r1t3v5x7z9b1d", CreatedAt: at("10:14:37"), Tool: "orders.execute_sql", State: "sealed", Status: ledger.StatusDenied,
			Policy: ledger.ActionPolicy{RuleID: sp("no-deletes-on-orders")}, Deed: sp("deed_01j9zw5p5m7p9r1t3v5x7z9b1e")},
		{ID: "act_01j9zw6q5m7p9r1t3v5x7z9b1d", CreatedAt: at("10:14:52"), Tool: "orders.execute_sql", State: "pending_approval", Status: ledger.StatusPendingApproval,
			Policy: ledger.ActionPolicy{RuleID: sp("orders-writes-need-approval")}, Approval: &ledger.ActionApproval{ID: "apr_01j9zw6q5m7p9r1t3v5x7z9b1f"}},
		{ID: "act_01j9zw7r5m7p9r1t3v5x7z9b1d", CreatedAt: at("10:15:10"), Tool: "everything.hang", State: "sealed", Status: ledger.StatusExecuted,
			Execution: &ledger.ActionExecution{LatencyMS: i64(30002)}, Witness: &ledger.ActionWitness{Level: "L0", Verdict: "UNVERIFIABLE"}},
		{ID: "act_01j9zw8s5m7p9r1t3v5x7z9b1d", CreatedAt: at("10:15:30"), Tool: "everything.echo", State: "sealed", Status: ledger.StatusFailed,
			Execution: &ledger.ActionExecution{ErrorCode: sp("connector_unavailable")}},
	}
	var b bytes.Buffer
	for _, a := range acts {
		writeFollowLine(&b, ui.Style{}, a)
	}
	p := filepath.Join("..", "..", "testdata", "cli", "actions-follow.txt")
	if *updateFollow {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(want, b.Bytes()) {
		t.Fatalf("differs from %s (-update-follow):\n%s", p, b.String())
	}
	for _, a := range acts {
		if k, ok := followKey(a); !ok || k == "" {
			t.Fatalf("%s: not followed", a.ID)
		}
	}
	if _, ok := followKey(ledger.Action{State: "executing", Status: ledger.StatusAuthorized}); ok {
		t.Fatal("an action still running is printed")
	}
}
