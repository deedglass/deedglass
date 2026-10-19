//go:build integration

package identity_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/api"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// TestOrgPHIMode (HC-P09): settings.phi_mode on PATCH /v1/org. Owners only; turning it on is
// sealed in org.updated, shows on GET /v1/org and marks the org's actions that still hold payloads
// (actions.phi) so the serve job minimizes them; actions recorded while it's on are marked at
// insert (migration 0031's trigger); turning it off stops marking new ones and unmarks nothing.
func TestOrgPHIMode(t *testing.T) {
	w := newWorld(t)
	c, priya := w.org(t, "Clinic", "priya@clinic.test")
	_, sam := w.invite(t, priya, "sam@clinic.test", "admin")
	get := func() identity.Org {
		t.Helper()
		var o identity.Org
		r := priya.do("GET", "/v1/org", "")
		r.json(t, &o)
		for _, err := range httpx.CheckRequired(api.Spec, "Org", r.body) {
			t.Error(err)
		}
		return o
	}
	if o := get(); o.Settings == nil || o.Settings.PHIMode || strings.Contains(string(priya.do("GET", "/v1/org", "").body), "phi_mode") {
		t.Fatalf("default: %+v", o.Settings)
	}
	// An action recorded before PHI mode, and its agent.
	var agent struct{ ID string }
	if r := priya.do("POST", "/v1/agents", `{"name":"charting-agent"}`); r.status != 201 {
		t.Fatalf("agent: %d %s", r.status, r.body)
	} else {
		r.json(t, &agent)
	}
	agentID := ids.MustParse[ids.AgentKind](agent.ID)
	newAction := func() ids.ActionID {
		t.Helper()
		var id ids.ActionID
		if err := w.e.DB.OrgTx(ctx, c.Org.ID, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			id, err = storetest.NewAction(ctx, tx, c.Org.ID, agentID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	phiOf := func(id ids.ActionID) bool {
		t.Helper()
		var phi bool
		if err := w.e.DB.OrgReadTx(ctx, c.Org.ID, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT phi FROM deedglass.actions WHERE id = $1`, id).Scan(&phi)
		}); err != nil {
			t.Fatal(err)
		}
		return phi
	}
	before := newAction()
	if phiOf(before) {
		t.Fatal("an action recorded outside PHI mode is marked")
	}
	base := adminActions(t, w, c.Org.ID)["org.updated"]

	// Owners only; a wrong type is a validation error; nothing is sealed.
	if r := sam.do("PATCH", "/v1/org", `{"settings":{"phi_mode":true}}`); r.status != 403 || r.code() != "insufficient_role" ||
		!strings.Contains(r.message(), "changing PHI mode") {
		t.Fatalf("admin: %d %s", r.status, r.body)
	}
	if r := priya.do("PATCH", "/v1/org", `{"settings":{"phi_mode":"yes"}}`); r.status != 400 || r.code() != "validation_failed" {
		t.Fatalf("a string: %d %s", r.status, r.body)
	}
	if n := adminActions(t, w, c.Org.ID)["org.updated"]; n != base {
		t.Fatalf("refusals sealed %d deeds", n-base)
	}

	// On: sealed, shown, the earlier action marked, a new one marked at insert.
	r := priya.do("PATCH", "/v1/org", `{"settings":{"phi_mode":true}}`)
	if r.status != 200 || !strings.Contains(string(r.body), `"phi_mode": true`) {
		t.Fatalf("on: %d %s", r.status, r.body)
	}
	if o := get(); !o.Settings.PHIMode || o.Settings.Approvals.EmailLinks != "decide" {
		t.Fatalf("after on: %+v", o.Settings)
	}
	if n := adminActions(t, w, c.Org.ID)["org.updated"]; n != base+1 {
		t.Fatalf("on sealed %d deeds, want 1", n-base)
	}
	if !phiOf(before) {
		t.Fatal("turning PHI mode on didn't mark the earlier action")
	}
	during := newAction()
	if !phiOf(during) {
		t.Fatal("an action recorded in PHI mode isn't marked")
	}
	// The same value again changes nothing and seals nothing.
	if r := priya.do("PATCH", "/v1/org", `{"settings":{"phi_mode":true}}`); r.status != 200 {
		t.Fatalf("again: %d %s", r.status, r.body)
	}
	if n := adminActions(t, w, c.Org.ID)["org.updated"]; n != base+1 {
		t.Fatalf("an unchanged setting sealed a deed")
	}
	// The org.updated deed's data names the field and the value.
	var data string
	if err := w.e.DB.OrgReadTx(ctx, c.Org.ID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT data::text FROM deedglass.admin_events WHERE action = 'org.updated' ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&data)
	}); err != nil || !strings.Contains(data, `"settings.phi_mode"`) || !strings.Contains(data, `"phi_mode": true`) {
		t.Fatalf("deed data %q (%v)", data, err)
	}

	// Off: sealed; new actions aren't marked; marked ones stay marked.
	if r := priya.do("PATCH", "/v1/org", `{"settings":{"phi_mode":false}}`); r.status != 200 || strings.Contains(string(r.body), "phi_mode") {
		t.Fatalf("off: %d %s", r.status, r.body)
	}
	if after := newAction(); phiOf(after) || !phiOf(during) {
		t.Fatal("off: a new action is marked, or an earlier one lost its mark")
	}
	if n := adminActions(t, w, c.Org.ID)["org.updated"]; n != base+2 {
		t.Fatalf("off sealed %d deeds, want 1", n-base-1)
	}
}
