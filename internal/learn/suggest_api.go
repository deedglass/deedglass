package learn

import (
	"net/http"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
)

// The suggestion routes (S28 §3.5). Viewers read; admins (sessions) and org admin keys generate,
// accept and reject; agent keys get 403 everywhere, as on every Insights route.

// SuggestionRoutes returns the five routes. A nil service mounts them answering 503.
func SuggestionRoutes(s *SuggestionService) []httpx.Route {
	a := &sugAPI{s: s}
	admin := []string{httpx.ScopeAdmin}
	small := int64(16 << 10)
	r := func(method, pattern, op string, min httpx.Role, purpose string, h httpx.HandlerFunc) httpx.Route {
		return httpx.Route{Method: method, Pattern: pattern, OperationID: op, Auth: httpx.SessionOrKey, MinRole: min, Scopes: admin,
			AgentKeyMessage: MsgAgentKey, Purpose: purpose, Handler: h}
	}
	routes := []httpx.Route{
		r("GET", "/v1/suggestions", "listSuggestions", httpx.RoleViewer, "reading suggestions", a.list),
		r("GET", "/v1/suggestions/{id}", "getSuggestion", httpx.RoleViewer, "reading a suggestion", a.get),
		r("POST", "/v1/suggestions/generate", "generateSuggestions", httpx.RoleAdmin, "generating suggestions", a.generate),
		r("POST", "/v1/suggestions/{id}/accept", "acceptSuggestion", httpx.RoleAdmin, "accepting suggestions", a.accept),
		r("POST", "/v1/suggestions/{id}/reject", "rejectSuggestion", httpx.RoleAdmin, "rejecting suggestions", a.reject),
	}
	for i := range routes {
		rt := &routes[i]
		switch rt.OperationID {
		case "generateSuggestions":
			rt.Idempotency, rt.MaxBody = httpx.IdemOptional, httpx.NoBody
			rt.Timeout = 60 * time.Second // a pass replays every candidate (< 5 s on the seed-7 week)
		case "acceptSuggestion", "rejectSuggestion":
			rt.Idempotency, rt.MaxBody = httpx.IdemOptional, small
		}
	}
	return routes
}

type sugAPI struct{ s *SuggestionService }

func (a *sugAPI) who(r *http.Request) (*identity.Principal, Actor, error) {
	if a.s == nil {
		return nil, Actor{}, httpx.E(httpx.CodeServiceUnavailable)
	}
	return (&api{s: &Service{}}).who(r)
}

func (a *sugAPI) list(w http.ResponseWriter, r *http.Request) error {
	p, _, err := a.who(r)
	if err != nil {
		return err
	}
	if err := onlyParams(r, "status", "kind", "limit", "cursor"); err != nil {
		return err
	}
	f := SuggestionFilter{Status: r.URL.Query().Get("status"), Kind: r.URL.Query().Get("kind")}
	switch f.Status {
	case "", "all", SugOpen, SugAccepting, SugAccepted, SugRejected, SugSuperseded:
	default:
		return httpx.ErrValidation("status", MsgSuggestionStatus)
	}
	switch f.Kind {
	case "", KindLoosen, KindTighten, KindBudget, KindExplicitRule, KindAdvisory:
	default:
		return httpx.ErrValidation("kind", MsgSuggestionKind)
	}
	limit, cursor, err := pageOf(r)
	if err != nil {
		return err
	}
	l, err := a.s.List(r.Context(), p.OrgID, f, limit, cursor)
	if err != nil {
		return cursorErr(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, l)
}

func (a *sugAPI) get(w http.ResponseWriter, r *http.Request) error {
	p, _, err := a.who(r)
	if err != nil {
		return err
	}
	if err := onlyParams(r); err != nil {
		return err
	}
	s, err := a.s.Get(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, s)
}

func (a *sugAPI) generate(w http.ResponseWriter, r *http.Request) error {
	p, act, err := a.who(r)
	if err != nil {
		return err
	}
	run, err := a.s.Generate(r.Context(), p.OrgID, act)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, run)
}

func (a *sugAPI) accept(w http.ResponseWriter, r *http.Request) error {
	p, act, err := a.who(r)
	if err != nil {
		return err
	}
	var body AcceptRequest
	if err := httpx.DecodeJSONOptional(r, &body); err != nil {
		return err
	}
	s, err := a.s.Accept(r.Context(), p.OrgID, act, r.PathValue("id"), body, httpx.RequestID(r.Context()))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, s)
}

func (a *sugAPI) reject(w http.ResponseWriter, r *http.Request) error {
	p, act, err := a.who(r)
	if err != nil {
		return err
	}
	var body RejectRequest
	if err := httpx.DecodeJSONOptional(r, &body); err != nil {
		return err
	}
	s, err := a.s.Reject(r.Context(), p.OrgID, act, r.PathValue("id"), body, httpx.RequestID(r.Context()))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, s)
}
