package httpx

import (
	"net/http"
	"testing"
)

// viewerMutations are the only mutating routes a viewer may call, each with the reason. Every
// other POST, PUT, PATCH or DELETE that people can call needs admin or owner (S34 review row 2).
var viewerMutations = map[string]string{
	"POST /v1/auth/logout":            "ends the caller's own session",
	"POST /v1/auth/switch-org":        "moves the caller's own session to another org they belong to",
	"PATCH /v1/me":                    "the caller's own name and preferences",
	"POST /v1/policies/lint":          "no side effects (IdemNever)",
	"POST /v1/policies/evaluate":      "no side effects (IdemNever)",
	"POST /v1/policies/test":          "no side effects (IdemNever)",
	"POST /v1/approvals/{id}/approve": "eligibility decides, not the role: a viewer the rule doesn't name gets approver_not_eligible (attack case P06)",
	"POST /v1/approvals/{id}/deny":    "eligibility decides, not the role",
	"POST /v1/weekly/email":           "the caller's own weekly_email setting, nothing else (S52)",
}

// TestAuthzMatrix: every route people can call, by method and role. A viewer never reaches a
// mutation outside viewerMutations; agent-key-only routes are never callable by a session.
func TestAuthzMatrix(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range ContractRoutes() {
		if c.Auth != Session && c.Auth != SessionOrKey {
			continue
		}
		mutating := c.Method != http.MethodGet && c.Method != http.MethodHead
		k := c.Key()
		switch {
		case !mutating:
		case c.Access == "admin" || c.Access == "owner":
		case viewerMutations[k] != "":
			seen[k] = true
		default:
			t.Errorf("%s: a viewer (access %q) could mutate. Give it admin or owner, or add it to viewerMutations with a reason.", k, c.Access)
		}
	}
	for k := range viewerMutations {
		if !seen[k] {
			t.Errorf("viewerMutations lists %s, which is no longer a viewer route", k)
		}
	}
}
