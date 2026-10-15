package httpx

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/ids"
)

// S18 §3.7 names five auth kinds: public | agent_key | session | admin_key | signed. The route
// table has four (routes.go), and maps them like this:
//
//   - public    → Public: no credentials at all.
//   - signed    → Public plus a signature the handler checks (POST /v1/slack/interact, Slack's
//     signing secret; contract note "Slack signature"). The pipeline sees no principal, so the
//     route sits in the public set below with its reason.
//   - agent_key → AgentKey: any API key; the route's Scopes decide which keys pass.
//   - admin_key → not a kind of its own. An org admin key is an API key holding the admin scope,
//     acting as the admin role and never as owner (keyAllowed), so it reaches AgentKey and
//     SessionOrKey routes whose Scopes or MinRole allow an admin.
//   - session   → Session: a Console session only; any API key is refused (insufficient_role).
//
// SessionOrKey is the fifth row the table needs: a Console session, or an API key.

// publicRoutes is the exact set of routes that answer without credentials, each with the reason it
// may. A public route missing here fails TestRouteAuthCoverage; so does a row here that the
// contract table no longer marks public.
var publicRoutes = map[string]string{
	"GET /weekly/off":  "the page a Monday email links to (S52): shows one button and changes nothing on load",
	"POST /weekly/off": "its button (S52): a signed single-purpose token turns that one person's weekly email off, nothing else",
	// S18 §3.7's list.
	"GET /v1/health":                            "liveness and readiness for load balancers; no org data",
	"GET /v1/version":                           "build and API versions, for clients checking compatibility",
	"GET /api/openapi.yaml":                     "the API description, the same document the repo ships",
	"GET /.well-known/deedglass/keys.json":      "public seal keys, so anyone can verify a deed offline",
	"GET /.well-known/oauth-protected-resource": "RFC 9728 metadata an MCP client reads before it holds a key",
	"POST /v1/auth/login":                       "starts sign-in by email; the same answer for known and unknown addresses",
	"POST /v1/auth/callback":                    "trades a one-time sign-in token for a session; the token is the credential",
	"POST /v1/slack/interact":                   "signed: Slack's request signature, checked by the handler (S29), form body",
	// RFC 9728 path-inserted variants of the metadata document (contract §9, S09).
	"GET /.well-known/oauth-protected-resource/mcp/{connector}": "RFC 9728 metadata for one MCP endpoint; never reads the database",
	"GET /.well-known/oauth-protected-resource/mcp/native":      "RFC 9728 metadata for /mcp/native; never reads the database",
	// The rest of S07's sign-in flow (contract §9).
	"POST /v1/auth/preview": "shows what a one-time sign-in link will do before it is spent; the token is the credential",
	"GET /login":            "the sign-in page",
	"GET /auth/confirm":     "the page a sign-in link opens; it posts the token to /v1/auth/callback",
	// S22's approval links (contract §9, planned). Not in S18 §3.7's list: the contract made them
	// public because a one-time approval token in the body is the credential.
	"POST /v1/approval-links/preview": "S22: one-time approval token in the body is the credential",
	"POST /v1/approval-links/decide":  "S22: one-time approval token in the body is the credential",
	"POST /v1/approval-links/status":  "S22: one-time approval token in the body is the credential",
	"GET /approve":                    "S22: the page an approval link opens; it posts the token to /v1/approval-links/*",
	// S24's Console pages: static HTML with no data; the handler checks the session itself and
	// redirects to /login?next= without one (a page is never served signed out).
	"GET /app":           "S24: the Console's pages hold no data; without a session the handler redirects to /login",
	"GET /app/{path...}": "S24: the Console's pages and assets hold no data; without a session pages redirect to /login",
}

// sweepCookie is the session cookie sweepSessions accepts.
const (
	sweepCookieName  = "deedglass_session"
	sweepCookieValue = "sweep-session-owner"
)

// sweepSessions is a fake session authenticator: one known cookie is an owner of testOrg; anything
// else, including no cookie, is authentication_required, as identity.Service answers.
type sweepSessions struct{}

