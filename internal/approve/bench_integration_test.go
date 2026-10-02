//go:build integration

package approve_test

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ledger"
)

func pct(d []time.Duration, p int) float64 {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return float64(s[(len(s)-1)*p/100].Microseconds()) / 1000
}

// TestApprovalsBudget is S22 §3.12 on a local Postgres: submit → pending (no estimate: fake.write
// has none), Decide, decide → the worker picks the action up (NOTIFY), and the approval long poll's
// wake after a decision. make bench-approvals runs it and keeps the raw output in bench/results/.
func TestApprovalsBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("a budget run: make bench-approvals")
	}
	w := newWorld(t)
	const warm, n = 20, 300
	var pend, dec, pickup, wake []time.Duration
	agent := approve.Viewer{Kind: httpx.KindAgentKey, Org: w.org, AgentID: w.agent.ID}
	for i := range warm + n {
		t0 := time.Now()
		a := w.pause("approvals", "")
		tPend := time.Since(t0)
		dg := w.digest(a.Approval.ID)
		woke := make(chan time.Time, 1)
		go func() {
			_, _ = w.ap.Wait(bg, agent, a.Approval.ID, 10*time.Second)
			woke <- time.Now()
		}()
		time.Sleep(20 * time.Millisecond) // the wait is parked
		t1 := time.Now()
		if _, err := w.ap.Decide(bg, approve.DecideInput{Org: w.org, ApprovalID: a.Approval.ID, Vote: approve.VoteApprove, ContextDigest: dg,
			Approver: approve.Approver{UserID: w.people["priya"], Via: approve.ViaConsole}}); err != nil {
			t.Fatal(err)
		}
		t2 := time.Now()
		var tPick time.Duration
		for deadline := t2.Add(10 * time.Second); ; {
			v, err := w.led.ReadActionView(bg, w.org, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if v.State != "pending_approval" && v.Status != ledger.StatusPendingApproval && v.State != "authorized" {
				tPick = time.Since(t2)
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("not picked up: %s %s", v.State, v.Status)
			}
			time.Sleep(2 * time.Millisecond)
		}
		at := <-woke
		if i >= warm {
			pend, dec, pickup, wake = append(pend, tPend), append(dec, t2.Sub(t1)), append(pickup, tPick), append(wake, at.Sub(t2))
		}
	}
	perf := map[string]any{"benchmark": "TestApprovalsBudget", "approvals": n, "race": raceEnabled,
		"submit_pending_p50_ms": pct(pend, 50), "submit_pending_p99_ms": pct(pend, 99),
		"decide_p50_ms": pct(dec, 50), "decide_p95_ms": pct(dec, 95),
		"decide_to_pickup_p50_ms": pct(pickup, 50), "decide_to_pickup_p95_ms": pct(pickup, 95),
		"wait_wake_p50_ms": pct(wake, 50), "wait_wake_p99_ms": pct(wake, 99),
		"targets": "submit p50 < 15 ms, p99 < 40 ms; decide p95 < 50 ms; pickup p95 < 1 s; wake < 250 ms"}
	b, _ := json.Marshal(perf)
	t.Logf("PERF %s", b)
	if pct(pend, 50) > 15 || pct(pend, 99) > 40 || pct(dec, 95) > 50 || pct(pickup, 95) > 1000 || pct(wake, 99) > 250 {
		t.Errorf("over budget: %s", b)
	}
}
