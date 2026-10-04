package approve

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Approver is the person deciding and how. Only people decide: every transport resolves to a user
// id before Decide (a session, an email link's token, a Slack user mapped in S29, the dev CLI).
type Approver struct {
	UserID    ids.UserID
	Via       Via
	SessionID *ids.SessionID // console
	TokenID   string         // email_link: the token's internal id, never shown
	External  string         // slack: the Slack user id (S29)
}

// DecideInput is one person's decision.
type DecideInput struct {
	Org        ids.OrgID
	ApprovalID string
	Approver   Approver
	Vote       Vote
	Reason     string // required to deny; ≤ 280 characters; no control or bidi characters
	// ContextDigest is the digest of the snapshot the person saw; it must equal the approval's.
	ContextDigest string
	RequestID     string
	// consume runs first in the decision's transaction (an email link marks its token used there,
	// so a refused decision never burns the link).
	consume func(ctx context.Context, tx pgx.Tx) error
}

// MaxReason is the longest reason, in characters.
const MaxReason = 280

// CheckDecideInput validates the input before any database work (the page and the CLI call it to
// answer early).
func CheckDecideInput(in DecideInput) error {
	switch in.Vote {
	case VoteApprove, VoteDeny:
	default:
		return httpx.ErrValidation("vote", MsgVoteInvalid)
	}
	r := strings.TrimSpace(in.Reason)
	switch {
	case hasForbidden(r):
		return httpx.ErrValidation("reason", MsgReasonChars)
	case utf8.RuneCountInString(r) > MaxReason:
		return httpx.ErrValidation("reason", MsgReasonTooLong)
	case in.Vote == VoteDeny && r == "":
		return httpx.ErrValidation("reason", MsgReasonRequired)
	case in.ContextDigest == "":
		return httpx.ErrValidation("context_digest", MsgDigestRequired)
	}
	return nil
}

