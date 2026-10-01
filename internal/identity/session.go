package identity

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Cookie and header names (contract §8, S07 §3.6–§3.7).
const (
	SessionCookie    = "__Host-deedglass_session"
	CSRFCookie       = "__Host-deedglass_csrf"
	DevSessionCookie = "deedglass_session" // dev over http on localhost only
	DevCSRFCookie    = "deedglass_csrf"
	CSRFHeader       = "Deedglass-CSRF"
)

// Session revocation reasons (sessions.revoked_reason).
const (
	RevokeLogout           = "logout"
	RevokeLogoutEverywhere = "logout_everywhere"
	RevokeOrgSwitch        = "org_switch"
	RevokeMemberRemoved    = "member_removed"
)

type cookieNames struct {
	session, csrf string
	secure        bool
}

// cookieNamesFor picks the cookie names. __Host- needs Secure; browsers differ on Secure cookies
// over http://localhost, so dev on an http localhost base URL drops the prefix and Secure. Prod
// already refuses a non-https base URL (S01).
func cookieNamesFor(prod bool, base *url.URL) cookieNames {
	if !prod && base.Scheme == "http" {
		switch base.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return cookieNames{session: DevSessionCookie, csrf: DevCSRFCookie}
		}
	}
	return cookieNames{session: SessionCookie, csrf: CSRFCookie, secure: true}
}

// NewSession is a session just created, with its two secrets. The raw values exist only here and
// in the cookies; the database holds their sha256.
type NewSession struct {
	ID            ids.SessionID
	OrgID         ids.OrgID
	UserID        ids.UserID
	Token         string // the session cookie's value
	CSRF          string // the CSRF cookie's value (and the Deedglass-CSRF header)
	IdleExpiresAt time.Time
	ExpiresAt     time.Time
}

// createSession inserts a session in the caller's org transaction. A fresh id and fresh secrets
// are issued at every sign-in and org switch, so a session id is never fixed by anyone else.
func (s *Service) createSession(ctx context.Context, tx pgx.Tx, org ids.OrgID, user ids.UserID, rotatedFrom *ids.SessionID) (NewSession, error) {
	tok, tokHash, err := NewToken()
	if err != nil {
		return NewSession{}, err
	}
	csrf, csrfHash, err := NewToken()
	if err != nil {
		return NewSession{}, err
	}
	now := s.now().UTC()
	ns := NewSession{ID: ids.New[ids.SessionKind](), OrgID: org, UserID: user, Token: tok, CSRF: csrf,
		IdleExpiresAt: now.Add(SessionIdle), ExpiresAt: now.Add(SessionAbsolute)}
	var ip *string
	if v := clientIP(ctx); v != nil && net.ParseIP(*v) != nil {
		ip = v
	}
	_, err = tx.Exec(ctx, `INSERT INTO deedglass.sessions (id, org_id, user_id, token_hash, csrf_hash, idle_expires_at, expires_at,
	    last_seen_at, ip, user_agent, rotated_from, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::inet, $10, $11, $8)`,
		ns.ID, org, user, tokHash[:], csrfHash[:], ns.IdleExpiresAt, ns.ExpiresAt, now, ip, clipUA(userAgent(ctx)), rotatedFrom)
	return ns, err
}

// setCookies writes the session and CSRF cookies. Max-Age is the time to absolute expiry.
func (s *Service) setCookies(w http.ResponseWriter, ns NewSession) {
	maxAge := int(ns.ExpiresAt.Sub(s.now()).Seconds())
	http.SetCookie(w, &http.Cookie{Name: s.cookies.session, Value: ns.Token, Path: "/", MaxAge: maxAge,
		Secure: s.cookies.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: s.cookies.csrf, Value: ns.CSRF, Path: "/", MaxAge: maxAge,
		Secure: s.cookies.secure, SameSite: http.SameSiteStrictMode})
}

// clearCookies expires both cookies.
func (s *Service) clearCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: s.cookies.session, Value: "", Path: "/", MaxAge: -1,
		Secure: s.cookies.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: s.cookies.csrf, Value: "", Path: "/", MaxAge: -1,
		Secure: s.cookies.secure, SameSite: http.SameSiteStrictMode})
}

// sessionToken reads and hashes the session cookie.
func (s *Service) sessionToken(r *http.Request) ([32]byte, bool) {
	c, err := r.Cookie(s.cookies.session)
	if err != nil {
		return [32]byte{}, false
	}
	h, err := HashToken(c.Value)
	return h, err == nil
}

