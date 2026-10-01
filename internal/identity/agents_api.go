package identity

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// AgentRoutes are S08's routes (contract §9): agents and their lifecycle, keys, Stop all, org admin
// keys and whoami. Control-plane routes accept a Console session (by role) or an org admin key
// (Scopes admin); agent keys are refused on all of them, so no agent changes any agent.
func (s *Service) AgentRoutes() []httpx.Route {
	admin := []string{httpx.ScopeAdmin}
	small := int64(4 << 10)
	return []httpx.Route{
		{Method: "GET", Pattern: "/v1/agents", OperationID: "listAgents", Auth: httpx.SessionOrKey, MinRole: RoleViewer, Scopes: admin,
			Handler: s.handleListAgents},
		{Method: "POST", Pattern: "/v1/agents", OperationID: "createAgent", Auth: httpx.SessionOrKey, MinRole: RoleAdmin, Scopes: admin,
			Idempotency: httpx.IdemOptional, MaxBody: 16 << 10, Purpose: "creating agents", Handler: s.handleCreateAgent},
		{Method: "GET", Pattern: "/v1/agents/{id}", OperationID: "getAgent", Auth: httpx.SessionOrKey, MinRole: RoleViewer, Scopes: admin,
			Handler: s.handleGetAgent},
		{Method: "PATCH", Pattern: "/v1/agents/{id}", OperationID: "updateAgent", Auth: httpx.SessionOrKey, MinRole: RoleAdmin, Scopes: admin,
			MaxBody: 16 << 10, Purpose: "changing agents", Handler: s.handlePatchAgent},
		{Method: "POST", Pattern: "/v1/agents/{id}/suspend", OperationID: "suspendAgent", Auth: httpx.SessionOrKey, MinRole: RoleAdmin, Scopes: admin,
			Idempotency: httpx.IdemOptional, MaxBody: small, Purpose: "suspending agents", Handler: s.statusHandler(OpSuspend)},
		{Method: "POST", Pattern: "/v1/agents/{id}/resume", OperationID: "resumeAgent", Auth: httpx.SessionOrKey, MinRole: RoleAdmin, Scopes: admin,
			Idempotency: httpx.IdemOptional, MaxBody: small, Purpose: "resuming agents", Handler: s.statusHandler(OpResume)},
		{Method: "POST", Pattern: "/v1/agents/{id}/stop", OperationID: "stopAgent", Auth: httpx.SessionOrKey, MinRole: RoleAdmin, Scopes: admin,
			Idempotency: httpx.IdemOptional, MaxBody: small, Purpose: "stopping agents", Handler: s.statusHandler(OpStop)},
		{Method: "POST", Pattern: "/v1/agents/{id}/start", OperationID: "startAgent", Auth: httpx.SessionOrKey, MinRole: RoleAdmin, Scopes: admin,
			Idempotency: httpx.IdemOptional, MaxBody: small, Purpose: "starting agents", Handler: s.statusHandler(OpStart)},
		{Method: "POST", Pattern: "/v1/org/stop-all", OperationID: "stopAllAgents", Auth: httpx.SessionOrKey, MinRole: RoleAdmin, Scopes: admin,
			Idempotency: httpx.IdemOptional, MaxBody: small, Purpose: "stopping all agents", Handler: s.handleStopAll},
		{Method: "POST", Pattern: "/v1/org/start-all", OperationID: "startAllAgents", Auth: httpx.Session, MinRole: RoleOwner,
			Idempotency: httpx.IdemOptional, MaxBody: small, Purpose: "starting all agents", Handler: s.handleStartAll},
		{Method: "GET", Pattern: "/v1/agents/{id}/keys", OperationID: "listAgentKeys", Auth: httpx.SessionOrKey, MinRole: RoleViewer, Scopes: admin,
			Handler: s.handleListAgentKeys},
		{Method: "POST", Pattern: "/v1/agents/{id}/keys", OperationID: "createAgentKey", Auth: httpx.SessionOrKey, MinRole: RoleAdmin, Scopes: admin,
			Idempotency: httpx.IdemOptional, Sensitive: true, MaxBody: small, Purpose: "creating API keys", Handler: s.handleCreateAgentKey},
		{Method: "POST", Pattern: "/v1/keys/{id}/roll", OperationID: "rollKey", Auth: httpx.SessionOrKey, MinRole: RoleAdmin, Scopes: admin,
			Idempotency: httpx.IdemOptional, Sensitive: true, MaxBody: small, Purpose: "rolling API keys", Handler: s.handleRollKey},
		{Method: "POST", Pattern: "/v1/keys/{id}/revoke", OperationID: "revokeKey", Auth: httpx.SessionOrKey, MinRole: RoleAdmin, Scopes: admin,
			Idempotency: httpx.IdemOptional, MaxBody: small, Purpose: "revoking API keys", Handler: s.handleRevokeKey},
		{Method: "GET", Pattern: "/v1/whoami", OperationID: "whoami", Auth: httpx.AgentKey, SkipStopChecks: true, Handler: s.handleWhoami},
		{Method: "GET", Pattern: "/v1/org/keys", OperationID: "listAdminKeys", Auth: httpx.Session, MinRole: RoleAdmin,
			Purpose: "listing admin keys", Handler: s.handleListAdminKeys},
		{Method: "POST", Pattern: "/v1/org/keys", OperationID: "createAdminKey", Auth: httpx.Session, MinRole: RoleAdmin,
			Idempotency: httpx.IdemOptional, Sensitive: true, MaxBody: small, Purpose: "creating admin keys", Handler: s.handleCreateAdminKey},
	}
}

