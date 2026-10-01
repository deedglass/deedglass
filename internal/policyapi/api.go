// Package policyapi mounts S12's policy routes (contract §9): policies and immutable versions,
// activation and deactivation sealed as deeds, the active set, and the lint, dry-run and test
// endpoints. It turns HTTP into policy.Service calls and policy.Error into the error envelope;
// the service holds the logic and internal/policy stays pure.
//
// Every route takes a Console session (by role) or an org admin key (scope admin). Agent keys are
// refused on every one, reads included: an agent probing its own policy for gaps is an attack.
package policyapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"encoding/json/jsontext"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/policy"
)

// Limits (S12 §3.6).
const (
	MaxPushBody     = 1 << 20 // the source (≤ 256 KiB, checked by the compiler) as a JSON string, and notes
	MaxEvaluateBody = 1 << 20
	MaxTestBody     = 2 << 20 // a source and ≤ 1 MiB of cases
	ToolTimeout     = 2 * time.Second
	ToolsPerMinute  = 60 // lint, evaluate and test, per person or admin key
)

// AgentKeyMessage is insufficient_scope's message for an agent key on any policy route.
const AgentKeyMessage = "Agents cannot read or test policies. Use the Console or an admin key."

// Options configures Routes.
type Options struct {
	Now func() time.Time // the rate limiter's clock (tests)
}

type api struct {
	svc   *policy.Service
	tools *httpx.Limiter
}

// Routes returns the 12 policy routes.
func Routes(svc *policy.Service, o Options) []httpx.Route {
	a := &api{svc: svc, tools: httpx.NewLimiter(ToolsPerMinute, time.Minute, o.Now)}
	admin := []string{httpx.ScopeAdmin}
	small := int64(16 << 10)
	r := func(method, pattern, op string, min httpx.Role, h httpx.HandlerFunc) httpx.Route {
		return httpx.Route{Method: method, Pattern: pattern, OperationID: op, Auth: httpx.SessionOrKey, MinRole: min, Scopes: admin,
			AgentKeyMessage: AgentKeyMessage, Handler: h}
	}
	routes := []httpx.Route{
		r("GET", "/v1/policies", "listPolicies", identity.RoleViewer, a.list),
		r("POST", "/v1/policies", "createPolicy", identity.RoleAdmin, a.create),
		r("GET", "/v1/policies/active", "getActivePolicies", identity.RoleViewer, a.active),
		r("GET", "/v1/policies/{id}", "getPolicy", identity.RoleViewer, a.get),
		r("GET", "/v1/policies/{id}/versions", "listPolicyVersions", identity.RoleViewer, a.listVersions),
		r("POST", "/v1/policies/{id}/versions", "createPolicyVersion", identity.RoleAdmin, a.addVersion),
		r("GET", "/v1/policies/{id}/versions/{v}", "getPolicyVersion", identity.RoleViewer, a.getVersion),
		r("POST", "/v1/policies/{id}/versions/{v}/activate", "activatePolicyVersion", identity.RoleAdmin, a.activate),
		r("POST", "/v1/policies/{id}/versions/{v}/deactivate", "deactivatePolicyVersion", identity.RoleAdmin, a.deactivate),
		r("POST", "/v1/policies/lint", "lintPolicy", identity.RoleViewer, a.lint),
		r("POST", "/v1/policies/evaluate", "evaluatePolicy", identity.RoleViewer, a.evaluate),
		r("POST", "/v1/policies/test", "testPolicy", identity.RoleViewer, a.test),
	}
	for i := range routes {
		rt := &routes[i]
		switch rt.OperationID {
		case "createPolicy", "createPolicyVersion":
			rt.Idempotency, rt.MaxBody, rt.Purpose = httpx.IdemOptional, MaxPushBody, "pushing policies"
		case "activatePolicyVersion":
			rt.Idempotency, rt.MaxBody, rt.Purpose = httpx.IdemOptional, small, "activating policies"
		case "deactivatePolicyVersion":
			rt.Idempotency, rt.MaxBody, rt.Purpose = httpx.IdemOptional, small, "switching off a shadow policy"
		case "lintPolicy":
			rt.Idempotency, rt.MaxBody, rt.Timeout = httpx.IdemNever, MaxPushBody, ToolTimeout
		case "evaluatePolicy":
			rt.Idempotency, rt.MaxBody, rt.Timeout = httpx.IdemNever, MaxEvaluateBody, ToolTimeout
		case "testPolicy":
			rt.Idempotency, rt.MaxBody, rt.Timeout = httpx.IdemNever, MaxTestBody, ToolTimeout
		}
	}
	return routes
}

