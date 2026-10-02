package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mcp/httpgw"
)

// rauthKeys is a stub key store: one agent key and one org admin key it knows; every other key,
// including a well-formed dg_test_ key it has never seen, is invalid_api_key (401).
type rauthKeys struct {
	org          ids.OrgID
	agent, admin string
}

func (k rauthKeys) AuthenticateKey(_ context.Context, bearer string) (httpx.Principal, error) {
	switch bearer {
	case k.agent:
		return httpx.Principal{Kind: httpx.KindAgentKey, OrgID: k.org, AgentID: ids.New[ids.AgentKind](), KeyID: ids.New[ids.APIKeyKind](),
			AgentName: "support-bot", AgentStatus: "active", OrgName: "Acme", Scopes: httpx.DefaultAgentScopes, APIVersion: "2026-10-01"}, nil
	case k.admin:
		return httpx.Principal{Kind: httpx.KindAdminKey, OrgID: k.org, KeyID: ids.New[ids.APIKeyKind](), Role: httpx.RoleAdmin,
			OrgName: "Acme", Scopes: []string{httpx.ScopeAdmin}, APIVersion: "2026-10-01"}, nil
	}
	return httpx.Principal{}, httpx.E(httpx.CodeInvalidAPIKey)
}

// rauthSessions accepts one known session cookie as an owner; every other request goes to the real
// identity.Service, which has no database here and so answers "You're not signed in" (401).
type rauthSessions struct {
	people       *identity.Service
	org          ids.OrgID
	name, cookie string
}

func (s rauthSessions) AuthenticateSession(ctx context.Context, r *http.Request) (httpx.Principal, error) {
	if c, err := r.Cookie(s.name); err == nil && c.Value == s.cookie {
		return httpx.Principal{Kind: httpx.KindUser, OrgID: s.org, UserID: ids.New[ids.UserKind](), Role: httpx.RoleOwner,
			OrgName: "Acme", APIVersion: "2026-10-01"}, nil
	}
	return s.people.AuthenticateSession(ctx, r)
}

// Every route serve mounts, swept with no credentials: each non-public route answers 401
// authentication_error with WWW-Authenticate, never 200, 404 or 500. Then a valid agent key, a
// valid admin key and an unknown key on every session-only route, and a valid session on every
// agent-key-only route: 401 or 403, never the handler (S18 §3.7).
func TestServeRoutesUnauthenticated(t *testing.T) {
	const issuer = "http://127.0.0.1:8080"
	newKey := func() string {
		k, _, err := identity.NewAPIKey(identity.ModeTest)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	org := ids.New[ids.OrgKind]()
	keys := rauthKeys{org: org, agent: newKey(), admin: newKey()}
	unknownKey := newKey()
	people := routeListingIdentity(issuer)
	cookieName, _ := people.CookieNames()
	sessions := rauthSessions{people: people, org: org, name: cookieName, cookie: "rauth-session-owner"}

	contract := map[string]httpx.ContractRoute{}
	for _, c := range httpx.ContractRoutes() {
		contract[c.Key()] = c
	}

	for _, cfg := range []struct {
		name  string
		opts  httpx.Options
		mcp   *mcpWiring
		cross bool
	}{
		// As the other route-table tests mount it: no authenticators, so every route fails closed.
		{name: "no authenticators", opts: httpx.Options{}},
		// As serve wires it: key and session authenticators, and the MCP gateway's guard.
		{name: "authenticators wired", opts: httpx.Options{Keys: keys, Sessions: sessions},
			mcp: &mcpWiring{gw: &httpgw.Gateway{}, timeout: time.Minute}, cross: true},
	} {
		t.Run(cfg.name, func(t *testing.T) {
			cfg.opts.Logger = slog.New(slog.DiscardHandler)
			api := httpx.New(cfg.opts)
			api.Mount(serveRoutes(api, nil, nil, nil, nil, nil, nil, nil, nil, cfg.mcp, nil, nil, nil)...)
			routes := api.Routes()
			mux := http.NewServeMux()
			for _, rt := range routes {
				mux.Handle(rt.Key(), http.NotFoundHandler())
			}
			do := func(rt httpx.Route, opt func(*http.Request)) (int, http.Header, []byte) {
				t.Helper()
				r := httptest.NewRequest(rt.Method, rauthPath(t, rt.Pattern), nil)
				if _, pat := mux.Handler(r); pat != rt.Key() {
					t.Fatalf("%s: the placeholder path %s routes to %q", rt.Key(), r.URL.Path, pat)
				}
				if opt != nil {
					opt(r)
				}
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				res := w.Result()
				b, _ := io.ReadAll(res.Body)
				return res.StatusCode, res.Header, b
			}

			public, swept := 0, 0
			for _, rt := range routes {
				if rt.Auth == httpx.Public {
					public++
					// A public route is public in the contract table (internal/httpx's
					// TestRouteAuthCoverage holds that allowed set and its reasons), or an ADR
					// extension listed in rauthPublicExtensions.
					c, inContract := contract[rt.Key()]
					_, allowed := rauthPublicExtensions[rt.Key()]
					if !(inContract && c.Auth == httpx.Public) && !(rt.Extension != "" && allowed) {
						t.Errorf("A new public route: %s. Add it to the allowed set with a reason, or give it auth.", rt.Key())
					}
					continue
				}
				if (rt.Auth == httpx.Session || rt.Auth == httpx.SessionOrKey) && rt.MinRole == "" {
					t.Errorf("%s: people can call it, but it declares no MinRole", rt.Key())
				}
				swept++
				status, h, body := do(rt, nil)
				if status != http.StatusUnauthorized {
					t.Errorf("%s with no credentials answered %d, want 401: %s", rt.Key(), status, body)
					continue
				}
				if typ := rauthErrType(body); typ != "authentication_error" {
					t.Errorf("%s with no credentials: error type %q, want authentication_error: %s", rt.Key(), typ, body)
				}
				if h.Get("WWW-Authenticate") == "" {
					t.Errorf("%s: a 401 without WWW-Authenticate", rt.Key())
				}
			}
			if swept < 60 {
				t.Fatalf("only %d non-public routes: the sweep isn't seeing serve's route table", swept)
			}
			t.Logf("routes total %d, public %d, swept %d", len(routes), public, swept)
			if !cfg.cross {
				return
			}

			bearer := func(k string) func(*http.Request) {
				return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+k) }
			}
			cookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: sessions.name, Value: sessions.cookie}) }
			sessionOnly, keyOnly := 0, 0
			for _, rt := range routes {
				var probes map[string]func(*http.Request)
				switch rt.Auth {
				case httpx.Session:
					sessionOnly++
					probes = map[string]func(*http.Request){"agent key": bearer(keys.agent), "admin key": bearer(keys.admin), "unknown key": bearer(unknownKey)}
				case httpx.AgentKey:
					keyOnly++
					probes = map[string]func(*http.Request){"session cookie": cookie}
				default:
					continue
				}
				for what, opt := range probes {
					status, _, body := do(rt, opt)
					if status != http.StatusUnauthorized && status != http.StatusForbidden {
						t.Errorf("%s with a %s answered %d, want 401 or 403: %s", rt.Key(), what, status, body)
					}
				}
			}
			if sessionOnly == 0 || keyOnly == 0 {
				t.Fatalf("session-only routes %d, agent-key-only routes %d: the cross-credential check saw nothing", sessionOnly, keyOnly)
			}
			t.Logf("cross-credential: session-only routes %d (3 keys each), agent-key-only routes %d (a session each)", sessionOnly, keyOnly)
		})
	}
}

