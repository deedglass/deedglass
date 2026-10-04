package learn_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
)

// roleAuth authenticates every request as one fixed principal: a session for a person, a key
// for an API key, and nothing for "none".
type roleAuth struct{ p httpx.Principal }

func (a roleAuth) AuthenticateSession(context.Context, *http.Request) (httpx.Principal, error) {
	if a.p.Kind != httpx.KindUser {
		return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	return a.p, nil
}

func (a roleAuth) AuthenticateKey(context.Context, string) (httpx.Principal, error) {
	if a.p.Kind == httpx.KindUser || a.p.Kind == "" {
		return httpx.Principal{}, httpx.E(httpx.CodeInvalidAPIKey)
	}
	return a.p, nil
}

func rolePrincipals() map[string]httpx.Principal {
	org := ids.New[ids.OrgKind]()
	user := func(r httpx.Role) httpx.Principal {
		return httpx.Principal{Kind: httpx.KindUser, OrgID: org, UserID: ids.New[ids.UserKind](), Role: r, OrgName: "Acme"}
	}
	return map[string]httpx.Principal{
		"owner":     user(httpx.RoleOwner),
		"admin":     user(httpx.RoleAdmin),
		"viewer":    user(httpx.RoleViewer),
		"admin key": {Kind: httpx.KindAdminKey, OrgID: org, KeyID: ids.New[ids.APIKeyKind](), Role: httpx.RoleAdmin, Scopes: []string{httpx.ScopeAdmin}},
		"agent key": {Kind: httpx.KindAgentKey, OrgID: org, KeyID: ids.New[ids.APIKeyKind](), AgentID: ids.New[ids.AgentKind](),
			Scopes: []string{"actions:execute", "actions:read", "deeds:read"}, AgentStatus: "active", AgentName: "support-bot"},
		"none": {},
	}
}

// The route × role matrix (S27 §3.9) with no database: reads are viewer+, changes are admin+, an
// org admin key acts as admin, an agent key is refused everywhere (reads included) with Insights'
// own message, and no credentials is 401. With a nil *learn.Service every handler answers 503
// service_unavailable, so a 503 means the request passed authentication and the role check.
func TestRouteRoleMatrix(t *testing.T) {
	routes := learn.Routes(nil)
	if len(routes) != 12 {
		t.Fatalf("%d routes, want 12", len(routes))
	}
	bodies := map[string]string{
		"createReplay":           `{"from":"2026-09-26T00:00:00Z","to":"2026-10-04T00:00:00Z"}`,
		"resolveAnomaly":         `{"reason":"The merge was the planned release."}`,
		"updateInsightsSettings": `{"anomaly_facts":true,"reason":"Turning anomaly facts on."}`,
	}
	ps := rolePrincipals()
	for _, name := range []string{"owner", "admin", "viewer", "admin key", "agent key", "none"} {
		p := ps[name]
		srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: roleAuth{p}, Keys: roleAuth{p}})
		srv.Mount(routes...)
		ts := httptest.NewServer(srv)
		for _, rt := range routes {
			path := strings.NewReplacer("{id}", "x", "{v}", "3").Replace(rt.Pattern)
			switch {
			case strings.HasPrefix(rt.Pattern, "/v1/policies/"):
				path = strings.Replace(rt.Pattern, "{id}", "acme-sim", 1)
				path = strings.Replace(path, "{v}", "3", 1)
			case strings.HasPrefix(rt.Pattern, "/v1/replays/"):
				path = strings.Replace(rt.Pattern, "{id}", "rpl_01m42vjp4z65td5hkdkcefczms", 1)
			case strings.HasPrefix(rt.Pattern, "/v1/insights/anomalies/"):
				path = strings.Replace(rt.Pattern, "{id}", "anm_01m42vb270xzbzmgbgyantw49w", 1)
			}
			var body io.Reader
			if b, ok := bodies[rt.OperationID]; ok {
				body = strings.NewReader(b)
			}
			req, err := http.NewRequest(rt.Method, ts.URL+path, body)
			if err != nil {
				t.Fatal(err)
			}
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			if p.Kind != httpx.KindUser && p.Kind != "" {
				req.Header.Set("Authorization", "Bearer dg_test_AAAAAAAA_"+strings.Repeat("B", 32))
			}
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			got := string(b)
			key := name + " · " + rt.Method + " " + rt.Pattern
			write := rt.Method != "GET"
			if write != (rt.MinRole == httpx.RoleAdmin) {
				t.Errorf("%s: MinRole %s, want admin for writes and viewer for reads", key, rt.MinRole)
			}
			passed := r.StatusCode == http.StatusServiceUnavailable && strings.Contains(got, `"service_unavailable"`)
			switch {
			case name == "none":
				if r.StatusCode != http.StatusUnauthorized || !strings.Contains(got, `"authentication_required"`) {
					t.Errorf("%s: %d %s, want 401 authentication_required", key, r.StatusCode, got)
				}
			case name == "agent key":
				if r.StatusCode != http.StatusForbidden || !strings.Contains(got, `"insufficient_scope"`) || !strings.Contains(got, learn.MsgAgentKey) {
					t.Errorf("%s: %d %s, want 403 insufficient_scope with %q", key, r.StatusCode, got, learn.MsgAgentKey)
				}
			case name == "viewer" && write:
				if r.StatusCode != http.StatusForbidden || !strings.Contains(got, `"insufficient_role"`) {
					t.Errorf("%s: %d %s, want 403 insufficient_role", key, r.StatusCode, got)
				}
			default: // owner, admin, admin key, and viewer reads
				if !passed {
					t.Errorf("%s: %d %s, want 503 service_unavailable (past auth)", key, r.StatusCode, got)
				}
			}
		}
		ts.Close()
	}
}
