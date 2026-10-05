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

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

// roleSessions signs in one person per role by cookie value (S34's authorization matrix).
type roleSessions struct{ org ids.OrgID }

func (s roleSessions) AuthenticateSession(_ context.Context, r *http.Request) (httpx.Principal, error) {
	c, err := r.Cookie("authz-role")
	if err != nil {
		return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	role := httpx.Role(c.Value)
	return httpx.Principal{Kind: httpx.KindUser, OrgID: s.org, UserID: ids.New[ids.UserKind](), Role: role, OrgName: "Acme", APIVersion: "2026-10-01"}, nil
}

// TestAuthzMatrixServe (S34 review row 2): every route serve mounts that people can call, with a
// viewer, an admin and an owner session. A viewer never gets past the role check of a mutation
// outside httpx's viewerMutations list; an admin never gets past an owner-only route. "Past" means
// anything but 403 insufficient_role (the handlers have no dependencies here, so a route that let
// the role through would fail differently).
func TestAuthzMatrixServe(t *testing.T) {
	allowed := map[string]bool{"POST /v1/auth/logout": true, "POST /v1/auth/switch-org": true, "PATCH /v1/me": true,
		"POST /v1/policies/lint": true, "POST /v1/policies/evaluate": true, "POST /v1/policies/test": true,
		"POST /v1/approvals/{id}/approve": true, "POST /v1/approvals/{id}/deny": true}
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: roleSessions{org: ids.New[ids.OrgKind]()}})
	api.Mount(serveRoutes(api, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)...)
	checked := 0
	for _, rt := range api.Routes() {
		if rt.Auth != httpx.Session && rt.Auth != httpx.SessionOrKey {
			continue
		}
		mutating := rt.Method != http.MethodGet && rt.Method != http.MethodHead
		for _, role := range []httpx.Role{httpx.RoleViewer, httpx.RoleAdmin, httpx.RoleOwner} {
			mustRefuse := false
			switch {
			case role == httpx.RoleViewer && mutating && !allowed[rt.Key()]:
				mustRefuse = true
			case role == httpx.RoleViewer && rt.MinRole != httpx.RoleViewer:
				mustRefuse = true
			case role == httpx.RoleAdmin && rt.MinRole == httpx.RoleOwner:
				mustRefuse = true
			}
			if !mustRefuse {
				continue
			}
			checked++
			var body io.Reader
			if mutating && rt.MaxBody != httpx.NoBody {
				body = strings.NewReader(`{}`)
			}
			req := httptest.NewRequest(rt.Method, rauthPath(t, rt.Pattern), body)
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			req.AddCookie(&http.Cookie{Name: "authz-role", Value: string(role)})
			w := httptest.NewRecorder()
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("%s as %s reached the handler (panic: %v)", rt.Key(), role, p)
					}
				}()
				api.ServeHTTP(w, req)
			}()
			out, _ := io.ReadAll(w.Result().Body)
			var e struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(out, &e)
			if w.Code != http.StatusForbidden || e.Error.Code != "insufficient_role" {
				t.Errorf("%s as %s: %d %s, want 403 insufficient_role", rt.Key(), role, w.Code, e.Error.Code)
			}
		}
	}
	if checked < 40 {
		t.Fatalf("only %d refusals checked: the matrix isn't seeing serve's routes", checked)
	}
	t.Logf("%d route × role refusals checked", checked)
}