// AuthenticateSession implements httpx.SessionAuthenticator. It runs, in order: the session
// lookup (store.PreAuth.Session), the expiry checks, cross-origin protection and the bound
// double-submit CSRF token for unsafe methods, and the live membership and role read. The route's
// MinRole is checked by httpx with the role returned here.
func (s *Service) AuthenticateSession(ctx context.Context, r *http.Request) (Principal, error) {
	notSignedIn := httpx.E(httpx.CodeAuthenticationRequired, httpx.Msgf("You're not signed in. Sign in at %s.", s.LoginURL()))
	if s.db == nil {
		return Principal{}, notSignedIn
	}
	tokHash, ok := s.sessionToken(r)
	if !ok {
		return Principal{}, notSignedIn
	}
	ses, err := s.pre.Session(ctx, tokHash[:])
	if errors.Is(err, store.ErrNotFound) {
		return Principal{}, notSignedIn
	}
	if err != nil {
		return Principal{}, err
	}
	now := s.now()
	switch {
	case ses.RevokedAt != nil:
		return Principal{}, httpx.E(httpx.CodeSessionExpired, httpx.Msgf("This session was signed out. Sign in again at %s.", s.LoginURL()))
	case !now.Before(ses.ExpiresAt):
		return Principal{}, httpx.E(httpx.CodeSessionExpired, httpx.Msgf("Your session ended after 7 days. Sign in again at %s.", s.LoginURL()))
	case !now.Before(ses.IdleExpiresAt):
		return Principal{}, httpx.E(httpx.CodeSessionExpired,
			httpx.Msgf("Your session ended after 12 hours without activity. Sign in again at %s.", s.LoginURL()))
	}
	if unsafeMethod(r.Method) {
		if err := s.checkCSRF(r, ses.CSRFHash); err != nil {
			return Principal{}, err
		}
	}
	p := Principal{Kind: KindUser, OrgID: ses.OrgID, UserID: ses.UserID, SessionID: ses.ID}
	touch := ses.LastSeenAt == nil || now.Sub(*ses.LastSeenAt) >= TouchEvery
	read := func(ctx context.Context, tx pgx.Tx) error {
		var role, mstatus, ostatus, ustatus string
		err := tx.QueryRow(ctx, `SELECT m.id, m.role, m.status, o.name, o.status, o.api_version, u.status
		    FROM deedglass.memberships AS m
		    JOIN deedglass.orgs AS o ON o.id = m.org_id
		    JOIN deedglass.users AS u ON u.id = m.user_id
		    WHERE m.org_id = $1 AND m.user_id = $2`, ses.OrgID, ses.UserID).
			Scan(&p.MembershipID, &role, &mstatus, &p.OrgName, &ostatus, &p.APIVersion, &ustatus)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (mstatus != "active" || ostatus != "active" || ustatus == "disabled")) {
			return errAccessEnded
		}
		if err != nil {
			return err
		}
		p.Role = Role(role)
		if touch {
			idle := now.Add(SessionIdle)
			if idle.After(ses.ExpiresAt) {
				idle = ses.ExpiresAt
			}
			_, err = tx.Exec(ctx, `UPDATE deedglass.sessions SET last_seen_at = $2, idle_expires_at = $3
			    WHERE id = $1 AND revoked_at IS NULL`, ses.ID, now, idle)
		}
		return err
	}
	if touch {
		err = s.db.OrgTx(ctx, ses.OrgID, read)
	} else {
		err = s.db.OrgReadTx(ctx, ses.OrgID, read)
	}
	if errors.Is(err, errAccessEnded) {
		return Principal{}, httpx.E(httpx.CodeSessionExpired, httpx.Msgf("Your access to this org ended. Sign in again at %s.", s.LoginURL()))
	}
	if err != nil {
		return Principal{}, err
	}
	return p, nil
}

var errAccessEnded = errors.New("identity: the membership behind this session is no longer active")

func unsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

// checkCSRF applies two of the three CSRF layers (the third is SameSite on the cookies):
// cross-origin protection (Sec-Fetch-Site / Origin), then the double-submit token bound to the
// session: Deedglass-CSRF must equal the CSRF cookie, and its sha256 must equal sessions.csrf_hash.
// Both comparisons are constant time.
func (s *Service) checkCSRF(r *http.Request, stored []byte) error {
	fail := httpx.E(httpx.CodeCSRFFailed)
	if err := s.cop.Check(r); err != nil {
		return fail
	}
	hv := r.Header.Values(CSRFHeader)
	if len(hv) != 1 {
		return fail
	}
	c, err := r.Cookie(s.cookies.csrf)
	if err != nil {
		return fail
	}
	hh, err1 := HashToken(hv[0])
	ch, err2 := HashToken(c.Value)
	if err1 != nil || err2 != nil || !Equal(hh, ch) || !equalBytes(stored, hh) {
		return fail
	}
	return nil
}

// csrfFromCookie returns the CSRF cookie's value when it belongs to the session behind r (GET
// /v1/me hands it to the Console), else "".
func (s *Service) csrfFromCookie(ctx context.Context, r *http.Request) string {
	c, err := r.Cookie(s.cookies.csrf)
	if err != nil {
		return ""
	}
	h, err := HashToken(c.Value)
	if err != nil {
		return ""
	}
	tokHash, ok := s.sessionToken(r)
	if !ok {
		return ""
	}
	ses, err := s.pre.Session(ctx, tokHash[:])
	if err != nil || !equalBytes(ses.CSRFHash, h) {
		return ""
	}
	return c.Value
}

