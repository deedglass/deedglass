package httpx

import (
	"strings"
	"testing"
)

// keyAllowed is S08 §3.5 steps 6–9 and §3.4's invariants for admin keys.
func TestKeyAllowed(t *testing.T) {
	agent := Principal{Kind: KindAgentKey, AgentName: "support-bot", AgentStatus: "active", OrgName: "Acme",
		Scopes: []string{ScopeActionsExecute, ScopeActionsRead, ScopeDeedsRead}}
	readOnly := agent
	readOnly.Scopes = []string{ScopeActionsRead}
	admin := Principal{Kind: KindAdminKey, Role: RoleAdmin, OrgName: "Acme", Scopes: []string{ScopeAdmin}}
	with := func(p Principal, f func(*Principal)) Principal { f(&p); return p }

	control := Route{Method: "POST", Auth: SessionOrKey, MinRole: RoleAdmin, Scopes: []string{ScopeAdmin}, Purpose: "stopping agents"}
	deeds := Route{Method: "GET", Auth: SessionOrKey, MinRole: RoleViewer, Scopes: []string{ScopeDeedsRead}}
	listAgents := Route{Method: "GET", Auth: SessionOrKey, MinRole: RoleViewer, Scopes: []string{ScopeAdmin}}
	patch := Route{Method: "PATCH", Auth: SessionOrKey, MinRole: RoleAdmin, Scopes: []string{ScopeAdmin}}
	execute := Route{Auth: AgentKey, Scopes: []string{ScopeActionsExecute}}
	whoami := Route{Auth: AgentKey, SkipStopChecks: true}
	ownerOnly := Route{Auth: SessionOrKey, MinRole: RoleOwner, Scopes: []string{ScopeAdmin}, Purpose: "starting all agents"}

	cases := []struct {
		name string
		rt   Route
		p    Principal
		code Code
	}{
		{"agent key reads deeds", deeds, agent, ""},
		{"agent key executes", execute, agent, ""},
		{"read-only key can't read deeds", deeds, readOnly, CodeInsufficientScope},
		{"agent key can't manage", control, agent, CodeInsufficientScope},
		{"admin key manages", control, admin, ""},
		{"admin key reads deeds (acts as admin)", deeds, admin, ""},
		{"admin key can't execute", execute, admin, CodeInsufficientScope},
		{"admin key never acts as owner", ownerOnly, admin, CodeInsufficientRole},
		{"whoami for any key", whoami, admin, ""},
		{"stopped agent", deeds, with(agent, func(p *Principal) { p.AgentStatus = "stopped" }), CodeAgentStopped},
		{"suspended agent", deeds, with(agent, func(p *Principal) { p.AgentStatus = "suspended" }), CodeAgentSuspended},
		{"unknown status fails closed", deeds, with(agent, func(p *Principal) { p.AgentStatus = "" }), CodeAgentStopped},
		{"org stopped beats agent stopped", deeds, with(agent, func(p *Principal) { p.AgentStatus = "stopped"; p.OrgStopped = true }), CodeOrgStopped},
		{"org stopped refuses admin-key writes", control, with(admin, func(p *Principal) { p.OrgStopped = true }), CodeOrgStopped},
		{"org stopped refuses admin-key PATCH", patch, with(admin, func(p *Principal) { p.OrgStopped = true }), CodeOrgStopped},
		{"org stopped: admin keys still read agents", listAgents, with(admin, func(p *Principal) { p.OrgStopped = true }), ""},
		{"org stopped: admin keys still read deeds", deeds, with(admin, func(p *Principal) { p.OrgStopped = true }), ""},
		{"org stopped: agent keys can't even read", deeds, with(agent, func(p *Principal) { p.OrgStopped = true }), CodeOrgStopped},
		{"stop checks before scopes", deeds, with(readOnly, func(p *Principal) { p.AgentStatus = "stopped" }), CodeAgentStopped},
		{"whoami skips the stop checks", whoami, with(agent, func(p *Principal) { p.AgentStatus = "stopped"; p.OrgStopped = true }), ""},
	}
	for _, c := range cases {
		err := keyAllowed(c.rt, c.p)
		got := Code("")
		if e := AsError(err); e != nil {
			got = e.Code
		}
		if got != c.code {
			t.Errorf("%s: got %q, want %q (%v)", c.name, got, c.code, err)
		}
	}
	e := keyAllowed(control, agent).(*Error)
	if e.Message != "This is an agent key, and managing agents needs an admin key. Create one with deedglass org keys create on the server." ||
		e.Header.Get("WWW-Authenticate") != `Bearer realm="deedglass", error="insufficient_scope", scope="admin"` {
		t.Fatalf("%q %v", e.Message, e.Header)
	}
	if m := ErrKeyOnSessionRoute(admin, RoleOwner, "starting all agents").Message; !strings.Contains(m, "starting all agents needs an owner") {
		t.Fatal(m)
	}
}

func TestChallengeText(t *testing.T) {
	got := challengeText(`Agent "a\b" — Console › Agents… ok`)
	if got != `Agent 'a'b'  Console > Agents... ok` {
		t.Fatalf("%q", got)
	}
}