// caller is the service's view of the request's principal.
func caller(r *http.Request) (policy.Caller, error) {
	p, ok := identity.PrincipalFrom(r.Context())
	if !ok || p.OrgID.IsZero() {
		return policy.Caller{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	a, err := identity.ActorOf(p)
	if err != nil {
		return policy.Caller{}, err
	}
	return policy.Caller{Org: p.OrgID.String(), Actor: a.Ref, RequestID: httpx.RequestID(r.Context())}, nil
}

// limit spends one of the caller's lint/evaluate/test tokens.
func (a *api) limit(r *http.Request) error {
	p, _ := identity.PrincipalFrom(r.Context())
	key := "anon"
	switch {
	case p == nil:
	case !p.UserID.IsZero():
		key = p.UserID.String()
	case !p.KeyID.IsZero():
		key = p.KeyID.String()
	}
	if ok, wait := a.tools.Allow(key); !ok {
		return httpx.ErrRateLimited(int((wait + time.Second - 1) / time.Second))
	}
	return nil
}

// diagnostic is a policy_diagnostic as the API shows it.
type diagnostic struct {
	Object string `json:"object"`
	policy.Diagnostic
}

func diagnostics(list []policy.Diagnostic) []diagnostic {
	out := make([]diagnostic, len(list))
	for i, d := range list {
		out[i] = diagnostic{Object: "policy_diagnostic", Diagnostic: d}
	}
	return out
}

// apiErr maps a service error onto the catalog.
func apiErr(err error) error {
	var e *policy.Error
	if !errors.As(err, &e) {
		if errors.Is(err, context.DeadlineExceeded) {
			return err // the pipeline renders the route's timeout
		}
		return err
	}
	switch e.Kind {
	case policy.KindInvalid:
		return httpx.E(httpx.CodePolicyInvalid, httpx.Param(e.Param), httpx.Msg(e.Message), httpx.WithDiagnostics(diagnostics(e.Diagnostics)))
	case policy.KindValidation:
		return httpx.ErrValidation(e.Param, e.Message)
	case policy.KindConflict:
		return httpx.E(httpx.CodeConflict, httpx.Msg(e.Message))
	case policy.KindNotFound:
		return httpx.E(httpx.CodeNotFound, httpx.Param(e.Param), httpx.Msg(e.Message))
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
			return httpx.ErrValidation(k, "This endpoint takes only limit and cursor as query parameters.")
		}
	}
	return nil
}

func page(r *http.Request) (policy.Page, error) {
	if err := onlyQuery(r, "limit", "cursor"); err != nil {
		return policy.Page{}, err
	}
	q := r.URL.Query()
	pg := policy.Page{Cursor: q.Get("cursor")}
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > policy.MaxListLimit {
			return pg, httpx.ErrValidation("limit", "`limit` must be between 1 and 100.")
		}
		pg.Limit = n
	}
	return pg, nil
}

func versionRef(r *http.Request) (policy.VersionRef, error) {
	ref, ok := policy.ParseVersionRef(r.PathValue("v"))
	if !ok {
		return ref, httpx.ErrValidation("v", "The version is a number, like 3, or a version id, like pv_01jb8m3c0d2f4g6h8j0k2m4n6p.")
	}
	return ref, nil
}

// --- handlers --------------------------------------------------------------------------------------

