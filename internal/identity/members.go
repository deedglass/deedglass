package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mailer"
	"github.com/deedglass/deedglass/internal/store"
)

func userRef(id ids.UserID) deed.Ref         { return deed.Ref{Type: "user", ID: id.String()} }
func memberRef(id ids.MembershipID) deed.Ref { return deed.Ref{Type: "membership", ID: id.String()} }
func orgRef(id ids.OrgID) *deed.Ref          { return &deed.Ref{Type: "org", ID: id.String()} }
func sessionRef(id ids.SessionID) *deed.Ref  { return &deed.Ref{Type: "session", ID: id.String()} }
func operator() deed.Ref                     { return deed.Ref{Type: "system", ID: "operator"} }
func ptrRef(r deed.Ref) *deed.Ref            { return &r }

// completeSignIn finishes a sign-in inside the org transaction that consumed the token: it accepts
// an invite (sealing member.joined), marks the person active, opens a session and records
// user.signed_in.
func (s *Service) completeSignIn(ctx context.Context, tx pgx.Tx, org ids.OrgID, user ids.UserID, purpose, via string, now time.Time) (SignedIn, error) {
	if purpose == PurposeInvite {
		if s.signup == "closed" {
			return SignedIn{}, httpx.E(httpx.CodeLoginLinkInvalid,
				httpx.Msg("Invites are turned off on this server. Ask the person who runs it to set DEEDGLASS_SIGNUP=invite."))
		}
		var mem ids.MembershipID
		var role string
		err := tx.QueryRow(ctx, `UPDATE deedglass.memberships SET status = 'active', joined_at = $3
		    WHERE org_id = $1 AND user_id = $2 AND status = 'invited' RETURNING id, role`, org, user, now).Scan(&mem, &role)
		if errors.Is(err, pgx.ErrNoRows) {
			return SignedIn{}, s.linkInvalid()
		}
		if err != nil {
			return SignedIn{}, err
		}
		if _, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminMemberJoined, Actor: userRef(user),
			Target: ptrRef(memberRef(mem)), Data: map[string]any{"role": role}}); err != nil {
			return SignedIn{}, err
		}
	}
	var mstatus, ostatus string
	err := tx.QueryRow(ctx, `SELECT m.status, o.status FROM deedglass.memberships AS m JOIN deedglass.orgs AS o ON o.id = m.org_id
	    WHERE m.org_id = $1 AND m.user_id = $2`, org, user).Scan(&mstatus, &ostatus)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (mstatus != "active" || ostatus != "active")) {
		return SignedIn{}, s.linkInvalid()
	}
	if err != nil {
		return SignedIn{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE deedglass.users SET status = CASE WHEN status = 'invited' THEN 'active' ELSE status END,
	    last_login_at = $2, email_verified_at = COALESCE(email_verified_at, $2) WHERE id = $1 AND status <> 'disabled'`, user, now)
	if err != nil {
		return SignedIn{}, err
	}
	if tag.RowsAffected() != 1 {
		return SignedIn{}, s.linkInvalid()
	}
	if _, err := tx.Exec(ctx, `UPDATE deedglass.memberships SET last_used_at = $3 WHERE org_id = $1 AND user_id = $2`, org, user, now); err != nil {
		return SignedIn{}, err
	}
	ns, err := s.createSession(ctx, tx, org, user, nil)
	if err != nil {
		return SignedIn{}, err
	}
	if err := s.event(ctx, tx, ledger.AdminEvent{Action: ledger.AdminUserSignedIn, Actor: userRef(user), Target: sessionRef(ns.ID),
		Data: map[string]any{"via": via}}); err != nil {
		return SignedIn{}, err
	}
	return SignedIn{Session: ns, Via: via}, nil
}

// Member is a membership as the API shows it.
type Member struct {
	Object    string      `json:"object"` // "membership"
	ID        string      `json:"id"`
	Org       string      `json:"org"`
	User      MemberUser  `json:"user"`
	Role      string      `json:"role"`
	Status    string      `json:"status"`
	InvitedBy *string     `json:"invited_by"`
	InvitedAt *httpx.Time `json:"invited_at"`
	JoinedAt  *httpx.Time `json:"joined_at"`
	RemovedAt *httpx.Time `json:"removed_at"`
	CreatedAt httpx.Time  `json:"created_at"`
	Deed      *string     `json:"deed,omitzero"` // the admin deed a change sealed
}

// MemberUser is the person on a membership.
type MemberUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

const memberCols = `m.id, m.org_id, m.user_id, u.email, COALESCE(u.display_name, ''), m.role, m.status, m.invited_by,
  m.invited_at, m.joined_at, m.removed_at, m.created_at`

func scanMember(row pgx.Row) (Member, error) {
	m := Member{Object: "membership"}
	var invBy *string
	var inv, joined, removed *time.Time
	var created time.Time
	err := row.Scan(&m.ID, &m.Org, &m.User.ID, &m.User.Email, &m.User.Name, &m.Role, &m.Status, &invBy, &inv, &joined, &removed, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, store.ErrNotFound
	}
	m.InvitedBy, m.InvitedAt, m.JoinedAt, m.RemovedAt, m.CreatedAt = invBy, tp(inv), tp(joined), tp(removed), httpx.Time(created)
	return m, err
}

func tp(t *time.Time) *httpx.Time {
	if t == nil {
		return nil
	}
	v := httpx.Time(*t)
	return &v
}

func getMember(ctx context.Context, tx pgx.Tx, id ids.MembershipID) (Member, error) {
	return scanMember(tx.QueryRow(ctx, `SELECT `+memberCols+` FROM deedglass.memberships AS m
	    JOIN deedglass.users AS u ON u.id = m.user_id WHERE m.id = $1`, id))
}

// lockOrg takes the org row lock that serializes every membership change in the org: two owners
// demoting each other can't both see two owners. It returns the org's name.
func lockOrg(ctx context.Context, tx pgx.Tx, org ids.OrgID) (string, error) {
	var name string
	err := tx.QueryRow(ctx, `SELECT name FROM deedglass.orgs WHERE id = $1 FOR UPDATE`, org).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrNotFound
	}
	return name, err
}

func activeOwners(ctx context.Context, tx pgx.Tx) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*)::integer FROM deedglass.memberships WHERE role = 'owner' AND status = 'active'`).Scan(&n)
	return n, err
}

