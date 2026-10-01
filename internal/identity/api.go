package identity

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

// Me is GET /v1/me: who is signed in, where, and the CSRF token the Console sends back.
type Me struct {
	Object     string       `json:"object"` // "me"
	User       MeUser       `json:"user"`
	Org        Org          `json:"org"`
	Membership MeMembership `json:"membership"`
	Orgs       []MeOrg      `json:"orgs"`
	Session    MeSession    `json:"session"`
	CSRFToken  *string      `json:"csrf_token"`
}

// MeUser is the signed-in person.
type MeUser struct {
	Object    string     `json:"object"` // "user"
	ID        string     `json:"id"`
	Email     string     `json:"email"`
	Name      string     `json:"name"`
	CreatedAt httpx.Time `json:"created_at"`
}

// MeMembership is the person's membership in the session's org.
type MeMembership struct {
	Object string `json:"object"` // "membership"
	ID     string `json:"id"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

// MeOrg is one org the person can switch to.
type MeOrg struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// MeSession is the current session's lifetime.
type MeSession struct {
	ID            string     `json:"id"`
	IdleExpiresAt httpx.Time `json:"idle_expires_at"`
	ExpiresAt     httpx.Time `json:"expires_at"`
}

// Me builds the me object for a session.
func (s *Service) Me(ctx context.Context, org ids.OrgID, user ids.UserID, ses ids.SessionID, csrf string) (Me, error) {
	me := Me{Object: "me", User: MeUser{Object: "user", ID: user.String()}, Membership: MeMembership{Object: "membership"},
		Session: MeSession{ID: ses.String()}, Orgs: []MeOrg{}}
	if csrf != "" {
		me.CSRFToken = &csrf
	}
	err := s.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var name *string
		var created time.Time
		if err := tx.QueryRow(ctx, `SELECT email, display_name, created_at FROM deedglass.users WHERE id = $1`, user).
			Scan(&me.User.Email, &name, &created); err != nil {
			return err
		}
		if name != nil {
			me.User.Name = *name
		}
		me.User.CreatedAt = httpx.Time(created)
		var err error
		if me.Org, err = getOrg(ctx, tx, org); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id, role, status FROM deedglass.memberships WHERE org_id = $1 AND user_id = $2`, org, user).
			Scan(&me.Membership.ID, &me.Membership.Role, &me.Membership.Status); err != nil {
			return err
		}
		var idle, abs time.Time
		if err := tx.QueryRow(ctx, `SELECT idle_expires_at, expires_at FROM deedglass.sessions WHERE id = $1`, ses).Scan(&idle, &abs); err != nil {
			return err
		}
		me.Session.IdleExpiresAt, me.Session.ExpiresAt = httpx.Time(idle), httpx.Time(abs)
		return nil
	})
	if err != nil {
		return Me{}, err
	}
	u, err := s.pre.UserByEmail(ctx, me.User.Email)
	if err != nil {
		return Me{}, err
	}
	for _, m := range u.Memberships {
		if m.Status == "active" && m.OrgStatus == "active" {
			me.Orgs = append(me.Orgs, MeOrg{ID: m.OrgID.String(), Name: m.OrgName, Role: m.Role})
		}
	}
	return me, nil
}

