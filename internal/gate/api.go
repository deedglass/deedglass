package gate

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
)

// The Gate's HTTP routes (S13 §3.7). GET /v1/actions and GET /v1/actions/{id} stay S06's Ledger
// routes; the Gate plugs its long poll into the second one (ledger.RoutesWith(…, p.Wait)).

// actionBody is POST /v1/actions.
type actionBody struct {
	Tool          *string         `json:"tool"`
	Args          json.RawMessage `json:"args"`
	Mode          string          `json:"mode"`
	Wait          *float64        `json:"wait"`
	Context       *RequestContext `json:"context"`
	ConnectorType string          `json:"connector_type"`
	ToolDigest    string          `json:"tool_digest"`
}

// postTimeout bounds POST /v1/actions: the longest wait plus room to record the outcome.
const postTimeout = MaxWait + 10*time.Second

// Routes are POST /v1/actions, POST /v1/actions/{id}/cancel and POST /v1/actions/{id}/result.
// With a nil pipeline (route-table tests) every handler answers service_unavailable.
func Routes(p *Pipeline) []httpx.Route {
	h := handlers{p}
	return []httpx.Route{
		// The Gate owns idempotency on this route (a unique index on org, agent and key, and an
		// in-flight duplicate waits for the original), so the middleware stays out: IdemNever.
		// RecordStopped: a stopped or suspended agent's attempt is recorded and sealed, then refused.
		{Method: "POST", Pattern: "/v1/actions", OperationID: "createAction", Auth: httpx.AgentKey,
			Scopes: []string{httpx.ScopeActionsExecute}, Idempotency: httpx.IdemNever, RecordStopped: true,
			Timeout: postTimeout, Handler: h.create},
		// The same agent (actions:execute), or a person or admin key of the org (admin).
		{Method: "POST", Pattern: "/v1/actions/{id}/cancel", OperationID: "cancelAction", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleAdmin, Purpose: "cancelling an action", Scopes: []string{httpx.ScopeActionsExecute},
			Idempotency: httpx.IdemOptional, MaxBody: httpx.NoBody, Handler: h.cancel},
		{Method: "POST", Pattern: "/v1/actions/{id}/result", OperationID: "reportActionResult", Auth: httpx.AgentKey,
			Scopes: []string{httpx.ScopeActionsExecute}, Idempotency: httpx.IdemOptional, MaxBody: 2 << 20, Handler: h.result},
	}
}

type handlers struct{ p *Pipeline }

func (h handlers) principalOf(r *http.Request) (principal, error) {
	if h.p == nil {
		return principal{}, httpx.E(httpx.CodeServiceUnavailable)
	}
	pr, ok := httpx.PrincipalFrom(r.Context())
	if !ok || pr.OrgID.IsZero() {
		return pr, httpx.E(httpx.CodeAuthenticationRequired)
	}
	return pr, nil
}

func (h handlers) create(w http.ResponseWriter, r *http.Request) error {
	pr, err := h.principalOf(r)
	if err != nil {
		return err
	}
	var b actionBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	if b.Tool == nil {
		return httpx.ErrValidation("tool", msgToolEmpty)
	}
	if len(r.Header.Values(identity.HeaderIntent)) > 1 {
		return httpx.ErrIntent("syntax", "") // one intent per call; two headers are ambiguous
	}
	req := ActionRequest{Tool: *b.Tool, Args: b.Args, Mode: Mode(b.Mode), ToolDigest: b.ToolDigest, ConnectorType: b.ConnectorType,
		Context: b.Context, IdempotencyKey: r.Header.Get("Idempotency-Key"), Intent: r.Header.Get(identity.HeaderIntent),
		RequestID: httpx.RequestID(r.Context())}
	if req.IdempotencyKey != "" && !validKey(req.IdempotencyKey) {
		return httpx.E(httpx.CodeIdempotencyKeyInvalid, httpx.Param("Idempotency-Key"))
	}
	if b.Wait != nil {
		if *b.Wait < 0 || *b.Wait > MaxWait.Seconds() {
			return httpx.ErrValidation("wait", msgWait)
		}
		req.Wait, req.WaitSet = time.Duration(*b.Wait*float64(time.Second)), true
	}
	obo := identity.OBOFrom(r.Context())
	req.OnBehalfOf, req.Session = obo.OnBehalfOf, obo.Session
	httpx.Annotate(r.Context(), pr.OrgID.String(), pr.AgentID.String(), "")
	a, err := h.p.Submit(r.Context(), pr, req)
	if a != nil {
		httpx.Annotate(r.Context(), "", "", a.ID)
		if a.replayed {
			w.Header().Set("Deedglass-Idempotent-Replayed", "true")
		}
	}
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, a.HTTPStatus(), a)
}

// validKey is S03's Idempotency-Key grammar: 1 to 255 printable ASCII characters.
func validKey(k string) bool {
	if len(k) == 0 || len(k) > 255 {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x20 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

func (h handlers) cancel(w http.ResponseWriter, r *http.Request) error {
	pr, err := h.principalOf(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	a, err := h.p.Cancel(r.Context(), pr, id)
	if err != nil {
		return err
	}
	httpx.Annotate(r.Context(), pr.OrgID.String(), a.Agent.ID, a.ID)
	return httpx.WriteJSON(w, http.StatusOK, a)
}

func (h handlers) result(w http.ResponseWriter, r *http.Request) error {
	pr, err := h.principalOf(r)
	if err != nil {
		return err
	}
	var b EdgeResult
	if err := httpx.DecodeJSON(r, &b); err != nil {
		var he *httpx.Error
		if errors.As(err, &he) && he.Param == "" && he.Code == httpx.CodeValidationFailed {
			he.Param = "status"
		}
		return err
	}
	a, err := h.p.ReportResult(r.Context(), pr, r.PathValue("id"), b)
	if err != nil {
		return err
	}
	httpx.Annotate(r.Context(), pr.OrgID.String(), a.Agent.ID, a.ID)
	return httpx.WriteJSON(w, http.StatusOK, a)
}
