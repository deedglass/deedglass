package httpx

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/api"
)

func systemServer() *Server {
	s := New(Options{Logger: NewLogger(io.Discard, true, nil)})
	s.Mount(s.SystemRoutes(SystemDeps{Spec: api.Spec, Health: &fakeHealth{state: MigrationState{Applied: 9, Expected: 9}}})...)
	s.Mount(s.WellKnownRoutes(testKeySet, nil)...)
	s.Mount(elsewhere()...)
	return s
}

// elsewhere stands in for routes other packages declare (internal/ledger S06, internal/identity
// S07), so the spec checks
// here see every documented operation. cmd/deedglass's TestServeRoutesMatchOpenAPI checks the real
// declarations, from the list serve mounts.
func elsewhere() []Route {
	stub := func(w http.ResponseWriter, r *http.Request) error { return E(CodeNotFound) }
	var out []Route
	for _, op := range []struct{ pattern, id, scope string }{
		{"/v1/actions", "listActions", "actions:read"}, {"/v1/actions/{id}", "getAction", "actions:read"},
		{"/v1/deeds", "listDeeds", "deeds:read"}, {"/v1/deeds/{id}", "getDeed", "deeds:read"},
		{"/v1/deeds/export", "exportDeeds", "deeds:read"}, {"/v1/ledger/check", "checkLedger", "deeds:read"},
	} {
		out = append(out, Route{Method: "GET", Pattern: op.pattern, OperationID: op.id, Auth: SessionOrKey,
			MinRole: RoleViewer, Scopes: []string{op.scope}, Handler: stub})
	}
	// S07: people.
	pub := func(m, p, id string) Route {
		r := Route{Method: m, Pattern: p, OperationID: id, Auth: Public, Handler: stub}
		if m == "POST" {
			r.Idempotency = IdemNever
		}
		return r
	}
	ses := func(m, p, id string, role Role, idem Idem) Route {
		return Route{Method: m, Pattern: p, OperationID: id, Auth: Session, MinRole: role, Idempotency: idem, Handler: stub}
	}
	out = append(out,
		pub("POST", "/v1/auth/login", "requestLogin"), pub("POST", "/v1/auth/preview", "previewLogin"),
		pub("POST", "/v1/auth/callback", "completeLogin"),
		ses("POST", "/v1/auth/logout", "logout", RoleViewer, IdemNever), ses("POST", "/v1/auth/switch-org", "switchOrg", RoleViewer, IdemNever),
		ses("GET", "/v1/me", "getMe", RoleViewer, 0), ses("PATCH", "/v1/me", "updateMe", RoleViewer, 0),
		ses("GET", "/v1/org", "getOrg", RoleViewer, 0), ses("PATCH", "/v1/org", "updateOrg", RoleAdmin, 0),
		ses("GET", "/v1/org/members", "listMembers", RoleViewer, 0), ses("POST", "/v1/org/members", "inviteMember", RoleAdmin, IdemOptional),
		ses("PATCH", "/v1/org/members/{id}", "updateMember", RoleOwner, 0), ses("DELETE", "/v1/org/members/{id}", "removeMember", RoleAdmin, 0),
		pub("GET", "/login", "loginPage"), pub("GET", "/auth/confirm", "confirmPage"), pub("GET", "/app", "consoleIndex"), pub("GET", "/app/{path...}", "consolePage"),
		Route{Method: "GET", Pattern: "/auth/assets/{file}", OperationID: "authAsset", Auth: Public, Extension: "ADR-028", PublicReason: "static files", Handler: stub},
	)
	// S08: agents, keys and Stop.
	adm := func(m, p, id string, role Role) Route {
		r := Route{Method: m, Pattern: p, OperationID: id, Auth: SessionOrKey, MinRole: role, Scopes: []string{ScopeAdmin}, Handler: stub}
		if m == "POST" {
			r.Idempotency = IdemOptional
		}
		return r
	}
	out = append(out,
		adm("GET", "/v1/agents", "listAgents", RoleViewer), adm("POST", "/v1/agents", "createAgent", RoleAdmin),
		adm("GET", "/v1/agents/{id}", "getAgent", RoleViewer), adm("PATCH", "/v1/agents/{id}", "updateAgent", RoleAdmin),
		adm("POST", "/v1/agents/{id}/suspend", "suspendAgent", RoleAdmin), adm("POST", "/v1/agents/{id}/resume", "resumeAgent", RoleAdmin),
		adm("POST", "/v1/agents/{id}/stop", "stopAgent", RoleAdmin), adm("POST", "/v1/agents/{id}/start", "startAgent", RoleAdmin),
		adm("POST", "/v1/org/stop-all", "stopAllAgents", RoleAdmin), ses("POST", "/v1/org/start-all", "startAllAgents", RoleOwner, IdemOptional),
		adm("GET", "/v1/agents/{id}/keys", "listAgentKeys", RoleViewer), adm("POST", "/v1/agents/{id}/keys", "createAgentKey", RoleAdmin),
		adm("POST", "/v1/keys/{id}/roll", "rollKey", RoleAdmin), adm("POST", "/v1/keys/{id}/revoke", "revokeKey", RoleAdmin),
		Route{Method: "GET", Pattern: "/v1/whoami", OperationID: "whoami", Auth: AgentKey, SkipStopChecks: true, Handler: stub},
		ses("GET", "/v1/org/keys", "listAdminKeys", RoleAdmin, 0), ses("POST", "/v1/org/keys", "createAdminKey", RoleAdmin, IdemOptional),
	)
	// S09: intent keys, the conformance check and MCP authorization readiness.
	key := func(p, id string) Route {
		return Route{Method: "POST", Pattern: p, OperationID: id, Auth: AgentKey, Scopes: []string{ScopeActionsExecute}, Idempotency: IdemNever, Handler: stub}
	}
	out = append(out,
		adm("GET", "/v1/agents/{id}/intent-keys", "listIntentKeys", RoleViewer), adm("POST", "/v1/agents/{id}/intent-keys", "addIntentKey", RoleAdmin),
		adm("DELETE", "/v1/agents/{id}/intent-keys/{kid}", "removeIntentKey", RoleAdmin), key("/v1/intents/verify", "verifyIntent"),
		pub("GET", "/.well-known/oauth-protected-resource", "getProtectedResourceMetadata"),
		pub("GET", "/.well-known/oauth-protected-resource/mcp/native", "getNativeMCPResourceMetadata"),
		pub("GET", "/.well-known/oauth-protected-resource/mcp/{connector}", "getMCPResourceMetadata"),
		key("/mcp/{connector}", "mcpConnector"), key("/mcp/native", "mcpNative"),
	)
	// S12: policies (internal/policyapi). lint, evaluate and test change nothing (IdemNever).
	tool := func(p, id string) Route {
		r := adm("POST", p, id, RoleViewer)
		r.Idempotency = IdemNever
		return r
	}
	out = append(out,
		adm("GET", "/v1/policies", "listPolicies", RoleViewer), adm("POST", "/v1/policies", "createPolicy", RoleAdmin),
		adm("GET", "/v1/policies/active", "getActivePolicies", RoleViewer), adm("GET", "/v1/policies/{id}", "getPolicy", RoleViewer),
		adm("GET", "/v1/policies/{id}/versions", "listPolicyVersions", RoleViewer),
		adm("POST", "/v1/policies/{id}/versions", "createPolicyVersion", RoleAdmin),
		adm("GET", "/v1/policies/{id}/versions/{v}", "getPolicyVersion", RoleViewer),
		adm("POST", "/v1/policies/{id}/versions/{v}/activate", "activatePolicyVersion", RoleAdmin),
		adm("POST", "/v1/policies/{id}/versions/{v}/deactivate", "deactivatePolicyVersion", RoleAdmin),
		tool("/v1/policies/lint", "lintPolicy"), tool("/v1/policies/evaluate", "evaluatePolicy"), tool("/v1/policies/test", "testPolicy"),
	)
	// S13: the Gate (internal/gate).
	out = append(out,
		Route{Method: "POST", Pattern: "/v1/actions", OperationID: "createAction", Auth: AgentKey, Scopes: []string{ScopeActionsExecute}, Idempotency: IdemNever, Handler: stub},
		Route{Method: "POST", Pattern: "/v1/actions/{id}/cancel", OperationID: "cancelAction", Auth: SessionOrKey, MinRole: RoleAdmin, Scopes: []string{ScopeActionsExecute}, Idempotency: IdemOptional, Handler: stub},
		Route{Method: "POST", Pattern: "/v1/actions/{id}/result", OperationID: "reportActionResult", Auth: AgentKey, Scopes: []string{ScopeActionsExecute}, Idempotency: IdemOptional, Handler: stub},
	)
	// S14: tool visibility for MCP wrappers (internal/gate).
	out = append(out,
		Route{Method: "POST", Pattern: "/v1/tools/resolve", OperationID: "resolveTools", Auth: AgentKey, Scopes: []string{ScopeActionsExecute}, Idempotency: IdemNever, Handler: stub},
	)
	// S15: the native tool catalog (cmd/deedglass serve_mcp.go).
	out = append(out,
		Route{Method: "GET", Pattern: "/v1/tools", OperationID: "listTools", Auth: AgentKey, Scopes: []string{ScopeActionsExecute}, Handler: stub},
	)
	// S16: connectors (internal/connectorapi).
	out = append(out,
		adm("GET", "/v1/connectors", "listConnectors", RoleViewer), adm("POST", "/v1/connectors", "createConnector", RoleAdmin),
		adm("GET", "/v1/connectors/{id}", "getConnector", RoleViewer), adm("PATCH", "/v1/connectors/{id}", "updateConnector", RoleAdmin),
		adm("DELETE", "/v1/connectors/{id}", "deleteConnector", RoleAdmin),
		adm("PUT", "/v1/connectors/{id}/credentials", "setConnectorCredentials", RoleAdmin),
		adm("DELETE", "/v1/connectors/{id}/credentials", "deleteConnectorCredentials", RoleAdmin),
		adm("POST", "/v1/connectors/{id}/test", "testConnector", RoleAdmin), adm("GET", "/v1/connector-types", "listConnectorTypes", RoleViewer),
		adm("GET", "/v1/connector-types/{type}", "getConnectorType", RoleViewer),
	)
	// S17: the Postgres setup record (internal/connectorapi).
	out = append(out, adm("POST", "/v1/connectors/{id}/initialized", "recordConnectorInitialized", RoleAdmin))
	// S38: sealed files (internal/ledger/files.go).
	out = append(out, adm("POST", "/v1/deeds/files", "sealFile", RoleAdmin))
	// S20: the decoder (internal/connectorapi/decoder.go).
	out = append(out, adm("GET", "/v1/connectors/{id}/decoder", "getConnectorDecoder", RoleViewer),
		adm("DELETE", "/v1/connectors/{id}/decoder", "dropConnectorDecoder", RoleAdmin))
	// S22: approvals, approval links, the /approve page (internal/approve) and webhooks (internal/notify/webhook).
	actionsRead := []string{ScopeActionsRead}
	link := func(method, pattern, op string) Route {
		r := Route{Method: method, Pattern: pattern, OperationID: op, Auth: Public, Handler: stub}
		if method == "POST" {
			r.Idempotency = IdemNever
		}
		return r
	}
	out = append(out,
		Route{Method: "GET", Pattern: "/v1/approvals", OperationID: "listApprovals", Auth: SessionOrKey, MinRole: RoleViewer, Scopes: actionsRead, Handler: stub},
		Route{Method: "GET", Pattern: "/v1/approvals/{id}", OperationID: "getApproval", Auth: SessionOrKey, MinRole: RoleViewer, Scopes: actionsRead, Handler: stub},
		Route{Method: "POST", Pattern: "/v1/approvals/{id}/approve", OperationID: "approveApproval", Auth: SessionOrKey, MinRole: RoleViewer, Scopes: actionsRead, Idempotency: IdemOptional, Handler: stub},
		Route{Method: "POST", Pattern: "/v1/approvals/{id}/deny", OperationID: "denyApproval", Auth: SessionOrKey, MinRole: RoleViewer, Scopes: actionsRead, Idempotency: IdemOptional, Handler: stub},
		link("POST", "/v1/approval-links/preview", "previewApprovalLink"), link("POST", "/v1/approval-links/decide", "decideApprovalLink"),
		link("POST", "/v1/approval-links/status", "approvalLinkStatus"), link("GET", "/approve", "approvePage"),
		Route{Method: "GET", Pattern: "/approve/assets/{file}", OperationID: "approveAsset", Auth: Public, Extension: "ADR-048", PublicReason: "static files", Handler: stub},
		adm("GET", "/v1/webhooks", "listWebhooks", RoleAdmin), adm("POST", "/v1/webhooks", "createWebhook", RoleAdmin),
		adm("GET", "/v1/webhooks/{id}", "getWebhook", RoleAdmin), adm("DELETE", "/v1/webhooks/{id}", "deleteWebhook", RoleAdmin),
		adm("POST", "/v1/webhooks/{id}/test", "testWebhook", RoleAdmin), adm("GET", "/v1/webhooks/{id}/deliveries", "listWebhookDeliveries", RoleAdmin),
	)
	// S24: the live event stream (internal/console).
	out = append(out, Route{Method: "GET", Pattern: "/v1/events", OperationID: "streamEvents", Auth: SessionOrKey, MinRole: RoleViewer,
		Scopes: actionsRead, Streaming: true, Handler: stub})
	// S23: undo (internal/undoapi).
	out = append(out, adm("POST", "/v1/actions/{id}/undo/preview", "previewUndo", RoleAdmin), adm("POST", "/v1/actions/{id}/undo", "createUndo", RoleAdmin))
	// S25: the Overview (internal/console).
	out = append(out, Route{Method: "GET", Pattern: "/v1/overview", OperationID: "getOverview", Auth: SessionOrKey, MinRole: RoleViewer,
		Scopes: []string{ScopeAdmin}, Handler: stub})
	// S27: Insights (internal/learn).
	out = append(out,
		adm("POST", "/v1/policies/{id}/versions/{v}/replay", "createReplay", RoleAdmin), adm("GET", "/v1/replays", "listReplays", RoleViewer),
		adm("GET", "/v1/replays/{id}", "getReplay", RoleViewer), adm("POST", "/v1/replays/{id}/cancel", "cancelReplay", RoleAdmin),
		adm("GET", "/v1/insights/stats", "getInsightsStats", RoleViewer), adm("GET", "/v1/insights/shadow", "getShadowReport", RoleViewer),
		adm("GET", "/v1/insights/anomalies", "listAnomalies", RoleViewer), adm("GET", "/v1/insights/anomalies/{id}", "getAnomaly", RoleViewer),
		adm("POST", "/v1/insights/anomalies/{id}/acknowledge", "acknowledgeAnomaly", RoleAdmin),
		adm("POST", "/v1/insights/anomalies/{id}/resolve", "resolveAnomaly", RoleAdmin),
		adm("GET", "/v1/insights/settings", "getInsightsSettings", RoleViewer), adm("PATCH", "/v1/insights/settings", "updateInsightsSettings", RoleAdmin),
	)
	// S28: suggestions (internal/learn).
	out = append(out,
		adm("GET", "/v1/suggestions", "listSuggestions", RoleViewer), adm("GET", "/v1/suggestions/{id}", "getSuggestion", RoleViewer),
		adm("POST", "/v1/suggestions/generate", "generateSuggestions", RoleAdmin),
		adm("POST", "/v1/suggestions/{id}/accept", "acceptSuggestion", RoleAdmin), adm("POST", "/v1/suggestions/{id}/reject", "rejectSuggestion", RoleAdmin),
	)
	// S50: observe mode (internal/observeapi).
	out = append(out,
		adm("POST", "/v1/observe/sessions", "openObserveSession", RoleAdmin), adm("GET", "/v1/observe/sessions", "listObserveSessions", RoleViewer),
		adm("GET", "/v1/observe/sessions/{id}", "getObserveSession", RoleViewer),
		adm("POST", "/v1/observe/sessions/{id}/close", "closeObserveSession", RoleAdmin),
		adm("POST", "/v1/observe/sessions/{id}/frames/{dtx}/exclude", "excludeObserveFrame", RoleAdmin),
		adm("GET", "/v1/observe/sessions/{id}/undo_preview", "previewObserveUndo", RoleViewer),
		adm("POST", "/v1/observe/sessions/{id}/undo", "createObserveUndo", RoleAdmin),
	)
	// S51: the security review pack (internal/securitypackapi). The build seals new records, so a
	// replayed key must not hand back an old folder (IdemNever).
	pack := adm("POST", "/v1/security_pack", "createSecurityPack", RoleAdmin)
	pack.Idempotency = IdemNever
	out = append(out, adm("GET", "/v1/security_pack/readiness", "getSecurityPackReadiness", RoleViewer), pack)
	// S29: Slack interactivity (internal/notify/slack): public, a form body, Slack's signature.
	out = append(out, Route{Method: "POST", Pattern: "/v1/slack/interact", OperationID: "slackInteract", Auth: Public, Idempotency: IdemNever,
		BodyType: "application/x-www-form-urlencoded", MaxBody: 256 << 10, Handler: stub})
	return out
}

