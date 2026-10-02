//go:build integration

package approve_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ledger"
)

// S22 §6: the decision table, on PG with the real Gate, Ledger and worker.

func TestApproveRunsOnce(t *testing.T) {
	w := newWorld(t)
	a := w.pause("approvals", "")
	apr := a.Approval.ID
	// The snapshot and its digest; the notify job emails every eligible person (owner, priya, arjun).
	g, err := w.ap.Get(bg, w.viewer("priya"), apr)
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != approve.StatusPending || g.You == nil || !g.You.CanDecide || !strings.HasPrefix(g.Summary, "billing-sync wants to run fake.write") {
		t.Fatalf("pending: %+v you %+v", g, g.You)
	}
	deadline := time.Now().Add(5 * time.Second)
	for w.notes.linkFor(apr, w.people["priya"].String()) == "" {
		if time.Now().After(deadline) {
			t.Fatal("the notify job never sent priya a link")
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := w.decide("priya", apr, approve.VoteApprove, "Looks right")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != approve.StatusApproved || got.ActionStatus != ledger.StatusProcessing || got.Deed == nil {
		t.Fatalf("decide: %+v", got)
	}
	v := w.waitAction(a.ID, sealed)
	if v.Status != ledger.StatusExecuted || w.fake.CallsFor(a.ID) != 1 {
		t.Fatalf("after approval: %s %s, %d calls", v.State, v.Status, w.fake.CallsFor(a.ID))
	}
	ap := w.deedApproval(a.ID)
	if ap == nil || ap.Outcome != "approved" || !slices.Equal(ap.DecidedBy, []string{w.people["priya"].String()}) || ap.Deed == nil || *ap.Deed != *got.Deed {
		t.Fatalf("deed approval section: %+v", ap)
	}
	if n := w.count(`SELECT count(*) FROM deedglass.approval_votes WHERE approval_id = $1`, apr); n != 1 {
		t.Errorf("%d votes", n)
	}
	if n := w.count(`SELECT count(*) FROM deedglass.admin_events WHERE action = 'approval.decided'`); n != 1 {
		t.Errorf("%d approval.decided admin events", n)
	}
	// The links of everyone else are void now.
	if n := w.count(`SELECT count(*) FROM deedglass.approval_tokens WHERE approval_id = $1 AND voided_at IS NULL AND used_at IS NULL`, apr); n != 0 {
		t.Errorf("%d live links after the decision", n)
	}
	// A second decision is approval_decided, naming who and when.
	if _, err := w.decide("arjun", apr, approve.VoteApprove, ""); code(err) != httpx.CodeApprovalDecided ||
		!strings.Contains(err.Error(), "approved by Priya Raman at ") {
		t.Errorf("second decision: %v", err)
	}
}

func TestDecisionTable(t *testing.T) {
	w := newWorld(t)

	t.Run("deny without reason", func(t *testing.T) {
		a := w.pause("approvals", "")
		_, err := w.decide("priya", a.Approval.ID, approve.VoteDeny, "  ")
		if code(err) != httpx.CodeValidationFailed || httpx.AsError(err).Param != "reason" {
			t.Fatalf("%v", err)
		}
		if n := w.count(`SELECT count(*) FROM deedglass.approval_votes WHERE approval_id = $1`, a.Approval.ID); n != 0 {
			t.Errorf("%d votes", n)
		}
	})
	t.Run("viewer not named", func(t *testing.T) {
		a := w.pause("approvals", "")
		if _, err := w.decide("viv", a.Approval.ID, approve.VoteApprove, ""); code(err) != httpx.CodeApproverNotEligible ||
			!strings.Contains(err.Error(), `Rule "writes-need-approval" needs role:admin`) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("named viewer", func(t *testing.T) {
		a := w.pause("sam", "")
		if got, err := w.decide("sam", a.Approval.ID, approve.VoteApprove, ""); err != nil || got.Status != approve.StatusApproved {
			t.Fatalf("%+v %v", got, err)
		}
		w.waitAction(a.ID, sealed)
	})
	t.Run("four-eyes", func(t *testing.T) {
		a := w.pause("four", "")
		apr := a.Approval.ID
		got, err := w.decide("priya", apr, approve.VoteApprove, "")
		if err != nil || got.Status != approve.StatusPending || got.ActionStatus != ledger.StatusPendingApproval {
			t.Fatalf("first: %+v %v", got, err)
		}
		if _, err := w.decide("priya", apr, approve.VoteApprove, ""); code(err) != httpx.CodeAlreadyCountersigned {
			t.Fatalf("again: %v", err)
		}
		if w.fake.CallsFor(a.ID) != 0 {
			t.Fatal("ran after one countersign")
		}
		if got, err = w.decide("arjun", apr, approve.VoteApprove, ""); err != nil || got.Status != approve.StatusApproved {
			t.Fatalf("second: %+v %v", got, err)
		}
		w.waitAction(a.ID, sealed)
		want := []string{w.people["priya"].String(), w.people["arjun"].String()}
		slices.Sort(want)
		if ap := w.deedApproval(a.ID); ap == nil || !slices.Equal(ap.DecidedBy, want) {
			t.Fatalf("decided_by %+v, want %v", ap, want)
		}
		if n := w.count(`SELECT count(*) FROM deedglass.admin_events WHERE action IN ('approval.countersigned', 'approval.decided')
		    AND target_id = $1`, apr); n != 2 {
			t.Errorf("%d admin deeds", n)
		}
		if w.fake.CallsFor(a.ID) != 1 {
			t.Errorf("%d calls", w.fake.CallsFor(a.ID))
		}
	})
	t.Run("veto", func(t *testing.T) {
		a := w.pause("four", "")
		if _, err := w.decide("priya", a.Approval.ID, approve.VoteApprove, ""); err != nil {
			t.Fatal(err)
		}
		got, err := w.decide("arjun", a.Approval.ID, approve.VoteDeny, "Order 42 hasn't been paid yet.")
		if err != nil || got.Status != approve.StatusDenied || got.ActionStatus != ledger.StatusDenied {
			t.Fatalf("%+v %v", got, err)
		}
		v, _ := w.led.ReadActionView(bg, w.org, a.ID)
		if v.State != "sealed" || v.Status != ledger.StatusDenied || w.fake.CallsFor(a.ID) != 0 {
			t.Fatalf("after the veto: %s %s", v.State, v.Status)
		}
		if v.Approval == nil || v.Approval.DecisionReason == nil || *v.Approval.DecisionReason != "Order 42 hasn't been paid yet." {
			t.Errorf("the agent doesn't see the reason: %+v", v.Approval)
		}
		ap := w.deedApproval(a.ID)
		if ap.Outcome != "denied" || !slices.Equal(ap.DecidedBy, []string{w.people["arjun"].String()}) {
			t.Errorf("deed approval %+v", ap)
		}
	})
	t.Run("self", func(t *testing.T) {
		a := w.pause("approvals", w.people["priya"].String())
		if _, err := w.decide("priya", a.Approval.ID, approve.VoteApprove, ""); code(err) != httpx.CodeSelfApproval {
			t.Fatalf("approve: %v", err)
		}
		g, _ := w.ap.Get(bg, w.viewer("priya"), a.Approval.ID)
		if g.You == nil || !g.You.CanDecide || g.You.WhyNot == nil {
			t.Errorf("you: %+v", g.You)
		}
		if got, err := w.decide("priya", a.Approval.ID, approve.VoteDeny, "Not mine to approve"); err != nil || got.Status != approve.StatusDenied {
			t.Fatalf("deny: %+v %v", got, err)
		}
		// An email address names the person too.
		b := w.pause("approvals", "PRIYA@acme.example.test")
		if _, err := w.decide("priya", b.Approval.ID, approve.VoteApprove, ""); code(err) != httpx.CodeSelfApproval {
			t.Fatalf("by email: %v", err)
		}
	})
	t.Run("self allowed", func(t *testing.T) {
		w.settings(`{"approvals": {"self_countersign": "allow"}}`)
		defer w.settings(`{}`)
		a := w.pause("approvals", w.people["priya"].String())
		if got, err := w.decide("priya", a.Approval.ID, approve.VoteApprove, ""); err != nil || got.Status != approve.StatusApproved {
			t.Fatalf("%+v %v", got, err)
		}
		w.waitAction(a.ID, sealed)
	})
	t.Run("stale context", func(t *testing.T) {
		a := w.pause("approvals", "")
		_, err := w.ap.Decide(bg, approve.DecideInput{Org: w.org, ApprovalID: a.Approval.ID, Vote: approve.VoteApprove,
			ContextDigest: "sha256:" + strings.Repeat("0", 64), Approver: approve.Approver{UserID: w.people["priya"], Via: approve.ViaConsole}})
		if code(err) != httpx.CodeContextChanged {
			t.Fatalf("%v", err)
		}
	})
	t.Run("unsatisfiable", func(t *testing.T) {
		before := len(w.notes.notices)
		a, err := w.submit("unsat", "")
		if code(err) != httpx.CodeApprovalUnsatisfiable || a != nil && a.Status != ledger.StatusDenied {
			t.Fatalf("%+v %v", a, err)
		}
		he := httpx.AsError(err)
		v, ok := he.Action.(*ledger.Action)
		if !ok || v.State != "sealed" || !strings.Contains(he.Message, "the rule needs 2 people and only 1 is eligible") {
			t.Fatalf("refusal %+v", he)
		}
		// The deed carries no approval section (a denial there names a person); the approval row
		// records the system's denial.
		if ap := w.deedApproval(v.ID); ap != nil {
			t.Errorf("deed approval %+v", ap)
		}
		if n := w.count(`SELECT count(*) FROM deedglass.approvals WHERE action_id = $1 AND status = 'denied' AND decided_by IS NULL
		    AND decision_reason LIKE 'No one in this org can approve this:%'`, v.ID); n != 1 {
			t.Errorf("%d system-denied approvals", n)
		}
		if n := w.count(`SELECT count(*) FROM deedglass.witness_jobs WHERE action_id = $1`, v.ID); n != 0 {
			t.Errorf("%d jobs for an unsatisfiable approval", n)
		}
		time.Sleep(200 * time.Millisecond)
		w.notes.mu.Lock()
		for _, n := range w.notes.notices[before:] {
			if n.ActionID == v.ID {
				t.Error("an unsatisfiable approval sent email")
			}
		}
		w.notes.mu.Unlock()
	})
	t.Run("stopped before resume", func(t *testing.T) {
		a := w.pause("approvals", "")
		if err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE deedglass.agents SET status = 'stopped' WHERE id = $1`, w.agent.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE deedglass.agents SET status = 'active' WHERE id = $1`, w.agent.ID)
				return err
			})
		}()
		if _, err := w.decide("priya", a.Approval.ID, approve.VoteApprove, ""); err != nil {
			t.Fatal(err)
		}
		v := w.waitAction(a.ID, sealed)
		if v.Status != ledger.StatusDenied || w.fake.CallsFor(a.ID) != 0 {
			t.Fatalf("%s, %d calls", v.Status, w.fake.CallsFor(a.ID))
		}
		if g, _ := w.ap.Get(bg, w.viewer("priya"), a.Approval.ID); g.Status != approve.StatusApproved {
			t.Errorf("approval %s, want approved", g.Status)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		a := w.pause("approvals", "")
		if _, err := w.p.Cancel(bg, w.agentPr(), a.ID); err != nil {
			t.Fatal(err)
		}
		g, _ := w.ap.Get(bg, w.viewer("priya"), a.Approval.ID)
		if g.Status != approve.StatusCancelled {
			t.Errorf("approval %s", g.Status)
		}
		if n := w.count(`SELECT count(*) FROM deedglass.approval_tokens WHERE approval_id = $1 AND voided_at IS NULL AND used_at IS NULL`, a.Approval.ID); n != 0 {
			t.Errorf("%d live links", n)
		}
		if _, err := w.decide("priya", a.Approval.ID, approve.VoteApprove, ""); code(err) != httpx.CodeApprovalDecided {
			t.Errorf("decide after cancel: %v", err)
		}
	})
	t.Run("expiry", func(t *testing.T) {
		a := w.pause("approvals", "")
		w.skew.Store(int64(2 * time.Hour))
		defer w.skew.Store(0)
		if _, err := w.decide("priya", a.Approval.ID, approve.VoteApprove, ""); code(err) != httpx.CodeApprovalExpired {
			t.Fatalf("decide after expiry: %v", err)
		}
		if _, err := w.p.Resume(bg, w.org, aid(t, a.ID)); err != nil {
			t.Fatal(err)
		}
		v, _ := w.led.ReadActionView(bg, w.org, a.ID)
		if v.State != "sealed" || v.Status != ledger.StatusExpired {
			t.Fatalf("%s %s", v.State, v.Status)
		}
		if ap := w.deedApproval(a.ID); ap.Outcome != "expired" || len(ap.DecidedBy) != 0 || ap.Deed != nil {
			t.Errorf("deed approval %+v", ap)
		}
	})
	t.Run("keys never vote", func(t *testing.T) {
		a := w.pause("approvals", "")
		_, err := w.ap.Decide(bg, approve.DecideInput{Org: w.org, ApprovalID: a.Approval.ID, Vote: approve.VoteApprove, ContextDigest: w.digest(a.Approval.ID)})
		if code(err) != httpx.CodeApprovalNeedsPerson {
			t.Fatalf("%v", err)
		}
	})
	t.Run("agent view", func(t *testing.T) {
		a := w.pause("approvals", "")
		g, err := w.ap.Get(bg, approve.Viewer{Kind: httpx.KindAgentKey, Org: w.org, AgentID: w.agent.ID}, a.Approval.ID)
		if err != nil || g.Context != nil || g.Approvers != nil || g.You != nil {
			t.Fatalf("agent view %+v %v", g, err)
		}
		if _, ok := g.Countersigned.(int); !ok {
			t.Errorf("countersigned %T", g.Countersigned)
		}
		other := approve.Viewer{Kind: httpx.KindAgentKey, Org: w.org, AgentID: w.agent.ID}
		other.AgentID = w.newAgent("other-bot")
		if _, err := w.ap.Get(bg, other, a.Approval.ID); code(err) != httpx.CodeNotFound {
			t.Errorf("another agent: %v", err)
		}
	})
}

// TestApprovalGuard: the database keeps status and context honest (DG006).
func TestApprovalGuard(t *testing.T) {
	w := newWorld(t)
	a := w.pause("approvals", "")
	for _, q := range []string{
		`UPDATE deedglass.approvals SET context = '{"v":1}' WHERE id = $1`,
		`UPDATE deedglass.approvals SET expires_at = expires_at + interval '1 hour' WHERE id = $1`,
	} {
		err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, q, a.Approval.ID)
			return err
		})
		if storeCode(err) != "DG006" {
			t.Errorf("%s: %v", q, err)
		}
	}
	if _, err := w.decide("priya", a.Approval.ID, approve.VoteApprove, ""); err != nil {
		t.Fatal(err)
	}
	err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.approvals SET status = 'denied' WHERE id = $1`, a.Approval.ID)
		return err
	})
	if storeCode(err) != "DG006" {
		t.Errorf("re-deciding: %v", err)
	}
	err = w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.approval_votes SET reason = 'x' WHERE approval_id = $1`, a.Approval.ID)
		return err
	})
	if storeCode(err) != "42501" && storeCode(err) != "DG001" {
		t.Errorf("editing a vote: %v", err)
	}
}