func (sweepSessions) AuthenticateSession(_ context.Context, r *http.Request) (Principal, error) {
	if c, err := r.Cookie(sweepCookieName); err == nil && c.Value == sweepCookieValue {
		return Principal{Kind: KindUser, OrgID: testOrg, Role: RoleOwner, OrgName: "Acme", APIVersion: "2026-10-01"}, nil
	}
	return Principal{}, E(CodeAuthenticationRequired, Msg("You're not signed in. Sign in at http://127.0.0.1:8080/login."))
}

func TestRouteAuthCoverage(t *testing.T) {
	rows := ContractRoutes()

	// 1. Every row declares an auth kind, and every route people can call a minimum role (or, for
	// a SessionOrKey row whose access is a key scope, the scope the mounted route turns into its
	// MinRole and Scopes).
	public := map[string]bool{}
	for _, c := range rows {
		switch c.Auth {
		case Public:
			public[c.Key()] = true
			if c.Access != "" {
				t.Errorf("%s is public but declares access %q", c.Key(), c.Access)
			}
		case Session:
			if Role(c.Access).Rank() == 0 {
				t.Errorf("%s is a session route without a minimum role (access %q)", c.Key(), c.Access)
			}
		case SessionOrKey:
			if c.Access == "" {
				t.Errorf("%s takes a session or a key but declares no minimum role or scope", c.Key())
			}
		case AgentKey:
		default:
			t.Errorf("%s declares no auth kind (%v)", c.Key(), c.Auth)
		}
	}

	// 2. The public set is exactly publicRoutes.
	var added []string
	for k := range public {
		if _, ok := publicRoutes[k]; !ok {
			added = append(added, k)
		}
	}
	sort.Strings(added)
	for _, k := range added {
		t.Errorf("A new public route: %s. Add it to the allowed set with a reason, or give it auth.", k)
	}
	for k, why := range publicRoutes {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s is in the allowed public set without a reason", k)
		}
		if !public[k] {
			t.Errorf("%s is in the allowed public set, but the contract table no longer has it as public; remove it", k)
		}
	}

	// 3. Mount every row with a stub handler and sweep the non-public ones with no credentials:
	// first with no authenticators (fail closed), then with fake key and session authenticators.
	for _, cfg := range []struct {
		name string
		o    Options
	}{
		{"no authenticators", Options{}},
		{"authenticators wired", Options{Keys: fakeKeys{}, Sessions: sweepSessions{}}},
	} {
		t.Run(cfg.name, func(t *testing.T) {
			cfg.o.Logger = slog.New(slog.DiscardHandler)
			s := New(cfg.o)
			s.Mount(sweepRoutes(rows)...)
			swept := 0
			for _, c := range rows {
				if c.Auth == Public {
					continue
				}
				swept++
				res := sweepDo(t, s, c, nil)
				if res.status != http.StatusUnauthorized {
					t.Errorf("%s with no credentials answered %d, want 401: %s", c.Key(), res.status, res.body)
					continue
				}
				if typ := sweepErrType(res.body); typ != string(TypeAuthentication) {
					t.Errorf("%s with no credentials: error.type %q, want authentication_error: %s", c.Key(), typ, res.body)
				}
				if res.header.Get("WWW-Authenticate") == "" {
					t.Errorf("%s: a 401 without WWW-Authenticate", c.Key())
				}
			}
			if cfg.o.Keys == nil {
				t.Logf("routes total %d, public %d, swept %d", len(rows), len(public), swept)
				return
			}
			// 4. A valid agent key on a session-only route, and a valid session on an agent-key-only
			// route: 401 or 403, never the handler.
			cross := 0
			for _, c := range rows {
				var opt func(*http.Request)
				switch c.Auth {
				case Session:
					opt = func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+goodKey) }
				case AgentKey:
					opt = func(r *http.Request) { r.AddCookie(&http.Cookie{Name: sweepCookieName, Value: sweepCookieValue}) }
				default:
					continue
				}
				cross++
				res := sweepDo(t, s, c, opt)
				if res.status != http.StatusUnauthorized && res.status != http.StatusForbidden {
					t.Errorf("%s with the other kind of credential answered %d, want 401 or 403: %s", c.Key(), res.status, res.body)
				}
			}
			if cross == 0 {
				t.Fatal("no session-only or agent-key-only routes: the cross-credential check saw nothing")
			}
		})
	}
}

