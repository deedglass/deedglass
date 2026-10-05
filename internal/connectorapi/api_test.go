package connectorapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/rest"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

type who struct{ p httpx.Principal }

func (w who) AuthenticateSession(context.Context, *http.Request) (httpx.Principal, error) {
	if w.p.Kind != httpx.KindUser {
		return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	return w.p, nil
}

func (w who) AuthenticateKey(context.Context, string) (httpx.Principal, error) {
	if w.p.Kind == httpx.KindUser || w.p.Kind == "" {
		return httpx.Principal{}, httpx.E(httpx.CodeInvalidAPIKey)
	}
	return w.p, nil
}

func principals() map[string]httpx.Principal {
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

// TestRouteRoleMatrix (S16 §6): viewers read, admins change, an org admin key acts as admin, and
// an agent key is refused everywhere, reads included, with S16's message.
func TestRouteRoleMatrix(t *testing.T) {
	routes := Routes(&API{Registry: connector.New(connector.Options{Types: []connector.Connector{rest.New()}}), Log: slog.New(slog.DiscardHandler)})
	if len(routes) != 13 {
		t.Fatalf("%d routes, want 13 (S16's nine, S17's setup record, S20's two decoder routes, S30's connector type)", len(routes))
	}
	for name, p := range principals() {
		srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: who{p}, Keys: who{p}})
		srv.Mount(routes...)
		ts := httptest.NewServer(srv)
		for _, rt := range routes {
			path := strings.NewReplacer("{id}", "orders").Replace(rt.Pattern)
			var body io.Reader
			switch rt.Method {
			case "POST", "PATCH", "PUT":
				body = strings.NewReader(`{"type":"rest","name":"orders","config":{},"kind":"bearer","secret":{"token":"x"},"description":"x"}`)
			}
			req, _ := http.NewRequest(rt.Method, ts.URL+path, body)
			req.Header.Set("Content-Type", "application/json")
			if p.Kind != httpx.KindUser && p.Kind != "" {
				req.Header.Set("Authorization", "Bearer dg_test_AAAAAAAA_"+strings.Repeat("B", 32))
			}
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			key := name + " · " + rt.Key()
			write := rt.MinRole == httpx.RoleAdmin
			switch name {
			case "none":
				if r.StatusCode != 401 {
					t.Errorf("%s: %d, want 401", key, r.StatusCode)
				}
			case "agent key":
				if r.StatusCode != 403 || !strings.Contains(string(b), "insufficient_scope") || !strings.Contains(string(b), AgentKeyMessage) {
					t.Errorf("%s: %d %s", key, r.StatusCode, b)
				}
			case "viewer":
				if write && (r.StatusCode != 403 || !strings.Contains(string(b), "insufficient_role")) {
					t.Errorf("%s: %d %s, want 403 insufficient_role", key, r.StatusCode, b)
				}
				if !write && (r.StatusCode == 401 || r.StatusCode == 403) {
					t.Errorf("%s: %d %s, want allowed", key, r.StatusCode, b)
				}
			default:
				if r.StatusCode == 401 || r.StatusCode == 403 {
					t.Errorf("%s: %d %s, want allowed", key, r.StatusCode, b)
				}
			}
		}
		ts.Close()
	}
}

func TestNames(t *testing.T) {
	reg := connector.New(connector.Options{Env: connector.Env{Name: "prod"}})
	for _, bad := range []string{"", "a", "Orders", "orders_db", "-x", "deedglass", "mcp", "native", "fake", strings.Repeat("a", 33)} {
		if checkName(bad, reg) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"orders", "orders-v2", "ab"} {
		if err := checkName(ok, reg); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	dev := connector.New(connector.Options{Env: connector.Env{Name: "dev"}})
	if checkName("fake", dev) != nil {
		t.Error("fake is a free name in dev without the built-in")
	}
}

// TestDescriptionRefusesCredentials (S34, SR-013; attack case Q07): a description that carries a
// credential the vault's scrubber recognizes is refused, and the message never echoes it.
func TestDescriptionRefusesCredentials(t *testing.T) {
	for _, d := range []string{"sk_live_" + strings.Repeat("a", 24), "token xoxb-1234567890-abcdefghij", "rk_test_" + strings.Repeat("b", 24) + " for refunds"} {
		_, err := checkDescription(d)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || he.Param != "description" || strings.Contains(he.Message, d[:8]) {
			t.Fatalf("%q: %v", d, err)
		}
	}
	for _, d := range []string{"", "Refunds under $20 for the support team.", "Reads sk_ prefixes? No: plain words."} {
		if _, err := checkDescription(d); err != nil {
			t.Fatalf("%q: %v", d, err)
		}
	}
}
