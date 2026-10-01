package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/fake"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/mcp/front"
	"github.com/deedglass/deedglass/internal/mcp/httpgw"
	"github.com/deedglass/deedglass/internal/mcp/upstream"
)

// mcpWiring is S15's MCP gateway as serve mounts it: the native tools, the upstream pool and the
// HTTP handlers.
type mcpWiring struct {
	gw      *httpgw.Gateway
	native  *front.NativeSource
	pool    *upstream.Pool
	timeout time.Duration
	origins []string
}

// nativeConnectors are the in-process connectors /mcp/native serves: fake in dev only (S16
// reserves the name outside dev).
func nativeConnectors(cfg *config.Config) []connector.Connector {
	if cfg.Env == config.Dev {
		return []connector.Connector{fake.New()}
	}
	return nil
}

// newPool is the upstream MCP servers of mcp.upstreams. Header values are read from the
// environment once, here, and never logged.
func newPool(cfg *config.Config, getenv func(string) string, log *slog.Logger, pol *egress.Policy) (*upstream.Pool, error) {
	ups := make([]upstream.Config, 0, len(cfg.MCP.Upstreams))
	for _, u := range cfg.MCP.Upstreams {
		if len(u.HeadersFromEnv) > 0 && log != nil {
			// Prod refuses this at config load (S18); dev keeps it for local servers.
			log.Warn("mcp: headers_from_env is for dev; in prod, create an mcp_http connector and keep its credential in the vault", "upstream", u.Slug)
		}
		ups = append(ups, upstream.Config{Slug: u.Slug, URL: u.URL, HeadersFromEnv: u.HeadersFromEnv, Timeout: u.Timeout, ToolsTTL: u.ToolsTTL})
	}
	return upstream.New(upstream.Options{Upstreams: ups, AllowPrivate: cfg.AllowPrivateNetworks, Egress: pol, Version: buildInfo().Version,
		Getenv: getenv, Log: log})
}

// newMCP wires the fronts: /mcp/native over the native connectors, and one /mcp/<slug> per
// upstream. Each call waits gate.execute_timeout + 2 s for the Gate's answer.
func newMCP(cfg *config.Config, gp *gate.Pipeline, natives []connector.Connector, pool *upstream.Pool, reg *connector.Registry, typ *upstream.Type, guard identity.Guard, log *slog.Logger) *mcpWiring {
	exec := gp.Config().ExecuteTimeout
	base := front.Front{Version: buildInfo().Version, Gate: gp, CallWait: exec + 2*time.Second, ExecuteTimeout: exec, Log: log}
	ns := &front.NativeSource{Connectors: natives, Vis: gp}
	if reg != nil {
		// S18: the org's own native connectors (postgres.*, orders.*, …) on /mcp/native too.
		ns.Rows = func(ctx context.Context, p identity.Principal) ([]connector.RowTools, error) {
			return reg.NativeRows(ctx, p.OrgID)
		}
	}
	nf := base
	nf.Tools = ns
	pass := map[string]bool{}
	for _, f := range cfg.MCP.Passthrough {
		pass[f] = true
	}
	ups := map[string]*front.Front{}
	for _, slug := range pool.Slugs() {
		src := pool.Source(slug, gp)
		f := base
		f.Tools, f.Results, f.Passthrough, f.PassMethods = src, pool, src, pass
		ups[slug] = &f
	}
	gw := &httpgw.Gateway{Native: &nf, Upstreams: ups, Guard: guard, Log: log}
	if reg != nil && typ != nil {
		// S18: the org's mcp_http connectors, served at /mcp/{name} with their vault credential.
		gw.Rows = func(ctx context.Context, p identity.Principal, name string) (*front.Front, error) {
			ok, err := typ.Serves(ctx, reg, p, name)
			if err != nil || !ok {
				return nil, err
			}
			src := typ.RowSource(reg, p.OrgID, name, gp)
			f := base
			f.Tools, f.Results, f.Passthrough, f.PassMethods = src, pool, src, pass
			return &f, nil
		}
	}
	return &mcpWiring{gw: gw, native: ns, pool: pool, timeout: exec + 30*time.Second, origins: cfg.MCP.AllowedOrigins}
}

// mcpRoutes are the MCP endpoints: the gateway when wired, S09's stubs otherwise (route tests).
func mcpRoutes(auth *identity.MCPAuth, mw *mcpWiring) []httpx.Route {
	if mw == nil {
		return auth.MCPRoutes()
	}
	auth.WithAllowedOrigins(mw.origins)
	return auth.GatewayRoutes(identity.MCPGateway{Connector: mw.gw.Connector, Native: mw.gw.NativeHandler, Timeout: mw.timeout})
}

// toolsRoutes is GET /v1/tools (S15): the native tools for the calling agent.
func toolsRoutes(mw *mcpWiring) []httpx.Route {
	var ns *front.NativeSource
	if mw != nil {
		ns = mw.native
	}
	return []httpx.Route{{Method: "GET", Pattern: "/v1/tools", OperationID: "listTools", Auth: httpx.AgentKey,
		Scopes: []string{httpx.ScopeActionsExecute}, Handler: toolsHandler(ns)}}
}

// toolObject is one native tool in GET /v1/tools.
type toolObject struct {
	Object      string          `json:"object"` // "tool"
	Name        string          `json:"name"`
	Connector   string          `json:"connector"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	SideEffect  string          `json:"side_effect"`
	Visibility  string          `json:"visibility"`
}

type toolList struct {
	Object        string       `json:"object"`
	Data          []toolObject `json:"data"`
	HasMore       bool         `json:"has_more"`
	NextCursor    *string      `json:"next_cursor"`
	PolicyVersion *string      `json:"policy_version"`
}

func toolsHandler(ns *front.NativeSource) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if ns == nil {
			return httpx.E(httpx.CodeServiceUnavailable)
		}
		p, ok := identity.PrincipalFrom(r.Context())
		if !ok || p.Kind != httpx.KindAgentKey {
			return httpx.ErrInsufficientScope(httpx.ScopeActionsExecute)
		}
		hidden := false
		if v := r.URL.Query().Get("include_hidden"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return httpx.ErrValidation("include_hidden", "include_hidden must be true or false.")
			}
			hidden = b
		}
		httpx.Annotate(r.Context(), p.OrgID.String(), p.AgentID.String(), "")
		nts, pv, err := ns.AllWithPolicy(r.Context(), *p)
		if err != nil {
			return err
		}
		out := toolList{Object: "list", Data: []toolObject{}, PolicyVersion: pv}
		for _, t := range nts {
			vis := gate.VisibilityVisible
			if t.Hidden {
				if !hidden {
					continue
				}
				vis = gate.VisibilityHidden
			}
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			out.Data = append(out.Data, toolObject{Object: "tool", Name: t.Name, Connector: t.Connector, Title: t.Title,
				Description: t.Description, InputSchema: schema, SideEffect: t.SideEffect, Visibility: vis})
		}
		return httpx.WriteJSON(w, http.StatusOK, out)
	}
}
