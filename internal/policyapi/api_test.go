package policyapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// noDB is a policy.Repo with no database: every transaction fails, so an allowed request reaches
// the handler and ends in 500, never in 401 or 403.
type noDB struct{}

var errNoDB = errors.New("no database in this test")

func (noDB) InOrgTx(context.Context, string, func(policy.Tx) error) error     { return errNoDB }
func (noDB) InOrgReadTx(context.Context, string, func(policy.Tx) error) error { return errNoDB }
func (noDB) LoadActive(context.Context, string) (*policy.ActiveRows, error)   { return nil, errNoDB }
func (noDB) LiveActivationIDs(context.Context, string) ([]string, error)      { return nil, errNoDB }

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

// The route × role matrix (S12 §6): readers and the dry-run tools are viewer+, changes are admin+,
// an org admin key acts as admin, an agent key is refused everywhere (reads included) with S12's
// message, and no credentials is 401.
func TestRouteRoleMatrix(t *testing.T) {
	routes := Routes(policy.NewService(noDB{}), Options{})
	if len(routes) != 12 {
		t.Fatalf("%d routes, want 12", len(routes))
	}
	for name, p := range principals() {
		srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: who{p}, Keys: who{p}})
		srv.Mount(routes...)
		ts := httptest.NewServer(srv)
		for _, rt := range routes {
			path := strings.NewReplacer("{id}", "postgres-safe", "{v}", "1").Replace(rt.Pattern)
			var body io.Reader
			if rt.Method == "POST" {
				body = strings.NewReader(`{"source":"deedglass: policy/v1\nname: x\ndefaults:\n  decision: deny\n  reason: No.\n","mode":"enforce","reason":"Testing it.","input":{"tool":"a.b"},"cases":[]}`)
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
			default: // owner, admin, admin key
				if r.StatusCode == 401 || r.StatusCode == 403 {
					t.Errorf("%s: %d %s, want allowed", key, r.StatusCode, b)
				}
			}
		}
		ts.Close()
	}
}

// Lint answers 200 even for an invalid policy, and the tools are limited to 60 a minute per person.
func TestLintAndLimit(t *testing.T) {
	p := principals()["viewer"]
	srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: who{p}, Keys: who{p}})
	srv.Mount(Routes(policy.NewService(noDB{}), Options{})...)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	post := func(body string) (int, string) {
		r, err := http.Post(ts.URL+"/v1/policies/lint", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	code, b := post(`{"source":"deedglass: policy/v1\nname: demo\nrules:\n  - id: a\n    desicion: deny\n"}`)
	if code != 200 || !strings.Contains(b, `"valid": false`) || !strings.Contains(b, `"object": "policy_lint_result"`) {
		t.Fatalf("%d %s", code, b)
	}
	for i := 1; i < ToolsPerMinute; i++ {
		if code, b := post(`{"source":"x"}`); code != 200 {
			t.Fatalf("request %d: %d %s", i+1, code, b)
		}
	}
	code, b = post(`{"source":"x"}`)
	if code != 429 || !strings.Contains(b, "rate_limited") {
		t.Fatalf("61st: %d %s", code, b)
	}
}

func TestErrorMapping(t *testing.T) {
	d := []policy.Diagnostic{{Severity: policy.SevError, Code: "unknown_key", Message: "Unknown key.", Line: 5}}
	for _, c := range []struct {
		in   error
		code httpx.Code
	}{
		{&policy.Error{Kind: policy.KindInvalid, Param: "source", Message: "The policy has 1 error.", Diagnostics: d}, httpx.CodePolicyInvalid},
		{&policy.Error{Kind: policy.KindValidation, Param: "reason", Message: "A reason is required."}, httpx.CodeValidationFailed},
		{&policy.Error{Kind: policy.KindConflict, Message: "Already enforced."}, httpx.CodeConflict},
		{&policy.Error{Kind: policy.KindNotFound, Message: "There's no policy x."}, httpx.CodeNotFound},
	} {
		e := httpx.AsError(apiErr(c.in))
		if e == nil || e.Code != c.code {
			t.Fatalf("%v → %v", c.in, e)
		}
		if c.code == httpx.CodePolicyInvalid {
			ds, ok := e.Diagnostics.([]diagnostic)
			if !ok || len(ds) != 1 || ds[0].Object != "policy_diagnostic" || e.Param != "source" {
				t.Fatalf("%+v", e.Diagnostics)
			}
		}
	}
	if err := apiErr(errNoDB); !errors.Is(err, errNoDB) {
		t.Fatal("other errors pass through to the pipeline (internal_error, logged)")
	}
}
