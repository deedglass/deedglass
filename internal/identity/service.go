package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mailer"
	"github.com/deedglass/deedglass/internal/store"
)

// Session lifetimes and limits (S07 §3.6, §3.12). They stay constants until someone needs to
// change them.
const (
	SessionIdle     = 12 * time.Hour
	SessionAbsolute = 7 * 24 * time.Hour
	TouchEvery      = 5 * time.Minute // last_seen_at and idle_expires_at are written at most this often

	LoginPerIP      = 20 // sign-in requests per IP per hour
	LoginPerEmail   = 5  // sign-in requests per (peppered) email per hour, known or not
	TokensPerUser   = 5  // tokens created per user per hour, counted in the database (global)
	FailuresPerIP   = 30 // failed previews, callbacks and codes per IP per hour
	limitWindow     = time.Hour
	maxUserAgentLen = 512
)

// MailQueue is where sign-in and invite emails go: mailer.Queue in serve. Submit never blocks;
// false means the job was dropped (and the queue logged it).
type MailQueue interface {
	Submit(kind, userID, requestID string, j mailer.Job) bool
}

// Options configure a Service.
type Options struct {
	DB      *store.DB
	Ledger  *ledger.Ledger // seals admin deeds
	Mail    MailQueue      // nil: no email (prod without Resend; use deedglass org login-link)
	Pepper  []byte         // DEEDGLASS_API_KEY_PEPPER, decoded: peppers code hashes and email-limit keys
	BaseURL *url.URL       // links in emails and the one trusted origin
	Signup  string         // "invite" (default) | "closed"
	Prod    bool
	Logger  *slog.Logger
	Now     func() time.Time
}

// Service is people: sign-in, sessions, orgs and members. It implements
// httpx.SessionAuthenticator.
type Service struct {
	db      *store.DB
	pre     store.PreAuth
	led     *ledger.Ledger
	mail    MailQueue
	pepper  []byte
	base    *url.URL
	signup  string
	prod    bool
	log     *slog.Logger
	now     func() time.Time
	cookies cookieNames
	cop     *http.CrossOriginProtection

	loginIP    *httpx.Limiter
	loginEmail *httpx.Limiter
	failIP     *httpx.Limiter

	// failNotify, when set (tests), fails deedglass_identity notifications: the change must roll
	// back with no deed and no admin event.
	failNotify func() error
}

// New checks the options and returns a Service.
func New(o Options) (*Service, error) {
	if o.BaseURL == nil {
		return nil, errors.New("identity: BaseURL is required")
	}
	if len(o.Pepper) < 32 {
		return nil, errors.New("identity: the pepper must be at least 32 bytes")
	}
	switch o.Signup {
	case "":
		o.Signup = "invite"
	case "invite", "closed":
	default:
		return nil, errors.New("identity: Signup must be invite or closed")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Service{db: o.DB, led: o.Ledger, mail: o.Mail, pepper: o.Pepper, base: o.BaseURL, signup: o.Signup,
		prod: o.Prod, log: o.Logger, now: o.Now, cookies: cookieNamesFor(o.Prod, o.BaseURL),
		loginIP:    httpx.NewLimiter(LoginPerIP, limitWindow, o.Now),
		loginEmail: httpx.NewLimiter(LoginPerEmail, limitWindow, o.Now),
		failIP:     httpx.NewLimiter(FailuresPerIP, limitWindow, o.Now),
	}
	if o.DB != nil {
		s.pre = o.DB.PreAuth()
	}
	s.cop = http.NewCrossOriginProtection()
	if err := s.cop.AddTrustedOrigin(origin(o.BaseURL)); err != nil {
		return nil, err
	}
	return s, nil
}

// InsecureDevCookies reports whether the session cookies drop the __Host- prefix and Secure: only
// in dev, on an http base URL whose host is localhost or 127.0.0.1 (serve logs it once).
func (s *Service) InsecureDevCookies() bool { return !s.cookies.secure }

// CookieNames returns the session and CSRF cookie names in use.
func (s *Service) CookieNames() (session, csrf string) { return s.cookies.session, s.cookies.csrf }

func origin(u *url.URL) string { return u.Scheme + "://" + u.Host }

// baseURL is DEEDGLASS_BASE_URL without a trailing slash.
func (s *Service) baseURL() string { return strings.TrimSuffix(s.base.String(), "/") }

// LoginURL is where people sign in.
func (s *Service) LoginURL() string { return s.baseURL() + "/login" }

// ConfirmLink is the link an email carries: the token sits in the fragment, which browsers never
// send to servers.
func (s *Service) ConfirmLink(rawToken string) string {
	return s.baseURL() + "/auth/confirm#t=" + rawToken
}

// emailKey is the rate-limit key for an address: sha256(pepper ‖ domain ‖ email), so the limiter
// never holds an address.
func (s *Service) emailKey(email string) string {
	h := sha256.New()
	h.Write(s.pepper)
	h.Write([]byte("deedglass/login-email/v1\n"))
	h.Write([]byte(email))
	return hex.EncodeToString(h.Sum(nil))
}

// recordAdmin seals an admin deed for a change made in this package (S06's one admin-deed path).
func (s *Service) recordAdmin(ctx context.Context, tx pgx.Tx, e ledger.AdminEvent) (*string, error) {
	if s.led == nil {
		return nil, errors.New("identity: no ledger to seal admin deeds with")
	}
	if e.RequestID == "" {
		e.RequestID = httpx.RequestID(ctx)
	}
	env, err := s.led.RecordAdmin(ctx, tx, e)
	if err != nil {
		return nil, err
	}
	id := env.ID
	return &id, nil
}

// event writes an unsealed admin event (sign-in, sign-out).
func (s *Service) event(ctx context.Context, tx pgx.Tx, e ledger.AdminEvent) error {
	if s.led == nil {
		return nil
	}
	if e.RequestID == "" {
		e.RequestID = httpx.RequestID(ctx)
	}
	_, err := s.led.AppendAdminEvent(ctx, tx, e)
	return err
}

// clipUA keeps at most 512 bytes of a User-Agent, cut at a rune boundary.
func clipUA(ua string) *string {
	if ua == "" {
		return nil
	}
	if len(ua) > maxUserAgentLen {
		ua = strings.ToValidUTF8(ua[:maxUserAgentLen], "")
	}
	return &ua
}

// clientIP is the request's client address as S03's trusted-proxy logic resolved it, or nil.
func clientIP(ctx context.Context) *string {
	ip := httpx.ClientIP(ctx)
	if ip == "" {
		return nil
	}
	return &ip
}

// orgOf returns the principal's org, or 401.
func principalOf(r *http.Request) (*Principal, error) {
	p, ok := PrincipalFrom(r.Context())
	if !ok || p.Kind != KindUser || p.OrgID.IsZero() || p.UserID.IsZero() {
		return nil, httpx.E(httpx.CodeAuthenticationRequired)
	}
	return p, nil
}

// orgName reads the org's name inside an org transaction.
func orgName(ctx context.Context, tx pgx.Tx, org ids.OrgID) (string, error) {
	var name string
	err := tx.QueryRow(ctx, `SELECT name FROM deedglass.orgs WHERE id = $1`, org).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrNotFound
	}
	return name, err
}
