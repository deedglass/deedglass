//go:build integration

package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// orgTables lists every control-plane table with an org_id column, from the catalog.
func purgeTables(t *testing.T, c *pgx.Conn) []string {
	t.Helper()
	rows, err := c.Query(context.Background(), `SELECT c.relname FROM pg_catalog.pg_class AS c
	    JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
	    JOIN pg_catalog.pg_attribute AS a ON a.attrelid = c.oid AND a.attname = 'org_id' AND NOT a.attisdropped
	   WHERE n.nspname = 'deedglass' AND c.relkind = 'r' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// fingerprint is a digest of every row an org has in every org table (and its orgs row).
func fingerprint(t *testing.T, c *pgx.Conn, tables []string, org string) (string, map[string]int) {
	t.Helper()
	h := sha256.New()
	counts := map[string]int{}
	for _, tbl := range append([]string{"orgs"}, tables...) {
		col := "org_id"
		if tbl == "orgs" {
			col = "id"
		}
		// tbl comes from the catalog query above, never from input.
		rows, err := c.Query(context.Background(), `SELECT t::text FROM deedglass.`+tbl+` AS t WHERE t.`+col+` = $1 ORDER BY t::text`, org) //deedglass:allow-sql-build catalog table names in a test
		if err != nil {
			t.Fatalf("%s: %v", tbl, err)
		}
		rs, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		counts[tbl] = len(rs)
		h.Write([]byte(tbl + "\x00" + strings.Join(rs, "\x00")))
	}
	return hex.EncodeToString(h.Sum(nil)), counts
}

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// TestPurgeOrg (S35 §6): purge one org; every one of its rows is gone, the other org is unchanged
// byte for byte, a wrong confirmation deletes nothing, and the runtime role can use neither the
// function nor the trigger's opening.
func TestPurgeOrg(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	ctx := context.Background()
	a := storetest.NewOrg(t, e.DB, "purge-a")
	b := storetest.NewOrg(t, e.DB, "purge-b")
	mig := e.Conn(t, e.MigratorURL)
	adm := e.Conn(t, e.AdminURL)
	tables := purgeTables(t, adm)
	if len(tables) < 30 {
		t.Fatalf("only %d org tables found", len(tables))
	}
	// A person in both orgs must survive purging one of them.
	var shared string
	if err := adm.QueryRow(ctx, `SELECT user_id FROM deedglass.memberships WHERE org_id = $1 AND role = 'owner'`, b.ID).Scan(&shared); err != nil {
		t.Fatal(err)
	}
	if _, err := adm.Exec(ctx, `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status)
	    VALUES ($1, $2, $3, 'viewer', 'active')`, ids.New[ids.MembershipKind]().String(), a.ID, shared); err != nil {
		t.Fatal(err)
	}
	fpA, countsA := fingerprint(t, adm, tables, a.ID.String())
	fpB, _ := fingerprint(t, adm, tables, b.ID.String())
	for _, tbl := range tables {
		if countsA[tbl] == 0 {
			t.Errorf("the fixture has no %s row for org A, so the purge of it isn't tested", tbl)
		}
	}

	// Wrong confirmation: DG007, nothing deleted.
	_, err := mig.Exec(ctx, `SELECT deedglass.purge_org($1, 'purge-b')`, a.ID)
	if pgCode(err) != "DG007" || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Fatalf("wrong confirm: %v", err)
	}
	if fp, _ := fingerprint(t, adm, tables, a.ID.String()); fp != fpA {
		t.Fatal("a refused purge changed org A")
	}
	// The runtime role: no EXECUTE on the function, no DELETE on history even with the setting.
	app := e.Conn(t, e.AppURL)
	if _, err := app.Exec(ctx, `SELECT deedglass.purge_org($1, 'purge-a')`, a.ID); pgCode(err) != "42501" {
		t.Fatalf("app role called purge_org: %v", err)
	}
	if _, err := app.Exec(ctx, `BEGIN; SELECT pg_catalog.set_config('deedglass.purging_org', $1, true); SELECT pg_catalog.set_config('deedglass.org_id', $1, true)`, a.ID); err == nil {
		_, err = app.Exec(ctx, `DELETE FROM deedglass.deeds WHERE org_id = $1`, a.ID)
		_, _ = app.Exec(ctx, `ROLLBACK`)
		if pgCode(err) != "42501" {
			t.Fatalf("app role deleted deeds: %v", err)
		}
	}
	// The owner without purge_org still meets the append-only trigger.
	if _, err := mig.Exec(ctx, `DELETE FROM deedglass.deeds WHERE org_id = $1`, a.ID); pgCode(err) != "DG001" {
		t.Fatalf("migrator delete outside purge_org: %v", err)
	}
	// The setting alone, for another org, opens nothing either.
	tx, err := mig.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tx.Exec(ctx, `SELECT pg_catalog.set_config('deedglass.purging_org', $1, true)`, b.ID)
	_, err = tx.Exec(ctx, `DELETE FROM deedglass.deeds WHERE org_id = $1`, a.ID)
	_ = tx.Rollback(ctx)
	if pgCode(err) != "DG001" {
		t.Fatalf("a purge setting for B deleted A's deeds: %v", err)
	}

	// The purge.
	var out []byte
	if err := mig.QueryRow(ctx, `SELECT deedglass.purge_org($1, 'purge-a')::text`, a.ID).Scan(&out); err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range tables {
		if got, _ := res[tbl].(float64); int(got) != countsA[tbl] {
			t.Errorf("purge_org reported %v %s rows, the org had %d", res[tbl], tbl, countsA[tbl])
		}
	}
	if fp, counts := fingerprint(t, adm, tables, a.ID.String()); counts["orgs"] != 0 {
		t.Fatalf("org A still exists: %v %s", counts, fp)
	} else {
		for tbl, n := range counts {
			if n != 0 {
				t.Errorf("%d %s rows of org A left", n, tbl)
			}
		}
	}
	if fp, _ := fingerprint(t, adm, tables, b.ID.String()); fp != fpB {
		t.Fatal("purging A changed org B")
	}
	var users int
	_ = adm.QueryRow(ctx, `SELECT count(*) FROM deedglass.users WHERE id = $1`, shared).Scan(&users)
	if users != 1 {
		t.Fatal("a person who is also in org B was deleted")
	}
	_ = adm.QueryRow(ctx, `SELECT count(*) FROM deedglass.users WHERE id = $1`, a.Owner).Scan(&users)
	if users != 0 || res["users"].(float64) < 1 {
		t.Fatalf("org A's own owner wasn't deleted (users %v)", res["users"])
	}
	// After the purge, the opening is closed again in that transaction's successors.
	if _, err := mig.Exec(ctx, `DELETE FROM deedglass.deeds WHERE org_id = $1`, b.ID); pgCode(err) != "DG001" {
		t.Fatalf("after a purge: %v", err)
	}
}

// TestBackupRole (S35 §6): deploy/cloud/postgres/20-backup-role.sql's role reads every row of every
// org table despite row-level security, and can't write.
func TestBackupRole(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	ctx := context.Background()
	storetest.NewOrg(t, e.DB, "backup-a")
	storetest.NewOrg(t, e.DB, "backup-b")
	adm := e.Conn(t, e.AdminURL)
	src, err := os.ReadFile(filepath.Join("..", "..", "deploy", "cloud", "postgres", "20-backup-role.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var stmts []string
	for _, l := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(l, "--") || strings.HasPrefix(l, `\`) || strings.TrimSpace(l) == "" {
			continue
		}
		stmts = append(stmts, l)
	}
	sql := strings.NewReplacer(`:'backup_password'`, `'it-backup-throwaway'`, `:"db"`, `"`+e.Name+`"`).Replace(strings.Join(stmts, "\n"))
	_, _ = adm.Exec(ctx, `DROP ROLE IF EXISTS deedglass_backup`)
	if _, err := adm.Exec(ctx, sql); err != nil {
		t.Fatalf("20-backup-role.sql: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adm.Exec(context.Background(), `REVOKE ALL ON DATABASE "`+e.Name+`" FROM deedglass_backup`) //deedglass:allow-sql-build generated test database name
		_, _ = adm.Exec(context.Background(), `DROP ROLE IF EXISTS deedglass_backup`)
	})
	u, err := url.Parse(e.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("deedglass_backup", "it-backup-throwaway")
	bc, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close(ctx)
	for _, tbl := range purgeTables(t, adm) {
		var want, got int
		_ = adm.QueryRow(ctx, `SELECT count(*) FROM deedglass.`+tbl).Scan(&want)                   //deedglass:allow-sql-build catalog table names in a test
		if err := bc.QueryRow(ctx, `SELECT count(*) FROM deedglass.`+tbl).Scan(&got); err != nil { //deedglass:allow-sql-build catalog table names in a test
			t.Fatalf("%s: %v", tbl, err)
		}
		if got != want || want == 0 {
			t.Errorf("%s: the backup role sees %d of %d rows", tbl, got, want)
		}
	}
	if _, err := bc.Exec(ctx, `INSERT INTO deedglass.waitlist (id, email) VALUES ($1, 'x@example.test')`, ids.New[ids.WaitlistKind]().String()); err == nil {
		t.Fatal("the backup role wrote a row")
	}
}
