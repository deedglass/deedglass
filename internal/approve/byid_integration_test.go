//go:build integration

package approve_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

// TestByID (S24: the live stream hydrates approvals with it): one read returns exactly the list
// shape for each id; unknown ids, another org's view and another agent's approvals are absent.
func TestByID(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	a := w.pause("approvals", "")
	b := w.pause("approvals", "")
	admin := approve.Viewer{Kind: httpx.KindAdminKey, Org: w.org}
	got, err := w.ap.ByID(ctx, admin, []string{a.Approval.ID, b.Approval.ID, "apr_nope", ids.New[ids.ApprovalKind]().String()})
	if err != nil || len(got) != 2 {
		t.Fatalf("%d, %v", len(got), err)
	}
	list, err := w.ap.List(ctx, admin, approve.Filter{}, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Data {
		if x, ok := got[item.ID]; ok {
			lb, _ := json.Marshal(item)
			xb, _ := json.Marshal(x)
			if string(lb) != string(xb) {
				t.Fatalf("ByID differs from the list item:\n%s\n%s", lb, xb)
			}
		}
	}
	mine, err := w.ap.ByID(ctx, approve.Viewer{Kind: httpx.KindAgentKey, Org: w.org, AgentID: w.agent.ID}, []string{a.Approval.ID})
	if err != nil || len(mine) != 1 || mine[a.Approval.ID].Context != nil {
		t.Fatalf("own agent: %v %v", mine, err)
	}
	other, err := w.ap.ByID(ctx, approve.Viewer{Kind: httpx.KindAgentKey, Org: w.org, AgentID: ids.New[ids.AgentKind]()}, []string{a.Approval.ID})
	if err != nil || len(other) != 0 {
		t.Fatalf("another agent: %v %v", other, err)
	}
	foreign, err := w.ap.ByID(ctx, approve.Viewer{Kind: httpx.KindAdminKey, Org: ids.New[ids.OrgKind]()}, []string{a.Approval.ID})
	if err != nil || len(foreign) != 0 {
		t.Fatalf("another org: %v %v", foreign, err)
	}
}
