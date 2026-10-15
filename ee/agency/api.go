package agency

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/weekly"
)

// MsgProRequired is pro_required's message on the agency routes and flags (S52 §5).
const MsgProRequired = "Agency reports for every client are part of the Agency plan (deedglass.com/agencies). Each client's own report stays free: deedglass report --org <client>."

// API is GET /v1/clients.
type API struct {
	Reader Reader
	// Memberships lists the signed-in person's memberships (the lookup GET /v1/me uses).
	Memberships func(ctx context.Context, org ids.OrgID, user ids.UserID) ([]Membership, error)
	// Allowed is serve's agency check (cmd/deedglass/licence.go); false answers pro_required.
	Allowed func() bool
	Now     func() time.Time
}

// Routes is the one route. With a nil API (route-table tests) it answers service_unavailable.
func Routes(a *API) []httpx.Route {
	return []httpx.Route{
		{Method: "GET", Pattern: "/v1/clients", OperationID: "listClients", Auth: httpx.Session, MinRole: httpx.RoleViewer,
			Purpose: "listing your client orgs", Timeout: 60 * time.Second, Handler: a.list},
	}
}

func (a *API) list(w http.ResponseWriter, r *http.Request) error {
	if a == nil || a.Reader == nil || a.Memberships == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	pr, ok := httpx.PrincipalFrom(r.Context())
	if !ok || pr.Kind != httpx.KindUser || pr.UserID.IsZero() {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	if a.Allowed == nil || !a.Allowed() {
		return httpx.E(httpx.CodeProRequired, httpx.Msg(MsgProRequired))
	}
	q := r.URL.Query()
	now := time.Now().UTC()
	if a.Now != nil {
		now = a.Now().UTC()
	}
	p, err := PeriodParam(q.Get("period"), now)
	if err != nil {
		return httpx.ErrValidation("period", "period is a week like 2026-W43, a month like 2026-10, or current (the default).")
	}
	o := Options{Period: p, Now: now, Cursor: q.Get("cursor"), Exclude: q["exclude"]}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxClients {
			return httpx.ErrValidation("limit", "limit is a whole number from 1 to 200.")
		}
		o.Limit = n
	}
	ctx := r.Context()
	ms, err := a.Memberships(ctx, pr.OrgID, pr.UserID)
	if err != nil {
		return err
	}
	l, err := Clients(ctx, a.Reader, pr.UserID, ms, o)
	if errors.Is(err, ErrCursor) {
		return httpx.ErrValidation("cursor", "The cursor isn't one of your clients. Start again without it.")
	}
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, l)
}

// PeriodParam reads a clients period: "" or "current" is this week so far (UTC), else a week or a
// month (UTC, so every client's row covers the same hours).
func PeriodParam(s string, now time.Time) (weekly.Period, error) {
	switch strings.TrimSpace(s) {
	case "", "current":
		return weekly.WeekOf(now, time.UTC), nil
	}
	return weekly.ParsePeriod(s, time.UTC)
}
