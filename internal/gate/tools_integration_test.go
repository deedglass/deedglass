//go:build integration

package gate

import (
	"errors"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/httpx"
)

// TestResolveTools is POST /v1/tools/resolve (S14 §3.5) on Postgres with the test policy:
// scratch.* is allowed with a when (visible, can't tell), fake.* allowed, the rest default deny
// (hidden); the request's validation; and a non-agent principal is refused.
func TestResolveTools(t *testing.T) {
	w := newWorld(t, worldOpts{})
	out, err := w.p.ResolveTools(bg, w.pr(0), ResolveRequest{Connector: "scratch", Tools: []ToolRef{
		{Name: "write_file", Digest: "sha256:" + strings.Repeat("a", 64)}, {Name: "read.v2-x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Object != "list" || len(out.Data) != 2 || out.PolicyVersion == nil || !strings.HasPrefix(*out.PolicyVersion, "pv_") {
		t.Fatalf("%+v", out)
	}
	for _, d := range out.Data {
		if d.Visibility != VisibilityVisible || d.Reason != nil || d.RuleID == nil || *d.RuleID != "allow-edge" || d.Object != "tool_visibility" {
			t.Fatalf("%+v", d)
		}
	}
	out, err = w.p.ResolveTools(bg, w.pr(0), ResolveRequest{Connector: "other", ConnectorType: "postgres", Tools: []ToolRef{{Name: "drop_all"}}})
	if err != nil || out.Data[0].Visibility != VisibilityHidden || out.Data[0].Reason == nil || out.Data[0].RuleID != nil || out.Data[0].Tool != "other.drop_all" {
		t.Fatalf("%v %+v", err, out)
	}
	bad := []ResolveRequest{
		{Connector: "Scratch", Tools: []ToolRef{{Name: "x"}}},
		{Connector: "scratch", Tools: make([]ToolRef, MaxResolveTools+1)},
		{Connector: "scratch", Tools: []ToolRef{{Name: "has space"}}},
		{Connector: "scratch", Tools: []ToolRef{{Name: "x", Digest: "md5:1"}}},
		{Connector: "scratch", ConnectorType: "oracle", Tools: []ToolRef{{Name: "x"}}},
	}
	for i, b := range bad {
		var he *httpx.Error
		if _, err := w.p.ResolveTools(bg, w.pr(0), b); !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
			t.Errorf("%d: %v", i, err)
		}
	}
	// No tools asks only for the policy (S22: the wrapper's startup line names it).
	if out, err := w.p.ResolveTools(bg, w.pr(0), ResolveRequest{Connector: "scratch"}); err != nil || len(out.Data) != 0 || out.Policy == nil || out.Policy.Name == "" {
		t.Fatalf("no tools: %v %+v", err, out)
	}
	if _, err := w.p.ResolveTools(bg, principal{Kind: httpx.KindAdminKey, OrgID: w.org}, ResolveRequest{}); err == nil {
		t.Fatal("admin key")
	}
}
