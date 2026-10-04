package learn

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
)

// The Insights routes (S27 §3.9). Every route takes a Console session (viewer reads, admin writes)
// or an org admin key (scope admin); agent keys get 403 everywhere here.

// Routes returns the twelve Insights routes. A nil service mounts them answering 503 (route-table
// tests).
func Routes(s *Service) []httpx.Route {
	a := &api{s: s}
	admin := []string{httpx.ScopeAdmin}
	small := int64(16 << 10)
	r := func(method, pattern, op string, min httpx.Role, purpose string, h httpx.HandlerFunc) httpx.Route {
		return httpx.Route{Method: method, Pattern: pattern, OperationID: op, Auth: httpx.SessionOrKey, MinRole: min, Scopes: admin,
			AgentKeyMessage: MsgAgentKey, Purpose: purpose, Handler: h}
	}
	routes := []httpx.Route{
		r("POST", "/v1/policies/{id}/versions/{v}/replay", "createReplay", httpx.RoleAdmin, "replaying a policy", a.createReplay),
		r("GET", "/v1/replays", "listReplays", httpx.RoleViewer, "reading replays", a.listReplays),
		r("GET", "/v1/replays/{id}", "getReplay", httpx.RoleViewer, "reading a replay", a.getReplay),
		r("POST", "/v1/replays/{id}/cancel", "cancelReplay", httpx.RoleAdmin, "cancelling a replay", a.cancelReplay),
		r("GET", "/v1/insights/stats", "getInsightsStats", httpx.RoleViewer, "reading Insights", a.stats),
		r("GET", "/v1/insights/shadow", "getShadowReport", httpx.RoleViewer, "reading Insights", a.shadow),
		r("GET", "/v1/insights/anomalies", "listAnomalies", httpx.RoleViewer, "reading anomalies", a.listAnomalies),
		r("GET", "/v1/insights/anomalies/{id}", "getAnomaly", httpx.RoleViewer, "reading an anomaly", a.getAnomaly),
		r("POST", "/v1/insights/anomalies/{id}/acknowledge", "acknowledgeAnomaly", httpx.RoleAdmin, "acknowledging anomalies", a.acknowledge),
		r("POST", "/v1/insights/anomalies/{id}/resolve", "resolveAnomaly", httpx.RoleAdmin, "resolving anomalies", a.resolve),
		r("GET", "/v1/insights/settings", "getInsightsSettings", httpx.RoleViewer, "reading Insights settings", a.getSettings),
		r("PATCH", "/v1/insights/settings", "updateInsightsSettings", httpx.RoleAdmin, "changing Insights settings", a.patchSettings),
	}
	for i := range routes {
		rt := &routes[i]
		switch rt.OperationID {
		case "createReplay":
			rt.Idempotency, rt.MaxBody = httpx.IdemOptional, small
		case "cancelReplay", "acknowledgeAnomaly":
			rt.Idempotency, rt.MaxBody = httpx.IdemOptional, httpx.NoBody
		case "resolveAnomaly":
			rt.Idempotency, rt.MaxBody = httpx.IdemOptional, small
		case "updateInsightsSettings":
			rt.MaxBody = small
		case "getInsightsStats", "getShadowReport":
			rt.Timeout = 10 * time.Second
		}
	}
	return routes
}

type api struct{ s *Service }

// who is the caller's org and actor.
func (a *api) who(r *http.Request) (*identity.Principal, Actor, error) {
	if a.s == nil {
		return nil, Actor{}, httpx.E(httpx.CodeServiceUnavailable)
	}
	p, ok := identity.PrincipalFrom(r.Context())
	if !ok || p.OrgID.IsZero() {
		return nil, Actor{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	act := Actor{Type: "system", ID: "deedglass"}
	if ac, err := identity.ActorOf(p); err == nil {
		act = Actor{Type: ac.Ref.Type, ID: ac.Ref.ID}
	}
	httpx.Annotate(r.Context(), p.OrgID.String(), "", "")
	return p, act, nil
}

// onlyParams refuses query parameters outside allowed (each at most once).
func onlyParams(r *http.Request, allowed ...string) error {
	for k, vs := range r.URL.Query() {
		ok := false
		for _, x := range allowed {
			if k == x {
				ok = true
			}
		}
		if !ok {
			return httpx.ErrValidation(k, "Unknown parameter "+clipID(k)+". This endpoint takes "+strings.Join(allowed, ", ")+".")
		}
		if len(vs) != 1 {
			return httpx.ErrValidation(k, k+" is sent once.")
		}
	}
	return nil
}

func pageOf(r *http.Request) (int, string, error) {
	limit := 25
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return 0, "", httpx.ErrValidation("limit", "limit must be 1 to 100.")
		}
		limit = n
	}
	return limit, r.URL.Query().Get("cursor"), nil
}

