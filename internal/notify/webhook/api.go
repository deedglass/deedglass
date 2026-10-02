package webhook

import (
	"net/http"
	"strconv"

	"github.com/deedglass/deedglass/internal/httpx"
)

func errVault() *httpx.Error { return httpx.E(httpx.CodeVaultUnavailable, httpx.Msg(msgVault)) }

// Routes are the webhook routes (S22): an admin+ session or an org admin key; agent keys never
// manage webhooks. With a nil Service (route-table tests) every handler answers
// service_unavailable.
func Routes(s *Service) []httpx.Route {
	h := handlers{s}
	admin := []string{httpx.ScopeAdmin}
	return []httpx.Route{
		{Method: "GET", Pattern: "/v1/webhooks", OperationID: "listWebhooks", Auth: httpx.SessionOrKey, MinRole: httpx.RoleAdmin,
			Purpose: "managing webhooks", Scopes: admin, Handler: h.list},
		{Method: "POST", Pattern: "/v1/webhooks", OperationID: "createWebhook", Auth: httpx.SessionOrKey, MinRole: httpx.RoleAdmin,
			Purpose: "managing webhooks", Scopes: admin, Idempotency: httpx.IdemOptional, Sensitive: true, MaxBody: 16 << 10, Handler: h.create},
		{Method: "GET", Pattern: "/v1/webhooks/{id}", OperationID: "getWebhook", Auth: httpx.SessionOrKey, MinRole: httpx.RoleAdmin,
			Purpose: "managing webhooks", Scopes: admin, Handler: h.get},
		{Method: "DELETE", Pattern: "/v1/webhooks/{id}", OperationID: "deleteWebhook", Auth: httpx.SessionOrKey, MinRole: httpx.RoleAdmin,
			Purpose: "managing webhooks", Scopes: admin, Handler: h.delete},
		{Method: "POST", Pattern: "/v1/webhooks/{id}/test", OperationID: "testWebhook", Auth: httpx.SessionOrKey, MinRole: httpx.RoleAdmin,
			Purpose: "managing webhooks", Scopes: admin, Idempotency: httpx.IdemNever, MaxBody: httpx.NoBody, Handler: h.test},
		{Method: "GET", Pattern: "/v1/webhooks/{id}/deliveries", OperationID: "listWebhookDeliveries", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleAdmin, Purpose: "managing webhooks", Scopes: admin, Handler: h.deliveries},
	}
}

type handlers struct{ s *Service }

func (h handlers) principal(r *http.Request) (httpx.Principal, error) {
	if h.s == nil {
		return httpx.Principal{}, httpx.E(httpx.CodeServiceUnavailable)
	}
	p, ok := httpx.PrincipalFrom(r.Context())
	if !ok || p.OrgID.IsZero() {
		return p, httpx.E(httpx.CodeAuthenticationRequired)
	}
	return p, nil
}

type list struct {
	Object     string  `json:"object"`
	Data       any     `json:"data"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

func (h handlers) list(w http.ResponseWriter, r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	ws, err := h.s.List(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, list{Object: "list", Data: ws})
}

func (h handlers) create(w http.ResponseWriter, r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	var in CreateInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	out, err := h.s.Create(r.Context(), ActorOf(p), p.OrgID, in)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusCreated, out)
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	out, err := h.s.Get(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func (h handlers) delete(w http.ResponseWriter, r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	out, err := h.s.Delete(r.Context(), ActorOf(p), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func (h handlers) test(w http.ResponseWriter, r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	out, err := h.s.Test(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func (h handlers) deliveries(w http.ResponseWriter, r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return httpx.ErrValidation("limit", "limit must be a whole number from 1 to 100.")
		}
		limit = n
	}
	ds, err := h.s.Deliveries(r.Context(), p.OrgID, r.PathValue("id"), limit)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, list{Object: "list", Data: ds})
}
