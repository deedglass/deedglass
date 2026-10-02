package approve

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Email links (S22 §3.7). One token per (approval, person): 32 random bytes as base64url (43
// characters), stored only as sha256("deedglass/approval-link/v1\n" ‖ token). The link is
// {base}/approve#t=<token>: the fragment never reaches a server or a log. GET /approve renders a
// page and reads nothing; preview never consumes; only POST …/decide (a button) decides, once.
// Unknown, used, voided and expired tokens all answer the same 401 at similar timing.

const (
	linkDomain = "deedglass/approval-link/v1\n"
	tokenBytes = 32
	tokenLen   = 43
)

// errLinkInvalid is the one answer for every token that can't be used.
func errLinkInvalid() *httpx.Error { return httpx.E(httpx.CodeApprovalLinkInvalid) }

// newLinkToken returns a fresh token and its stored hash.
func newLinkToken() (string, []byte, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	raw := base64.RawURLEncoding.EncodeToString(b)
	return raw, hashLink(raw), nil
}

// hashLink is the stored form of a token.
func hashLink(raw string) []byte {
	h := sha256.Sum256([]byte(linkDomain + raw))
	return h[:]
}

// wellFormed reports a token of the right shape: 43 base64url characters that decode to 32 bytes.
func wellFormed(raw string) bool {
	if len(raw) != tokenLen {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	return err == nil && len(b) == tokenBytes
}

// dummyHash is looked up for a malformed token, so it costs what a real miss costs.
var dummyHash = hashLink("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

// mintToken voids the person's older unused links for this approval and stores a new one. It
// returns the raw token (for the email only) and the token's id.
func mintToken(ctx context.Context, tx pgx.Tx, org ids.OrgID, apr ids.ApprovalID, user ids.UserID, exp time.Time) (string, ids.ApprovalTokenID, error) {
	if _, err := tx.Exec(ctx, `UPDATE deedglass.approval_tokens SET voided_at = pg_catalog.now()
	    WHERE org_id = $1 AND approval_id = $2 AND user_id = $3 AND used_at IS NULL AND voided_at IS NULL`, org, apr, user); err != nil {
		return "", ids.ApprovalTokenID{}, err
	}
	raw, hash, err := newLinkToken()
	if err != nil {
		return "", ids.ApprovalTokenID{}, err
	}
	id := ids.New[ids.ApprovalTokenKind]()
	if _, err := tx.Exec(ctx, `INSERT INTO deedglass.approval_tokens (id, org_id, approval_id, user_id, token_hash, purpose, expires_at)
	    VALUES ($1, $2, $3, $4, $5, 'decide', $6)`, id, org, apr, user, hash, exp); err != nil {
		return "", ids.ApprovalTokenID{}, err
	}
	return raw, id, nil
}

// voidTokens voids every unused link of an approval (any final decision).
func voidTokens(ctx context.Context, tx pgx.Tx, org ids.OrgID, apr ids.ApprovalID) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.approval_tokens SET voided_at = pg_catalog.now()
	    WHERE org_id = $1 AND approval_id = $2 AND used_at IS NULL AND voided_at IS NULL`, org, apr)
	return err
}

// linkToken looks a token up before its org is known. Every failure is the same 401.
func (s *Service) linkToken(ctx context.Context, raw string) (store.PreAuthApprovalToken, error) {
	h := dummyHash
	if wellFormed(raw) {
		h = hashLink(raw)
	}
	t, err := s.o.DB.PreAuth().ApprovalToken(ctx, h)
	if errors.Is(err, store.ErrNotFound) || !wellFormed(raw) {
		return t, errLinkInvalid()
	}
	return t, err
}

// usable reports a token that can still preview or decide.
func (s *Service) usable(t store.PreAuthApprovalToken) bool {
	return t.UsedAt == nil && t.VoidedAt == nil && s.now().Before(t.ExpiresAt)
}

// LinkPreview is POST /v1/approval-links/preview: the approval as its person sees it.
type LinkPreview struct {
	Object     string    `json:"object"` // "approval_link_preview"
	Approval   *Approval `json:"approval"`
	ReviewOnly bool      `json:"review_only"`
	OrgName    string    `json:"org_name"`
	TimeZone   string    `json:"time_zone"`
	// AgentStopped: the agent is stopped, so an approval would be recorded but nothing would run.
	AgentStopped bool   `json:"agent_stopped"`
	SQL          string `json:"sql,omitempty"`
	ConsoleURL   string `json:"console_url"`
	// Clocks in the org's zone, formatted like the email ("10:41 IST").
	ExpiresClock      string   `json:"expires_clock"`
	DecidedClock      string   `json:"decided_clock,omitempty"`
	CountersignClocks []string `json:"countersign_clocks"`
}

// Preview shows the approval behind a usable token without consuming it (it notes the first
// preview). Email scanners that fetch the page never get here: the page runs only in a browser.
func (s *Service) Preview(ctx context.Context, raw string) (*LinkPreview, error) {
	t, err := s.linkToken(ctx, raw)
	if err != nil {
		return nil, err
	}
	if !s.usable(t) {
		return nil, errLinkInvalid()
	}
	var out *LinkPreview
	err = s.o.DB.OrgTx(ctx, t.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE deedglass.approval_tokens SET previewed_at = COALESCE(previewed_at, pg_catalog.now())
		    WHERE org_id = $1 AND id = $2`, t.OrgID, t.TokenID); err != nil {
			return err
		}
		var err error
		out, err = s.linkView(ctx, tx, t)
		return err
	})
	return out, err
}

