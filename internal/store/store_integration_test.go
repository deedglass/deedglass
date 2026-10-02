//go:build integration

package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

var ctx = context.Background()

// The spec's table (S02 §3.6): which tables carry system_scope, and the app role's grants.
var (
	globals  = []string{"orgs", "schema_migrations", "seal_keys", "users", "waitlist"}
	sysScope = []string{"agents", "api_keys", "approval_tokens", "approvals", "decoded_txs", "decoder_slots",
		"idempotency_keys", "memberships", "orgs", "replay_jobs", "sessions", "usage_counters", "users",
		"waitlist", "webhook_deliveries"}
	appendOnly = []string{"action_events", "admin_events", "approval_votes", "deeds", "policy_versions"}
	// S I U D; "u" = a column-level UPDATE grant only.
	grants = map[string]string{
		"orgs": "SIu", "users": "SIu", "memberships": "SIUD", "sessions": "SIUD", "login_tokens": "SIUD",
		"agents": "SIu", "api_keys": "SIu", "intent_nonces": "SID", "policies": "SIU", "policy_versions": "SI",
		"policy_activations": "SIu", "usage_counters": "SIUD", "connectors": "SIu", "credentials": "SIUD",
		"actions": "SIu", "action_events": "SI", "approvals": "SIU", "approval_tokens": "SIUD", "approval_votes": "SI",
		"idempotency_keys": "SIUD", "seal_keys": "SIu", "deeds": "SI", "admin_events": "SI",
		"witness_jobs": "SIUD", "decoded_txs": "SI", "decoder_slots": "SIUD", "suggestions": "SIU",
		"anomalies": "SIU", "replay_jobs": "SIU", "webhooks": "SIUD", "webhook_deliveries": "SIUD",
		"waitlist": "SI", "schema_migrations": "S",
	}
)

func code(err error) string { return store.SQLState(err) }

func expectCode(t *testing.T, what string, err error, want string) {
	t.Helper()
	if got := code(err); got != want {
		t.Errorf("%s: SQLSTATE %q (%v), want %s", what, got, err, want)
	}
}