func lastOwner(org string) *httpx.Error {
	return httpx.E(httpx.CodeLastOwner, httpx.Msgf("%s needs at least one owner. Make someone else an owner first.", org))
}

func memberNotFound(id string) *httpx.Error {
	return httpx.E(httpx.CodeNotFound, httpx.Msgf("There's no member with id %s in this org. GET /v1/org/members lists them.", clipID(id)))
}

func clipID(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "(unprintable)"
		}
	}
	return s
}

// InviteInput is POST /v1/org/members.
type InviteInput struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// Invite adds a person to the principal's org as an invited member (creating their user row when
// the address is new), sends them a one-time invite link valid for 7 days, and seals
// member.invited. Re-inviting someone still pending voids their old link, sends a fresh one and
// returns created=false. Inviting an active member is 409 conflict.
func (s *Service) Invite(ctx context.Context, p *Principal, in InviteInput) (m Member, created bool, err error) {
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return Member{}, false, httpx.ErrValidation("email", err.Error())
	}
	role, ok := ParseRole(in.Role)
	if !ok {
		return Member{}, false, httpx.ErrValidation("role", "`role` must be viewer, admin or owner.")
	}
	if role == RoleOwner && !p.Role.AtLeast(RoleOwner) {
		return Member{}, false, httpx.ErrInsufficientRole(p.OrgName, p.Role, RoleOwner, "inviting an owner")
	}
	if s.signup == "closed" {
		return Member{}, false, httpx.E(httpx.CodeConflict,
			httpx.Msg("Invites are turned off on this server (DEEDGLASS_SIGNUP=closed). Ask the person who runs it to set it to invite."))
	}
	raw, hash, err := NewToken()
	if err != nil {
		return Member{}, false, err
	}
	var (
		org      string
		inviter  PersonRef
		tokID    = ids.New[ids.LoginTokenKind]()
		expires  time.Time
		inviteID ids.UserID
	)
	err = s.db.OrgTx(ctx, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now().UTC()
		expires = now.Add(InviteTTL)
		var err error
		if org, err = lockOrg(ctx, tx, p.OrgID); err != nil {
			return err
		}
		var iname *string
		if err := tx.QueryRow(ctx, `SELECT email, display_name FROM deedglass.users WHERE id = $1`, p.UserID).Scan(&inviter.Email, &iname); err != nil {
			return err
		}
		if iname != nil {
			inviter.Name = *iname
		}
		u, err := s.pre.UserByEmailTx(ctx, tx, email)
		switch {
		case errors.Is(err, store.ErrNotFound):
			if err := insertInvitee(ctx, tx, email); err != nil {
				return err
			}
			if u, err = s.pre.UserByEmailTx(ctx, tx, email); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		if u.Status == "disabled" {
			return httpx.E(httpx.CodeConflict, httpx.Param("email"), httpx.Msgf("%s can't be invited: their account is disabled.", email))
		}
		inviteID = u.ID
		var memID ids.MembershipID
		var status string
		err = tx.QueryRow(ctx, `SELECT id, status FROM deedglass.memberships WHERE org_id = $1 AND user_id = $2`, p.OrgID, u.ID).Scan(&memID, &status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			memID, created = ids.New[ids.MembershipKind](), true
			_, err = tx.Exec(ctx, `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, invited_by, invited_at)
			    VALUES ($1, $2, $3, $4, 'invited', $5, $6)`, memID, p.OrgID, u.ID, string(role), p.UserID, now)
		case err != nil:
		case status == "active":
			return httpx.E(httpx.CodeConflict, httpx.Param("email"), httpx.Msgf("%s is already a member of %s.", email, org))
		case status == "removed":
			created = true
			_, err = tx.Exec(ctx, `UPDATE deedglass.memberships SET status = 'invited', role = $2, invited_by = $3, invited_at = $4,
			    removed_at = NULL, joined_at = NULL WHERE id = $1`, memID, string(role), p.UserID, now)
		default: // invited: a fresh link, possibly a new role
			_, err = tx.Exec(ctx, `UPDATE deedglass.memberships SET role = $2, invited_by = $3, invited_at = $4 WHERE id = $1`,
				memID, string(role), p.UserID, now)
		}
		if err != nil {
			return err
		}
		if err := voidTokens(ctx, tx, u.ID, PurposeInvite, now); err != nil {
			return err
		}
		if err := insertToken(ctx, tx, tokenRow{id: tokID, org: p.OrgID, user: u.ID, membership: &memID, purpose: PurposeInvite,
			hash: hash, expires: expires, ip: httpx.ClientIP(ctx), ua: userAgent(ctx)}); err != nil {
			return err
		}
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminMemberInvited, Actor: userRef(p.UserID),
			Target: ptrRef(memberRef(memID)), Data: map[string]any{"user": u.ID.String(), "role": string(role)}})
		if err != nil {
			return err
		}
		if m, err = getMember(ctx, tx, memID); err != nil {
			return err
		}
		m.Deed = d
		return nil
	})
	if err != nil {
		return Member{}, false, err
	}
	s.sendInvite(ctx, mailer.InviteData{InviterName: inviter.Name, InviterEmail: inviter.Email, OrgName: org, Role: string(role),
		Email: email, Link: s.ConfirmLink(raw), ExpiresAt: expires, Now: s.now()}, tokID, inviteID)
	return m, created, nil
}