// caller returns the request's principal and actor (people and admin keys only).
func caller(r *http.Request) (*Principal, Actor, error) {
	p, ok := PrincipalFrom(r.Context())
	if !ok || p.OrgID.IsZero() {
		return nil, Actor{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	a, err := ActorOf(p)
	return p, a, err
}

func notFoundAs(err error, nf func() *httpx.Error) error {
	if errors.Is(err, store.ErrNotFound) {
		return nf()
	}
	return err
}

func onlyQuery(r *http.Request, allowed ...string) error {
	for k := range r.URL.Query() {
		ok := false
		for _, a := range allowed {
			ok = ok || k == a
		}
		if !ok {
			if len(allowed) == 0 {
				return httpx.ErrValidation(k, "This endpoint takes no query parameters.")
			}
			return httpx.ErrValidation(k, "`"+clipID(k)+"` isn't a filter here. Use "+strings.Join(allowed, ", ")+".")
		}
	}
	return nil
}

func (s *Service) handleListAgents(w http.ResponseWriter, r *http.Request) error {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	if err := onlyQuery(r, "status", "label", "name", "limit", "cursor"); err != nil {
		return err
	}
	q := r.URL.Query()
	f := AgentFilter{Status: AgentStatus(q.Get("status")), Name: q.Get("name")}
	switch f.Status {
	case "", AgentActive, AgentSuspended, AgentStopped:
	default:
		return httpx.ErrValidation("status", "`status` must be active, suspended or stopped.")
	}
	if l := q.Get("label"); l != "" {
		k, v, found := strings.Cut(l, ":")
		if !found || !labelKeyRE.MatchString(k) {
			return httpx.ErrValidation("label", "`label` is key:value, like env:prod.")
		}
		f.LabelK, f.LabelV = k, v
	}
	if f.Name != "" && !ValidAgentName(f.Name) {
		return errAgentName()
	}
	lp, err := httpx.ParseListParams(r)
	if err != nil {
		return err
	}
	f.Limit = lp.Limit
	if lp.Cursor != nil {
		if ids.Check(ids.Agent, lp.Cursor.ID) != nil {
			return httpx.ErrValidation("cursor", "`cursor` isn't a cursor this API issued. Pass the next_cursor value from the previous page unchanged.")
		}
		f.AfterID = lp.Cursor.ID
	}
	list, more, err := s.ListAgents(r.Context(), p.OrgID, f)
	if err != nil {
		return err
	}
	var next *httpx.Cursor
	if more && len(list) > 0 {
		if next, err = httpx.NewCursor(list[len(list)-1].ID); err != nil {
			return err
		}
	}
	return httpx.WriteJSON(w, http.StatusOK, httpx.NewList(list, more, next))
}

func (s *Service) handleCreateAgent(w http.ResponseWriter, r *http.Request) error {
	p, a, err := caller(r)
	if err != nil {
		return err
	}
	var in CreateAgentInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	res, err := s.CreateAgent(r.Context(), a, p.OrgID, in)
	if err != nil {
		return err
	}
	httpx.Annotate(r.Context(), "", res.ID, "")
	return httpx.WriteJSON(w, http.StatusCreated, res)
}

func (s *Service) handleGetAgent(w http.ResponseWriter, r *http.Request) error {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	id := r.PathValue("id")
	ag, err := s.GetAgent(r.Context(), p.OrgID, id)
	if err != nil {
		return notFoundAs(err, func() *httpx.Error { return ErrAgentNotFound(id) })
	}
	return httpx.WriteJSON(w, http.StatusOK, ag)
}

func (s *Service) handlePatchAgent(w http.ResponseWriter, r *http.Request) error {
	p, a, err := caller(r)
	if err != nil {
		return err
	}
	var in PatchAgentInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	res, err := s.PatchAgent(r.Context(), a, p.OrgID, id, in)
	if err != nil {
		return notFoundAs(err, func() *httpx.Error { return ErrAgentNotFound(id) })
	}
	return httpx.WriteJSON(w, http.StatusOK, res)
}

type reasonBody struct {
	Reason string `json:"reason"`
}

func (s *Service) statusHandler(op StatusOp) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		p, a, err := caller(r)
		if err != nil {
			return err
		}
		var in reasonBody
		if err := httpx.DecodeJSONOptional(r, &in); err != nil {
			return err
		}
		id := r.PathValue("id")
		httpx.Annotate(r.Context(), "", id, "")
		res, err := s.SetAgentStatus(r.Context(), a, p.OrgID, id, op, in.Reason)
		if err != nil {
			return notFoundAs(err, func() *httpx.Error { return ErrAgentNotFound(id) })
		}
		return httpx.WriteJSON(w, http.StatusOK, res)
	}
}