func (a *api) createReplay(w http.ResponseWriter, r *http.Request) error {
	p, act, err := a.who(r)
	if err != nil {
		return err
	}
	v, err := strconv.Atoi(r.PathValue("v"))
	if err != nil || v < 1 {
		return httpx.ErrValidation("v", MsgReplayVersionShape)
	}
	var body ReplayRequest
	if err := httpx.DecodeJSONOptional(r, &body); err != nil {
		return err
	}
	rp, err := a.s.CreateReplay(r.Context(), p.OrgID, act, r.PathValue("id"), v, body)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusAccepted, rp)
}

func (a *api) listReplays(w http.ResponseWriter, r *http.Request) error {
	p, _, err := a.who(r)
	if err != nil {
		return err
	}
	if err := onlyParams(r, "policy", "status", "limit", "cursor"); err != nil {
		return err
	}
	f := ReplayFilter{PolicyID: r.URL.Query().Get("policy"), Status: r.URL.Query().Get("status")}
	switch f.Status {
	case "", ReplayQueued, ReplayRunning, ReplaySucceeded, ReplayFailed, ReplayCancelled:
	default:
		return httpx.ErrValidation("status", "status must be queued, running, succeeded, failed or cancelled.")
	}
	limit, cursor, err := pageOf(r)
	if err != nil {
		return err
	}
	data, next, err := a.s.Store.ListReplays(r.Context(), p.OrgID, f, limit, cursor)
	if err != nil {
		return err
	}
	return writeList(w, data, next)
}

func writeList[T any](w http.ResponseWriter, data []T, next string) error {
	if data == nil {
		data = []T{}
	}
	out := struct {
		Object     string  `json:"object"`
		Data       []T     `json:"data"`
		HasMore    bool    `json:"has_more"`
		NextCursor *string `json:"next_cursor"`
	}{Object: "list", Data: data, HasMore: next != ""}
	if next != "" {
		out.NextCursor = &next
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func (a *api) getReplay(w http.ResponseWriter, r *http.Request) error {
	p, _, err := a.who(r)
	if err != nil {
		return err
	}
	rp, err := a.s.GetReplay(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, rp)
}

func (a *api) cancelReplay(w http.ResponseWriter, r *http.Request) error {
	p, act, err := a.who(r)
	if err != nil {
		return err
	}
	rp, err := a.s.CancelReplay(r.Context(), p.OrgID, r.PathValue("id"), act)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, rp)
}

// ParseStatsQuery validates GET /v1/insights/stats's query.
func ParseStatsQuery(r *http.Request, now time.Time) (StatsQuery, error) {
	if err := onlyParams(r, "window", "from", "to", "by", "agent", "tool", "compare"); err != nil {
		return StatsQuery{}, err
	}
	q := r.URL.Query()
	sq := StatsQuery{By: ByNone, To: now}
	switch b := q.Get("by"); b {
	case "":
	case ByRule, ByTool, ByAgent, ByNone:
		sq.By = b
	default:
		return sq, httpx.ErrValidation("by", MsgStatsBy)
	}
	win, from, to := q.Get("window"), q.Get("from"), q.Get("to")
	switch {
	case win != "" && (from != "" || to != ""):
		return sq, httpx.ErrValidation("window", MsgStatsWindow)
	case from != "" || to != "":
		f, err1 := time.Parse(time.RFC3339, from)
		t, err2 := time.Parse(time.RFC3339, to)
		if from == "" || to == "" || err1 != nil || err2 != nil {
			return sq, httpx.ErrValidation("from", MsgStatsWindow)
		}
		sq.From, sq.To = f.UTC(), t.UTC()
		if err := CheckWindow(Window{From: sq.From, To: sq.To}); err != nil {
			return sq, httpx.E(httpx.CodeReplayWindowInvalid, httpx.Param("from"), httpx.Msg(MsgWindowInvalid))
		}
	default:
		days := 7
		switch win {
		case "", "7d":
		case "30d":
			days = 30
		default:
			return sq, httpx.ErrValidation("window", MsgStatsWindow)
		}
		sq.From = sq.To.Add(-time.Duration(days) * 24 * time.Hour)
		sq.Relative = strconv.Itoa(days) + "d"
	}
	if a := q.Get("agent"); a != "" {
		if !strings.HasPrefix(a, "agt_") || len(a) > 64 {
			return sq, httpx.ErrValidation("agent", "agent must be an agent id (agt_…).")
		}
		sq.Agent = a
	}
	if t := q.Get("tool"); t != "" {
		if len(t) > 200 {
			return sq, httpx.ErrValidation("tool", "tool is a tool name or a glob like stripe.*, up to 200 characters.")
		}
		sq.Tool = t
	}
	switch c := q.Get("compare"); c {
	case "":
	case "previous":
		sq.Compare = true
	default:
		return sq, httpx.ErrValidation("compare", MsgStatsCompare)
	}
	return sq, nil
}

func (a *api) stats(w http.ResponseWriter, r *http.Request) error {
	p, _, err := a.who(r)
	if err != nil {
		return err
	}
	q, err := ParseStatsQuery(r, a.s.now())
	if err != nil {
		return err
	}
	obj, err := a.s.Stats(r.Context(), p.OrgID, q)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	return httpx.WriteJSON(w, http.StatusOK, obj)
}

func (a *api) shadow(w http.ResponseWriter, r *http.Request) error {
	p, _, err := a.who(r)
	if err != nil {
		return err
	}
	if err := onlyParams(r, "shadow_version", "from", "to"); err != nil {
		return err
	}
	q := ShadowQuery{ShadowVersionID: r.URL.Query().Get("shadow_version")}
	for _, k := range []string{"from", "to"} {
		if v := r.URL.Query().Get(k); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return httpx.ErrValidation(k, k+" is an RFC 3339 time, like 2026-10-02T00:00:00Z.")
			}
			t = t.UTC()
			if k == "from" {
				q.From = &t
			} else {
				q.To = &t
			}
		}
	}
	if q.From != nil && q.To != nil && !q.From.Before(*q.To) {
		return httpx.E(httpx.CodeReplayWindowInvalid, httpx.Param("from"), httpx.Msg(MsgWindowInvalid))
	}
	rep, err := a.s.Shadow(r.Context(), p.OrgID, q)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, rep)
}