// insertInvitee adds a person's users row in its first state. Row-level security lets the org
// insert it but not read it back (ON CONFLICT would need to), so a concurrent invite of the same
// new address, in any org, shows up as a unique violation inside a savepoint and is fine.
func insertInvitee(ctx context.Context, tx pgx.Tx, email string) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	_, err = sp.Exec(ctx, `INSERT INTO deedglass.users (id, email, status) VALUES ($1, $2, 'invited')`, ids.New[ids.UserKind](), email)
	if store.SQLState(err) == store.CodeUniqueViolation {
		return sp.Rollback(ctx)
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	return sp.Commit(ctx)
}

// sendInvite queues the invite email after the transaction committed.
func (s *Service) sendInvite(ctx context.Context, d mailer.InviteData, tok ids.LoginTokenID, user ids.UserID) {
	if s.mail == nil {
		return
	}
	rid := httpx.RequestID(ctx)
	s.mail.Submit(PurposeInvite, user.String(), rid, func(ctx context.Context, m mailer.Mailer) error {
		msg, err := mailer.InviteMessage(d)
		if err != nil {
			return err
		}
		msg.IdempotencyKey = "invite/" + tok.String()
		msg.UserID, msg.RequestID = user.String(), rid
		return m.Send(ctx, msg)
	})
}