// Routes are S07's API routes and pages (contract §9). Public auth routes take no
// Idempotency-Key: they have no principal to scope it to.
func (s *Service) Routes() []httpx.Route {
	return append([]httpx.Route{
		{Method: "POST", Pattern: "/v1/auth/login", OperationID: "requestLogin", Auth: httpx.Public, Idempotency: httpx.IdemNever,
			MaxBody: 4 << 10, Handler: s.handleLogin},
		{Method: "POST", Pattern: "/v1/auth/preview", OperationID: "previewLogin", Auth: httpx.Public, Idempotency: httpx.IdemNever,
			MaxBody: 4 << 10, Handler: s.handlePreview},
		{Method: "POST", Pattern: "/v1/auth/callback", OperationID: "completeLogin", Auth: httpx.Public, Idempotency: httpx.IdemNever,
			MaxBody: 4 << 10, Handler: s.handleCallback},
		{Method: "POST", Pattern: "/v1/auth/logout", OperationID: "logout", Auth: httpx.Session, MinRole: RoleViewer,
			Idempotency: httpx.IdemNever, MaxBody: 4 << 10, Purpose: "signing out", Handler: s.handleLogout},
		{Method: "POST", Pattern: "/v1/auth/switch-org", OperationID: "switchOrg", Auth: httpx.Session, MinRole: RoleViewer,
			Idempotency: httpx.IdemNever, MaxBody: 4 << 10, Purpose: "switching orgs", Handler: s.handleSwitchOrg},
		{Method: "GET", Pattern: "/v1/me", OperationID: "getMe", Auth: httpx.Session, MinRole: RoleViewer, Handler: s.handleGetMe},
		{Method: "PATCH", Pattern: "/v1/me", OperationID: "updateMe", Auth: httpx.Session, MinRole: RoleViewer, MaxBody: 4 << 10,
			Purpose: "changing your name", Handler: s.handlePatchMe},
		{Method: "GET", Pattern: "/v1/org", OperationID: "getOrg", Auth: httpx.Session, MinRole: RoleViewer, Handler: s.handleGetOrg},
		{Method: "PATCH", Pattern: "/v1/org", OperationID: "updateOrg", Auth: httpx.Session, MinRole: RoleAdmin, MaxBody: 4 << 10,
			Purpose: "changing the org's name or time zone", Handler: s.handlePatchOrg},
		{Method: "GET", Pattern: "/v1/org/members", OperationID: "listMembers", Auth: httpx.Session, MinRole: RoleViewer, Handler: s.handleListMembers},
		{Method: "POST", Pattern: "/v1/org/members", OperationID: "inviteMember", Auth: httpx.Session, MinRole: RoleAdmin,
			Idempotency: httpx.IdemOptional, MaxBody: 4 << 10, Purpose: "inviting people", Handler: s.handleInvite},
		{Method: "PATCH", Pattern: "/v1/org/members/{id}", OperationID: "updateMember", Auth: httpx.Session, MinRole: RoleOwner,
			MaxBody: 4 << 10, Purpose: "changing roles", Handler: s.handlePatchMember},
		{Method: "DELETE", Pattern: "/v1/org/members/{id}", OperationID: "removeMember", Auth: httpx.Session, MinRole: RoleAdmin,
			Purpose: "removing people", Handler: s.handleRemoveMember},
	}, s.pageRoutes()...)
}

// reqCtx carries the User-Agent to code that stores it.
func reqCtx(r *http.Request) context.Context { return withUserAgent(r.Context(), r.UserAgent()) }

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email string `json:"email"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	ctx := reqCtx(r)
	out, err := s.RequestLogin(ctx, in.Email, httpx.ClientIP(ctx), r.UserAgent())
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusAccepted, out)
}

func (s *Service) handlePreview(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token string `json:"token"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	pv, err := s.Preview(r.Context(), in.Token, httpx.ClientIP(r.Context()))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, pv)
}