func tables(t *testing.T, c *pgx.Conn, q string) []string {
	t.Helper()
	rows, err := c.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestSchemaShape(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	c := e.Admin(t)
	all := tables(t, c, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'deedglass' AND c.relkind = 'r'`)
	if len(all) != 33 {
		t.Fatalf("%d tables, want 33 (30 from contract §4 + idempotency_keys + schema_migrations + S22's approval_votes): %v", len(all), all)
	}
	for _, tbl := range all {
		if _, ok := grants[tbl]; !ok {
			t.Errorf("table %s isn't in the spec's grant table", tbl)
		}
	}
	noOrg := tables(t, c, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	    WHERE n.nspname = 'deedglass' AND c.relkind = 'r'
	      AND NOT EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'org_id' AND NOT a.attisdropped)`)
	if !slices.Equal(noOrg, globals) {
		t.Errorf("tables without org_id = %v, want exactly the five globals %v", noOrg, globals)
	}
	noRLS := tables(t, c, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	    WHERE n.nspname = 'deedglass' AND c.relkind = 'r' AND NOT c.relrowsecurity`)
	if !slices.Equal(noRLS, []string{"schema_migrations", "seal_keys"}) {
		t.Errorf("tables without RLS = %v", noRLS)
	}
	missing := tables(t, c, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	    WHERE n.nspname = 'deedglass' AND c.relkind = 'r'
	      AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'org_id' AND NOT a.attisdropped)
	      AND NOT (c.relrowsecurity AND EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid AND p.polname = 'org_isolation'))`)
	if len(missing) > 0 {
		t.Errorf("org tables without RLS and an org_isolation policy: %v", missing)
	}
	sys := tables(t, c, `SELECT c.relname FROM pg_policy p JOIN pg_class c ON c.oid = p.polrelid WHERE p.polname = 'system_scope'`)
	if !slices.Equal(sys, sysScope) {
		t.Errorf("system_scope tables = %v\nwant %v (S02 §3.6; system_scope is not widened after S02)", sys, sysScope)
	}
	// Every policy applies to deedglass_app only.
	if n := count(t, c, `SELECT count(*) FROM pg_policy p JOIN pg_class c ON c.oid = p.polrelid JOIN pg_namespace n ON n.oid = c.relnamespace
	    WHERE n.nspname = 'deedglass' AND p.polroles <> ARRAY[(SELECT oid FROM pg_roles WHERE rolname = 'deedglass_app')]::oid[]`); n != 0 {
		t.Errorf("%d policies not scoped TO deedglass_app", n)
	}
	// Every foreign key between two org tables includes org_id on both sides, at the same position.
	bad := tables(t, c, `SELECT con.conname FROM pg_constraint con
	    JOIN pg_class src ON src.oid = con.conrelid JOIN pg_namespace n ON n.oid = src.relnamespace
	    WHERE con.contype = 'f' AND n.nspname = 'deedglass'
	      AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = con.conrelid AND a.attname = 'org_id')
	      AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = con.confrelid AND a.attname = 'org_id')
	      AND NOT EXISTS (
	        SELECT 1 FROM UNNEST(con.conkey, con.confkey) AS k(src, dst)
	        JOIN pg_attribute sa ON sa.attrelid = con.conrelid AND sa.attnum = k.src
	        JOIN pg_attribute da ON da.attrelid = con.confrelid AND da.attnum = k.dst
	        WHERE sa.attname = 'org_id' AND da.attname = 'org_id')`)
	if len(bad) > 0 {
		t.Errorf("foreign keys between org tables without org_id: %v", bad)
	}
	// Append-only tables: a row trigger on UPDATE and DELETE, a statement trigger on UPDATE, DELETE
	// and TRUNCATE, all calling forbid_mutation.
	for _, tbl := range appendOnly {
		var row, stmt int
		if err := c.QueryRow(ctx, `SELECT
		    count(*) FILTER (WHERE (tgtype & 1) = 1 AND (tgtype & 8) = 8 AND (tgtype & 16) = 16),
		    count(*) FILTER (WHERE (tgtype & 1) = 0 AND (tgtype & 8) = 8 AND (tgtype & 16) = 16 AND (tgtype & 32) = 32)
		    FROM pg_trigger WHERE tgrelid = ('deedglass.' || $1)::regclass AND tgfoid = 'deedglass.forbid_mutation()'::regprocedure`, tbl).Scan(&row, &stmt); err != nil {
			t.Fatal(err)
		}
		if row != 1 || stmt != 1 {
			t.Errorf("%s: %d row and %d statement append-only triggers, want 1 and 1", tbl, row, stmt)
		}
	}
	// Every table with updated_at has the touch trigger.
	untouched := tables(t, c, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	    WHERE n.nspname = 'deedglass' AND c.relkind = 'r'
	      AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'updated_at')
	      AND NOT EXISTS (SELECT 1 FROM pg_trigger g WHERE g.tgrelid = c.oid AND g.tgfoid = 'deedglass.touch_updated_at()'::regprocedure)`)
	if len(untouched) > 0 {
		t.Errorf("tables with updated_at but no touch trigger: %v", untouched)
	}
	// The migrator owns everything; the app owns nothing.
	if n := count(t, c, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	    WHERE n.nspname = 'deedglass' AND c.relowner <> 'deedglass_migrator'::regrole`); n != 0 {
		t.Errorf("%d objects in schema deedglass not owned by deedglass_migrator", n)
	}
	// Grants match the spec: S/I/U/D at table level, or a column-level UPDATE ("u").
	for tbl, want := range grants {
		var s, i, u, d, colU bool
		if err := c.QueryRow(ctx, `SELECT
		    pg_catalog.has_table_privilege('deedglass_app', 'deedglass.' || $1, 'SELECT'),
		    pg_catalog.has_table_privilege('deedglass_app', 'deedglass.' || $1, 'INSERT'),
		    pg_catalog.has_table_privilege('deedglass_app', 'deedglass.' || $1, 'UPDATE'),
		    pg_catalog.has_table_privilege('deedglass_app', 'deedglass.' || $1, 'DELETE'),
		    pg_catalog.has_any_column_privilege('deedglass_app', 'deedglass.' || $1, 'UPDATE')`, tbl).Scan(&s, &i, &u, &d, &colU); err != nil {
			t.Fatal(err)
		}
		got := ""
		for _, p := range []struct {
			on bool
			c  string
		}{{s, "S"}, {i, "I"}, {u, "U"}, {!u && colU, "u"}, {d, "D"}} {
			if p.on {
				got += p.c
			}
		}
		if got != want {
			t.Errorf("grants on %s = %s, want %s", tbl, got, want)
		}
		var trunc, trig, refs bool
		if err := c.QueryRow(ctx, `SELECT pg_catalog.has_table_privilege('deedglass_app', 'deedglass.' || $1, 'TRUNCATE'),
		    pg_catalog.has_table_privilege('deedglass_app', 'deedglass.' || $1, 'TRIGGER'),
		    pg_catalog.has_table_privilege('deedglass_app', 'deedglass.' || $1, 'REFERENCES')`, tbl).Scan(&trunc, &trig, &refs); err != nil {
			t.Fatal(err)
		}
		if trunc || trig || refs {
			t.Errorf("%s: deedglass_app has TRUNCATE, TRIGGER or REFERENCES", tbl)
		}
	}
}

func count(t *testing.T, c *pgx.Conn, q string, args ...any) int {
	t.Helper()
	var n int
	if err := c.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// orgTables lists every table with org_id.
func orgTables(t *testing.T, e *storetest.Env) []string {
	return tables(t, e.Admin(t), `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	    WHERE n.nspname = 'deedglass' AND c.relkind = 'r'
	      AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'org_id' AND NOT a.attisdropped)`)
}

func tx(t *testing.T, db *store.DB, org ids.OrgID, fn func(pgx.Tx)) {
	t.Helper()
	if err := db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error { fn(tx); return nil }); err != nil {
		t.Fatal(err)
	}
}

