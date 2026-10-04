package github

import (
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/vault"
)

func checks(rep connector.TestReport) map[string]connector.Check {
	m := map[string]connector.Check{}
	for _, c := range rep.Checks {
		m[c.ID] = c
	}
	return m
}

func ids7(t *testing.T, rep connector.TestReport) {
	t.Helper()
	var got []string
	for _, c := range rep.Checks {
		got = append(got, c.ID)
	}
	if strings.Join(got, " · ") != "config · credentials · destination · auth · token · repos · rate" {
		t.Fatalf("checks %v", got)
	}
}

func TestTestReportPAT(t *testing.T) {
	c, inst, _ := setup(t, "test_pat", nil)
	rep, err := c.Test(ctxWithRedactor(), inst)
	if err != nil {
		t.Fatal(err)
	}
	ids7(t, rep)
	m := checks(rep)
	want := map[string]string{
		"config":      "valid · 1 repo · protected: main, master, release/*",
		"credentials": "set · fine-grained token · …Q2v",
		"auth":        "signed in as arjun-sandbox-bot",
		"token":       "expires 2026-11-03 · 30 days left",
		"repos":       "arjun/deedglass-sandbox · push allowed",
		"rate":        "4,987 of 5,000 left · resets 11:02 UTC",
	}
	tok := sentinelToken()
	want["credentials"] = "set · fine-grained token · …" + tok[len(tok)-4:]
	for id, d := range want {
		if m[id].Detail != d || m[id].Status != connector.CheckPass {
			t.Errorf("%s = %s %q, want pass %q", id, m[id].Status, m[id].Detail, d)
		}
	}
	if !strings.HasPrefix(m["destination"].Detail, "127.0.0.1") || rep.Status != connector.CheckPass {
		t.Fatalf("destination %+v status %s", m["destination"], rep.Status)
	}
}

func TestTestReportWarnings(t *testing.T) {
	// A classic token warns.
	srv := server(t, "test_pat")
	c := newConn()
	inst := instance(t, c, cfgJSON(srv.URL, nil), patOpener("ghp_"+strings.Repeat("k", 36)))
	rep, _ := c.Test(ctxWithRedactor(), inst)
	if m := checks(rep); m["token"].Status != connector.CheckWarn || !strings.Contains(m["token"].Detail, "classic token") || m["credentials"].Detail != "set · classic token · …kkkk" {
		t.Fatalf("classic: %+v", rep.Checks)
	}
	// Expiring in 3 days warns; a read-only repo warns.
	c, inst, _ = setup(t, "test_expiring", nil)
	rep, _ = c.Test(ctxWithRedactor(), inst)
	m := checks(rep)
	if m["token"].Status != connector.CheckWarn || m["token"].Detail != "expires 2026-10-07 · 3 days left" || m["token"].Fix == "" {
		t.Fatalf("expiring: %+v", m["token"])
	}
	if m["repos"].Status != connector.CheckWarn || !strings.Contains(m["repos"].Detail, "read-only for this token; write tools will fail") {
		t.Fatalf("repos: %+v", m["repos"])
	}
	if rep.Status != connector.CheckWarn {
		t.Fatal(rep.Status)
	}
	// Expired fails.
	c, inst, _ = setup(t, "test_expired", nil)
	rep, _ = c.Test(ctxWithRedactor(), inst)
	if m := checks(rep); m["token"].Status != connector.CheckFail || rep.Status != connector.CheckFail {
		t.Fatalf("expired: %+v", m["token"])
	}
	// A repo the token can't see fails.
	c, inst, _ = setup(t, "test_repo_404", nil)
	rep, _ = c.Test(ctxWithRedactor(), inst)
	if m := checks(rep); m["repos"].Status != connector.CheckFail || !strings.Contains(m["repos"].Detail, "not visible") || m["token"].Detail != "no expiry date" {
		t.Fatalf("404: %+v", rep.Checks)
	}
}

