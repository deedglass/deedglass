//go:build integration

package approve_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ledger"
)

// S40 §3.5: notify: [] sends nothing; the approval waits in the Console and API.
func TestNotifyNone(t *testing.T) {
	w := newWorld(t)
	a := w.pause("silent", "")
	b := w.pause("approvals", "")
	deadline := time.Now().Add(5 * time.Second)
	for w.count(`SELECT count(*) FROM deedglass.approvals WHERE action_id = $1 AND notified_count > 0`, b.ID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the ordinary approval was never notified")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := w.count(`SELECT count(*) FROM deedglass.witness_jobs WHERE action_id = $1 AND kind = 'approval_notify'`, a.ID); n != 0 {
		t.Fatalf("%d notify jobs for notify: []", n)
	}
	if n := w.count(`SELECT count(*) FROM deedglass.approvals WHERE action_id = $1 AND notified_count > 0`, a.ID); n != 0 {
		t.Fatal("notify: [] was notified")
	}
	for _, n := range w.notes.notices {
		if n.ActionID == a.ID {
			t.Fatal("a notifier saw the silent approval")
		}
	}
	// It still waits in the API and can be decided.
	if _, err := w.decide("priya", a.Approval.ID, approve.VoteApprove, ""); err != nil {
		t.Fatal(err)
	}
	if v := w.waitAction(a.ID, sealed); v.Status != ledger.StatusExecuted {
		t.Fatalf("status %s", v.Status)
	}
}

// S40 §3.4: the countersign schedules approval_resume at not_before; the context binds the schedule.
func TestScheduledApprovalRunsAtNotBefore(t *testing.T) {
	w := newWorld(t)
	nb := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	a, err := w.p.Submit(bg, w.agentPr(), gate.ActionRequest{Tool: "fake.write", Args: json.RawMessage(`{"rows":1,"table":"approvals"}`),
		WaitSet: true, Wait: time.Second, Schedule: &gate.Schedule{NotBefore: nb, NotAfter: nb.Add(2 * time.Hour)}})
	if err != nil || a.Approval == nil {
		t.Fatalf("%v", err)
	}
	full, err := w.ap.Get(bg, approve.Viewer{Kind: httpx.KindAdminKey, Org: w.org}, a.Approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(full.Context)
	if !strings.Contains(string(raw), `"schedule"`) || !strings.Contains(string(raw), "Runs ") {
		t.Fatalf("context has no schedule: %s", raw)
	}
	if _, err := w.decide("priya", a.Approval.ID, approve.VoteApprove, ""); err != nil {
		t.Fatal(err)
	}
	var runAfter time.Time
	if err := w.e.Admin(t).QueryRow(bg, `SELECT run_after FROM deedglass.witness_jobs WHERE action_id = $1 AND kind = 'approval_resume'`, a.ID).Scan(&runAfter); err != nil {
		t.Fatal(err)
	}
	if !runAfter.Equal(nb) {
		t.Fatalf("run_after %s, want %s", runAfter, nb)
	}
	time.Sleep(300 * time.Millisecond)
	if w.fake.Calls() != 0 {
		t.Fatal("ran before not_before")
	}
}