// inSavepoint runs q and rolls it back, returning its error and rows affected.
func inSavepoint(tx pgx.Tx, fn func(pgx.Tx) (int64, error)) (int64, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer sp.Rollback(ctx)
	return fn(sp)
}

func updatableColumn(t *testing.T, c *pgx.Conn, tbl string) string {
	var col string
	err := c.QueryRow(ctx, `SELECT a.attname FROM pg_attribute a WHERE a.attrelid = ('deedglass.' || $1)::regclass AND a.attnum > 0
	    AND NOT a.attisdropped AND a.attidentity = '' AND pg_catalog.has_column_privilege('deedglass_app', a.attrelid, a.attnum, 'UPDATE')
	    ORDER BY a.attnum LIMIT 1`, tbl).Scan(&col)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return col
}

func TestRLSMatrix(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	a := storetest.NewOrg(t, e.DB, "alpha")
	b := storetest.NewOrg(t, e.DB, "bravo")
	admin := e.Admin(t)
	var sk ids.SealKeyID
	if err := admin.QueryRow(ctx, `SELECT id FROM deedglass.seal_keys LIMIT 1`).Scan(&sk); err != nil {
		t.Fatal(err)
	}
	insertB := map[string]storetest.Row{}
	for _, r := range storetest.Rows(sk) {
		insertB[r.Table] = r
	}
	ot := orgTables(t, e)
	if len(ot) != 28 {
		t.Fatalf("%d org tables, want 28: %v", len(ot), ot)
	}
	checked := 0
	for _, tbl := range ot {
		t.Run(tbl, func(t *testing.T) {
			q := func(s string) string {
				return strings.ReplaceAll(s, "{T}", pgx.Identifier{"deedglass", tbl}.Sanitize())
			}
			// Both orgs have at least one row (seen by the owner, outside RLS).
			for _, o := range []storetest.Org{a, b} {
				if n := count(t, admin, q(`SELECT count(*) FROM {T} WHERE org_id = $1`), o.ID); n < 1 {
					t.Fatalf("seed wrote no row for %s", o.ID)
				}
			}
			tx(t, e.DB, a.ID, func(tx pgx.Tx) {
				var total, own, other int
				if err := tx.QueryRow(ctx, q(`SELECT count(*), count(*) FILTER (WHERE org_id = $1), count(*) FILTER (WHERE org_id = $2) FROM {T}`), a.ID, b.ID).Scan(&total, &own, &other); err != nil {
					t.Fatal(err)
				}
				if total != own || own < 1 || other != 0 {
					t.Errorf("OrgTx(A): total %d, A %d, B %d", total, own, other)
				}
				if col := updatableColumn(t, admin, tbl); col != "" {
					n, err := inSavepoint(tx, func(tx pgx.Tx) (int64, error) {
						ct, err := tx.Exec(ctx, q(fmt.Sprintf(`UPDATE {T} SET %s = %[1]s WHERE org_id = $1`, pgx.Identifier{col}.Sanitize())), b.ID)
						return ct.RowsAffected(), err
					})
					if err != nil || n != 0 {
						t.Errorf("OrgTx(A) UPDATE … WHERE org_id = B: %d rows, %v", n, err)
					}
				}
				n, err := inSavepoint(tx, func(tx pgx.Tx) (int64, error) {
					ct, err := tx.Exec(ctx, q(`DELETE FROM {T} WHERE org_id = $1`), b.ID)
					return ct.RowsAffected(), err
				})
				if !strings.Contains(grants[tbl], "D") {
					expectCode(t, "DELETE without the privilege", err, "42501")
				} else if err != nil || n != 0 {
					t.Errorf("OrgTx(A) DELETE … WHERE org_id = B: %d rows, %v", n, err)
				}
				bb := b
				_, err = inSavepoint(tx, func(tx pgx.Tx) (int64, error) { return 0, insertB[tbl].Insert(ctx, tx, &bb) })
				expectCode(t, "OrgTx(A) INSERT with org_id B", err, "42501")
			})
			// A plain transaction: no org, no scope.
			if err := pgx.BeginFunc(ctx, e.DB.PoolForTest(), func(tx pgx.Tx) error {
				if n := countTx(t, tx, q(`SELECT count(*) FROM {T}`)); n != 0 {
					t.Errorf("no org: %d rows visible", n)
				}
				aa := a
				_, err := inSavepoint(tx, func(tx pgx.Tx) (int64, error) { return 0, insertB[tbl].Insert(ctx, tx, &aa) })
				expectCode(t, "no org: INSERT", err, "42501")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// SystemTx sees both orgs on sys tables and nothing elsewhere.
			if err := e.DB.SystemTx(ctx, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
				orgs := countTx(t, tx, q(`SELECT count(DISTINCT org_id) FROM {T} WHERE org_id IN ($1, $2)`), a.ID, b.ID)
				want := 0
				if slices.Contains(sysScope, tbl) {
					want = 2
				}
				if orgs != want {
					t.Errorf("SystemTx sees %d orgs, want %d", orgs, want)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			checked++
		})
	}
	t.Logf("RLS matrix: %d org tables checked", checked)

	// Globals.
	tx(t, e.DB, a.ID, func(tx pgx.Tx) {
		if n := countTx(t, tx, `SELECT count(*) FROM deedglass.orgs`); n != 1 {
			t.Errorf("OrgTx(A) sees %d orgs", n)
		}
		if n := countTx(t, tx, `SELECT count(*) FROM deedglass.users WHERE id = $1`, b.Owner); n != 0 {
			t.Error("OrgTx(A) sees B's owner")
		}
		if n := countTx(t, tx, `SELECT count(*) FROM deedglass.users WHERE id = $1`, a.Owner); n != 1 {
			t.Error("OrgTx(A) doesn't see its own owner")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deedglass.waitlist (id, email) VALUES ($1, 'someone@example.test')`, ids.New[ids.WaitlistKind]()); err != nil {
			t.Errorf("waitlist insert: %v", err)
		}
		if n := countTx(t, tx, `SELECT count(*) FROM deedglass.waitlist`); n != 0 {
			t.Error("the waitlist is readable outside system scope")
		}
		// An org may add a person it invites (insert_invited, S07): a fresh row in its first state
		// only, which grants nothing and stays invisible until a membership links it.
		newUser := ids.New[ids.UserKind]()
		if _, err := inSavepoint(tx, func(tx pgx.Tx) (int64, error) {
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.users (id, email) VALUES ($1, 'new@example.test')`, newUser)
			return 0, err
		}); err != nil {
			t.Errorf("OrgTx inserting an invited user: %v", err)
		}
		if n := countTx(t, tx, `SELECT count(*) FROM deedglass.users WHERE email = 'new@example.test'`); n != 0 {
			t.Error("a user with no membership in the org is visible")
		}
		_, err := inSavepoint(tx, func(tx pgx.Tx) (int64, error) {
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.users (id, email, status) VALUES ($1, 'active@example.test', 'active')`, ids.New[ids.UserKind]())
			return 0, err
		})
		expectCode(t, "OrgTx inserting an active user", err, "42501")
		n, err := inSavepoint(tx, func(tx pgx.Tx) (int64, error) {
			ct, err := tx.Exec(ctx, `UPDATE deedglass.orgs SET name = 'x' WHERE id = $1`, b.ID)
			return ct.RowsAffected(), err
		})
		if err != nil || n != 0 {
			t.Errorf("OrgTx(A) renaming org B: %d, %v", n, err)
		}
	})
}

func countTx(t *testing.T, tx pgx.Tx, q string, args ...any) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// A pooled connection never carries an org into the next transaction.
func TestPooledConnectionLeak(t *testing.T) {
	e := storetest.New(t, storetest.Options{MaxConns: 1})
	a := storetest.NewOrg(t, e.DB, "alpha")
	tx(t, e.DB, a.ID, func(tx pgx.Tx) {
		if n := countTx(t, tx, `SELECT count(*) FROM deedglass.agents`); n < 1 {
			t.Fatal("no agents in A")
		}
	})
	if err := pgx.BeginFunc(ctx, e.DB.PoolForTest(), func(tx pgx.Tx) error {
		var pid int
		var org, scope *string
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.pg_backend_pid(), pg_catalog.current_setting('deedglass.org_id', true), pg_catalog.current_setting('deedglass.scope', true)`).Scan(&pid, &org, &scope); err != nil {
			return err
		}
		if org != nil && *org != "" {
			t.Errorf("deedglass.org_id leaked: %q", *org)
		}
		if n := countTx(t, tx, `SELECT count(*) FROM deedglass.agents`); n != 0 {
			t.Errorf("a plain transaction after OrgTx sees %d agents", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// And a failed OrgTx (rolled back) leaks nothing either.
	boom := errors.New("boom")
	if err := e.DB.OrgTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error { return boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if err := e.DB.SystemTx(ctx, store.ReasonHealth, func(ctx context.Context, tx pgx.Tx) error {
		if n := countTx(t, tx, `SELECT count(*) FROM deedglass.agents`); n < 1 {
			t.Error("SystemTx: agents has system_scope and should be visible")
		}
		if n := countTx(t, tx, `SELECT count(*) FROM deedglass.deeds`); n != 0 {
			t.Error("SystemTx must not see deeds")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.DB.OrgTx(ctx, ids.OrgID{}, nil); !errors.Is(err, store.ErrNoOrg) {
		t.Errorf("zero org: %v", err)
	}
	if err := e.DB.SystemTx(ctx, "whatever", nil); !errors.Is(err, store.ErrUnknownScope) {
		t.Errorf("unknown reason: %v", err)
	}
	// OrgReadTx refuses writes.
	err := e.DB.OrgReadTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.CreateAgent(ctx, tx, a.ID, store.NewAgent{Name: "reader"})
		return err
	})
	expectCode(t, "OrgReadTx write", err, "25006")
}

func TestPrivileges(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	a := storetest.NewOrg(t, e.DB, "alpha")
	app := e.App(t)
	if _, err := app.Exec(ctx, `SELECT pg_catalog.set_config('deedglass.org_id', $1, false)`, a.ID); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range appendOnly {
		for _, q := range []string{"UPDATE deedglass.%s SET created_at = created_at", "DELETE FROM deedglass.%s", "TRUNCATE deedglass.%s"} {
			_, err := app.Exec(ctx, fmt.Sprintf(q, tbl))
			expectCode(t, "app: "+fmt.Sprintf(q, tbl), err, "42501")
		}
	}
	for _, q := range []string{
		"ALTER TABLE deedglass.deeds DISABLE TRIGGER ALL",
		"ALTER TABLE deedglass.deeds DISABLE ROW LEVEL SECURITY",
		"SET ROLE deedglass_migrator",
		"CREATE TABLE deedglass.x ()",
		"DROP TRIGGER deeds_chain ON deedglass.deeds",
		"UPDATE deedglass.actions SET org_id = org_id",
		"UPDATE deedglass.agents SET name = name",
		"UPDATE deedglass.api_keys SET secret_hash = secret_hash",
		"UPDATE deedglass.seal_keys SET public_key = public_key",
		"INSERT INTO deedglass.schema_migrations (version, name, checksum, execution_ms, app_version) VALUES (99, 'x', 'sha256:' || repeat('0', 64), 0, 'x')",
	} {
		_, err := app.Exec(ctx, q)
		expectCode(t, "app: "+q, err, "42501")
	}
	// The owner is refused by the triggers (the second layer), with the exact message. TRUNCATE of a
	// table other tables reference is refused by Postgres itself (0A000) unless CASCADE, so CASCADE
	// is what reaches the trigger.
	mig := e.Migrator(t)
	for _, tbl := range appendOnly {
		for _, q := range []string{"UPDATE deedglass.%s SET created_at = created_at", "DELETE FROM deedglass.%s", "TRUNCATE deedglass.%s CASCADE"} {
			_, err := mig.Exec(ctx, fmt.Sprintf(q, tbl))
			expectCode(t, "owner: "+fmt.Sprintf(q, tbl), err, "DG001")
		}
	}
	_, err := mig.Exec(ctx, "UPDATE deedglass.deeds SET seal = 'x'")
	if err == nil || !strings.Contains(err.Error(), "deedglass: deedglass.deeds is append-only (UPDATE refused)") {
		t.Errorf("owner UPDATE deeds: %v", err)
	}
	_, err = app.Exec(ctx, "UPDATE deedglass.deeds SET seal = 'x'")
	if err == nil || !strings.Contains(err.Error(), "permission denied for table deeds") {
		t.Errorf("app UPDATE deeds: %v", err)
	}
}

func TestChain(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	a := storetest.NewOrg(t, e.DB, "alpha") // seq 1 exists after the seed
	var sk ids.SealKeyID
	if err := e.Admin(t).QueryRow(ctx, `SELECT id FROM deedglass.seal_keys LIMIT 1`).Scan(&sk); err != nil {
		t.Fatal(err)
	}
	var seq1 string
	if err := e.Admin(t).QueryRow(ctx, `SELECT body_hash FROM deedglass.deeds WHERE org_id = $1 AND seq = 1`, a.ID).Scan(&seq1); err != nil {
		t.Fatal(err)
	}
	ins := func(tx pgx.Tx, seq int64, prev string, body []byte, hash, seal string) error {
		_, err := inSavepoint(tx, func(tx pgx.Tx) (int64, error) {
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.deeds (id, org_id, seq, kind, action_id, body, body_jcs, body_hash, prev_hash, seal, seal_key_id, envelope, issued_at)
			    VALUES ($1, $2, $3, 'action', $4, '{}', $5, $6, $7, $8, $9, '\x7b7d', pg_catalog.clock_timestamp())`,
				ids.New[ids.DeedKind](), a.ID, seq, a.Action, body, hash, prev, seal, sk)
			return 0, err
		})
		return err
	}
	b2 := []byte(`{"seq":2}`)
	tx(t, e.DB, a.ID, func(tx pgx.Tx) {
		expectCode(t, "seq 1 again with the right genesis", ins(tx, 1, storetest.Genesis(a.ID.String()), b2, storetest.Digest(b2), storetest.Seal()), "23505")
		expectCode(t, "seq 1 with the wrong prev_hash", ins(tx, 1, storetest.Digest([]byte("x")), b2, storetest.Digest(b2), storetest.Seal()), "DG003")
		expectCode(t, "seq 3 without seq 2", ins(tx, 3, seq1, b2, storetest.Digest(b2), storetest.Seal()), "DG002")
		expectCode(t, "seq 2 not linking to seq 1", ins(tx, 2, storetest.Digest([]byte("x")), b2, storetest.Digest(b2), storetest.Seal()), "DG003")
		expectCode(t, "body_hash ≠ sha256(body_jcs)", ins(tx, 2, seq1, b2, storetest.Digest([]byte("other")), storetest.Seal()), "23514")
		expectCode(t, "malformed seal", ins(tx, 2, seq1, b2, storetest.Digest(b2), "ed25519:short"), "23514")
		if err := ins(tx, 2, seq1, b2, storetest.Digest(b2), storetest.Seal()); err != nil {
			t.Errorf("seq 2 linking to seq 1: %v", err)
		}
	})
	// A fresh org: seq 1 with the correct genesis is accepted.
	o, _, _, err := e.DB.CreateOrg(ctx, store.NewOrg{Name: "Charlie", Slug: "charlie", OwnerEmail: "c@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	tx(t, e.DB, o.ID, func(tx pgx.Tx) {
		ag, err := store.CreateAgent(ctx, tx, o.ID, store.NewAgent{Name: "bot"})
		if err != nil {
			t.Fatal(err)
		}
		act, err := storetest.NewAction(ctx, tx, o.ID, ag.ID)
		if err != nil {
			t.Fatal(err)
		}
		b := []byte(`{"seq":1}`)
		if _, err := tx.Exec(ctx, `INSERT INTO deedglass.deeds (id, org_id, seq, kind, action_id, body, body_jcs, body_hash, prev_hash, seal, seal_key_id, envelope, issued_at)
		    VALUES ($1, $2, 1, 'action', $3, '{}', $4, $5, $6, $7, $8, '\x7b7d', pg_catalog.now())`,
			ids.New[ids.DeedKind](), o.ID, act, b, storetest.Digest(b), storetest.Genesis(o.ID.String()), storetest.Seal(), sk); err != nil {
			t.Errorf("seq 1 with the correct genesis: %v", err)
		}
	})
}

// The Go genesis helper equals the SQL formula for 5 golden org ids (S04 must produce the same value).
func TestGenesisGolden(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	golden := map[string]string{
		"org_00000000000000000000000000": "sha256:52bbe8714d975b0952fdf73292f8a65383f97ccbc449f47ef1b0e77cd75933c8",
		"org_01j9zz0000000000000000000a": "sha256:73f662cffb19e009dd2039dbbd59272cfc394963c20478eadeab0f6687a2dec4",
		"org_01jaaaaaaaaaaaaaaaaaaaaaaa": "sha256:38dc732582e01dfdc41797c54749eb314f94b57ca184fdf6f3aff2bf33b7dd74",
		"org_7zzzzzzzzzzzzzzzzzzzzzzzzz": "sha256:3b6c27bb862545dbb617994dbc6b9064d7b354ec0baf76a054a89e156a7948ed",
		"org_01k6r3m5p8q9v2w4x7y0z1a3b5": "sha256:d38b4021c26c6db9c38b9d0058a8374a5ca9d5b440f8fea4b6de7ebe9f174711",
	}
	c := e.Admin(t)
	for org := range golden {
		if err := ids.Check(ids.Org, org); err != nil {
			t.Fatal(err)
		}
		var sql string
		if err := c.QueryRow(ctx, `SELECT 'sha256:' || pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to('deedglass/genesis' || E'\n' || $1, 'UTF8')), 'hex')`, org).Scan(&sql); err != nil {
			t.Fatal(err)
		}
		if g := storetest.Genesis(org); g != sql || sql != golden[org] {
			t.Errorf("%s: Go %s, SQL %s, golden %s", org, g, sql, golden[org])
		}
	}
}

func TestDomains(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	c := e.App(t)
	var want []string
	for _, p := range ids.Registry() {
		want = append(want, p.Prefix+"_id")
		id, _ := ids.NewString(p.Prefix)
		if _, err := c.Exec(ctx, fmt.Sprintf("SELECT $1::deedglass.%s_id", p.Prefix), id); err != nil {
			t.Errorf("%s: a fresh id is refused: %v", p.Prefix, err)
		}
		_, err := c.Exec(ctx, fmt.Sprintf("SELECT $1::deedglass.%s_id", p.Prefix), strings.ToUpper(id))
		expectCode(t, p.Prefix+" uppercase", err, "23514")
		other := "zzz"
		if p.Prefix == "zzz" {
			other = "yyy"
		}
		_, err = c.Exec(ctx, fmt.Sprintf("SELECT $1::deedglass.%s_id", p.Prefix), other+id[len(p.Prefix):])
		expectCode(t, p.Prefix+" wrong prefix", err, "23514")
		var def string
		if err := c.QueryRow(ctx, `SELECT pg_catalog.pg_get_constraintdef(con.oid) FROM pg_constraint con
		    WHERE con.contypid = ('deedglass.' || $1)::regtype`, p.Prefix+"_id").Scan(&def); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(def, "'"+ids.Pattern(p.Prefix)+"'") {
			t.Errorf("%s_id: %s doesn't contain %s", p.Prefix, def, ids.Pattern(p.Prefix))
		}
	}
	sort.Strings(want)
	got := tables(t, c, `SELECT t.typname FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
	    WHERE n.nspname = 'deedglass' AND t.typtype = 'd' AND t.typname LIKE '%\_id'`)
	if !slices.Equal(got, want) {
		t.Errorf("id domains = %v\nwant ids.Registry() = %v", got, want)
	}
	for _, bad := range []string{"sha256:ABC", "sha256:" + strings.Repeat("0", 63)} {
		_, err := c.Exec(ctx, "SELECT $1::deedglass.digest", bad)
		expectCode(t, "digest "+bad, err, "23514")
	}
	_, err := c.Exec(ctx, "SELECT $1::deedglass.currency", "USD")
	expectCode(t, "currency USD", err, "23514")
}

func TestOtherConstraints(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	a := storetest.NewOrg(t, e.DB, "alpha")
	// The last active owner can't be removed; checked at commit.
	err := e.DB.OrgTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.memberships SET status = 'removed', removed_at = pg_catalog.now() WHERE id = $1`, a.Membership)
		return err // nil: the check is deferred
	})
	expectCode(t, "remove the last owner", err, "DG005")
	err = e.DB.OrgTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM deedglass.memberships WHERE id = $1`, a.Membership)
		return err
	})
	expectCode(t, "delete the last owner", err, "DG005")
	// Handing over ownership inside one transaction is fine.
	second := ids.New[ids.MembershipKind]()
	if err := e.DB.SystemTx(ctx, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		u, err := store.UpsertUserByEmail(ctx, tx, "second@alpha.example.test", "")
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status) VALUES ($1, $2, $3, 'admin', 'active')`, second, a.ID, u.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err = e.DB.OrgTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE deedglass.memberships SET role = 'admin' WHERE id = $1`, a.Membership); err != nil {
			return err
		}
		ct, err := tx.Exec(ctx, `UPDATE deedglass.memberships SET role = 'owner' WHERE id = $1`, second)
		if err == nil && ct.RowsAffected() != 1 {
			err = fmt.Errorf("%d rows", ct.RowsAffected())
		}
		return err
	})
	if err != nil {
		t.Errorf("hand over ownership: %v", err)
	}
	tx(t, e.DB, a.ID, func(tx pgx.Tx) {
		_, err := inSavepoint(tx, func(tx pgx.Tx) (int64, error) {
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.policy_activations (id, org_id, policy_id, version_id, mode) VALUES ($1, $2, $3, $4, 'enforce')`,
				ids.New[ids.PolicyActivationKind](), a.ID, a.Policy, a.Version)
			if err != nil {
				return 0, err
			}
			_, err = tx.Exec(ctx, `INSERT INTO deedglass.policy_activations (id, org_id, policy_id, version_id, mode) VALUES ($1, $2, $3, $4, 'enforce')`,
				ids.New[ids.PolicyActivationKind](), a.ID, a.Policy, a.Version)
			return 0, err
		})
		expectCode(t, "a second active enforce activation", err, "23505")
		_, err = inSavepoint(tx, func(tx pgx.Tx) (int64, error) {
			for i := 0; i < 2; i++ {
				if _, err := tx.Exec(ctx, `INSERT INTO deedglass.idempotency_keys (id, org_id, principal, key, method, route, request_hash, status, locked_until, expires_at)
				    VALUES ($1, $2, $3, 'k-1', 'POST', 'POST /v1/agents', $4, 'in_progress', pg_catalog.now(), pg_catalog.now() + interval '1 day')`,
					ids.New[ids.IdempotencyKind](), a.ID, a.Agent, storetest.Digest([]byte("r"))); err != nil {
					return 0, err
				}
			}
			return 0, nil
		})
		expectCode(t, "a duplicate idempotency key", err, "23505")
		_, err = inSavepoint(tx, func(tx pgx.Tx) (int64, error) {
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.api_keys (id, org_id, agent_id, mode, prefix, secret_hash, scopes)
			    VALUES ($1, $2, $3, 'live', 'Abcdefgh', $4, ARRAY['admin'])`, ids.New[ids.APIKeyKind](), a.ID, a.Agent, make([]byte, 32))
			return 0, err
		})
		expectCode(t, "an agent key with the admin scope", err, "23514")
		_, err = inSavepoint(tx, func(tx pgx.Tx) (int64, error) {
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.actions (id, org_id, agent_id, mode, tool, request_digest, cost_amount)
			    VALUES ($1, $2, $3, 'gateway', 't', $4, 100)`, ids.New[ids.ActionKind](), a.ID, a.Agent, storetest.Digest([]byte("r")))
			return 0, err
		})
		expectCode(t, "cost without a currency", err, "23514")
	})
}

func TestCheckRuntimeRole(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	r, err := e.DB.CheckRuntimeRole(ctx)
	if err != nil || !r.Clean() || r.User != "deedglass_app" || r.ServerVersionNum < 160000 || len(r.Problems()) != 0 {
		t.Errorf("app: %+v %v", r, err)
	}
	for _, c := range []struct {
		url   string
		check func(store.RoleReport) bool
	}{
		{e.MigratorURL, func(r store.RoleReport) bool { return r.OwnsSchema && !r.Superuser && !r.Clean() }},
		{e.AdminURL, func(r store.RoleReport) bool { return r.Superuser && !r.Clean() }},
	} {
		db, err := store.Open(ctx, config.NewSecret(c.url), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		r, err := db.CheckRuntimeRole(ctx)
		db.Close()
		if err != nil || !c.check(r) || len(r.Problems()) == 0 {
			t.Errorf("%+v %v", r, err)
		}
		t.Logf("%s: %s", r.User, strings.Join(r.Problems(), " "))
	}
}

func TestRepositories(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	o, u, m, err := e.DB.CreateOrg(ctx, store.NewOrg{Name: "Acme Robotics", Slug: "acme", OwnerEmail: " Founder@Example.test ", OwnerName: "Founder"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "founder@example.test" || m.Role != "owner" || o.APIVersion != "2026-10-01" || o.Timezone != "UTC" {
		t.Errorf("%+v %+v %+v", o, u, m)
	}
	// The same owner email in a second org reuses the user.
	o2, u2, _, err := e.DB.CreateOrg(ctx, store.NewOrg{Name: "Second", Slug: "second", OwnerEmail: "founder@example.test"})
	if err != nil || u2.ID != u.ID {
		t.Fatalf("second org: %v, user %s vs %s", err, u2.ID, u.ID)
	}
	if _, _, _, err := e.DB.CreateOrg(ctx, store.NewOrg{Name: "Dup", Slug: "acme", OwnerEmail: "x@example.test"}); code(err) != "23505" {
		t.Errorf("duplicate slug: %v", err)
	}
	var created []store.Agent
	tx(t, e.DB, o.ID, func(tx pgx.Tx) {
		got, err := store.GetOrg(ctx, tx, o.ID)
		if err != nil || got.Slug != "acme" {
			t.Errorf("GetOrg: %+v %v", got, err)
		}
		if _, err := store.GetOrg(ctx, tx, o2.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetOrg(other org) = %v, want ErrNotFound", err)
		}
		if got, err := store.GetOrgBySlug(ctx, tx, "acme"); err != nil || got.ID != o.ID {
			t.Errorf("GetOrgBySlug: %v", err)
		}
		if _, err := store.GetOrgBySlug(ctx, tx, "second"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetOrgBySlug(other org) = %v", err)
		}
		if got, err := store.GetUser(ctx, tx, u.ID); err != nil || got.DisplayName != "Founder" {
			t.Errorf("GetUser: %+v %v", got, err)
		}
		for i := 0; i < 5; i++ {
			a, err := store.CreateAgent(ctx, tx, o.ID, store.NewAgent{Name: fmt.Sprintf("bot-%d", i), Description: "d", Labels: map[string]string{"team": "ops"}, CreatedBy: u.ID.String()})
			if err != nil {
				t.Fatal(err)
			}
			created = append(created, a)
		}
		_, err = inSavepoint(tx, func(tx pgx.Tx) (int64, error) {
			_, err := store.CreateAgent(ctx, tx, o.ID, store.NewAgent{Name: "bot-0"})
			return 0, err
		})
		expectCode(t, "duplicate agent name", err, "23505")
		_, err = inSavepoint(tx, func(tx pgx.Tx) (int64, error) {
			_, err := store.CreateAgent(ctx, tx, o.ID, store.NewAgent{Name: "Bad Name"})
			return 0, err
		})
		expectCode(t, "bad agent name", err, "23514")
		if a, err := store.GetAgent(ctx, tx, created[2].ID); err != nil || a.Name != "bot-2" || a.Labels["team"] != "ops" || a.Description != "d" {
			t.Errorf("GetAgent: %+v %v", a, err)
		}
		// Pages of 2, newest first, with a cursor.
		var seen []string
		var cur *store.AgentCursor
		for page := 0; page < 5; page++ {
			as, next, err := store.ListAgents(ctx, tx, o.ID, 2, cur)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range as {
				seen = append(seen, a.Name)
			}
			if next == nil {
				break
			}
			cur = next
		}
		if strings.Join(seen, ",") != "bot-4,bot-3,bot-2,bot-1,bot-0" {
			t.Errorf("pages: %v", seen)
		}
		if _, _, err := store.ListAgents(ctx, tx, ids.OrgID{}, 2, nil); !errors.Is(err, store.ErrNoOrg) {
			t.Error("ListAgents with no org")
		}
		// The explicit filter and RLS agree: asking for another org's agents returns none.
		if as, _, err := store.ListAgents(ctx, tx, o2.ID, 100, nil); err != nil || len(as) != 0 {
			t.Errorf("ListAgents(other org): %d %v", len(as), err)
		}
	})
	tx(t, e.DB, o2.ID, func(tx pgx.Tx) {
		if _, err := store.GetAgent(ctx, tx, created[0].ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetAgent across orgs: %v", err)
		}
	})
	if _, err := store.Open(ctx, config.Secret{}, store.Options{}); err == nil {
		t.Error("Open without a URL")
	}
	if _, err := store.Open(ctx, config.NewSecret("postgres://%zz"), store.Options{}); err == nil || strings.Contains(err.Error(), "%zz") {
		t.Errorf("Open with a bad URL: %v", err)
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := store.Open(cctx, config.NewSecret("postgres://nobody:secret-pw@127.0.0.1:1/x?connect_timeout=1"), store.Options{}); err == nil || strings.Contains(err.Error(), "secret-pw") {
		t.Errorf("Open to a closed port: %v", err)
	}
}
