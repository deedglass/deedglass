//go:build integration

package store_test

import (
	"testing"

	"github.com/deedglass/deedglass/internal/store/storetest"
)

// TestRuntimeRoleCannotBypassRLS (S34 review row 4): RLS holds for deedglass_app because the role
// is not a superuser, has no BYPASSRLS, and owns no table, view, sequence or function in schema
// deedglass (an owner skips RLS unless it is forced). The tables enable RLS but don't FORCE it on
// purpose: the SECURITY DEFINER functions owned by the migration role (preauth_*, maint_*,
// claim_*) need the owner's view across orgs, and the system_scope list pins which tables those
// reach (TestSchemaShape).
func TestRuntimeRoleCannotBypassRLS(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	c := e.Admin(t)
	if n := count(t, c, `SELECT count(*) FROM pg_catalog.pg_roles WHERE rolname = 'deedglass_app' AND (rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb OR rolreplication)`); n != 0 {
		t.Fatal("deedglass_app is a superuser or has BYPASSRLS, CREATEROLE, CREATEDB or REPLICATION")
	}
	if n := count(t, c, `SELECT count(*) FROM pg_catalog.pg_roles WHERE rolname = 'deedglass_app'`); n != 1 {
		t.Fatal("no deedglass_app role")
	}
	owned := tables(t, c, `SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	    WHERE n.nspname = 'deedglass' AND pg_catalog.pg_get_userbyid(c.relowner) = 'deedglass_app'`)
	if len(owned) != 0 {
		t.Fatalf("deedglass_app owns %v: an owner skips RLS", owned)
	}
	if n := count(t, c, `SELECT count(*) FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
	    WHERE n.nspname = 'deedglass' AND pg_catalog.pg_get_userbyid(p.proowner) = 'deedglass_app'`); n != 0 {
		t.Fatalf("deedglass_app owns %d functions", n)
	}
	// The member roles it can SET ROLE to carry none of those attributes either.
	if n := count(t, c, `SELECT count(*) FROM pg_catalog.pg_auth_members m JOIN pg_catalog.pg_roles r ON r.oid = m.roleid
	    WHERE m.member = (SELECT oid FROM pg_catalog.pg_roles WHERE rolname = 'deedglass_app') AND (r.rolsuper OR r.rolbypassrls)`); n != 0 {
		t.Fatal("deedglass_app is a member of a role that bypasses RLS")
	}
}
