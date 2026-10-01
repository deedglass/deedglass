package identity

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mailer"
	"github.com/deedglass/deedglass/internal/store"
)

// Token purposes (login_tokens.purpose).
const (
	PurposeLogin  = "login"
	PurposeInvite = "invite"
)

// LoginRequest is the 202 body of POST /v1/auth/login. It is the same for every address.
type LoginRequest struct {
	Object    string `json:"object"` // "login_request"
	Email     string `json:"email"`
	ExpiresIn int    `json:"expires_in"`
	Message   string `json:"message"`
}

// RequestLogin validates the address, applies the IP and email limits and queues the sign-in job.
// It never reads the database: the worker does, so the answer and its timing don't depend on
// whether an account exists.
func (s *Service) RequestLogin(ctx context.Context, rawEmail, ip, ua string) (LoginRequest, error) {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return LoginRequest{}, httpx.ErrValidation("email", err.Error())
	}
	if ok, wait := s.loginIP.Allow(ipKey(ip)); !ok {
		return LoginRequest{}, signInLimited(wait, "from this network")
	}
	if ok, wait := s.loginEmail.Allow(s.emailKey(email)); !ok {
		return LoginRequest{}, signInLimited(wait, "for this email address")
	}
	rid := httpx.RequestID(ctx)
	if s.mail != nil {
		s.mail.Submit(PurposeLogin, "", rid, s.loginJob(email, ip, ua, rid, ids.OrgID{}))
	}
	return LoginRequest{Object: "login_request", Email: email, ExpiresIn: int(LoginTTL.Seconds()),
		Message: fmt.Sprintf("If %s has a Deedglass account, a sign-in link and code are on their way.", email)}, nil
}

func signInLimited(wait time.Duration, scope string) *httpx.Error {
	n := httpx.RetryMinutes(wait)
	return httpx.E(httpx.CodeRateLimited, httpx.Msgf("Too many sign-in requests %s. Try again in %d %s.", scope, n, plural(n, "minute", "minutes")),
		httpx.WithHeader("Retry-After", fmt.Sprint(max(1, int(wait.Seconds())))))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ipKey normalizes an address for the limiter; an unparsable one shares one bucket.
func ipKey(ip string) string {
	if a := net.ParseIP(ip); a != nil {
		return a.String()
	}
	return "unknown"
}

// EmailSignIn sends a sign-in email (link and code) for org now, through m: deedglass org create
// in prod. It is the sign-in worker's job, run inline.
func (s *Service) EmailSignIn(ctx context.Context, m mailer.Mailer, email string, org ids.OrgID) error {
	return s.loginJob(email, "", "", "", org)(ctx, m)
}

// loginJob is the sign-in worker for one request: find the person, pick their landing org (or
// org, when set and they are an active member), void their older sign-in tokens there, store a new
// token and code (hashes only), then send the email after the transaction commits. Unknown
// addresses, disabled people and capped users stop silently.
func (s *Service) loginJob(email, ip, ua, rid string, only ids.OrgID) mailer.Job {
	return func(ctx context.Context, m mailer.Mailer) error {
		u, err := s.pre.UserByEmail(ctx, email)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if u.Status == "disabled" {
			return nil
		}
		if u.TokensLastHour >= TokensPerUser {
			s.log.InfoContext(ctx, "login.capped", "user_id", u.ID.String(), "request_id", rid)
			return nil
		}
		org, ok := landingOrg(u.Memberships)
		if !only.IsZero() {
			ok = false
			for _, m := range u.Memberships {
				if m.OrgID == only && m.Status == "active" && m.OrgStatus == "active" {
					org, ok = only, true
				}
			}
		}
		if !ok {
			return nil
		}
		ctx = withUserAgent(ctx, ua)
		raw, hash, err := NewToken()
		if err != nil {
			return err
		}
		code, err := NewCode()
		if err != nil {
			return err
		}
		now := s.now().UTC()
		tokID := ids.New[ids.LoginTokenKind]()
		var name string
		err = s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			if name, err = orgName(ctx, tx, org); err != nil {
				return err
			}
			if err := voidTokens(ctx, tx, u.ID, PurposeLogin, now); err != nil {
				return err
			}
			codeHash := HashCode(s.pepper, tokID.String(), code)
			return insertToken(ctx, tx, tokenRow{id: tokID, org: org, user: u.ID, purpose: PurposeLogin, hash: hash,
				codeHash: codeHash[:], expires: now.Add(LoginTTL), ip: ip, ua: ua})
		})
		if err != nil {
			return err
		}
		msg, err := mailer.LoginMessage(mailer.LoginData{OrgName: name, Email: email, Link: s.ConfirmLink(raw), Code: code,
			RequestedFrom: DescribeUserAgent(ua), RequestedAt: now, ExpiresIn: LoginTTL})
		if err != nil {
			return err
		}
		msg.IdempotencyKey = "login/" + tokID.String()
		msg.UserID, msg.RequestID = u.ID.String(), rid
		return m.Send(ctx, msg)
	}
}