func (a *api) listAnomalies(w http.ResponseWriter, r *http.Request) error {
	p, _, err := a.who(r)
	if err != nil {
		return err
	}
	if err := onlyParams(r, "status", "agent", "kind", "since", "limit", "cursor"); err != nil {
		return err
	}
	q := r.URL.Query()
	f := AnomalyFilter{Status: q.Get("status"), Agent: q.Get("agent"), Kind: q.Get("kind")}
	switch f.Status {
	case "", StatusOpen, StatusAcknowledged, StatusResolved, "all":
	default:
		return httpx.ErrValidation("status", MsgAnomalyStatus)
	}
	if f.Kind != "" && !validKind(f.Kind) {
		return httpx.ErrValidation("kind", "kind is one of "+strings.Join(AnomalyKinds, ", ")+".")
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return httpx.ErrValidation("since", "since is an RFC 3339 time.")
		}
		f.Since = t.UTC()
	}
	limit, cursor, err := pageOf(r)
	if err != nil {
		return err
	}
	l, err := a.s.Anomalies(r.Context(), p.OrgID, f, limit, cursor)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, l)
}

func validKind(k string) bool {
	for _, x := range AnomalyKinds {
		if x == k {
			return true
		}
	}
	return false
}

func (a *api) getAnomaly(w http.ResponseWriter, r *http.Request) error {
	p, _, err := a.who(r)
	if err != nil {
		return err
	}
	an, err := a.s.Anomaly(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, an)
}

func (a *api) acknowledge(w http.ResponseWriter, r *http.Request) error {
	p, act, err := a.who(r)
	if err != nil {
		return err
	}
	an, err := a.s.Acknowledge(r.Context(), p.OrgID, r.PathValue("id"), act)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, an)
}

func (a *api) resolve(w http.ResponseWriter, r *http.Request) error {
	p, act, err := a.who(r)
	if err != nil {
		return err
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSONOptional(r, &body); err != nil {
		return err
	}
	an, err := a.s.Resolve(r.Context(), p.OrgID, r.PathValue("id"), act, body.Reason, httpx.RequestID(r.Context()))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, an)
}

func (a *api) getSettings(w http.ResponseWriter, r *http.Request) error {
	p, _, err := a.who(r)
	if err != nil {
		return err
	}
	o, err := a.s.SettingsOf(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, o)
}

func (a *api) patchSettings(w http.ResponseWriter, r *http.Request) error {
	p, act, err := a.who(r)
	if err != nil {
		return err
	}
	var body SettingsPatch
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	o, err := a.s.PatchSettings(r.Context(), p.OrgID, act, body, httpx.RequestID(r.Context()))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, o)
}