// Decide records one person's decision on an approval, exactly once (S22 §3.6). In one org
// transaction, it locks the action, then the approval (S13's sweeper and Cancel lock in the same
// order), checks the decision, seals the admin deed, inserts the vote and moves the approval. A
// final approve enqueues approval_resume (the worker runs the action: the approver never waits for
// the tool); a deny is a veto that denies and seals the action in this transaction. The first final
// decision wins: anything after it is approval_decided.
func (s *Service) Decide(ctx context.Context, in DecideInput) (*Approval, error) {
	if err := CheckDecideInput(in); err != nil {
		return nil, err
	}
	aid, err := ids.Parse[ids.ApprovalKind](in.ApprovalID)
	if err != nil {
		return nil, errNotFound(in.ApprovalID)
	}
	if in.Approver.UserID.IsZero() {
		return nil, httpx.E(httpx.CodeApprovalNeedsPerson)
	}
	reason := strings.TrimSpace(in.Reason)
	var (
		out   *Approval
		final Status
	)
	err = s.o.Ledger.OrgTxRetry(ctx, in.Org, func(ctx context.Context, tx pgx.Tx) error {
		out, final = nil, ""
		if in.consume != nil {
			if err := in.consume(ctx, tx); err != nil {
				return err
			}
		}
		// The lock order: the action, then the approval, then (inside RecordAdmin) the deed chain.
		var action ids.ActionID
		if err := tx.QueryRow(ctx, `SELECT action_id FROM deedglass.approvals WHERE org_id = $1 AND id = $2`, in.Org, aid).Scan(&action); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound(in.ApprovalID)
			}
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM deedglass.actions WHERE org_id = $1 AND id = $2 FOR UPDATE`, in.Org, action); err != nil {
			return err
		}
		x, err := readRow(ctx, tx, in.Org, aid, true)
		if err != nil {
			return err
		}
		org, err := readOrg(ctx, tx, in.Org)
		if err != nil {
			return err
		}
		loc := location(org.TimeZone)
		now := s.now()
		votes, err := readVotes(ctx, tx, in.Org, aid)
		if err != nil {
			return err
		}
		// The checks, in order (§3.6), each a typed error.
		switch {
		case x.Status == StatusExpired:
			return httpx.E(httpx.CodeApprovalExpired, httpx.Msg(MsgExpired(x.ExpiresAt, loc)))
		case x.Status != StatusPending:
			return decidedError(x, votes, loc)
		case !now.Before(x.ExpiresAt):
			return httpx.E(httpx.CodeApprovalExpired, httpx.Msg(MsgExpired(x.ExpiresAt, loc)))
		case x.ContextDigest == nil || *x.ContextDigest != in.ContextDigest:
			return httpx.E(httpx.CodeContextChanged, httpx.Param("context_digest"))
		}
		p, ok, err := member(ctx, tx, in.Org, in.Approver.UserID)
		if err != nil {
			return err
		}
		if !ok || !x.Spec.Eligible(p) {
			return httpx.E(httpx.CodeApproverNotEligible, httpx.Msg(MsgNotEligible(deref(x.RuleID), x.Spec.Approvers)))
		}
		if in.Vote == VoteApprove && IsSelf(deref(x.OnBehalfOf), p) && org.Settings.SelfCountersign != "allow" {
			return httpx.E(httpx.CodeSelfApproval)
		}
		approves := 0
		for _, v := range votes {
			if v.User == in.Approver.UserID {
				return httpx.E(httpx.CodeAlreadyCountersigned, httpx.Msg(MsgAlreadyCountersigned(x.Required-countApproves(votes))))
			}
			if v.Vote == VoteApprove {
				approves++
			}
		}
		// The decision.
		action2 := ledger.AdminApprovalCountersigned
		switch {
		case in.Vote == VoteDeny:
			final, action2 = StatusDenied, ledger.AdminApprovalDecided
		case approves+1 >= x.Required:
			final, action2 = StatusApproved, ledger.AdminApprovalDecided
		}
		count := approves
		if in.Vote == VoteApprove {
			count++
		}
		data := map[string]any{"approval": aid.String(), "action": x.Action.String(), "vote": string(in.Vote), "via": string(in.Approver.Via),
			"count": count, "required": x.Required, "rule_id": deref(x.RuleID), "context_digest": in.ContextDigest,
			"request_digest": x.RequestDigest}
		env, err := s.o.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: action2, Actor: deed.Ref{Type: "user", ID: in.Approver.UserID.String()},
			Target: &deed.Ref{Type: "approval", ID: aid.String()}, Reason: reason, Data: data, RequestID: in.RequestID})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deedglass.approval_votes (org_id, approval_id, user_id, vote, reason, via, context_digest, deed_id)
		    VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, in.Org, aid, in.Approver.UserID, string(in.Vote), nonEmpty(reason),
			string(in.Approver.Via), in.ContextDigest, env.ID); err != nil {
			if store.SQLState(err) == "23505" {
				return httpx.E(httpx.CodeAlreadyCountersigned, httpx.Msg(MsgAlreadyCountersigned(x.Required-approves)))
			}
			return err
		}
		user := in.Approver.UserID.String()
		var tag interface{ RowsAffected() int64 }
		switch final {
		case StatusDenied:
			tag, err = tx.Exec(ctx, `UPDATE deedglass.approvals SET status = 'denied', decided_by = $3, decided_at = $6::timestamptz,
			    decision_reason = $4, deed_id = $5 WHERE org_id = $1 AND id = $2 AND status = 'pending'`, in.Org, aid, user, nonEmpty(reason), env.ID, now.UTC())
		case StatusApproved:
			tag, err = tx.Exec(ctx, `UPDATE deedglass.approvals SET status = 'approved', approved_by = pg_catalog.array_append(approved_by, $3::text),
			    decided_by = $3, decided_at = $6::timestamptz, decision_reason = $4, deed_id = $5
			    WHERE org_id = $1 AND id = $2 AND status = 'pending'`, in.Org, aid, user, nonEmpty(reason), env.ID, now.UTC())
		default:
			tag, err = tx.Exec(ctx, `UPDATE deedglass.approvals SET approved_by = pg_catalog.array_append(approved_by, $3::text)
			    WHERE org_id = $1 AND id = $2 AND status = 'pending'`, in.Org, aid, user)
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return decidedError(x, votes, loc)
		}
		deedID := env.ID
		switch final {
		case StatusApproved:
			if err := store.EnqueueJob(ctx, tx, in.Org, x.Action, gate.JobApprovalResume, now, 8); err != nil {
				return err
			}
			if err := voidTokens(ctx, tx, in.Org, aid); err != nil {
				return err
			}
			if err := s.publish(ctx, tx, in.Org, aid, ledger.EventApprovalDecided, StatusApproved, &deedID); err != nil {
				return err
			}
		case StatusDenied:
			if err := voidTokens(ctx, tx, in.Org, aid); err != nil {
				return err
			}
			if err := s.publish(ctx, tx, in.Org, aid, ledger.EventApprovalDecided, StatusDenied, &deedID); err != nil {
				return err
			}
			if s.gate == nil {
				return errors.New("approve: no Gate attached to conclude the denied action")
			}
			if err := s.gate.ConcludeApprovalTx(ctx, tx, in.Org, x.Action); err != nil {
				return err
			}
		}
		// The answer, read in this transaction.
		x2, err := readRow(ctx, tx, in.Org, aid, false)
		if err != nil {
			return err
		}
		votes2, err := readVotes(ctx, tx, in.Org, aid)
		if err != nil {
			return err
		}
		v := Viewer{Kind: httpx.KindUser, Org: in.Org, UserID: in.Approver.UserID}
		if out, err = s.object(ctx, tx, v, x2, votes2, org); err != nil {
			return err
		}
		switch final {
		case StatusApproved:
			out.ActionStatus = ledger.StatusProcessing
		case StatusDenied:
			out.ActionStatus = ledger.StatusDenied
		default:
			out.ActionStatus = ledger.StatusPendingApproval
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.gate != nil {
		s.gate.Broker().Notify(out.Action)
	}
	if final != "" {
		s.resolved(context.WithoutCancel(ctx), in.Org, aid)
	} else {
		s.changed(context.WithoutCancel(ctx), in.Org, aid, out.Action)
	}
	return out, nil
}

func countApproves(vs []vote) int {
	n := 0
	for _, v := range vs {
		if v.Vote == VoteApprove {
			n++
		}
	}
	return n
}

// decidedError is approval_decided naming who decided and when.
func decidedError(x *row, votes []vote, loc *time.Location) error {
	name := ""
	outcome := string(x.Status)
	switch x.Status {
	case StatusApproved, StatusDenied:
		for i := len(votes) - 1; i >= 0; i-- {
			if x.DecidedBy != nil && votes[i].User.String() == *x.DecidedBy {
				name = votes[i].Name
				break
			}
		}
	case StatusCancelled, StatusExpired:
	}
	at := time.Time{}
	if x.DecidedAt != nil {
		at = *x.DecidedAt
	}
	return httpx.E(httpx.CodeApprovalDecided, httpx.Msg(MsgDecided(outcome, name, at, loc)))
}

// location is the org's time zone, UTC when unknown.
func location(tz string) *time.Location {
	if l, err := time.LoadLocation(tz); err == nil {
		return l
	}
	return time.UTC
}