// ChangeRole sets a member's role (owners only, by route). Demoting the last active owner is 409
// last_owner; the org row lock makes concurrent demotions take turns.
func (s *Service) ChangeRole(ctx context.Context, p *Principal, id string, rawRole string) (Member, error) {
	memID, err := ids.Parse[ids.MembershipKind](id)
	if err != nil {
		return Member{}, memberNotFound(id)
	}
	role, ok := ParseRole(rawRole)
	if !ok {
		return Member{}, httpx.ErrValidation("role", "`role` must be viewer, admin or owner.")
	}
	var m Member
	err = s.db.OrgTx(ctx, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		org, err := lockOrg(ctx, tx, p.OrgID)
		if err != nil {
			return err
		}
		cur, err := getMember(ctx, tx, memID)
		if errors.Is(err, store.ErrNotFound) {
			return memberNotFound(id)
		}
		if err != nil {
			return err
		}
		if cur.Status == "removed" {
			return httpx.E(httpx.CodeConflict, httpx.Msg("This person was removed from the org. Invite them again to give them a role."))
		}
		if cur.Role == string(role) {
			m = cur
			return nil
		}
		if cur.Role == string(RoleOwner) && cur.Status == "active" {
			n, err := activeOwners(ctx, tx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return lastOwner(org)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.memberships SET role = $2 WHERE id = $1`, memID, string(role)); err != nil {
			return err
		}
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminMemberRoleChanged, Actor: userRef(p.UserID),
			Target: ptrRef(memberRef(memID)), Data: map[string]any{"from": cur.Role, "to": string(role)}})
		if err != nil {
			return err
		}
		if m, err = getMember(ctx, tx, memID); err != nil {
			return err
		}
		m.Deed = d
		return nil
	})
	return m, mapLastOwner(err)
}

// Remove takes a member out of the org: their sessions in the org end at once and their unused
// links stop working. Admins remove viewers and admins; owners remove anyone; removing yourself is
// leaving. The last active owner can't be removed.
func (s *Service) Remove(ctx context.Context, p *Principal, id string) (Member, error) {
	memID, err := ids.Parse[ids.MembershipKind](id)
	if err != nil {
		return Member{}, memberNotFound(id)
	}
	var m Member
	err = s.db.OrgTx(ctx, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now().UTC()
		org, err := lockOrg(ctx, tx, p.OrgID)
		if err != nil {
			return err
		}
		cur, err := getMember(ctx, tx, memID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && cur.Status == "removed") {
			return memberNotFound(id)
		}
		if err != nil {
			return err
		}
		self := cur.User.ID == p.UserID.String()
		if cur.Role == string(RoleOwner) && !self && !p.Role.AtLeast(RoleOwner) {
			return httpx.ErrInsufficientRole(p.OrgName, p.Role, RoleOwner, "removing an owner")
		}
		if cur.Role == string(RoleOwner) && cur.Status == "active" {
			n, err := activeOwners(ctx, tx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return lastOwner(org)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.memberships SET status = 'removed', removed_at = $2 WHERE id = $1`, memID, now); err != nil {
			return err
		}
		user := ids.MustParse[ids.UserKind](cur.User.ID)
		if _, err := revokeUserSessions(ctx, tx, user, RevokeMemberRemoved, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.login_tokens SET voided_at = $2 WHERE user_id = $1 AND used_at IS NULL AND voided_at IS NULL`,
			user, now); err != nil {
			return err
		}
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminMemberRemoved, Actor: userRef(p.UserID),
			Target: ptrRef(memberRef(memID)), Data: map[string]any{"role": cur.Role, "self": self}})
		if err != nil {
			return err
		}
		if m, err = getMember(ctx, tx, memID); err != nil {
			return err
		}
		m.Deed = d
		return nil
	})
	return m, mapLastOwner(err)
}

// mapLastOwner turns the database's own guard (DG005, checked at commit) into last_owner, in case
// anything ever slips past the org-row-lock count.
func mapLastOwner(err error) error {
	if err != nil && store.SQLState(err) == store.CodeLastOwner {
		return httpx.E(httpx.CodeLastOwner, httpx.Cause(err))
	}
	return err
}

// MemberFilter is GET /v1/org/members's query.
type MemberFilter struct {
	Status string // "", "invited", "active" or "removed" ("" = invited and active)
	Limit  int
	After  *httpx.Cursor
}

// ListMembers returns one page of the org's members, oldest first. Membership ids are monotonic
// ULIDs, so the id alone orders the list and is the whole cursor (a timestamp cursor at the API's
// millisecond precision would repeat rows created within the same millisecond).
func (s *Service) ListMembers(ctx context.Context, p *Principal, f MemberFilter) ([]Member, bool, error) {
	var out []Member
	afterID := ""
	if f.After != nil {
		if _, err := ids.Parse[ids.MembershipKind](f.After.ID); err != nil {
			return nil, false, httpx.ErrValidation("cursor", "`cursor` isn't a cursor this API issued. Pass the next_cursor value from the previous page unchanged.")
		}
		afterID = f.After.ID
	}
	err := s.db.OrgReadTx(ctx, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+memberCols+` FROM deedglass.memberships AS m JOIN deedglass.users AS u ON u.id = m.user_id
		    WHERE m.org_id = $1
		      AND (($2 = '' AND m.status <> 'removed') OR m.status = $2)
		      AND ($3 = '' OR m.id > $3::text)
		    ORDER BY m.id LIMIT $4`, p.OrgID, f.Status, afterID, f.Limit+1)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Member, error) { return scanMember(r) })
		return err
	})
	if err != nil {
		return nil, false, err
	}
	more := len(out) > f.Limit
	if more {
		out = out[:f.Limit]
	}
	return out, more, nil
}
