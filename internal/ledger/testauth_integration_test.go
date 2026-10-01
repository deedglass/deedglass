//go:build integration

package ledger_test

import (
	"context"
	"net/http"

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
	return httpx.Principal{Kind: "user", OrgID: org, UserID: ids.New[ids.UserKind](), Role: httpx.RoleViewer, OrgName: "Test"}, nil
}