// revokeSession revokes one session in its org's transaction.
func revokeSession(ctx context.Context, tx pgx.Tx, id ids.SessionID, reason string, now time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.sessions SET revoked_at = $2, revoked_reason = $3
	    WHERE id = $1 AND revoked_at IS NULL`, id, now, reason)
	return err
}

// revokeUserSessions revokes every live session of user in the transaction's org.
func revokeUserSessions(ctx context.Context, tx pgx.Tx, user ids.UserID, reason string, now time.Time) (int64, error) {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.sessions SET revoked_at = $2, revoked_reason = $3
	    WHERE user_id = $1 AND revoked_at IS NULL`, user, now, reason)
	return tag.RowsAffected(), err
}

// Logout revokes the principal's session, or with everywhere every session of theirs in every
// org they belong to, and records user.signed_out.
func (s *Service) Logout(ctx context.Context, p *Principal, everywhere bool) error {
	now := s.now()
	err := s.db.OrgTx(ctx, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if everywhere {
			_, err = revokeUserSessions(ctx, tx, p.UserID, RevokeLogoutEverywhere, now)
		} else {
			err = revokeSession(ctx, tx, p.SessionID, RevokeLogout, now)
		}
		if err != nil {
			return err
		}
		return s.event(ctx, tx, ledger.AdminEvent{Action: ledger.AdminUserSignedOut, Actor: userRef(p.UserID),
			Target: sessionRef(p.SessionID), Data: map[string]any{"everywhere": everywhere}})
	})
	if err != nil || !everywhere {
		return err
	}
	// The user's sessions in their other orgs. Users are global and memberships list the orgs.
	email, err := s.userEmail(ctx, p.OrgID, p.UserID)
	if err != nil {
		return err
	}
	u, err := s.pre.UserByEmail(ctx, email)
	if err != nil {
		return err
	}
	for _, m := range u.Memberships {
		if m.OrgID == p.OrgID {
			continue
		}
		if err := s.db.OrgTx(ctx, m.OrgID, func(ctx context.Context, tx pgx.Tx) error {
			_, err := revokeUserSessions(ctx, tx, p.UserID, RevokeLogoutEverywhere, now)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// SwitchOrg issues a new session in another org the principal is an active member of, and revokes
// the current one.
func (s *Service) SwitchOrg(ctx context.Context, p *Principal, target ids.OrgID) (NewSession, error) {
	email, err := s.userEmail(ctx, p.OrgID, p.UserID)
	if err != nil {
		return NewSession{}, err
	}
	u, err := s.pre.UserByEmail(ctx, email)
	if err != nil {
		return NewSession{}, err
	}
	member := false
	for _, m := range u.Memberships {
		if m.OrgID == target && m.Status == "active" && m.OrgStatus == "active" {
			member = true
		}
	}
	if !member {
		return NewSession{}, httpx.E(httpx.CodeNotFound, httpx.Param("org"),
			httpx.Msgf("You aren't a member of an org with id %s. GET /v1/me lists your orgs.", target))
	}
	if target == p.OrgID {
		return NewSession{}, httpx.E(httpx.CodeConflict, httpx.Param("org"), httpx.Msg("You're already signed in to this org."))
	}
	now := s.now()
	var ns NewSession
	old := p.SessionID
	err = s.db.OrgTx(ctx, target, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if ns, err = s.createSession(ctx, tx, target, p.UserID, &old); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.memberships SET last_used_at = $3 WHERE org_id = $1 AND user_id = $2`,
			target, p.UserID, now); err != nil {
			return err
		}
		return s.event(ctx, tx, ledger.AdminEvent{Action: ledger.AdminUserSignedIn, Actor: userRef(p.UserID),
			Target: sessionRef(ns.ID), Data: map[string]any{"via": "org_switch"}})
	})
	if err != nil {
		return NewSession{}, err
	}
	err = s.db.OrgTx(ctx, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		return revokeSession(ctx, tx, old, RevokeOrgSwitch, now)
	})
	return ns, err
}

// userEmail reads a member's email inside their org.
func (s *Service) userEmail(ctx context.Context, org ids.OrgID, user ids.UserID) (string, error) {
	var email string
	err := s.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT email FROM deedglass.users WHERE id = $1`, user).Scan(&email)
	})
	return email, err
}

type uaKey struct{}

// withUserAgent carries the request's User-Agent to code that stores it (sessions, tokens).
func withUserAgent(ctx context.Context, ua string) context.Context {
	return context.WithValue(ctx, uaKey{}, ua)
}

func userAgent(ctx context.Context) string {
	ua, _ := ctx.Value(uaKey{}).(string)
	return ua
}
