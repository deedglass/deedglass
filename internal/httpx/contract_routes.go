package httpx

import (
	"fmt"
	"sort"
	"strings"
)

// ContractRoute is one row of contract §9 as data: the route, its auth kind, the role or scope it
// needs, and the session that owns it. Mount refuses a route whose auth differs from its row, and
// testdata/routes.golden lists every row with its status, so a change in auth shows up in review.
//
// Later sessions sharpen Access (the role or scope); they never change Auth.
type ContractRoute struct {
	Method, Pattern string
	Auth            Auth
	Access          string // "viewer", "admin", "owner", a scope such as "actions:execute", or "" (any)
	Owner           string // the session that mounts it
	Note            string // Sensitive, IdemNever, one-time token, …
}

// Key is "METHOD pattern".
func (c ContractRoute) Key() string { return c.Method + " " + c.Pattern }

func cr(method, pattern string, a Auth, access, owner string, note ...string) ContractRoute {
	return ContractRoute{Method: method, Pattern: pattern, Auth: a, Access: access, Owner: owner, Note: strings.Join(note, "; ")}
}

// contractRoutes is contract §9 plus its additive routes (revision 2026-10-01), in contract order.
var contractRoutes = []ContractRoute{
	// S03: system.
	cr("GET", "/v1/health", Public, "", "S03"),
	cr("GET", "/v1/version", Public, "", "S03"),
	cr("GET", "/api/openapi.yaml", Public, "", "S03"),
	// S04: seal keys.
	cr("GET", "/.well-known/deedglass/keys.json", Public, "", "S04", "CORS *"),

	// S07: people and sessions.
	cr("POST", "/v1/auth/login", Public, "", "S07"),
	cr("POST", "/v1/auth/preview", Public, "", "S07"),
	cr("POST", "/v1/auth/callback", Public, "", "S07"),
	cr("POST", "/v1/auth/logout", Session, "viewer", "S07"),
	cr("POST", "/v1/auth/switch-org", Session, "viewer", "S07"),
	cr("GET", "/v1/me", Session, "viewer", "S07"),
	cr("PATCH", "/v1/me", Session, "viewer", "S07"),
	cr("GET", "/v1/org", Session, "viewer", "S07"),
	cr("PATCH", "/v1/org", Session, "admin", "S07"),
	cr("GET", "/v1/org/members", Session, "viewer", "S07"),
	cr("POST", "/v1/org/members", Session, "admin", "S07"),
	cr("PATCH", "/v1/org/members/{id}", Session, "owner", "S07"),
	cr("DELETE", "/v1/org/members/{id}", Session, "admin", "S07"),
	cr("GET", "/login", Public, "", "S07", "page"),
	cr("GET", "/auth/confirm", Public, "", "S07", "page"),

	// S08: agents, keys, stop.
	cr("GET", "/v1/agents", SessionOrKey, "viewer", "S08"),
	cr("POST", "/v1/agents", SessionOrKey, "admin", "S08"),
	cr("GET", "/v1/agents/{id}", SessionOrKey, "viewer", "S08"),
	cr("PATCH", "/v1/agents/{id}", SessionOrKey, "admin", "S08"),
	cr("POST", "/v1/agents/{id}/suspend", SessionOrKey, "admin", "S08"),
	cr("POST", "/v1/agents/{id}/resume", SessionOrKey, "admin", "S08"),
	cr("POST", "/v1/agents/{id}/stop", SessionOrKey, "admin", "S08"),
	cr("POST", "/v1/agents/{id}/start", SessionOrKey, "admin", "S08"),
	cr("POST", "/v1/org/stop-all", SessionOrKey, "admin", "S08"),
	cr("POST", "/v1/org/start-all", Session, "owner", "S08"),
	cr("GET", "/v1/agents/{id}/keys", SessionOrKey, "viewer", "S08"),
	cr("POST", "/v1/agents/{id}/keys", SessionOrKey, "admin", "S08", "Sensitive"),
	cr("POST", "/v1/keys/{id}/roll", SessionOrKey, "admin", "S08", "Sensitive"),
	cr("POST", "/v1/keys/{id}/revoke", SessionOrKey, "admin", "S08"),
	cr("GET", "/v1/whoami", AgentKey, "", "S08", "any key; skips the stop checks"),
	cr("GET", "/v1/org/keys", Session, "admin", "S08"),
	cr("POST", "/v1/org/keys", Session, "admin", "S08", "Sensitive; admin keys can't mint admin keys"),

	// S09: signed intents and OAuth metadata.
	cr("GET", "/v1/agents/{id}/intent-keys", SessionOrKey, "viewer", "S09"),
	cr("POST", "/v1/agents/{id}/intent-keys", SessionOrKey, "admin", "S09"),
	cr("DELETE", "/v1/agents/{id}/intent-keys/{kid}", SessionOrKey, "admin", "S09"),
	cr("POST", "/v1/intents/verify", AgentKey, "", "S09"),
	cr("GET", "/.well-known/oauth-protected-resource", Public, "", "S09", "CORS *"),
	cr("GET", "/.well-known/oauth-protected-resource/mcp/{connector}", Public, "", "S09", "CORS *"),
	cr("GET", "/.well-known/oauth-protected-resource/mcp/native", Public, "", "S09", "CORS *"),

	// S12: policies. Sessions by role, org admin keys (scope admin) as admin; agent keys are
	// refused even for reads.
	cr("GET", "/v1/policies", SessionOrKey, "viewer", "S12"),
	cr("POST", "/v1/policies", SessionOrKey, "admin", "S12"),
	cr("GET", "/v1/policies/active", SessionOrKey, "viewer", "S12"),
	cr("GET", "/v1/policies/{id}", SessionOrKey, "viewer", "S12"),
	cr("GET", "/v1/policies/{id}/versions", SessionOrKey, "viewer", "S12"),
	cr("POST", "/v1/policies/{id}/versions", SessionOrKey, "admin", "S12"),
	cr("GET", "/v1/policies/{id}/versions/{v}", SessionOrKey, "viewer", "S12"),
	cr("POST", "/v1/policies/{id}/versions/{v}/activate", SessionOrKey, "admin", "S12"),
	cr("POST", "/v1/policies/{id}/versions/{v}/deactivate", SessionOrKey, "admin", "S12"),
	cr("POST", "/v1/policies/lint", SessionOrKey, "viewer", "S12", "IdemNever: no side effects"),
	cr("POST", "/v1/policies/evaluate", SessionOrKey, "viewer", "S12", "IdemNever: no side effects"),
	cr("POST", "/v1/policies/test", SessionOrKey, "viewer", "S12", "IdemNever: no side effects"),
	cr("POST", "/v1/policies/{id}/versions/{v}/replay", SessionOrKey, "admin", "S27"),

	// S13/S14: actions.
	cr("POST", "/v1/actions", AgentKey, "actions:execute", "S13", "IdemNever: the Gate owns its idempotency"),
	cr("GET", "/v1/actions", SessionOrKey, "actions:read", "S06", "agent keys: own agent only (S08)"),
	cr("GET", "/v1/actions/{id}", SessionOrKey, "actions:read", "S06", "agent keys: own agent only (S08)"),
	cr("POST", "/v1/actions/{id}/cancel", SessionOrKey, "admin", "S13"),
	cr("POST", "/v1/actions/{id}/result", AgentKey, "actions:execute", "S13", "proxy mode"),
	cr("POST", "/v1/tools/resolve", AgentKey, "actions:execute", "S14"),
	cr("GET", "/v1/tools", AgentKey, "actions:execute", "S15"),

	// S23: undo.
	cr("POST", "/v1/actions/{id}/undo/preview", SessionOrKey, "admin", "S23", "agent keys: insufficient_scope; IdemNever: writes nothing"),
	cr("POST", "/v1/actions/{id}/undo", SessionOrKey, "admin", "S23", "agent keys: insufficient_scope; the console system agent acts for the person"),

	// S22: approvals, approval links and webhooks.
	cr("GET", "/v1/approvals", SessionOrKey, "viewer", "S22", "agent keys see their own actions only"),
	cr("GET", "/v1/approvals/{id}", SessionOrKey, "viewer", "S22"),
	cr("POST", "/v1/approvals/{id}/approve", SessionOrKey, "viewer", "S22", "only a person countersigns: keys get approval_needs_person; eligibility (not the role) decides"),
	cr("POST", "/v1/approvals/{id}/deny", SessionOrKey, "viewer", "S22", "only a person countersigns: keys get approval_needs_person; eligibility (not the role) decides"),
	cr("POST", "/v1/approval-links/preview", Public, "", "S22", "one-time token in the body"),
	cr("POST", "/v1/approval-links/decide", Public, "", "S22", "one-time token in the body"),
	cr("POST", "/v1/approval-links/status", Public, "", "S22", "one-time token in the body"),
	cr("GET", "/approve", Public, "", "S22", "page"),
	cr("GET", "/v1/webhooks", SessionOrKey, "admin", "S22"),
	cr("POST", "/v1/webhooks", SessionOrKey, "admin", "S22", "Sensitive"),
	cr("GET", "/v1/webhooks/{id}", SessionOrKey, "admin", "S22"),
	cr("DELETE", "/v1/webhooks/{id}", SessionOrKey, "admin", "S22"),
	cr("POST", "/v1/webhooks/{id}/test", SessionOrKey, "admin", "S22"),
	cr("GET", "/v1/webhooks/{id}/deliveries", SessionOrKey, "admin", "S22"),

	// S06: deeds and the Ledger.
	cr("GET", "/v1/deeds", SessionOrKey, "deeds:read", "S06", "agent keys: own agent only (S08)"),
	cr("GET", "/v1/deeds/{id}", SessionOrKey, "deeds:read", "S06", "agent keys: own agent only (S08)"),
	cr("GET", "/v1/deeds/export", SessionOrKey, "viewer", "S06", "keys: admin (S08)"),
	cr("POST", "/v1/deeds/files", SessionOrKey, "admin", "S38", "seals file.sealed from a file's sha256, size, media type and label; the file stays local"),
	cr("GET", "/v1/ledger/check", SessionOrKey, "viewer", "S06", "keys: admin (S08)"),

	// S16/S17/S20/S30: connectors.
	cr("GET", "/v1/connectors", SessionOrKey, "viewer", "S16"),
	cr("POST", "/v1/connectors", SessionOrKey, "admin", "S16"),
	cr("GET", "/v1/connectors/{id}", SessionOrKey, "viewer", "S16"),
	cr("PATCH", "/v1/connectors/{id}", SessionOrKey, "admin", "S16"),
	cr("DELETE", "/v1/connectors/{id}", SessionOrKey, "admin", "S16"),
	cr("PUT", "/v1/connectors/{id}/credentials", SessionOrKey, "admin", "S16", "write-only; never stored for replay"),
	cr("DELETE", "/v1/connectors/{id}/credentials", SessionOrKey, "admin", "S16"),
	cr("POST", "/v1/connectors/{id}/test", SessionOrKey, "admin", "S16", "IdemNever: 10 per minute per connector"),
	cr("POST", "/v1/connectors/{id}/initialized", SessionOrKey, "admin", "S17", "postgres only: seals connector.postgres_initialized"),
	cr("GET", "/v1/connector-types", SessionOrKey, "viewer", "S16"),
	cr("GET", "/v1/connector-types/{type}", SessionOrKey, "viewer", "S30", "keys: admin; ETag, If-None-Match → 304"),
	cr("GET", "/v1/connectors/{id}/decoder", SessionOrKey, "viewer", "S20", "keys: admin; ?recent=0..50"),
	cr("DELETE", "/v1/connectors/{id}/decoder", SessionOrKey, "admin", "S20", "body {confirm: <name>}; seals decoder.slot_dropped"),

	// S24/S25: the Console and the live stream.
	cr("GET", "/v1/events", SessionOrKey, "viewer", "S24", "SSE; agent keys need actions:read and see their own agent's events only"),
	cr("GET", "/app", Public, "", "S24", "Console pages: the handler redirects to /login?next= without a session"),
	cr("GET", "/app/{path...}", Public, "", "S24", "Console pages: the handler redirects to /login?next= without a session"),
	cr("GET", "/v1/overview", SessionOrKey, "viewer", "S25", "keys: admin; agent keys 403"),

	// S27/S28: Insights.
	cr("GET", "/v1/insights/stats", SessionOrKey, "viewer", "S27"),
	cr("GET", "/v1/insights/anomalies", SessionOrKey, "viewer", "S27"),
	cr("GET", "/v1/insights/anomalies/{id}", SessionOrKey, "viewer", "S27"),
	cr("POST", "/v1/insights/anomalies/{id}/acknowledge", SessionOrKey, "admin", "S27"),
	cr("POST", "/v1/insights/anomalies/{id}/resolve", SessionOrKey, "admin", "S27"),
	cr("GET", "/v1/insights/shadow", SessionOrKey, "viewer", "S27"),
	cr("GET", "/v1/insights/settings", SessionOrKey, "viewer", "S27"),
	cr("PATCH", "/v1/insights/settings", SessionOrKey, "admin", "S27"),
	cr("GET", "/v1/replays", SessionOrKey, "viewer", "S27"),
	cr("GET", "/v1/replays/{id}", SessionOrKey, "viewer", "S27"),
	cr("POST", "/v1/replays/{id}/cancel", SessionOrKey, "admin", "S27"),
	cr("GET", "/v1/suggestions", SessionOrKey, "viewer", "S28"),
	cr("GET", "/v1/suggestions/{id}", SessionOrKey, "viewer", "S28"),
	cr("POST", "/v1/suggestions/{id}/accept", SessionOrKey, "admin", "S28"),
	cr("POST", "/v1/suggestions/{id}/reject", SessionOrKey, "admin", "S28"),
	cr("POST", "/v1/suggestions/generate", SessionOrKey, "admin", "S28"),

	// S50: observe mode (watched sessions). Agent keys get 403 on every route.
	cr("POST", "/v1/observe/sessions", SessionOrKey, "admin", "S50", "keys: admin; 409 observe_not_enabled when the connector's observe mode is off"),
	cr("POST", "/v1/observe/sessions/{id}/close", SessionOrKey, "admin", "S50", "moves to closing; the serve job seals it"),
	cr("GET", "/v1/observe/sessions", SessionOrKey, "viewer", "S50", "keys: admin; ?connector ?state ?caught, cursor pagination"),
	cr("GET", "/v1/observe/sessions/{id}", SessionOrKey, "viewer", "S50", "keys: admin; transactions with 20 diff rows each"),
	cr("POST", "/v1/observe/sessions/{id}/frames/{dtx}/exclude", SessionOrKey, "admin", "S50", "sealed sessions only; seals observe.frame_excluded"),
	cr("GET", "/v1/observe/sessions/{id}/undo_preview", SessionOrKey, "viewer", "S50", "free; never executes; carries the undo SQL"),
	cr("POST", "/v1/observe/sessions/{id}/undo", SessionOrKey, "admin", "S50", "Pro: 403 pro_required without undo_session; the console system agent acts"),

	// S15: MCP (GET and DELETE answer 405; sessionless).
	cr("POST", "/mcp/{connector}", AgentKey, "actions:execute", "S15"),
	cr("POST", "/mcp/native", AgentKey, "actions:execute", "S15"),

	// S29: Slack (Slack's signature, form body).
	cr("POST", "/v1/slack/interact", Public, "", "S29", "Slack signature; form body"),
}