// TestOpenAPI runs checks 1–6: routes ⇄ operations, operationIds, x-deedglass-auth, the code enum,
// the default error response and info.version.
func TestOpenAPI(t *testing.T) {
	if !strings.HasPrefix(string(api.Spec), "openapi: 3.1.1\n") {
		t.Fatal("api/openapi.yaml must start with `openapi: 3.1.1`")
	}
	for _, err := range CheckSpec(api.Spec, systemServer().Routes()) {
		t.Error(err)
	}
}

// TestOpenAPIRequired is check 7: real handler output has every property the spec requires.
func TestOpenAPIRequired(t *testing.T) {
	h := &harness{log: &logBuf{}}
	h.srv = systemServer()
	for path, schema := range map[string]string{"/v1/health": "Health", "/v1/version": "Version", "/v1/nope": "ErrorEnvelope", "/.well-known/deedglass/keys.json": "SealKeySet"} {
		r := h.do(t, "GET", path, "")
		for _, err := range CheckRequired(api.Spec, schema, r.body) {
			t.Errorf("%s: %v", path, err)
		}
	}
}

// TestOpenAPIChecksCatchDrift shows the checks fail when they should.
func TestOpenAPIChecksCatchDrift(t *testing.T) {
	s := systemServer()
	routes := s.Routes()

	drifted := strings.Replace(string(api.Spec), "operationId: getVersion", "operationId: getVersionX", 1)
	drifted = strings.Replace(drifted, "x-deedglass-auth: public\n      x-deedglass-since: \"2026-10-01\"\n      parameters:\n        - $ref: \"#/components/parameters/DeedglassVersion\"\n      responses:\n        \"200\":\n          description: This server's",
		"x-deedglass-auth: session\n      x-deedglass-since: \"2026-10-01\"\n      parameters:\n        - $ref: \"#/components/parameters/DeedglassVersion\"\n      responses:\n        \"200\":\n          description: This server's", 1)
	drifted = strings.Replace(drifted, "                - service_unavailable\n", "", 1)
	drifted = strings.Replace(drifted, `version: "2026-10-01"`, `version: "2026-09-01"`, 1)
	drifted = strings.Replace(drifted, "paths:\n", "paths:\n  /v1/phantom:\n    get:\n      operationId: phantom\n      summary: x\n      tags: [System]\n      x-deedglass-auth: public\n      x-deedglass-since: \"2026-10-01\"\n      responses: {}\n", 1)

	errs := CheckSpec([]byte(drifted), routes)
	all := ""
	for _, e := range errs {
		all += e.Error() + "\n"
	}
	for _, want := range []string{
		`operationId "getVersionX" in the spec`,
		`x-deedglass-auth "session"`,
		"code enum differs",
		"info.version",
		"GET /v1/phantom is in api/openapi.yaml but not mounted",
		"GET /v1/phantom: the default response",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("drift not caught: %q\nerrors:\n%s", want, all)
		}
	}
	extra := append(routes, Route{Method: "GET", Pattern: "/v1/not-documented", OperationID: "notDocumented", Auth: AgentKey, Since: "2026-10-01"})
	if errs := CheckSpec(api.Spec, extra); len(errs) != 1 || !strings.Contains(errs[0].Error(), "mounted but not in api/openapi.yaml") {
		t.Errorf("an undocumented route: %v", errs)
	}
	if errs := CheckRequired(api.Spec, "Version", []byte(`{"object":"version"}`)); len(errs) != 6 {
		t.Errorf("CheckRequired found %d missing properties, want 6: %v", len(errs), errs)
	}
	if errs := CheckRequired(api.Spec, "Health", []byte(`{"object":"health","status":"ok","version":"x","api_version":"x","time":"x","checks":{"database":{},"migrations":{"status":"ok"},"seal_key":{"status":"ok"}}}`)); len(errs) != 2 {
		t.Errorf("nested required: %v", errs)
	}
}