// rauthPublicExtensions are the public routes serve mounts beyond contract §9 (Route.Extension
// names their ADR), each with the reason it needs no credentials.
var rauthPublicExtensions = map[string]string{
	"GET /auth/assets/{file}":    "ADR-028: the sign-in pages' stylesheet and script (auth.css, auth.js); static files, no org data",
	"GET /approve/assets/{file}": "ADR-048: the approval page's stylesheet and script (approve.css, approve.js); static files, no org data",
}

var rauthIDPrefix = map[string]string{
	"agents": ids.Agent, "keys": ids.APIKey, "members": ids.Membership, "policies": ids.Policy,
	"actions": ids.Action, "approvals": ids.Approval, "webhooks": ids.Webhook, "deeds": ids.Deed,
	"connectors": ids.Connector, "anomalies": ids.Anomaly, "replays": ids.Replay, "suggestions": ids.Suggestion,
}

// rauthPath fills a pattern's wildcards with syntactically valid placeholders, chosen by the
// collection segment before them (an agt_ id after /agents/, and so on).
func rauthPath(t *testing.T, pattern string) string {
	t.Helper()
	segs := strings.Split(pattern, "/")
	for i, seg := range segs {
		if !strings.HasPrefix(seg, "{") {
			continue
		}
		name, prev := strings.Trim(seg, "{}."), segs[i-1]
		var v string
		switch name {
		case "v":
			v = "1"
		case "kid":
			v = "ik_sweep_1"
		case "type":
			v = "postgres"
		case "connector":
			v = ids.New[ids.ConnectorKind]().String()
		case "id":
			if p, ok := rauthIDPrefix[prev]; ok {
				s, err := ids.NewString(p)
				if err != nil {
					t.Fatal(err)
				}
				v = s
			}
		}
		if v == "" {
			t.Fatalf("%s: no placeholder for {%s} after /%s/; add one to rauthPath", pattern, name, prev)
		}
		segs[i] = v
	}
	return strings.Join(segs, "/")
}

// rauthErrType is error.type of the envelope, or error.data.type of a JSON-RPC error body (the MCP
// gateway's error renderer).
func rauthErrType(body []byte) string {
	var e struct {
		Error struct {
			Type string `json:"type"`
			Data struct {
				Type string `json:"type"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	if e.Error.Type != "" {
		return e.Error.Type
	}
	return e.Error.Data.Type
}