func TestTestReportRefusedAndMissing(t *testing.T) {
	srv := saastest.New(t)
	c := newConn()
	inst := instance(t, c, cfgJSON(srv.URL, nil), patOpener("ghs_"+strings.Repeat("Z", 36)))
	rep, _ := c.Test(ctxWithRedactor(), inst)
	ids7(t, rep)
	m := checks(rep)
	if m["credentials"].Status != connector.CheckFail || m["auth"].Status != connector.CheckSkip || rep.Status != connector.CheckFail {
		t.Fatalf("ghs_: %+v", rep.Checks)
	}
	inst = instance(t, c, cfgJSON(srv.URL, nil), nil)
	rep, _ = c.Test(ctxWithRedactor(), inst)
	if m := checks(rep); m["credentials"].Detail != "not set" || !strings.Contains(m["credentials"].Fix, "--from-env GITHUB_TOKEN") {
		t.Fatalf("missing: %+v", m["credentials"])
	}
	inst = instance(t, c, appCfg(srv.URL), nil)
	rep, _ = c.Test(ctxWithRedactor(), inst)
	if m := checks(rep); !strings.Contains(m["credentials"].Fix, "--field private_key=file:./app.pem") {
		t.Fatalf("app missing: %+v", m["credentials"])
	}
	inst = instance(t, c, cfgJSON(srv.URL, nil), &opener{err: vault.ErrIntegrity})
	rep, _ = c.Test(ctxWithRedactor(), inst)
	if m := checks(rep); m["credentials"].Status != connector.CheckFail || m["credentials"].DocURL == "" {
		t.Fatalf("vault: %+v", m["credentials"])
	}
	// A blocked destination (prod policy, loopback).
	inst = connector.NewInstance(testConn, testOrg, "github", Type, inst.Config, "h", connector.Env{Name: "prod"}, patOpener(sentinelToken()))
	rep, _ = c.Test(ctxWithRedactor(), inst)
	if m := checks(rep); m["destination"].Status != connector.CheckFail || m["auth"].Status != connector.CheckSkip {
		t.Fatalf("blocked: %+v", rep.Checks)
	}
}

func TestTestReportAuthFails(t *testing.T) {
	c, inst, _ := setup(t, "unauthorized_user", nil)
	rep, _ := c.Test(ctxWithRedactor(), inst)
	if m := checks(rep); m["auth"].Status != connector.CheckFail || !strings.Contains(m["auth"].Detail, "401") || m["token"].Status != connector.CheckSkip {
		t.Fatalf("%+v", rep.Checks)
	}
}

func TestTestReportApp(t *testing.T) {
	srv := server(t, "app_test")
	c := newConn()
	inst := instance(t, c, appCfg(srv.URL), appOpener())
	rep, err := c.Test(ctxWithRedactor(), inst)
	if err != nil {
		t.Fatal(err)
	}
	ids7(t, rep)
	m := checks(rep)
	if m["auth"].Detail != "signed in as deedglass-sandbox[bot] · installation 76543210 · 1 repo" || m["credentials"].Detail != "set · GitHub App key" {
		t.Fatalf("%+v", rep.Checks)
	}
	if !strings.HasPrefix(m["token"].Detail, "installation token · renews before 11:00 UTC") || rep.Status != connector.CheckPass {
		t.Fatalf("%+v", m["token"])
	}
	if !strings.HasSuffix(m["config"].Detail, "GitHub App") {
		t.Fatal(m["config"].Detail)
	}
}

func TestHelpers(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 4987: "4,987", 1234567: "1,234,567", -5000: "-5,000"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %s", n, got)
		}
	}
	if !parseExpiry("2026-11-09 00:00:00 UTC").Equal(parseExpiry("2026-11-09")) || !parseExpiry("junk").IsZero() {
		t.Fatal("parseExpiry")
	}
}
