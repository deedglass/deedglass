package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// PreAuth wraps the narrow SECURITY DEFINER functions of migration 0012: the reads that happen
// before the org is known (a session cookie, a sign-in token, the email typed at sign-in), and the
// cross-org identity prune. They are the only way across row-level security for the runtime role.
// Nothing outside internal/store names these functions (TestPreAuthNamesStayInStore).
//
// Each method runs on its own pooled connection outside any transaction: the functions are single
// statements, and every write that follows happens in a normal OrgTx.
type PreAuth struct{ db *DB }

// PreAuth returns the pre-auth lookups.
func (db *DB) PreAuth() PreAuth { return PreAuth{db: db} }

// PreAuthSession is the session behind a cookie.
type PreAuthSession struct {
	ID            ids.SessionID
	OrgID         ids.OrgID
	UserID        ids.UserID
	CSRFHash      []byte
	LastSeenAt    *time.Time
	IdleExpiresAt time.Time
	ExpiresAt     time.Time
	RevokedAt     *time.Time
}

// Session returns the session whose token hashes to tokenHash, or ErrNotFound.
func (p PreAuth) Session(ctx context.Context, tokenHash []byte) (PreAuthSession, error) {
	var s PreAuthSession
	err := p.db.pool.QueryRow(ctx, `SELECT id, org_id, user_id, csrf_hash, last_seen_at, idle_expires_at, expires_at, revoked_at
	    FROM deedglass.preauth_session($1)`, tokenHash).
		Scan(&s.ID, &s.OrgID, &s.UserID, &s.CSRFHash, &s.LastSeenAt, &s.IdleExpiresAt, &s.ExpiresAt, &s.RevokedAt)
	return s, notFound(err)
}

// PreAuthToken is the sign-in or invite token behind a link.
type PreAuthToken struct {
	ID           ids.LoginTokenID
	OrgID        ids.OrgID
	UserID       ids.UserID
	MembershipID *ids.MembershipID
	Purpose      string // "login" | "invite"
	ExpiresAt    time.Time
	UsedAt       *time.Time
	VoidedAt     *time.Time
}

// LoginToken returns the token whose raw value hashes to tokenHash, or ErrNotFound.
func (p PreAuth) LoginToken(ctx context.Context, tokenHash []byte) (PreAuthToken, error) {
	var t PreAuthToken
	err := p.db.pool.QueryRow(ctx, `SELECT id, org_id, user_id, membership_id, purpose, expires_at, used_at, voided_at
	    FROM deedglass.preauth_login_token($1)`, tokenHash).
		Scan(&t.ID, &t.OrgID, &t.UserID, &t.MembershipID, &t.Purpose, &t.ExpiresAt, &t.UsedAt, &t.VoidedAt)
	return t, notFound(err)
}

// PreAuthMembership is one membership of the person behind an email address.
type PreAuthMembership struct {
	OrgID      ids.OrgID
	OrgName    string
	OrgStatus  string // "active" | "disabled"
	Role       string
	Status     string // "invited" | "active"
	LastUsedAt *time.Time
	JoinedAt   *time.Time
}

// PreAuthUser is the person behind an email address and their memberships (removed ones excluded).
type PreAuthUser struct {
	ID             ids.UserID
	Status         string
	TokensLastHour int // sign-in and invite tokens created for this user in the last hour, in every org
	Memberships    []PreAuthMembership
}

// UserByEmail looks a person up by their normalized email address, or returns ErrNotFound. Run it
// in the sign-in worker or inside a transaction's caller, never on the POST /v1/auth/login path.
func (p PreAuth) UserByEmail(ctx context.Context, email string) (PreAuthUser, error) {
	return userByEmail(ctx, p.db.pool, email)
}

// UserByEmailTx is UserByEmail inside an open transaction (invites).
func (p PreAuth) UserByEmailTx(ctx context.Context, tx pgx.Tx, email string) (PreAuthUser, error) {
	return userByEmail(ctx, tx, email)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func userByEmail(ctx context.Context, q querier, email string) (PreAuthUser, error) {
	rows, err := q.Query(ctx, `SELECT user_id, user_status, org_id, org_name, org_status, role, membership_status, last_used_at, joined_at, tokens_last_hour
	    FROM deedglass.preauth_user_by_email($1)`, email)
	if err != nil {
		return PreAuthUser{}, err
	}
	defer rows.Close()
	var u PreAuthUser
	found := false
	for rows.Next() {
		var (
			uid           ids.UserID
			status        string
			org           *ids.OrgID
			oname, ostat  *string
			role, mstatus *string
			lastUsed      *time.Time
			joined        *time.Time
			n             int
		)
		if err := rows.Scan(&uid, &status, &org, &oname, &ostat, &role, &mstatus, &lastUsed, &joined, &n); err != nil {
			return PreAuthUser{}, err
		}
		found = true
		u.ID, u.Status, u.TokensLastHour = uid, status, n
		if org != nil && role != nil && mstatus != nil && oname != nil && ostat != nil {
			u.Memberships = append(u.Memberships, PreAuthMembership{OrgID: *org, OrgName: *oname, OrgStatus: *ostat,
				Role: *role, Status: *mstatus, LastUsedAt: lastUsed, JoinedAt: joined})
		}
	}
	if err := rows.Err(); err != nil {
		return PreAuthUser{}, err
	}
	if !found {
		return PreAuthUser{}, ErrNotFound
	}
	return u, nil
}

// CodeToken returns the one token a typed code can match for this user (the newest unused,
// unvoided, unexpired login token with a code), or ErrNotFound.
func (p PreAuth) CodeToken(ctx context.Context, user ids.UserID) (ids.LoginTokenID, ids.OrgID, error) {
	var (
		id  ids.LoginTokenID
		org ids.OrgID
	)
	err := p.db.pool.QueryRow(ctx, `SELECT id, org_id FROM deedglass.preauth_code_token($1)`, user).Scan(&id, &org)
	return id, org, notFound(err)
}

// PruneIdentity deletes up to limit expired sign-in tokens and up to limit old sessions
// (deedglass.maint_prune_identity), and returns how many rows it removed.
func (p PreAuth) PruneIdentity(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, errors.New("store: prune limit must be positive")
	}
	var n int64
	err := p.db.pool.QueryRow(ctx, `SELECT deedglass.maint_prune_identity($1)`, limit).Scan(&n)
	return n, err
}

// ScopeSystemTxToOrg sets the org of a SystemTx(org_create) transaction once the org row exists,
// so the org's first admin deed can be sealed in the same transaction (RLS on deeds and
// admin_events checks the org). It refuses outside a system transaction.
func ScopeSystemTxToOrg(ctx context.Context, tx pgx.Tx, org ids.OrgID) error {
	if org.IsZero() {
		return ErrNoOrg
	}
	var scope string
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.current_setting('deedglass.scope', true)`).Scan(&scope); err != nil {
		return err
	}
	if scope != "system" {
		return errors.New("store: ScopeSystemTxToOrg needs a SystemTx")
	}
	_, err := tx.Exec(ctx, "SELECT pg_catalog.set_config('deedglass.org_id', $1, true)", org.String())
	return err
}