// landingOrg is the active membership used most recently; ties go to the one joined first.
func landingOrg(ms []store.PreAuthMembership) (ids.OrgID, bool) {
	var live []store.PreAuthMembership
	for _, m := range ms {
		if m.Status == "active" && m.OrgStatus == "active" {
			live = append(live, m)
		}
	}
	if len(live) == 0 {
		return ids.OrgID{}, false
	}
	slices.SortStableFunc(live, func(a, b store.PreAuthMembership) int {
		switch {
		case a.LastUsedAt != nil && b.LastUsedAt == nil:
			return -1
		case a.LastUsedAt == nil && b.LastUsedAt != nil:
			return 1
		case a.LastUsedAt != nil && !a.LastUsedAt.Equal(*b.LastUsedAt):
			return b.LastUsedAt.Compare(*a.LastUsedAt)
		}
		switch {
		case a.JoinedAt != nil && b.JoinedAt != nil:
			return a.JoinedAt.Compare(*b.JoinedAt)
		case a.JoinedAt != nil:
			return -1
		case b.JoinedAt != nil:
			return 1
		}
		return 0
	})
	return live[0].OrgID, true
}

type tokenRow struct {
	id         ids.LoginTokenID
	org        ids.OrgID
	user       ids.UserID
	membership *ids.MembershipID
	purpose    string
	hash       [32]byte
	codeHash   []byte
	expires    time.Time
	ip, ua     string
}

func insertToken(ctx context.Context, tx pgx.Tx, t tokenRow) error {
	var ip *string
	if a := net.ParseIP(t.ip); a != nil {
		v := a.String()
		ip = &v
	}
	_, err := tx.Exec(ctx, `INSERT INTO deedglass.login_tokens (id, org_id, user_id, membership_id, purpose, token_hash, code_hash,
	    expires_at, requested_ip, requested_ua) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::inet, $10)`,
		t.id, t.org, t.user, t.membership, t.purpose, t.hash[:], t.codeHash, t.expires, ip, clipUA(t.ua))
	return err
}