func (a *api) list(w http.ResponseWriter, r *http.Request) error {
	c, err := caller(r)
	if err != nil {
		return err
	}
	pg, err := page(r)
	if err != nil {
		return err
	}
	out, err := a.svc.List(r.Context(), c, pg)
	if err != nil {
		return apiErr(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

type pushBody struct {
	Source *string `json:"source"`
	Notes  string  `json:"notes"`
}

func (b pushBody) request() (policy.PushRequest, error) {
	if b.Source == nil {
		return policy.PushRequest{}, httpx.ErrValidation("source", "source is required: the policy YAML, as a string.")
	}
	return policy.PushRequest{Source: []byte(*b.Source), Notes: b.Notes}, nil
}

// created is POST /v1/policies' body: the policy, with its first version (additive).
type created struct {
	*policy.Policy
	Version *policy.Version `json:"version"`
}

func (a *api) create(w http.ResponseWriter, r *http.Request) error {
	c, err := caller(r)
	if err != nil {
		return err
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	var b pushBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	req, err := b.request()
	if err != nil {
		return err
	}
	p, v, err := a.svc.Create(r.Context(), c, req)
	if err != nil {
		return apiErr(err)
	}
	return httpx.WriteJSON(w, http.StatusCreated, created{Policy: p, Version: v})
}

func (a *api) get(w http.ResponseWriter, r *http.Request) error {
	c, err := caller(r)
	if err != nil {
		return err
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	p, err := a.svc.Get(r.Context(), c, r.PathValue("id"))
	if err != nil {
		return apiErr(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, p)
}

func (a *api) listVersions(w http.ResponseWriter, r *http.Request) error {
	c, err := caller(r)
	if err != nil {
		return err
	}
	pg, err := page(r)
	if err != nil {
		return err
	}
	out, err := a.svc.ListVersions(r.Context(), c, r.PathValue("id"), pg)
	if err != nil {
		return apiErr(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func (a *api) addVersion(w http.ResponseWriter, r *http.Request) error {
	c, err := caller(r)
	if err != nil {
		return err
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	var b pushBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	req, err := b.request()
	if err != nil {
		return err
	}
	v, isNew, err := a.svc.AddVersion(r.Context(), c, r.PathValue("id"), req)
	if err != nil {
		return apiErr(err)
	}
	status := http.StatusOK // identical content: the existing version, nothing saved
	if isNew {
		status = http.StatusCreated
	}
	return httpx.WriteJSON(w, status, v)
}

func (a *api) getVersion(w http.ResponseWriter, r *http.Request) error {
	c, err := caller(r)
	if err != nil {
		return err
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	ref, err := versionRef(r)
	if err != nil {
		return err
	}
	v, err := a.svc.GetVersion(r.Context(), c, r.PathValue("id"), ref)
	if err != nil {
		return apiErr(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, v)
}

type activateBody struct {
	Mode                string   `json:"mode"`
	Reason              string   `json:"reason"`
	AcknowledgeWarnings []string `json:"acknowledge_warnings"`
}

func (a *api) activate(w http.ResponseWriter, r *http.Request) error {
	c, err := caller(r)
	if err != nil {
		return err
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	ref, err := versionRef(r)
	if err != nil {
		return err
	}
	var b activateBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	if b.Mode == "" {
		return httpx.ErrValidation("mode", "mode is required: enforce or shadow.")
	}
	out, err := a.svc.Activate(r.Context(), c, r.PathValue("id"), ref,
		policy.ActivateRequest{Mode: policy.Mode(b.Mode), Reason: b.Reason, AcknowledgeWarnings: b.AcknowledgeWarnings})
	if err != nil {
		return apiErr(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

type deactivateBody struct {
	Reason string `json:"reason"`
}

func (a *api) deactivate(w http.ResponseWriter, r *http.Request) error {
	c, err := caller(r)
	if err != nil {
		return err
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	ref, err := versionRef(r)
	if err != nil {
		return err
	}
	var b deactivateBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	out, err := a.svc.Deactivate(r.Context(), c, r.PathValue("id"), ref, policy.DeactivateRequest{Reason: b.Reason})
	if err != nil {
		return apiErr(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func (a *api) active(w http.ResponseWriter, r *http.Request) error {
	c, err := caller(r)
	if err != nil {
		return err
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	out, err := a.svc.Active(r.Context(), c)
	if err != nil {
		return apiErr(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

type lintBody struct {
	Source *string `json:"source"`
	Voice  bool    `json:"voice"`
}

func (a *api) lint(w http.ResponseWriter, r *http.Request) error {
	if _, err := caller(r); err != nil {
		return err
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	if err := a.limit(r); err != nil {
		return err
	}
	var b lintBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	if b.Source == nil {
		return httpx.ErrValidation("source", "source is required: the policy YAML, as a string.")
	}
	// 200 even when the policy is invalid: the result says so, with every diagnostic.
	return httpx.WriteJSON(w, http.StatusOK, policy.Lint([]byte(*b.Source), policy.Options{Voice: b.Voice}))
}

type evaluateBody struct {
	Policy  jsontext.Value `json:"policy"`
	Input   jsontext.Value `json:"input"`
	Explain bool           `json:"explain"`
}

// decodeGeneric reads a JSON value with whole numbers kept exact (json.Number, then int64).
func decodeGeneric(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return policy.NormalizeJSONValue(v), nil
}

func (a *api) evaluate(w http.ResponseWriter, r *http.Request) error {
	c, err := caller(r)
	if err != nil {
		return err
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	if err := a.limit(r); err != nil {
		return err
	}
	var b evaluateBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	if len(b.Input) == 0 {
		return httpx.ErrValidation("input", "input is required: the action to evaluate, like {\"tool\": \"postgres.query\", \"sql\": \"SELECT 1\"}.")
	}
	in, err := decodeGeneric(b.Input)
	if err != nil {
		return httpx.ErrValidation("input", "input must be a JSON object.")
	}
	out, err := a.svc.Evaluate(r.Context(), c, policy.DryRunRequest{Policy: json.RawMessage(b.Policy), Input: in, Explain: b.Explain})
	if err != nil {
		return apiErr(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

type testBody struct {
	Source  *string        `json:"source"`
	Policy  *string        `json:"policy"`
	Version jsontext.Value `json:"version"`
	Cases   jsontext.Value `json:"cases"`
}

func (a *api) test(w http.ResponseWriter, r *http.Request) error {
	c, err := caller(r)
	if err != nil {
		return err
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	if err := a.limit(r); err != nil {
		return err
	}
	var b testBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	req := policy.TestRequest{}
	if b.Source != nil {
		req.Source = *b.Source
	}
	if b.Policy != nil {
		req.PolicyRef = *b.Policy
		v, err := decodeGeneric(b.Version)
		n, ok := v.(int64)
		if err != nil || !ok || n < 1 {
			return httpx.ErrValidation("version", "version is required with policy: a version number like 3.")
		}
		req.Version = policy.VersionRef{Number: int(n)}
	}
	if len(b.Cases) > 0 {
		if req.Cases, err = decodeGeneric(b.Cases); err != nil {
			return httpx.ErrValidation("cases", "cases must be a cases file as a string, or a list of cases.")
		}
	}
	out, err := a.svc.Test(r.Context(), c, req)
	if err != nil {
		return apiErr(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}
