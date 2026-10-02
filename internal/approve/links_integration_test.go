//go:build integration

package approve_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ledger"
)

// token waits for the notify job to email name a link for apr and returns its token.
func (w *world) token(apr, name string) string {
	w.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if l := w.notes.linkFor(apr, w.people[name].String()); l != "" {
			_, tok, ok := strings.Cut(l, "#t=")
			if !ok {
				w.t.Fatalf("link without a fragment: %s", l)
			}
			return tok
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("no link for %s on %s", name, apr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLinks(t *testing.T) {
	w := newWorld(t)

	t.Run("preview twice then decide once", func(t *testing.T) {
		a := w.pause("approvals", "")
		tok := w.token(a.Approval.ID, "priya")
		for range 2 {
			p, err := w.ap.Preview(bg, tok)
			if err != nil || p.Approval.ID != a.Approval.ID || p.Approval.Context == nil || p.ExpiresClock == "" {
				t.Fatalf("preview: %+v %v", p, err)
			}
		}
		if n := w.count(`SELECT count(*) FROM deedglass.approval_tokens WHERE approval_id = $1 AND used_at IS NOT NULL`, a.Approval.ID); n != 0 {
			t.Fatalf("preview consumed the link (%d used)", n)
		}
		got, err := w.ap.DecideLink(bg, approve.LinkDecision{Token: tok, Vote: approve.VoteApprove, ContextDigest: w.digest(a.Approval.ID)}, "")
		if err != nil || got.Status != approve.StatusApproved {
			t.Fatalf("decide: %+v %v", got, err)
		}
		if cs, _ := got.Countersigned.([]approve.Countersign); len(cs) != 1 || cs[0].Via != approve.ViaEmailLink {
			t.Errorf("countersigned %+v", got.Countersigned)
		}
		_, err = w.ap.DecideLink(bg, approve.LinkDecision{Token: tok, Vote: approve.VoteApprove, ContextDigest: w.digest(a.Approval.ID)}, "")
		if code(err) != httpx.CodeApprovalLinkInvalid {
			t.Fatalf("second decide: %v", err)
		}
		// The used link follows the action until it is sealed.
		deadline := time.Now().Add(10 * time.Second)
		for {
			st, err := w.ap.Status(bg, tok)
			if err != nil {
				t.Fatal(err)
			}
			if st.Action != nil && st.Action.State == "sealed" {
				if st.Action.Execution != nil && st.Action.Execution.Result != nil {
					t.Error("the status carries the tool's result")
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("status never sealed: %+v", st.Action)
			}
			time.Sleep(50 * time.Millisecond)
		}
		// Preview after use: the same 401.
		if _, err := w.ap.Preview(bg, tok); code(err) != httpx.CodeApprovalLinkInvalid {
			t.Errorf("preview after use: %v", err)
		}
	})
	t.Run("a refused decision keeps the link", func(t *testing.T) {
		a := w.pause("approvals", "")
		tok := w.token(a.Approval.ID, "priya")
		_, err := w.ap.DecideLink(bg, approve.LinkDecision{Token: tok, Vote: approve.VoteApprove, ContextDigest: "sha256:" + strings.Repeat("1", 64)}, "")
		if code(err) != httpx.CodeContextChanged {
			t.Fatalf("stale: %v", err)
		}
		if _, err := w.ap.DecideLink(bg, approve.LinkDecision{Token: tok, Vote: approve.VoteDeny, Reason: "Not today",
			ContextDigest: w.digest(a.Approval.ID)}, ""); err != nil {
			t.Fatalf("after a refusal the link still works: %v", err)
		}
	})
	t.Run("one person per link", func(t *testing.T) {
		a := w.pause("approvals", "")
		b := w.pause("approvals", "")
		tokA := w.token(a.Approval.ID, "priya")
		// Priya's link for A decides only A, as Priya, whatever the caller sends.
		got, err := w.ap.DecideLink(bg, approve.LinkDecision{Token: tokA, Vote: approve.VoteApprove, ContextDigest: w.digest(a.Approval.ID)}, "")
		if err != nil {
			t.Fatal(err)
		}
		if cs := got.Countersigned.([]approve.Countersign); cs[0].User.ID != w.people["priya"].String() || got.ID != a.Approval.ID {
			t.Errorf("decided as %+v on %s", cs[0].User, got.ID)
		}
		// A's digest can't decide B through B's link, and A's link can't touch B at all.
		tokB := w.token(b.Approval.ID, "arjun")
		if _, err := w.ap.DecideLink(bg, approve.LinkDecision{Token: tokB, Vote: approve.VoteApprove, ContextDigest: w.digest(a.Approval.ID)}, ""); code(err) != httpx.CodeContextChanged {
			t.Errorf("cross digest: %v", err)
		}
	})
	t.Run("a newer link voids the older", func(t *testing.T) {
		a := w.pause("approvals", "")
		old := w.token(a.Approval.ID, "priya")
		// The notify job runs once; mint a second link the way a resend would.
		if err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE deedglass.approval_tokens SET voided_at = pg_catalog.now()
			    WHERE approval_id = $1 AND user_id = $2 AND used_at IS NULL`, a.Approval.ID, w.people["priya"])
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.ap.Preview(bg, old); code(err) != httpx.CodeApprovalLinkInvalid {
			t.Errorf("voided link: %v", err)
		}
	})
	t.Run("24 hours at most", func(t *testing.T) {
		a := w.pause("approvals", "")
		tok := w.token(a.Approval.ID, "priya")
		var exp time.Time
		if err := w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT expires_at FROM deedglass.approval_tokens WHERE approval_id = $1 AND user_id = $2 AND voided_at IS NULL`,
				a.Approval.ID, w.people["priya"]).Scan(&exp)
		}); err != nil {
			t.Fatal(err)
		}
		if exp.After(time.Now().Add(24*time.Hour+time.Minute)) || exp.After(time.Now().Add(61*time.Minute)) {
			t.Errorf("link expires %v (the approval lives 1 h)", exp)
		}
		w.skew.Store(int64(25 * time.Hour))
		defer w.skew.Store(0)
		if _, err := w.ap.Preview(bg, tok); code(err) != httpx.CodeApprovalLinkInvalid {
			t.Errorf("expired link: %v", err)
		}
	})
	t.Run("review mode", func(t *testing.T) {
		w.settings(`{"approvals": {"email_links": "review"}}`)
		defer w.settings(`{}`)
		a := w.pause("approvals", "")
		tok := w.token(a.Approval.ID, "priya")
		p, err := w.ap.Preview(bg, tok)
		if err != nil || !p.ReviewOnly {
			t.Fatalf("preview %+v %v", p, err)
		}
		if _, err := w.ap.DecideLink(bg, approve.LinkDecision{Token: tok, Vote: approve.VoteApprove, ContextDigest: w.digest(a.Approval.ID)}, ""); code(err) != httpx.CodeApprovalLinkReviewOnly {
			t.Errorf("decide: %v", err)
		}
	})
	t.Run("malformed and unknown look alike", func(t *testing.T) {
		for _, tok := range []string{"", "x", strings.Repeat("A", 43), strings.Repeat("A", 42) + "=", "AAAA" + strings.Repeat("b", 39)} {
			if _, err := w.ap.Preview(bg, tok); code(err) != httpx.CodeApprovalLinkInvalid {
				t.Errorf("%q: %v", tok, err)
			}
		}
	})
	t.Run("timing", func(t *testing.T) {
		a := w.pause("approvals", "")
		used := w.token(a.Approval.ID, "priya")
		if _, err := w.ap.DecideLink(bg, approve.LinkDecision{Token: used, Vote: approve.VoteDeny, Reason: "timing", ContextDigest: w.digest(a.Approval.ID)}, ""); err != nil {
			t.Fatal(err)
		}
		unknown := "Q" + strings.Repeat("u", 41) + "w"
		median := func(tok string) time.Duration {
			var ds []time.Duration
			for range 31 {
				s := time.Now()
				_, _ = w.ap.Preview(bg, tok)
				ds = append(ds, time.Since(s))
			}
			sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
			return ds[len(ds)/2]
		}
		mu, mk := median(used), median(unknown)
		if d := mu - mk; d > 5*time.Millisecond || d < -5*time.Millisecond {
			t.Errorf("used %v vs unknown %v: more than 5 ms apart", mu, mk)
		}
	})
	t.Run("no token in the logs", func(t *testing.T) {
		logMu.Lock()
		logs := w.logs.String()
		logMu.Unlock()
		w.notes.mu.Lock()
		defer w.notes.mu.Unlock()
		for _, n := range w.notes.notices {
			for _, r := range n.Recipients {
				_, tok, _ := strings.Cut(r.Link, "#t=")
				if strings.Contains(logs, tok) || strings.Contains(logs, r.Email) {
					t.Fatal("a token or an email address reached the log")
				}
			}
		}
	})
}

// TestWaitWakes: a wait started on a pending approval returns soon after the decision.
func TestWaitWakes(t *testing.T) {
	w := newWorld(t)
	a := w.pause("approvals", "")
	agent := approve.Viewer{Kind: httpx.KindAgentKey, Org: w.org, AgentID: w.agent.ID}
	done := make(chan time.Time, 1)
	go func() {
		g, err := w.ap.Wait(bg, agent, a.Approval.ID, 20*time.Second)
		if err == nil && g.Status == approve.StatusApproved {
			done <- time.Now()
		}
		close(done)
	}()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	if _, err := w.decide("priya", a.Approval.ID, approve.VoteApprove, ""); err != nil {
		t.Fatal(err)
	}
	at, ok := <-done
	if !ok {
		t.Fatal("the wait didn't return approved")
	}
	if d := at.Sub(start); d > 250*time.Millisecond+100*time.Millisecond {
		t.Errorf("the wait woke %v after the decision", d)
	}
	// The action's own long poll (S13) started while pending returns on the next change.
	b := w.pause("approvals", "")
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.decide("priya", b.Approval.ID, approve.VoteApprove, "")
	}()
	s := time.Now()
	g, err := w.p.Get(bg, w.agentPr(), b.ID, 10*time.Second)
	if err != nil || g.Status == ledger.StatusPendingApproval || time.Since(s) > 5*time.Second {
		t.Errorf("action wait: %v %v after %v", g.Status, err, time.Since(s))
	}
}
