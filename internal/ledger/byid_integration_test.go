//go:build integration

package ledger_test

import (
	"encoding/json"
	"testing"

	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/ledger/fake"
)

// TestActionsByID (S24: the live stream hydrates actions with it): the same bytes as the list's
// items, in one read; unknown, malformed and another org's ids are absent.
func TestActionsByID(t *testing.T) {
	en := newEnv(t)
	res := en.run(t, en.org, en.agents, 7, 12, fake.Allow, fake.Deny, fake.Pending)
	orgB, agentsB := en.newOrg(t, "beta")
	resB := en.run(t, orgB, agentsB, 7, 2, fake.Allow)
	got, err := en.led.ActionsByID(ctx, en.org, append(append([]string{}, res.IDs...), resB.IDs[0], "act_nope", "x"))
	if err != nil || len(got) != len(res.IDs) {
		t.Fatalf("%d of %d: %v", len(got), len(res.IDs), err)
	}
	l, err := en.led.ListActions(ctx, en.org, ledger.ActionFilter{}, ledger.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range l.Data {
		want, _ := json.Marshal(a)
		have, _ := json.Marshal(got[a.ID])
		if string(want) != string(have) {
			t.Fatalf("%s differs from the list:\n%s\n%s", a.ID, want, have)
		}
	}
	if _, err := en.led.ActionsByID(ctx, en.org, make([]string, ledger.MaxActionsByID+1)); err == nil {
		t.Fatal("too many ids")
	}
}