var contractByKey = func() map[string]ContractRoute {
	m := make(map[string]ContractRoute, len(contractRoutes))
	for _, c := range contractRoutes {
		if _, dup := m[c.Key()]; dup {
			panic("httpx: duplicate contract route " + c.Key())
		}
		m[c.Key()] = c
	}
	return m
}()

// ContractRoutes returns contract §9 as data, in contract order. The slice is a copy.
func ContractRoutes() []ContractRoute { return append([]ContractRoute(nil), contractRoutes...) }

// RoutesGolden renders the catalog with each row's status for testdata/routes.golden: "mounted"
// when mounted names it, else "planned". Mounted extension routes are listed after the catalog.
func RoutesGolden(mounted []Route) string {
	on := map[string]Route{}
	for _, r := range mounted {
		on[r.Key()] = r
	}
	var b strings.Builder
	b.WriteString("# Contract §9 as data (internal/httpx/contract_routes.go). Regenerate: go test ./internal/httpx -run Routes -update\n")
	b.WriteString("# status   method  pattern                                                auth            access           owner  note\n")
	for _, c := range contractRoutes {
		status := "planned"
		if _, ok := on[c.Key()]; ok {
			status = "mounted"
			delete(on, c.Key())
		}
		l := fmt.Sprintf("%-8s   %-6s  %-54s %-15s %-16s %-6s %s", status, c.Method, c.Pattern, c.Auth, dash(c.Access), c.Owner, c.Note)
		b.WriteString(strings.TrimRight(l, " ") + "\n")
	}
	var ext []string
	for k, r := range on {
		if r.Extension == "test" {
			continue
		}
		ext = append(ext, fmt.Sprintf("%-8s   %-6s  %-54s %-15s %-16s %-6s %s", "mounted", r.Method, r.Pattern, r.Auth, "", "", "extension "+r.Extension+" ("+k+")"))
	}
	sort.Strings(ext)
	for _, l := range ext {
		b.WriteString(strings.TrimRight(l, " ") + "\n")
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
