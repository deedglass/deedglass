package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/httpx"
)

const root = "../../../.."

// TestSDKErrorsFresh fails when errors.go changed without re-running gensdkerrors.
func TestSDKErrorsFresh(t *testing.T) {
	files, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range files {
		got, err := os.ReadFile(filepath.Join(root, p))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s is stale; run go run ./internal/httpx/cmd/gensdkerrors from the repo root", p)
		}
	}
	if n := strings.Count(string(files[pyPath]), "\n    \""); n != len(httpx.Catalog()) {
		t.Errorf("%d Python rows for %d codes", n, len(httpx.Catalog()))
	}
}

func TestClass(t *testing.T) {
	for _, c := range []struct {
		t    httpx.ErrType
		c    httpx.Code
		want string
	}{
		{httpx.TypeRateLimit, httpx.CodeBudgetExceeded, "BudgetExceededError"},
		{httpx.TypeRateLimit, httpx.CodeRateLimited, "RateLimitError"},
		{httpx.TypePolicy, httpx.CodePolicyDenied, "PolicyDeniedError"},
		{httpx.TypePermission, httpx.CodeAgentStopped, "PermissionDeniedError"},
		{httpx.TypeAuthentication, httpx.CodeInvalidAPIKey, "AuthenticationError"},
		{httpx.TypeInvalidRequest, httpx.CodeNotFound, "InvalidRequestError"},
		{httpx.TypeAPI, httpx.CodeInternalError, "APIError"},
		{"other", "x", "DeedglassError"},
	} {
		if got := Class(c.t, c.c); got != c.want {
			t.Errorf("%s/%s: %s", c.t, c.c, got)
		}
	}
}
