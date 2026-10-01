package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
)

// POST /v1/tools/resolve (S14 §3.5): which of an edge's tools the active policy always denies, so
// an MCP wrapper can hide them from tools/list. It reads the static structure of the enforced
// policy only (policy.AlwaysDeniesWhy): budgets and Stop are transient and ignored, and a rule with
// a when is "can't tell", so visible. Hiding is a convenience for the model; every call still
// goes through POST /v1/actions and fails closed there.

// MaxResolveTools bounds one request.
const MaxResolveTools = 500

// Visibility values.
const (
	VisibilityVisible = "visible"
	VisibilityHidden  = "hidden"
)

// ToolRef is one tool an edge serves.
type ToolRef struct {
	Name   string `json:"name"`
	Digest string `json:"digest,omitempty"`
}

// ResolveRequest is the body of POST /v1/tools/resolve.
type ResolveRequest struct {
	Connector     string    `json:"connector"`
	ConnectorType string    `json:"connector_type,omitempty"`
	Tools         []ToolRef `json:"tools"`
}

// ToolVisibility is one tool's answer.
type ToolVisibility struct {
	Object     string  `json:"object"` // "tool_visibility"
	Name       string  `json:"name"`
	Tool       string  `json:"tool"`
	Visibility string  `json:"visibility"`
	RuleID     *string `json:"rule_id"`
	Reason     *string `json:"reason"`
}

// ToolVisibilityList is the response.
type ToolVisibilityList struct {
	Object        string           `json:"object"` // "list"
	Data          []ToolVisibility `json:"data"`
	HasMore       bool             `json:"has_more"`
	NextCursor    *string          `json:"next_cursor"`
	PolicyVersion *string          `json:"policy_version"`
}

const (
	msgResolveConnector = `connector must be a connector name: 2 to 32 lowercase letters, digits or hyphens, starting with a letter, for example "scratch".`
	msgResolveTools     = "tools must be a list of 1 to 500 tools, each with a name."
	msgResolveDigest    = `tools[].digest must be "sha256:" followed by 64 lowercase hex characters, or left out.`
)

func msgResolveName(name string) string {
	return fmt.Sprintf("%q isn't an MCP tool name. Use 1 to 128 letters, digits, underscores, hyphens or dots.", clip(name, 80))
}

// ResolveTools answers POST /v1/tools/resolve for an agent key.
func (p *Pipeline) ResolveTools(ctx context.Context, pr principal, req ResolveRequest) (*ToolVisibilityList, error) {
	if pr.Kind != httpx.KindAgentKey || pr.AgentID.IsZero() || pr.OrgID.IsZero() {
		return nil, httpx.ErrInsufficientScope(httpx.ScopeActionsExecute)
	}
	if !reConnector.MatchString(req.Connector) {
		return nil, httpx.ErrValidation("connector", msgResolveConnector)
	}
	ctype := req.ConnectorType
	switch {
	case ctype == "":
		ctype = "mcp"
	case !policy.KnownConnectorTypes[ctype]:
		return nil, httpx.ErrValidation("connector_type", msgConnectorTypeUnknown(ctype))
	}
	if len(req.Tools) == 0 || len(req.Tools) > MaxResolveTools {
		return nil, httpx.ErrValidation("tools", msgResolveTools)
	}
	for _, t := range req.Tools {
		if !reMCPTool.MatchString(t.Name) {
			return nil, httpx.ErrValidation("tools", msgResolveName(t.Name))
		}
		if t.Digest != "" && !reDigest.MatchString(t.Digest) {
			return nil, httpx.ErrValidation("tools", msgResolveDigest)
		}
	}
	facts := make([]policy.ToolFacts, len(req.Tools))
	for i, t := range req.Tools {
		facts[i] = policy.ToolFacts{Name: req.Connector + "." + t.Name, Connector: req.Connector, ConnectorType: ctype, SideEffect: "unknown"}
	}
	out, err := p.Visibility(ctx, pr, facts)
	if err != nil {
		return nil, err
	}
	for i := range out.Data {
		out.Data[i].Name = req.Tools[i].Name
	}
	return out, nil
}

// Visibility answers, for each tool, whether the active policy always denies it to this agent
// (S14's resolve, S15's /mcp/* tools/list and GET /v1/tools). Name and Tool are both the
// qualified name; callers that list wire names set Name. An unavailable policy leaves every tool
// visible: calls are refused at POST /v1/actions anyway, and the cause is transient.
func (p *Pipeline) Visibility(ctx context.Context, pr principal, tools []policy.ToolFacts) (*ToolVisibilityList, error) {
	if pr.Kind != httpx.KindAgentKey || pr.AgentID.IsZero() || pr.OrgID.IsZero() {
		return nil, httpx.ErrInsufficientScope(httpx.ScopeActionsExecute)
	}
	var subj store.GateSubject
	if err := p.o.DB.OrgTx(ctx, pr.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		subj, err = store.LoadGateSubject(ctx, tx, pr.OrgID, pr.AgentID)
		return err
	}); err != nil {
		return nil, err
	}
	labels := map[string]string{}
	if len(subj.Labels) > 0 {
		if err := json.Unmarshal(subj.Labels, &labels); err != nil {
			return nil, fmt.Errorf("gate: agent labels: %w", err)
		}
	}
	agent := policy.AgentFacts{ID: pr.AgentID.String(), Name: subj.AgentName, Labels: labels, Status: subj.AgentStatus}
	act, err := p.o.Policies.Active(ctx, pr.OrgID.String())
	if err != nil {
		return nil, err
	}
	out := &ToolVisibilityList{Object: "list", Data: make([]ToolVisibility, 0, len(tools))}
	enforce := act.Enforce
	if enforce != nil && !enforce.Builtin && enforce.VersionID != "" {
		v := enforce.VersionID
		out.PolicyVersion = &v
	}
	for _, f := range tools {
		tv := ToolVisibility{Object: "tool_visibility", Name: f.Name, Tool: f.Name, Visibility: VisibilityVisible}
		if enforce != nil && enforce.Unavailable == nil {
			denied, rule, reason := policy.AlwaysDeniesWhy(enforce.Compiled, f, agent)
			if rule != "" {
				tv.RuleID = &rule
			}
			if denied {
				tv.Visibility, tv.Reason = VisibilityHidden, &reason
			}
		}
		out.Data = append(out.Data, tv)
	}
	return out, nil
}

// ToolRoutes is POST /v1/tools/resolve. With a nil pipeline (route-table tests) it answers
// service_unavailable.
func ToolRoutes(p *Pipeline) []httpx.Route {
	h := handlers{p}
	return []httpx.Route{
		// Read-only: no state changes, so the idempotency middleware stays out.
		{Method: "POST", Pattern: "/v1/tools/resolve", OperationID: "resolveTools", Auth: httpx.AgentKey,
			Scopes: []string{httpx.ScopeActionsExecute}, Idempotency: httpx.IdemNever, MaxBody: 256 << 10, Handler: h.resolve},
	}
}

func (h handlers) resolve(w http.ResponseWriter, r *http.Request) error {
	pr, err := h.principalOf(r)
	if err != nil {
		return err
	}
	var b ResolveRequest
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	httpx.Annotate(r.Context(), pr.OrgID.String(), pr.AgentID.String(), "")
	out, err := h.p.ResolveTools(r.Context(), pr, b)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}
