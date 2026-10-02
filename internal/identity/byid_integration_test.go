//go:build integration

package identity_test

import (
	"encoding/json"
	"testing"

	"github.com/deedglass/deedglass/internal/identity"
)

// TestAgentsByID (S24: the live stream hydrates agents with it): the list shape, one read; another
// org's agents are absent.
func TestAgentsByID(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.test")
	g := newFleet(t, w, "Globex", "hank@globex.test")
	a, b := f.agent(t, "support-bot"), f.agent(t, "refund-agent")
	c := g.agent(t, "globex-bot")
	got, err := w.svc.AgentsByID(ctx, f.org.Org.ID, []string{a.ID, b.ID, c.ID, "agt_nope"})
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	list, _, err := w.svc.ListAgents(ctx, f.org.Org.ID, identity.AgentFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		want, _ := json.Marshal(x)
		have, _ := json.Marshal(got[x.ID])
		if string(want) != string(have) {
			t.Fatalf("%s:\n%s\n%s", x.ID, want, have)
		}
	}
}