func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token string `json:"token"`
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	ctx := reqCtx(r)
	ip := httpx.ClientIP(ctx)
	var (
		si  SignedIn
		err error
	)
	switch {
	case in.Token != "" && in.Email == "" && in.Code == "":
		si, err = s.RedeemToken(ctx, in.Token, ip, r.UserAgent())
	case in.Token == "" && in.Email != "" && in.Code != "":
		si, err = s.RedeemCode(ctx, in.Email, in.Code, ip, r.UserAgent())
	default:
		return httpx.ErrValidation("", "Send either token, or email and code.")
	}
	if err != nil {
		return err
	}
	me, err := s.Me(ctx, si.Session.OrgID, si.Session.UserID, si.Session.ID, si.Session.CSRF)
	if err != nil {
		return err
	}
	s.setCookies(w, si.Session)
	httpx.Annotate(ctx, si.Session.OrgID.String(), "", "")
	return httpx.WriteJSON(w, http.StatusOK, me)
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) error {
	p, err := principalOf(r)
	if err != nil {
		return err
	}
	var in struct {
		Everywhere bool `json:"everywhere"`
	}
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(r, &in); err != nil {
			return err
		}
	}
	if err := s.Logout(reqCtx(r), p, in.Everywhere); err != nil {
		return err
	}
	s.clearCookies(w)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Service) handleSwitchOrg(w http.ResponseWriter, r *http.Request) error {
	p, err := principalOf(r)
	if err != nil {
		return err
	}
	var in struct {
		Org string `json:"org"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	target, err := ids.Parse[ids.OrgKind](in.Org)
	if err != nil {
		return httpx.ErrValidation("org", "`org` must be an org id (org_…). GET /v1/me lists your orgs.")
	}
	ctx := reqCtx(r)
	ns, err := s.SwitchOrg(ctx, p, target)
	if err != nil {
		return err
	}
	me, err := s.Me(ctx, ns.OrgID, ns.UserID, ns.ID, ns.CSRF)
	if err != nil {
		return err
	}
	s.setCookies(w, ns)
	return httpx.WriteJSON(w, http.StatusOK, me)
}

func (s *Service) handleGetMe(w http.ResponseWriter, r *http.Request) error {
	p, err := principalOf(r)
	if err != nil {
		return err
	}
	me, err := s.Me(r.Context(), p.OrgID, p.UserID, p.SessionID, s.csrfFromCookie(r.Context(), r))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, me)
}

func (s *Service) handlePatchMe(w http.ResponseWriter, r *http.Request) error {
	p, err := principalOf(r)
	if err != nil {
		return err
	}
	var in struct {
		Name *string `json:"name"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	if in.Name == nil {
		return httpx.ErrValidation("name", "Send name: the name other members see, or an empty string to clear it.")
	}
	name := strings.TrimSpace(*in.Name)
	if utf8.RuneCountInString(name) > 80 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return httpx.ErrValidation("name", "`name` must be at most 80 characters, without line breaks.")
	}
	var v *string
	if name != "" {
		v = &name
	}
	if err := s.db.OrgTx(r.Context(), p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.users SET display_name = $2 WHERE id = $1`, p.UserID, v)
		return err
	}); err != nil {
		return err
	}
	return s.handleGetMe(w, r)
}

func (s *Service) handleGetOrg(w http.ResponseWriter, r *http.Request) error {
	p, err := principalOf(r)
	if err != nil {
		return err
	}
	o, err := s.GetOrg(r.Context(), p)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, o)
}

// OrgWithDeed is the PATCH /v1/org response: the org and the org.updated deed (null when nothing
// changed).
type OrgWithDeed struct {
	Org
	Deed *string `json:"deed"`
}

func (s *Service) handlePatchOrg(w http.ResponseWriter, r *http.Request) error {
	p, err := principalOf(r)
	if err != nil {
		return err
	}
	var in OrgPatch
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	o, d, err := s.UpdateOrg(reqCtx(r), p, in)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, OrgWithDeed{Org: o, Deed: d})
}

func (s *Service) handleListMembers(w http.ResponseWriter, r *http.Request) error {
	p, err := principalOf(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	for k := range q {
		if k != "status" && k != "limit" && k != "cursor" {
			return httpx.ErrValidation(k, "`"+clipID(k)+"` isn't a filter here. Use status, limit or cursor.")
		}
	}
	f := MemberFilter{Status: q.Get("status")}
	switch f.Status {
	case "", "invited", "active", "removed":
	default:
		return httpx.ErrValidation("status", "`status` must be invited, active or removed.")
	}
	lp, err := httpx.ParseListParams(r)
	if err != nil {
		return err
	}
	f.Limit, f.After = lp.Limit, lp.Cursor
	ms, more, err := s.ListMembers(r.Context(), p, f)
	if err != nil {
		return err
	}
	var next *httpx.Cursor
	if more && len(ms) > 0 {
		last := ms[len(ms)-1]
		if next, err = httpx.NewCursor(last.ID); err != nil {
			return err
		}
	}
	return httpx.WriteJSON(w, http.StatusOK, httpx.NewList(ms, more, next))
}

func (s *Service) handleInvite(w http.ResponseWriter, r *http.Request) error {
	p, err := principalOf(r)
	if err != nil {
		return err
	}
	var in InviteInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	m, created, err := s.Invite(reqCtx(r), p, in)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return httpx.WriteJSON(w, status, m)
}

func (s *Service) handlePatchMember(w http.ResponseWriter, r *http.Request) error {
	p, err := principalOf(r)
	if err != nil {
		return err
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	m, err := s.ChangeRole(reqCtx(r), p, r.PathValue("id"), in.Role)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, m)
}

func (s *Service) handleRemoveMember(w http.ResponseWriter, r *http.Request) error {
	p, err := principalOf(r)
	if err != nil {
		return err
	}
	m, err := s.Remove(reqCtx(r), p, r.PathValue("id"))
	if err != nil {
		return err
	}
	if m.User.ID == p.UserID.String() {
		s.clearCookies(w) // leaving: this session just ended
	}
	return httpx.WriteJSON(w, http.StatusOK, m)
}