func (s *Service) linkView(ctx context.Context, tx pgx.Tx, t store.PreAuthApprovalToken) (*LinkPreview, error) {
	x, err := readRow(ctx, tx, t.OrgID, t.ApprovalID, false)
	if err != nil {
		return nil, err
	}
	org, err := readOrg(ctx, tx, t.OrgID)
	if err != nil {
		return nil, err
	}
	votes, err := readVotes(ctx, tx, t.OrgID, t.ApprovalID)
	if err != nil {
		return nil, err
	}
	a, err := s.object(ctx, tx, Viewer{Kind: httpx.KindUser, Org: t.OrgID, UserID: t.UserID}, x, votes, org)
	if err != nil {
		return nil, err
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM deedglass.agents WHERE org_id = $1 AND id = $2`, t.OrgID, x.Agent).Scan(&status); err != nil {
		return nil, err
	}
	p := &LinkPreview{Object: "approval_link_preview", Approval: a, ReviewOnly: org.Settings.EmailLinks == "review", OrgName: org.Name,
		TimeZone: org.TimeZone, AgentStopped: status != "active", ConsoleURL: s.consoleURL(a.ID)}
	if snap, err := parseSnapshot(x.Context); err == nil && snap != nil {
		p.SQL = snap.sqlText()
	}
	loc := location(org.TimeZone)
	p.ExpiresClock = Clock(x.ExpiresAt, loc)
	if x.DecidedAt != nil {
		p.DecidedClock = Clock(*x.DecidedAt, loc)
	}
	p.CountersignClocks = []string{}
	for _, v := range votes {
		if v.Vote == VoteApprove {
			p.CountersignClocks = append(p.CountersignClocks, Clock(v.At, loc))
		}
	}
	return p, nil
}

// consoleURL is the approval in the Console (S26 serves it; until then the link shows the Console's
// placeholder page after sign-in).
func (s *Service) consoleURL(apr string) string {
	u := *s.o.BaseURL
	u.Path = "/app/approvals/" + apr
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

// LinkDecision is POST /v1/approval-links/decide.
type LinkDecision struct {
	Token         string `json:"token"`
	Vote          Vote   `json:"vote"`
	Reason        string `json:"reason"`
	ContextDigest string `json:"context_digest"`
}

// DecideLink decides with an emailed link: the token is consumed in the same transaction as the
// decision, so a refused decision (stale context, not eligible) never burns the link, and a link
// works exactly once. The vote is the link's person's, via email_link.
func (s *Service) DecideLink(ctx context.Context, in LinkDecision, requestID string) (*Approval, error) {
	t, err := s.linkToken(ctx, in.Token)
	if err != nil {
		return nil, err
	}
	if !s.usable(t) {
		return nil, errLinkInvalid()
	}
	var review bool
	if err := s.o.DB.OrgReadTx(ctx, t.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		o, err := readOrg(ctx, tx, t.OrgID)
		review = o.Settings.EmailLinks == "review"
		return err
	}); err != nil {
		return nil, err
	}
	if review {
		return nil, httpx.E(httpx.CodeApprovalLinkReviewOnly)
	}
	d := DecideInput{Org: t.OrgID, ApprovalID: t.ApprovalID.String(), Vote: in.Vote, Reason: in.Reason, ContextDigest: in.ContextDigest,
		RequestID: requestID, Approver: Approver{UserID: t.UserID, Via: ViaEmailLink, TokenID: t.TokenID.String()}}
	if err := CheckDecideInput(d); err != nil {
		return nil, err
	}
	d.consume = func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE deedglass.approval_tokens SET used_at = pg_catalog.now()
		    WHERE org_id = $1 AND id = $2 AND used_at IS NULL AND voided_at IS NULL AND expires_at > pg_catalog.now()`, t.OrgID, t.TokenID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errLinkInvalid()
		}
		return nil
	}
	return s.Decide(ctx, d)
}