func (s *Service) handleStopAll(w http.ResponseWriter, r *http.Request) error {
	p, a, err := caller(r)
	if err != nil {
		return err
	}
	var in reasonBody
	if err := httpx.DecodeJSONOptional(r, &in); err != nil {
		return err
	}
	out, err := s.StopAll(r.Context(), a, p.OrgID, in.Reason)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) handleStartAll(w http.ResponseWriter, r *http.Request) error {
	p, a, err := caller(r)
	if err != nil {
		return err
	}
	var in reasonBody
	if err := httpx.DecodeJSONOptional(r, &in); err != nil {
		return err
	}
	out, err := s.StartAll(r.Context(), a, p.OrgID, in.Reason)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) handleListAgentKeys(w http.ResponseWriter, r *http.Request) error {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	id := r.PathValue("id")
	keys, err := s.ListAgentKeys(r.Context(), p.OrgID, id)
	if err != nil {
		return notFoundAs(err, func() *httpx.Error { return ErrAgentNotFound(id) })
	}
	return httpx.WriteJSON(w, http.StatusOK, httpx.NewList(keys, false, nil))
}

func (s *Service) handleCreateAgentKey(w http.ResponseWriter, r *http.Request) error {
	p, a, err := caller(r)
	if err != nil {
		return err
	}
	var in CreateKeyInput
	if err := httpx.DecodeJSONOptional(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	res, err := s.CreateAgentKey(r.Context(), a, p.OrgID, id, in)
	if err != nil {
		return notFoundAs(err, func() *httpx.Error { return ErrAgentNotFound(id) })
	}
	return httpx.WriteJSON(w, http.StatusCreated, res)
}

func (s *Service) handleRollKey(w http.ResponseWriter, r *http.Request) error {
	p, a, err := caller(r)
	if err != nil {
		return err
	}
	in := struct {
		ExpiresIn *int64 `json:"expires_in"`
	}{}
	if err := httpx.DecodeJSONOptional(r, &in); err != nil {
		return err
	}
	overlap := DefaultRollWindow
	if in.ExpiresIn != nil {
		if *in.ExpiresIn < 0 || *in.ExpiresIn > int64(MaxRollWindow/time.Second) {
			return httpx.ErrValidation("expires_in", "expires_in is the overlap in seconds, from 0 to 604800 (7 days). The default is 86400.")
		}
		overlap = time.Duration(*in.ExpiresIn) * time.Second
	}
	id := r.PathValue("id")
	res, err := s.RollKey(r.Context(), a, p.OrgID, id, overlap)
	if err != nil {
		return notFoundAs(err, func() *httpx.Error { return ErrKeyNotFound(id) })
	}
	return httpx.WriteJSON(w, http.StatusCreated, res)
}

func (s *Service) handleRevokeKey(w http.ResponseWriter, r *http.Request) error {
	p, a, err := caller(r)
	if err != nil {
		return err
	}
	var in reasonBody
	if err := httpx.DecodeJSONOptional(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	res, err := s.RevokeKey(r.Context(), a, p.OrgID, id, in.Reason)
	if err != nil {
		return notFoundAs(err, func() *httpx.Error { return ErrKeyNotFound(id) })
	}
	return httpx.WriteJSON(w, http.StatusOK, res)
}

func (s *Service) handleListAdminKeys(w http.ResponseWriter, r *http.Request) error {
	p, err := principalOf(r)
	if err != nil {
		return err
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	keys, err := s.ListAdminKeys(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, httpx.NewList(keys, false, nil))
}

func (s *Service) handleCreateAdminKey(w http.ResponseWriter, r *http.Request) error {
	p, err := principalOf(r)
	if err != nil {
		return err
	}
	a, err := ActorOf(p)
	if err != nil {
		return err
	}
	var in CreateKeyInput
	if err := httpx.DecodeJSONOptional(r, &in); err != nil {
		return err
	}
	res, err := s.CreateAdminKey(r.Context(), a, p.OrgID, in)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusCreated, res)
}

// Whoami is GET /v1/whoami: the key, its agent and its org, with statuses. It skips the stop
// checks, so a stopped agent can learn why it is refused. It never shows a reason or who stopped
// the agent.
type Whoami struct {
	Object  string       `json:"object"` // "whoami"
	Org     WhoamiOrg    `json:"org"`
	Agent   *WhoamiAgent `json:"agent"` // null for an org admin key
	Key     WhoamiKey    `json:"key"`
	Budgets *Budgets     `json:"budgets"` // null for an org admin key
}

// WhoamiOrg is the key's org.
type WhoamiOrg struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Stopped bool   `json:"stopped"`
}

// WhoamiAgent is the key's agent.
type WhoamiAgent struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Status AgentStatus       `json:"status"`
	Labels map[string]string `json:"labels"`
}

