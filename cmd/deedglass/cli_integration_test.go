//go:build integration

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/store/storetest"
)

// The real commands against a throwaway database: migrate up twice, status, verify, a tampered
// checksum, then dev seed twice.
func TestMigrateAndSeedLive(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	env := map[string]string{"DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL, "DEEDGLASS_DATABASE_URL": e.AppURL}
	expect := func(r result, code int, want ...string) {
		t.Helper()
		out := r.stdout + r.stderr
		if r.code != code {
			t.Fatalf("exit %d, want %d\n%s", r.code, code, out)
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Fatalf("missing %q in\n%s", w, out)
			}
		}
		if strings.Contains(out, storetest.Password) {
			t.Fatal("the password reached the output")
		}
	}
	r := cli(t, env, false, "migrate", "up", "--dry-run")
	expect(r, 0, "schema at 0000 · 23 pending", "Nothing was applied")
	r = cli(t, env, false, "migrate", "up")
	expect(r, 0, "as deedglass_migrator · 23 pending", "✓ 0001 foundation", "✓ Schema at 0023 · 23 applied in")
	t.Logf("\n%s", r.stdout)
	expect(cli(t, env, false, "migrate", "up"), 0, "✓ Schema is current at 0023")
	r = cli(t, env, false, "migrate", "status")
	expect(r, 0, "0023     overview            applied", "✓ 23 applied · schema is current.")
	t.Logf("\n%s", r.stdout)
	expect(cli(t, env, false, "migrate", "verify"), 0, "✓ 23 applied migrations match this build · 0 pending")

	r = cli(t, env, false, "dev", "seed")
	expect(r, 0, "✓ Seeded 1 org, 1 owner, 2 agents", "agent  support-bot", "agent  billing-agent",
		"policy default-deny enforced in 1 org", "policy 5 starter policies saved, inactive")
	t.Logf("\n%s", r.stdout)
	expect(cli(t, env, false, "dev", "seed"), 0, "✓ Already seeded: 1 org, 1 owner, 2 agents")

	// A tampered checksum (standing in for an edited file) fails verify with exit 1.
	if _, err := e.Admin(t).Exec(context.Background(), `UPDATE deedglass.schema_migrations SET checksum = 'sha256:' || pg_catalog.repeat('0', 64) WHERE version = 3`); err != nil {
		t.Fatal(err)
	}
	r = cli(t, env, false, "migrate", "verify")
	expect(r, 1, "✕ 0003 policy changed after it was applied.", "applied  sha256:0000…0000")
	t.Logf("\n%s", r.stderr)
	expect(cli(t, env, false, "migrate", "up"), 1, "0003 policy changed")
	expect(cli(t, env, false, "migrate", "status"), 1, "changed")

	// Pointing migrate at the runtime role: it can't create the schema objects.
	e2 := storetest.New(t, storetest.Options{NoMigrate: true})
	r = cli(t, map[string]string{"DEEDGLASS_DATABASE_URL": e2.AppURL}, false, "migrate", "up")
	expect(r, 1, "Using DEEDGLASS_DATABASE_URL for migrations (dev only).", "✕ Role deedglass_app can't change the schema in database "+e2.Name+".", "Run migrations as deedglass_migrator")
	t.Logf("\n%s", r.stderr)
}
