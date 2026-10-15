// Package weeklyapi is the weekly summary's routes (S52 §3.1): GET /v1/weekly (the Console's This
// week card and the API's summary), POST /v1/weekly/email (a person turns their Monday email on or
// off), and the one-click page the email links to (GET and POST /weekly/off). Free: nothing here
// reads a licence.
package weeklyapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/weekly"
	"github.com/deedglass/deedglass/internal/weekly/pgsource"
)

// MsgAgentKey is insufficient_scope's message for an agent key on GET /v1/weekly.
const MsgAgentKey = "Agents can't read the weekly summary; a person or an org admin key does. Use the Console or an org admin key."

// API is the routes' dependencies.
type API struct {
	Source *pgsource.Source
	// Mailer says whether this server can send email (Resend, or the log mailer in dev).
	Mailer bool
	OffKey []byte
	// ProUndoLine says whether the server lacks undo_session (the caught block's Pro line).
	ProUndoLine func() bool
	Now         func() time.Time
	Log         *slog.Logger
}

// Routes are the four routes. With a nil API (route-table tests) they answer service_unavailable.
func Routes(a *API) []httpx.Route {
	return []httpx.Route{
		{Method: "GET", Pattern: "/v1/weekly", OperationID: "getWeekly", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Purpose: "reading the weekly summary", Scopes: []string{httpx.ScopeAdmin}, AgentKeyMessage: MsgAgentKey, Timeout: 30 * time.Second, Handler: a.get},
		{Method: "POST", Pattern: "/v1/weekly/email", OperationID: "setWeeklyEmail", Auth: httpx.Session, MinRole: httpx.RoleViewer,
			Purpose: "changing your weekly email", Idempotency: httpx.IdemNever, MaxBody: 1 << 10, Handler: a.setEmail},
		{Method: "GET", Pattern: "/weekly/off", OperationID: "weeklyOffPage", Auth: httpx.Public,
			Handler: a.offPage},
		{Method: "POST", Pattern: "/weekly/off", OperationID: "weeklyOff", Auth: httpx.Public, Idempotency: httpx.IdemNever,
			MaxBody: 1 << 10, BodyType: "application/x-www-form-urlencoded",
			Handler: a.off},
	}
}

