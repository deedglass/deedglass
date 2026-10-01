package httpx

import (
	"net/http"
	"strings"
	"testing"
)

func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		p := recover()
		if p == nil {
			t.Fatalf("no panic, want one containing %q", want)
		}
		if s, _ := p.(string); !strings.Contains(s, want) {
			t.Fatalf("panic %v, want it to contain %q", p, want)
		}
	}()
	f()
}

func ok(http.ResponseWriter, *http.Request) error { return nil }

func TestMountRefusesBadRoutes(t *testing.T) {
	base := Route{Method: "GET", Pattern: "/v1/health", OperationID: "getHealth", Auth: Public, Handler: ok}
	cases := []struct {
		name string
		mod  func(*Route)
		want string
	}{
		{"zero auth", func(r *Route) { r.Auth = 0 }, "Auth is not declared"},
		{"POST without idempotency", func(r *Route) { r.Method, r.Pattern, r.Auth = "POST", "/v1/agents", SessionOrKey }, "must declare Idempotency"},
		{"not in catalog", func(r *Route) { r.Pattern = "/v1/nope" }, "isn't in the contract catalog"},
		{"auth differs from catalog", func(r *Route) { r.Auth = SessionOrKey }, "the contract catalog says public"},
		{"no handler", func(r *Route) { r.Handler = nil }, "Handler is nil"},
		{"no operation id", func(r *Route) { r.OperationID = "" }, "OperationID is empty"},
		{"bad method", func(r *Route) { r.Method = "TRACE" }, "method must be one of"},
		{"bad pattern", func(r *Route) { r.Pattern = "v1/health" }, "pattern must be a path"},
		{"GET with idempotency", func(r *Route) { r.Idempotency = IdemOptional }, "only POST routes"},
		{"public required", func(r *Route) {
			r.Method, r.Pattern, r.Idempotency = "POST", "/v1/auth/login", IdemRequired
		}, "no principal"},
		{"sensitive never", func(r *Route) {
			r.Method, r.Pattern, r.Auth, r.Idempotency, r.Sensitive = "POST", "/v1/agents/{id}/keys", SessionOrKey, IdemNever, true
		}, "Sensitive route"},
		{"bad role", func(r *Route) { r.MinRole = "root" }, "MinRole"},
		{"bad extension", func(r *Route) { r.Pattern, r.Extension = "/v1/nope", "because" }, "Extension must name an ADR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := base
			c.mod(&r)
			mustPanic(t, c.want, func() { New(Options{}).Mount(r) })
		})
	}
	t.Run("twice", func(t *testing.T) {
		s := New(Options{})
		s.Mount(base)
		mustPanic(t, "mounted twice", func() { s.Mount(base) })
	})
	t.Run("defaults", func(t *testing.T) {
		s := New(Options{})
		s.Mount(Route{Method: "POST", Pattern: "/v1/agents/{id}/keys", OperationID: "x", Auth: SessionOrKey, MinRole: RoleAdmin, Idempotency: IdemRequired, Sensitive: true, Handler: ok})
		r := s.Routes()[0]
		if r.Timeout != DefaultTimeout || r.MaxBody != DefaultMaxBody || r.BodyType != BodyJSON || r.Since != "2026-10-01" ||
			len(r.Redact) != 1 || r.Redact[0] != "secret" {
			t.Fatalf("defaults: %+v", r)
		}
		s.Mount(Route{Method: "GET", Pattern: "/v1/version", OperationID: "y", Auth: Public, Handler: ok})
		if s.Routes()[1].MaxBody != NoBody {
			t.Fatal("a GET takes no body by default")
		}
	})
}

// TestRoutesGolden pins the contract catalog and what this build mounts.
func TestRoutesGolden(t *testing.T) {
	s := New(Options{})
	s.Mount(s.SystemRoutes(SystemDeps{})...)
	golden(t, "routes.golden", []byte(RoutesGolden(s.Routes())))
}

func TestContractCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range ContractRoutes() {
		if c.Auth == 0 {
			t.Errorf("%s: no auth", c.Key())
		}
		if !strings.HasPrefix(c.Owner, "S") {
			t.Errorf("%s: owner %q", c.Key(), c.Owner)
		}
		seen[c.Key()] = true
	}
	// Spot checks of the decisions S07–S29 make (S03 §3.2).
	for key, want := range map[string]Auth{
		"GET /v1/health":                       Public,
		"GET /.well-known/deedglass/keys.json": Public,
		"POST /v1/auth/login":                  Public,
		"POST /v1/approval-links/decide":       Public,
		"POST /v1/slack/interact":              Public,
		"POST /v1/org/start-all":               Session,
		"POST /v1/approvals/{id}/approve":      Session,
		"GET /v1/org/keys":                     Session,
		"GET /v1/whoami":                       AgentKey,
		"POST /v1/actions":                     AgentKey,
		"POST /mcp/{connector}":                AgentKey,
		"POST /v1/org/stop-all":                SessionOrKey,
		"GET /v1/deeds":                        SessionOrKey,
		"POST /v1/agents/{id}/keys":            SessionOrKey,
		"GET /v1/events":                       SessionOrKey,
	} {
		c, ok := contractByKey[key]
		if !ok || c.Auth != want {
			t.Errorf("%s: auth %v, want %v", key, c.Auth, want)
		}
	}
	// Every pattern must be a valid Go 1.22 mux pattern and must not conflict with the others.
	mux := http.NewServeMux()
	for _, c := range ContractRoutes() {
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("%s: %v", c.Key(), p)
				}
			}()
			mux.Handle(c.Key(), http.NotFoundHandler())
		}()
	}
}

func TestAuthNames(t *testing.T) {
	for _, a := range []Auth{Public, Session, AgentKey, SessionOrKey} {
		if b, ok := ParseAuth(a.String()); !ok || b != a {
			t.Errorf("%v round trip", a)
		}
	}
	if _, ok := ParseAuth("invalid"); ok {
		t.Error("invalid must not parse")
	}
}
