//go:build integration

package ledger_test

import (
	"context"
	"net/http"
	"strings"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

// testOrgHeader names the org of the test principal: a viewer of that org, as a session would be.
const testOrgHeader = "Test-Org"

// testSessions stands in for identity's session authenticator in the Ledger's HTTP tests.
type testSessions struct{}

func (testSessions) AuthenticateSession(_ context.Context, r *http.Request) (httpx.Principal, error) {
	org, err := ids.Parse[ids.OrgKind](r.Header.Get(testOrgHeader))
	if err != nil {
		return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	role := httpx.RoleViewer
	if r.Header.Get(testRoleHeader) == "admin" {
		role = httpx.RoleAdmin
	}
	return httpx.Principal{Kind: "user", OrgID: org, UserID: ids.New[ids.UserKind](), Role: role, OrgName: "Test"}, nil
}

// testRoleHeader raises the test session to admin (S38's POST /v1/deeds/files).
const testRoleHeader = "Test-Role"

// testKeys stands in for identity's key authenticator: "admin-<org>" is an org admin key and
// "agent-<org>" an agent key with actions:read and deeds:read.
type testKeys struct{}

func (testKeys) AuthenticateKey(_ context.Context, bearer string) (httpx.Principal, error) {
	kind, orgID, _ := strings.Cut(bearer, "-")
	org, err := ids.Parse[ids.OrgKind](orgID)
	if err != nil {
		return httpx.Principal{}, httpx.E(httpx.CodeInvalidAPIKey)
	}
	switch kind {
	case "admin":
		return httpx.Principal{Kind: httpx.KindAdminKey, OrgID: org, KeyID: ids.New[ids.APIKeyKind](), Role: httpx.RoleAdmin,
			Scopes: []string{httpx.ScopeAdmin}, OrgName: "Test"}, nil
	case "agent":
		return httpx.Principal{Kind: httpx.KindAgentKey, OrgID: org, KeyID: ids.New[ids.APIKeyKind](), AgentID: ids.New[ids.AgentKind](),
			AgentStatus: "active", Scopes: []string{httpx.ScopeActionsRead, httpx.ScopeDeedsRead}, OrgName: "Test"}, nil
	}
	return httpx.Principal{}, httpx.E(httpx.CodeInvalidAPIKey)
}