// TestDecideRace: two admins approve a required-1 approval at once: one wins, every run.
func TestDecideRace(t *testing.T) {
	w := newWorld(t)
	runs := 200
	if testing.Short() {
		runs = 20
	}
	for i := range runs {
		a := w.pause("approvals", "")
		apr := a.Approval.ID
		dg := w.digest(apr)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j, who := range []string{"priya", "arjun"} {
			wg.Go(func() {
				_, errs[j] = w.ap.Decide(bg, approve.DecideInput{Org: w.org, ApprovalID: apr, Vote: approve.VoteApprove, ContextDigest: dg,
					Approver: approve.Approver{UserID: w.people[who], Via: approve.ViaConsole}})
			})
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case code(err) != httpx.CodeApprovalDecided:
				t.Fatalf("run %d: %v", i, err)
			}
		}
		if ok != 1 {
			t.Fatalf("run %d: %d winners", i, ok)
		}
		if n := w.count(`SELECT count(*) FROM deedglass.approval_votes WHERE approval_id = $1`, apr); n != 1 {
			t.Fatalf("run %d: %d votes", i, n)
		}
	}
	// Every approved action ran exactly once.
	deadline := time.Now().Add(30 * time.Second)
	for w.count(`SELECT count(*) FROM deedglass.actions WHERE state <> 'sealed'`) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("actions never sealed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := w.fake.Calls(); got != int64(runs) {
		t.Errorf("%d connector calls for %d approvals", got, runs)
	}
}

// TestApproveRacesExpiryAndCancel: exactly one outcome.
func TestApproveRacesExpiryAndCancel(t *testing.T) {
	w := newWorld(t)
	for i := range 30 {
		a := w.pause("approvals", "")
		dg := w.digest(a.Approval.ID)
		var wg sync.WaitGroup
		var decErr, canErr error
		wg.Go(func() {
			_, decErr = w.ap.Decide(bg, approve.DecideInput{Org: w.org, ApprovalID: a.Approval.ID, Vote: approve.VoteApprove, ContextDigest: dg,
				Approver: approve.Approver{UserID: w.people["priya"], Via: approve.ViaConsole}})
		})
		wg.Go(func() { _, canErr = w.p.Cancel(bg, w.agentPr(), a.ID) })
		wg.Wait()
		if (decErr == nil) == (canErr == nil) {
			t.Fatalf("run %d: decide %v, cancel %v", i, decErr, canErr)
		}
	}
}
