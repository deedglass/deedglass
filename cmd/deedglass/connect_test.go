package main

import (
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector/postgres"
	pgsetup "github.com/deedglass/deedglass/internal/connector/postgres/setup"
	"github.com/deedglass/deedglass/internal/policy"
)

func TestConnectName(t *testing.T) {
	for _, tc := range []struct {
		prov pgsetup.Provider
		db   string
		want string
	}{
		{pgsetup.ProviderSupabase, "postgres", "supabase"},
		{pgsetup.ProviderNeon, "neondb", "neon"},
		{pgsetup.ProviderLocal, "shop", "shop"},
		{pgsetup.ProviderOther, "Orders_DB", "orders-db"},
		{pgsetup.ProviderOther, "postgres", "db"}, // never the quickstart's connector name
		{pgsetup.ProviderOther, "9lives", "db"},
		{pgsetup.ProviderOther, "native", "db"},
	} {
		if got := connectName(tc.prov, tc.db); got != tc.want {
			t.Errorf("%s %s: %s, want %s", tc.prov, tc.db, got, tc.want)
		}
	}
}

func TestPoolerNote(t *testing.T) {
	parse := func(u string) *postgres.DSN {
		d, err := postgres.ParseDSN(u, cliEgress())
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	d := parse("postgres://postgres.abcdefgh:secretpw@aws-0-eu-west-1.pooler.supabase.com:5432/postgres")
	note, direct := poolerNote(d, pgsetup.Detect(d.Host, nil))
	if note == "" || direct != "postgres@db.abcdefgh.supabase.co:5432/postgres" || strings.Contains(note+direct, "secretpw") {
		t.Fatalf("supabase pooler: %q %q", note, direct)
	}
	d = parse("postgres://app:secretpw@ep-cool-name-123-pooler.eu-central-1.aws.neon.tech/neondb?sslmode=require")
	note, direct = poolerNote(d, pgsetup.Detect(d.Host, nil))
	if note == "" || direct != "app@ep-cool-name-123.eu-central-1.aws.neon.tech:5432/neondb" {
		t.Fatalf("neon pooler: %q %q", note, direct)
	}
	d = parse("postgres://app:secretpw@db.example.com:6543/app")
	if note, _ := poolerNote(d, pgsetup.ProviderOther); note == "" {
		t.Fatal("port 6543 is a pooler")
	}
	d = parse("postgres://postgres:secretpw@db.abcdefgh.supabase.co:5432/postgres")
	if note, _ := poolerNote(d, pgsetup.ProviderSupabase); note != "" {
		t.Fatalf("direct supabase: %q", note)
	}
}

// The scoped policy compiles, names this connector's tools, and keeps the demo's when asked.
func TestScopedPolicy(t *testing.T) {
	for _, starter := range []string{"postgres-safe", "postgres-check"} {
		for _, demo := range []bool{false, true} {
			src, name, err := scopedPolicy(starter, "shop", demo)
			if err != nil {
				t.Fatal(err)
			}
			c, diags := policy.Compile(src, policy.Options{File: name + ".yaml"})
			if c == nil {
				t.Fatalf("%s demo=%v: %+v", starter, demo, diags)
			}
			if c.Name != starter+"-shop" || !strings.Contains(string(src), "shop.query") || !strings.Contains(string(src), "shop.execute") {
				t.Fatalf("%s: %s", name, src)
			}
			if strings.Contains(string(src), "tool: postgres.query") {
				t.Fatalf("%s still names only the demo's tool", name)
			}
			if demo != strings.Contains(string(src), "postgres.query, shop.query") {
				t.Fatalf("%s demo=%v: %s", name, demo, src)
			}
		}
	}
}

// connect never echoes a URL's password, even when the URL is refused.
func TestConnectRefusalsHideThePassword(t *testing.T) {
	for _, args := range [][]string{
		{"connect", "postgres://u:hunter2secret@169.254.169.254:5432/db"},
		{"connect", "postgres://u:hunter2secret@127.0.0.1:1/db", "--policy", "nope"},
		{"connect", "postgres://u:hunter2secret@127.0.0.1:1/db", "--name", "Bad Name"},
		{"connect", "postgres://u:hunter2secret@127.0.0.1:1/db"},
		{"--json", "connect", "postgres://u:hunter2secret@127.0.0.1:1/db"},
	} {
		r := cli(t, map[string]string{"HOME": t.TempDir()}, false, args...)
		if r.code == ExitOK || strings.Contains(r.stdout+r.stderr, "hunter2secret") {
			t.Fatalf("%v: %d %s%s", args, r.code, r.stdout, r.stderr)
		}
	}
	if r := cli(t, nil, false, "connect"); r.code != ExitUsage || !strings.Contains(r.stderr, "DATABASE_URL") {
		t.Fatalf("no URL: %d %s", r.code, r.stderr)
	}
}

func TestApprovalsOpenUsage(t *testing.T) {
	if r := cli(t, nil, false, "approvals", "open"); r.code != ExitUsage {
		t.Fatalf("%d %s", r.code, r.stderr)
	}
	if r := cli(t, nil, false, "approvals", "open", "act_01j9zw6k8m4n5p6q7r8s9t0v1w"); r.code != ExitUsage || !strings.Contains(r.stderr, "isn't an approval id") {
		t.Fatalf("%d %s", r.code, r.stderr)
	}
}