// LinkStatus is POST /v1/approval-links/status: after a decision, the page follows the action until
// it is sealed (a used token, for link_status_window).
type LinkStatus struct {
	Object   string         `json:"object"` // "approval_link_status"
	Approval *Approval      `json:"approval"`
	Action   *ledger.Action `json:"action"`
}

// Status answers for a token used within the status window, or still usable (so the page can show
// "already decided" after someone else's decision).
func (s *Service) Status(ctx context.Context, raw string) (*LinkStatus, error) {
	t, err := s.linkToken(ctx, raw)
	if err != nil {
		return nil, err
	}
	now := s.now()
	switch {
	case t.UsedAt != nil && now.Sub(*t.UsedAt) <= s.cfg.LinkStatusWindow:
	case t.UsedAt == nil && t.VoidedAt == nil && now.Before(t.ExpiresAt):
	case t.VoidedAt != nil && t.UsedAt == nil && now.Sub(*t.VoidedAt) <= s.cfg.LinkStatusWindow:
		// voided by someone else's final decision: the page may still say who decided
	default:
		return nil, errLinkInvalid()
	}
	out := &LinkStatus{Object: "approval_link_status"}
	err = s.o.DB.OrgReadTx(ctx, t.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		p, err := s.linkView(ctx, tx, t)
		if err != nil {
			return err
		}
		out.Approval = p.Approval
		return nil
	})
	if err != nil {
		return nil, err
	}
	if v, err := s.o.Ledger.ReadActionView(ctx, t.OrgID, out.Approval.Action); err == nil {
		v.Execution = thinExecution(v.Execution)
		out.Action = v
	}
	return out, nil
}

// thinExecution keeps the execution's status and timing for the page, never the tool's result.
func thinExecution(e *ledger.ActionExecution) *ledger.ActionExecution {
	if e == nil {
		return nil
	}
	c := *e
	c.Result = nil
	return &c
}

// LinkHash is the hex of a token's stored hash, for the per-token rate limit key (never the token).
func LinkHash(raw string) string {
	if !wellFormed(raw) {
		return "malformed"
	}
	return hex.EncodeToString(hashLink(raw))[:32]
}
