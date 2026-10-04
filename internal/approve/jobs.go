package approve

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/notify"
)

// The approval_notify job (S22 §3.1): after Tx A commits, the worker resolves the approvers, mints
// one link per person (voiding their older ones) and hands the notice to every notifier (the email
// goes through S07's mail queue). It runs once: a retry after the links were minted sends nothing
// again, so a person never gets two emails for one approval.

// NotifyJob is the gate.JobHandler for approval_notify.
func (s *Service) NotifyJob(ctx context.Context, org ids.OrgID, action ids.ActionID, _ int) error {
	var (
		notice notify.ApprovalNotice
		send   bool
	)
	err := s.o.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		send = false
		var (
			apr      ids.ApprovalID
			notified int
		)
		err := tx.QueryRow(ctx, `SELECT id, notified_count FROM deedglass.approvals WHERE org_id = $1 AND action_id = $2 FOR UPDATE`, org, action).
			Scan(&apr, &notified)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		x, err := readRow(ctx, tx, org, apr, false)
		if err != nil {
			return err
		}
		if x.Status != StatusPending || notified > 0 || !s.now().Before(x.ExpiresAt) {
			return nil
		}
		o, err := readOrg(ctx, tx, org)
		if err != nil {
			return err
		}
		all, err := members(ctx, tx, org)
		if err != nil {
			return err
		}
		people, _ := eligiblePeople(all, x.Spec, deref(x.OnBehalfOf), true)
		if len(people) > s.cfg.MaxRecipients {
			people = people[:s.cfg.MaxRecipients]
		}
		votes, err := readVotes(ctx, tx, org, apr)
		if err != nil {
			return err
		}
		notice = s.notice(x, o, votes)
		exp := x.ExpiresAt
		if lim := s.now().Add(s.cfg.LinkMaxTTL); lim.Before(exp) {
			exp = lim
		}
		for _, p := range people {
			raw, tid, err := mintToken(ctx, tx, org, apr, p.UserID, exp)
			if err != nil {
				return err
			}
			notice.Recipients = append(notice.Recipients, notify.Recipient{UserID: p.UserID, Email: p.Email, Name: p.Name,
				Link: s.link(raw), LinkID: tid.String(), Self: o.Settings.SelfCountersign != "allow" && IsSelf(deref(x.OnBehalfOf), p)})
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.approvals SET notified_count = notified_count + 1, notified_at = pg_catalog.now()
		    WHERE org_id = $1 AND id = $2`, org, apr); err != nil {
			return err
		}
		send = len(notice.Recipients) > 0
		return nil
	})
	if err != nil || !send {
		return err
	}
	for _, n := range s.o.Notifiers {
		if err := n.ApprovalRequested(ctx, notice); err != nil {
			s.log.Warn("approve: a notifier failed", "notifier", n.Name(), "org_id", org.String(), "approval_id", notice.ApprovalID, "err", err)
		}
	}
	return nil
}

// link is the person's approval link: the token rides in the fragment.
func (s *Service) link(raw string) string {
	u := *s.o.BaseURL
	u.Path = "/approve"
	u.RawQuery, u.Fragment = "", ""
	return u.String() + "#t=" + raw
}

// notice is the notifier's view of an approval (no recipients yet).
func (s *Service) notice(x *row, o orgInfo, votes []vote) notify.ApprovalNotice {
	n := notify.ApprovalNotice{OrgID: x.Org, OrgName: o.Name, ApprovalID: x.ID.String(), ActionID: x.Action.String(),
		AgentName: x.AgentName, Tool: x.Tool, RuleID: deref(x.RuleID), Reason: deref(x.Reason), TimeZone: o.TimeZone,
		Required: x.Required, Countersigned: len(x.ApprovedBy), ExpiresAt: x.ExpiresAt, Status: string(x.Status),
		ReviewOnly: o.Settings.EmailLinks == "review"}
	n.Summary = ShortSummary(SummaryFacts{Tool: x.Tool})
	if snap, err := parseSnapshot(x.Context); err == nil && snap != nil {
		f := snap.summaryFacts()
		n.Summary, n.Estimated = ShortSummary(f), f.Estimated
		if o.Settings.EmailDetail == "full" {
			n.Detail = clipRunes(strings.Join(strings.Fields(snap.sqlText()), " "), 300)
		}
	}
	for _, v := range votes {
		if v.Vote == VoteApprove {
			n.CountersignedBy = append(n.CountersignedBy, notify.Countersign{Name: v.Name, At: v.At})
		}
	}
	return n
}

// resolved tells the notifiers that an approval reached a final status (Slack edits its message in
// S29; email sends nothing). Best effort, off the decision's path.
func (s *Service) resolved(ctx context.Context, org ids.OrgID, apr ids.ApprovalID) {
	if len(s.o.Notifiers) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var n notify.ApprovalNotice
		err := s.o.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			x, err := readRow(ctx, tx, org, apr, false)
			if err != nil {
				return err
			}
			o, err := readOrg(ctx, tx, org)
			if err != nil {
				return err
			}
			votes, err := readVotes(ctx, tx, org, apr)
			if err != nil {
				return err
			}
			n = s.notice(x, o, votes)
			return nil
		})
		if err != nil {
			return
		}
		for _, nt := range s.o.Notifiers {
			if err := nt.ApprovalResolved(ctx, n); err != nil {
				s.log.Warn("approve: a notifier failed", "notifier", nt.Name(), "org_id", org.String(), "approval_id", apr.String(), "err", err)
			}
		}
	}()
}

// changed tells the notifiers that can show progress (Slack, S29) that a countersign landed but
// the approval still waits for more. Best effort, off the decision's path.
func (s *Service) changed(ctx context.Context, org ids.OrgID, apr ids.ApprovalID, action string) {
	for _, nt := range s.o.Notifiers {
		u, ok := nt.(notify.ApprovalUpdater)
		if !ok {
			continue
		}
		go func() {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := u.ApprovalChanged(ctx, org, apr.String(), action); err != nil {
				s.log.Warn("approve: a notifier failed", "notifier", nt.Name(), "org_id", org.String(), "approval_id", apr.String(), "err", err)
			}
		}()
	}
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