// voidTokens voids a user's unused tokens of one purpose in the transaction's org, so only the
// newest email works.
func voidTokens(ctx context.Context, tx pgx.Tx, user ids.UserID, purpose string, now time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.login_tokens SET voided_at = $3
	    WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL AND voided_at IS NULL`, user, purpose, now)
	return err
}

// Preview is POST /v1/auth/preview: what a link would do, without using it. The confirm page shows
// whose account and which org before anyone clicks, which is what stops login CSRF.
type Preview struct {
	Object    string     `json:"object"` // "login_preview"
	Purpose   string     `json:"purpose"`
	Email     string     `json:"email"`
	Org       PreviewOrg `json:"org"`
	Role      string     `json:"role"`
	InvitedBy *PersonRef `json:"invited_by"`
	ExpiresAt httpx.Time `json:"expires_at"`
}

// PreviewOrg is the org a link signs into.
type PreviewOrg struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PersonRef names a person: their name ("" when unset) and email.
type PersonRef struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// linkInvalid is login_link_invalid with the base URL.
func (s *Service) linkInvalid() *httpx.Error {
	return httpx.E(httpx.CodeLoginLinkInvalid, httpx.Msgf("This sign-in link has expired or was already used. Get a new one at %s.", s.LoginURL()))
}

// failureBudget refuses an IP that has spent its failures for the hour (preview, callback, codes).
func (s *Service) failureBudget(ip string) error {
	if ok, wait := s.failIP.Check(ipKey(ip)); !ok {
		n := httpx.RetryMinutes(wait)
		return httpx.E(httpx.CodeRateLimited, httpx.Msgf("Too many attempts from this network. Try again in %d %s.", n, plural(n, "minute", "minutes")),
			httpx.WithHeader("Retry-After", fmt.Sprint(max(1, int(wait.Seconds())))))
	}
	return nil
}

func (s *Service) chargeFailure(ip string) { s.failIP.Allow(ipKey(ip)) }

// liveToken looks a raw token up and checks it is unused, unvoided and unexpired.
func (s *Service) liveToken(ctx context.Context, raw string) (store.PreAuthToken, error) {
	h, err := HashToken(raw)
	if err != nil {
		return store.PreAuthToken{}, s.linkInvalid()
	}
	t, err := s.pre.LoginToken(ctx, h[:])
	if errors.Is(err, store.ErrNotFound) {
		return store.PreAuthToken{}, s.linkInvalid()
	}
	if err != nil {
		return store.PreAuthToken{}, err
	}
	if t.UsedAt != nil || t.VoidedAt != nil || !s.now().Before(t.ExpiresAt) {
		return store.PreAuthToken{}, s.linkInvalid()
	}
	return t, nil
}

// Preview returns what the link would do. It consumes nothing.
func (s *Service) Preview(ctx context.Context, raw, ip string) (Preview, error) {
	if err := s.failureBudget(ip); err != nil {
		return Preview{}, err
	}
	t, err := s.liveToken(ctx, raw)
	if err != nil {
		if httpx.AsError(err) != nil {
			s.chargeFailure(ip)
		}
		return Preview{}, err
	}
	pv := Preview{Object: "login_preview", Purpose: t.Purpose, Org: PreviewOrg{ID: t.OrgID.String()}, ExpiresAt: httpx.Time(t.ExpiresAt)}
	err = s.db.OrgReadTx(ctx, t.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var mstatus, ostatus string
		var invBy *ids.UserID
		err := tx.QueryRow(ctx, `SELECT u.email, o.name, o.status, m.role, m.status, m.invited_by
		    FROM deedglass.memberships AS m
		    JOIN deedglass.users AS u ON u.id = m.user_id
		    JOIN deedglass.orgs AS o ON o.id = m.org_id
		    WHERE m.org_id = $1 AND m.user_id = $2`, t.OrgID, t.UserID).
			Scan(&pv.Email, &pv.Org.Name, &ostatus, &pv.Role, &mstatus, &invBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return s.linkInvalid()
		}
		if err != nil {
			return err
		}
		if ostatus != "active" || !purposeFits(t.Purpose, mstatus) {
			return s.linkInvalid()
		}
		if t.Purpose == PurposeInvite && invBy != nil {
			var name *string
			ref := PersonRef{}
			if err := tx.QueryRow(ctx, `SELECT email, display_name FROM deedglass.users WHERE id = $1`, *invBy).Scan(&ref.Email, &name); err == nil {
				if name != nil {
					ref.Name = *name
				}
				pv.InvitedBy = &ref
			}
		}
		return nil
	})
	if err != nil {
		if httpx.AsError(err) != nil {
			s.chargeFailure(ip)
		}
		return Preview{}, err
	}
	return pv, nil
}

// purposeFits: a login link needs an active membership; an invite link needs a pending invite.
func purposeFits(purpose, membershipStatus string) bool {
	return (purpose == PurposeLogin && membershipStatus == "active") || (purpose == PurposeInvite && membershipStatus == "invited")
}

// SignedIn is a completed sign-in: the new session and who it is for.
type SignedIn struct {
	Session NewSession
	Via     string // "link" | "code" | "invite"
}

// RedeemToken uses a link: atomically marks the token used (only while unused, unvoided and
// unexpired), accepts the invite when it is one, and opens a session. A token works once.
func (s *Service) RedeemToken(ctx context.Context, raw, ip, ua string) (SignedIn, error) {
	if err := s.failureBudget(ip); err != nil {
		return SignedIn{}, err
	}
	t, err := s.liveToken(ctx, raw)
	if err != nil {
		if httpx.AsError(err) != nil {
			s.chargeFailure(ip)
		}
		return SignedIn{}, err
	}
	ctx = withUserAgent(ctx, ua)
	var out SignedIn
	err = s.db.OrgTx(ctx, t.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now().UTC()
		var purpose string
		var mem *ids.MembershipID
		err := tx.QueryRow(ctx, `UPDATE deedglass.login_tokens SET used_at = $2
		    WHERE id = $1 AND used_at IS NULL AND voided_at IS NULL AND expires_at > $2
		    RETURNING purpose, membership_id`, t.ID, now).Scan(&purpose, &mem)
		if errors.Is(err, pgx.ErrNoRows) {
			return s.linkInvalid() // a concurrent callback won, or it just expired
		}
		if err != nil {
			return err
		}
		via := "link"
		if purpose == PurposeInvite {
			via = "invite"
		}
		out, err = s.completeSignIn(ctx, tx, t.OrgID, t.UserID, purpose, via, now)
		return err
	})
	if err != nil {
		if httpx.AsError(err) != nil {
			s.chargeFailure(ip)
		}
		return SignedIn{}, err
	}
	return out, nil
}

// RedeemCode signs in with an email address and a 6-digit code. Every miss (unknown address, no
// live token, wrong code, too many attempts, expired) gets the same login_code_invalid, and a
// dummy hash is computed when there is nothing to compare, so the timing stays close.
func (s *Service) RedeemCode(ctx context.Context, rawEmail, rawCode, ip, ua string) (SignedIn, error) {
	if err := s.failureBudget(ip); err != nil {
		return SignedIn{}, err
	}
	miss := func() (SignedIn, error) {
		s.chargeFailure(ip)
		return SignedIn{}, httpx.E(httpx.CodeLoginCodeInvalid)
	}
	email, eerr := NormalizeEmail(rawEmail)
	code, cok := NormalizeCode(rawCode)
	if eerr != nil || !cok {
		_ = HashCode(s.pepper, "ltk_dummy", "000000")
		return miss()
	}
	u, err := s.pre.UserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		_ = HashCode(s.pepper, "ltk_dummy", code)
		return miss()
	}
	if err != nil {
		return SignedIn{}, err
	}
	tokID, org, err := s.pre.CodeToken(ctx, u.ID)
	if errors.Is(err, store.ErrNotFound) {
		_ = HashCode(s.pepper, "ltk_dummy", code)
		return miss()
	}
	if err != nil {
		return SignedIn{}, err
	}
	ctx = withUserAgent(ctx, ua)
	var out SignedIn
	missed := false // a miss still commits: the attempt counter must stick
	err = s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now().UTC()
		var stored []byte
		var user ids.UserID
		var purpose string
		// Count the attempt in the same statement that reads the hash: at 5 the token is dead.
		err := tx.QueryRow(ctx, `UPDATE deedglass.login_tokens SET code_attempts = code_attempts + 1
		    WHERE id = $1 AND used_at IS NULL AND voided_at IS NULL AND expires_at > $2 AND code_attempts < $3 AND code_hash IS NOT NULL
		    RETURNING code_hash, user_id, purpose`, tokID, now, MaxCodeAttempts).Scan(&stored, &user, &purpose)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = HashCode(s.pepper, tokID.String(), code)
			missed = true
			return nil
		}
		if err != nil {
			return err
		}
		if !equalBytes(stored, HashCode(s.pepper, tokID.String(), code)) || user != u.ID || purpose != PurposeLogin {
			missed = true
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.login_tokens SET used_at = $2 WHERE id = $1`, tokID, now); err != nil {
			return err
		}
		out, err = s.completeSignIn(ctx, tx, org, u.ID, PurposeLogin, "code", now)
		return err
	})
	if err == nil && missed {
		return miss()
	}
	if err != nil {
		if httpx.AsError(err) != nil {
			s.chargeFailure(ip)
		}
		return SignedIn{}, err
	}
	return out, nil
}