// WhoamiKey is the key itself (never its secret).
type WhoamiKey struct {
	ID        string      `json:"id"`
	Prefix    string      `json:"prefix"`
	Mode      Mode        `json:"mode"`
	Scopes    []string    `json:"scopes"`
	ExpiresAt *httpx.Time `json:"expires_at"`
}

func (s *Service) handleWhoami(w http.ResponseWriter, r *http.Request) error {
	p, ok := PrincipalFrom(r.Context())
	if !ok || !p.IsKey() {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	out, err := s.WhoamiFor(r.Context(), p)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

// WhoamiFor builds the whoami object for a key principal.
func (s *Service) WhoamiFor(ctx context.Context, p *Principal) (Whoami, error) {
	out := Whoami{Object: "whoami"}
	err := s.db.OrgReadTx(ctx, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var stoppedAt *time.Time
		if err := tx.QueryRow(ctx, `SELECT id, name, slug, stopped_at FROM deedglass.orgs WHERE id = $1`, p.OrgID).
			Scan(&out.Org.ID, &out.Org.Name, &out.Org.Slug, &stoppedAt); err != nil {
			return err
		}
		out.Org.Stopped = stoppedAt != nil
		k, err := getKey(ctx, tx, p.OrgID, p.KeyID.String(), false)
		if err != nil {
			return err
		}
		out.Key = WhoamiKey{ID: k.ID, Prefix: k.Prefix, Mode: k.Mode, Scopes: k.Scopes, ExpiresAt: k.ExpiresAt}
		if !p.AgentID.IsZero() {
			ag, err := getAgent(ctx, tx, p.OrgID, p.AgentID.String())
			if err != nil {
				return err
			}
			out.Agent = &WhoamiAgent{ID: ag.ID, Name: ag.Name, Status: ag.Status, Labels: ag.Labels}
			b := ag.Budgets
			out.Budgets = &b
		}
		return nil
	})
	return out, err
}