func (a *API) now() time.Time {
	if a != nil && a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func (a *API) log() *slog.Logger {
	if a != nil && a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// View is GET /v1/weekly.
type View struct {
	Object  string         `json:"object"` // "weekly_view"
	Summary weekly.Summary `json:"summary"`
	Email   *EmailState    `json:"email"`
	ProLine bool           `json:"pro_undo_line"`
}

// EmailState is the signed-in person's Monday email.
type EmailState struct {
	On       bool `json:"on"`       // their weekly_email setting
	Receives bool `json:"receives"` // on, an owner, and this server can send
	Owner    bool `json:"owner"`
	Mailer   bool `json:"mailer"`
}

func periodErr() error {
	return httpx.ErrValidation("period", "period is a week like 2026-W43, a month like 2026-10, or current for this week so far.")
}

// period reads ?period: a week, a month, "current", or the last complete week by default.
func period(q string, loc *time.Location, now time.Time) (weekly.Period, error) {
	switch q {
	case "":
		return weekly.LastWeek(now, loc), nil
	case "current":
		return weekly.WeekOf(now, loc), nil
	}
	p, err := weekly.ParsePeriod(q, loc)
	if err != nil {
		return p, periodErr()
	}
	return p, nil
}

func (a *API) get(w http.ResponseWriter, r *http.Request) error {
	if a == nil || a.Source == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	pr, ok := httpx.PrincipalFrom(r.Context())
	if !ok || pr.OrgID.IsZero() {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	if pr.Kind != httpx.KindUser && pr.Kind != httpx.KindAdminKey {
		e := httpx.ErrInsufficientScope(httpx.ScopeAdmin)
		e.Message = MsgAgentKey
		return e
	}
	ctx := r.Context()
	_, tz, err := a.Source.Org(ctx, pr.OrgID)
	if err != nil {
		return err
	}
	now := a.now()
	p, err := period(r.URL.Query().Get("period"), weekly.LoadLocation(tz), now)
	if err != nil {
		return err
	}
	s, err := a.Source.Summarize(ctx, pr.OrgID, p, now)
	if err != nil {
		return err
	}
	v := View{Object: "weekly_view", Summary: s, ProLine: a.ProUndoLine != nil && a.ProUndoLine()}
	if pr.Kind == httpx.KindUser && !pr.UserID.IsZero() {
		on, role, err := a.Source.EmailSetting(ctx, pr.OrgID, pr.UserID)
		if err != nil {
			return err
		}
		v.Email = &EmailState{On: on, Owner: role == "owner", Mailer: a.Mailer, Receives: on && role == "owner" && a.Mailer}
	}
	httpx.Annotate(ctx, pr.OrgID.String(), "", "")
	return httpx.WriteJSON(w, http.StatusOK, v)
}

type emailBody struct {
	WeeklyEmail *bool `json:"weekly_email"`
}

func (a *API) setEmail(w http.ResponseWriter, r *http.Request) error {
	if a == nil || a.Source == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	pr, ok := httpx.PrincipalFrom(r.Context())
	if !ok || pr.Kind != httpx.KindUser || pr.UserID.IsZero() {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	var b emailBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	if b.WeeklyEmail == nil {
		return httpx.ErrValidation("weekly_email", "Send {\"weekly_email\": true} or {\"weekly_email\": false}.")
	}
	ctx := r.Context()
	if _, err := a.Source.SetEmail(ctx, pr.OrgID, pr.UserID, *b.WeeklyEmail); err != nil {
		return err
	}
	on, role, err := a.Source.EmailSetting(ctx, pr.OrgID, pr.UserID)
	if err != nil {
		return err
	}
	a.log().InfoContext(ctx, "weekly email setting changed", "org_id", pr.OrgID.String(), "on", on, "request_id", httpx.RequestID(ctx))
	return httpx.WriteJSON(w, http.StatusOK, EmailState{On: on, Owner: role == "owner", Mailer: a.Mailer, Receives: on && role == "owner" && a.Mailer})
}

// --- the one-click page ---

// pageStyle is the page's only style; the CSP names its hash, so nothing else inline runs.
const pageStyle = `:root{color-scheme:light dark;--bg:#F7F7F4;--card:#FFFFFF;--text:#0D0F14;--muted:#6B7080;--line:#E4E5EA;--accent:#2F5BEA;--on-accent:#FFFFFF}
@media (prefers-color-scheme:dark){:root{--bg:#07080C;--card:#0E1017;--text:#F5F6FA;--muted:#8B90A0;--line:#1C1F2A;--accent:#6F8DFF;--on-accent:#07080C}}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:16px;background:var(--bg);color:var(--text);font:15px/1.6 Inter,ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
main{width:100%;max-width:28rem;padding:32px;border:1px solid var(--line);border-radius:14px;background:var(--card)}
h1{margin:0 0 8px;font-size:20px;line-height:1.3;font-weight:600;letter-spacing:-.01em}p{margin:0 0 16px;color:var(--muted)}
button{min-height:44px;padding:0 20px;border:0;border-radius:10px;background:var(--accent);color:var(--on-accent);font:inherit;font-weight:600;cursor:pointer}
button:focus-visible{outline:2px solid var(--accent);outline-offset:2px}`

var pageCSP = func() string {
	sum := sha256.Sum256([]byte(pageStyle))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
}()

var pageTmpl = template.Must(template.New("off").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex">
<title>{{.Title}}</title><style>{{.Style}}</style></head>
<body><main><h1>{{.Title}}</h1><p>{{.Body}}</p>{{if .Token}}<form method="post" action="/weekly/off"><input type="hidden" name="t" value="{{.Token}}"><button type="submit">Stop these emails</button></form>{{end}}</main></body></html>
`))

type pageView struct {
	Title, Body, Token string
	Style              template.CSS
}

func (a *API) page(w http.ResponseWriter, status int, v pageView) error {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", pageCSP)
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	v.Style = template.CSS(pageStyle)
	w.WriteHeader(status)
	return pageTmpl.Execute(w, v)
}

var badLink = pageView{Title: "This link doesn't work", Body: "It may have been changed in transit. Turn the weekly email off in the Console: Settings, then Weekly email."}

func (a *API) offPage(w http.ResponseWriter, r *http.Request) error {
	if a == nil || a.Source == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	tok := r.URL.Query().Get("t")
	org, _, err := weekly.ParseOffToken(a.OffKey, tok)
	if err != nil {
		return a.page(w, http.StatusBadRequest, badLink)
	}
	name := a.orgName(r.Context(), org)
	return a.page(w, http.StatusOK, pageView{Title: "Stop the weekly email", Token: tok,
		Body: "You'll stop getting \"What your agents did this week\" for " + name + ". Nothing else changes, and you can turn it back on in the Console."})
}

func (a *API) off(w http.ResponseWriter, r *http.Request) error {
	if a == nil || a.Source == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	if err := r.ParseForm(); err != nil {
		return a.page(w, http.StatusBadRequest, badLink)
	}
	org, user, err := weekly.ParseOffToken(a.OffKey, r.PostForm.Get("t"))
	if err != nil {
		return a.page(w, http.StatusBadRequest, badLink)
	}
	ctx := r.Context()
	if _, err := a.Source.SetEmail(ctx, org, user, false); err != nil {
		return err
	}
	a.log().InfoContext(ctx, "weekly email turned off from the email link", "org_id", org.String(), "request_id", httpx.RequestID(ctx))
	return a.page(w, http.StatusOK, pageView{Title: "Done", Body: "You won't get the weekly email for " + a.orgName(ctx, org) +
		" any more. Turn it back on in the Console: Settings, then Weekly email."})
}

func (a *API) orgName(ctx context.Context, org ids.OrgID) string {
	o, _, err := a.Source.Org(ctx, org)
	if err != nil || strings.TrimSpace(o.Name) == "" {
		return "this org"
	}
	return o.Name
}