// sweepRoutes turns contract rows into mountable routes whose handler fails the sweep if reached.
func sweepRoutes(rows []ContractRoute) []Route {
	out := make([]Route, 0, len(rows))
	for i, c := range rows {
		rt := Route{Method: c.Method, Pattern: c.Pattern, Auth: c.Auth, OperationID: "sweep" + strconv.Itoa(i),
			Handler: func(w http.ResponseWriter, r *http.Request) error {
				return WriteJSON(w, http.StatusOK, map[string]string{"object": "reached", "route": c.Key()})
			}}
		switch {
		case Role(c.Access).Rank() > 0:
			rt.MinRole = Role(c.Access)
			rt.Scopes = []string{ScopeAdmin}
		case c.Access != "":
			rt.Scopes = []string{c.Access}
		}
		if rt.MinRole == "" && (c.Auth == Session || c.Auth == SessionOrKey) {
			rt.MinRole = RoleViewer
		}
		if c.Method == http.MethodPost {
			rt.Idempotency = IdemOptional
		}
		if strings.Contains(c.Note, "form body") {
			rt.BodyType = BodyForm
		}
		out = append(out, rt)
	}
	return out
}

type sweepResp struct {
	status int
	header http.Header
	body   []byte
}

// sweepDo sends c's method to its pattern with placeholder ids, no body and no credentials (plus
// opt), after checking the mux routes that path to c and nothing else.
func sweepDo(t *testing.T, s *Server, c ContractRoute, opt func(*http.Request)) sweepResp {
	t.Helper()
	r := httptest.NewRequest(c.Method, sweepPath(t, c.Pattern), nil)
	if _, pat := s.mux.Handler(r); pat != c.Key() {
		t.Fatalf("%s: the placeholder path %s routes to %q", c.Key(), r.URL.Path, pat)
	}
	if opt != nil {
		opt(r)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	res := w.Result()
	b, _ := io.ReadAll(res.Body)
	return sweepResp{status: res.StatusCode, header: res.Header, body: b}
}

// sweepPath fills a pattern's wildcards with syntactically valid placeholders, chosen by the
// collection segment before them (an agt_ id after /agents/, and so on).
func sweepPath(t *testing.T, pattern string) string {
	t.Helper()
	segs := strings.Split(pattern, "/")
	for i, seg := range segs {
		if !strings.HasPrefix(seg, "{") {
			continue
		}
		name, prev := strings.Trim(seg, "{}."), ""
		if i > 0 {
			prev = segs[i-1]
		}
		segs[i] = sweepPlaceholder(t, pattern, name, prev)
	}
	return strings.Join(segs, "/")
}

var sweepIDPrefix = map[string]string{
	"agents": ids.Agent, "keys": ids.APIKey, "members": ids.Membership, "policies": ids.Policy,
	"actions": ids.Action, "approvals": ids.Approval, "webhooks": ids.Webhook, "deeds": ids.Deed,
	"connectors": ids.Connector, "anomalies": ids.Anomaly, "replays": ids.Replay, "suggestions": ids.Suggestion,
	"sessions": ids.ObserveSession,
}

func sweepPlaceholder(t *testing.T, pattern, name, prev string) string {
	t.Helper()
	newID := func(prefix string) string {
		s, err := ids.NewString(prefix)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	switch name {
	case "v":
		return "1"
	case "kid":
		return "ik_sweep_1"
	case "type":
		return "postgres"
	case "connector":
		return newID(ids.Connector)
	case "dtx":
		return newID(ids.DecodedTx)
	case "id":
		if p, ok := sweepIDPrefix[prev]; ok {
			return newID(p)
		}
	}
	t.Fatalf("%s: no placeholder for {%s} after /%s/; add one to sweepPlaceholder", pattern, name, prev)
	return ""
}

// sweepErrType is error.type of the envelope, or error.data.type of a JSON-RPC error body (the MCP
// gateway's ErrorRenderer).
func sweepErrType(body []byte) string {
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
