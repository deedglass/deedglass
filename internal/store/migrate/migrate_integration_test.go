//go:build integration

package migrate_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/store/migrate"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

var ctx = context.Background()

func TestUpFreshThenCurrent(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	c := e.Migrator(t)
	var started int
	var applied []string
	res, err := migrate.Up(ctx, c, e.Migrations, migrate.UpOptions{Env: config.Dev, AppVersion: "test",
		OnStart: func(p []migrate.Migration) { started = len(p) },
		OnApply: func(a migrate.Applied) { applied = append(applied, a.File()) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 21 || started != 21 || len(applied) != 21 || res.Current != 21 || len(res.Warnings) != 0 {
		t.Fatalf("first up: %+v", res)
	}
	res, err = migrate.Up(ctx, c, e.Migrations, migrate.UpOptions{Env: config.Dev})
	if err != nil || len(res.Applied) != 0 || res.Current != 21 {
		t.Fatalf("second up: %+v %v", res, err)
	}
	rows, err := migrate.Status(ctx, c, e.Migrations)
	if err != nil || len(rows) != 21 {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.State != migrate.StateApplied || r.AppliedAt.IsZero() || r.Checksum != r.AppliedChecksum {
			t.Errorf("%+v", r)
		}
	}
	if err := migrate.Verify(ctx, c, e.Migrations); err != nil {
		t.Fatal(err)
	}
	var by, ver string
	var ms int
	if err := c.QueryRow(ctx, `SELECT applied_by, app_version, execution_ms FROM deedglass.schema_migrations WHERE version = 1`).Scan(&by, &ver, &ms); err != nil || by != "deedglass_migrator" || ver != "test" || ms < 0 {
		t.Errorf("row: %s %s %d %v", by, ver, ms, err)
	}
	// The app role can read the table, not write it.
	app := e.App(t)
	var n int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM deedglass.schema_migrations`).Scan(&n); err != nil || n != 21 {
		t.Errorf("app reads schema_migrations: %d %v", n, err)
	}
}

func TestStatusOnEmptyDatabase(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	c := e.Migrator(t)
	rows, err := migrate.Status(ctx, c, e.Migrations)
	if err != nil || len(rows) != 21 {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.State != migrate.StatePending {
			t.Errorf("%+v", r)
		}
	}
	if err := migrate.Verify(ctx, c, e.Migrations); err != nil {
		t.Errorf("verify with nothing applied: %v", err)
	}
	res, err := migrate.Up(ctx, c, e.Migrations, migrate.UpOptions{Env: config.Dev, DryRun: true})
	if err != nil || len(res.Pending) != 21 || len(res.Applied) != 0 || res.Current != 0 {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	var exists bool
	if err := c.QueryRow(ctx, `SELECT pg_catalog.to_regclass('deedglass.schema_migrations') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Error("a dry run wrote something")
	}
}

func TestChecksumMismatch(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	c := e.Migrator(t)
	tampered := append([]migrate.Migration(nil), e.Migrations...)
	tampered[2].SQL += "\n-- an edit\n"
	tampered[2].Checksum = "sha256:" + strings.Repeat("0", 64)
	for name, err := range map[string]error{
		"up":     func() error { _, err := migrate.Up(ctx, c, tampered, migrate.UpOptions{Env: config.Dev}); return err }(),
		"verify": migrate.Verify(ctx, c, tampered),
	} {
		var ce *migrate.ChecksumError
		if !errors.Is(err, migrate.ErrChecksumMismatch) || !errors.As(err, &ce) || ce.Version != 3 || ce.Name != "policy" || ce.File != tampered[2].Checksum || ce.Applied != e.Migrations[2].Checksum {
			t.Errorf("%s: %v", name, err)
		}
	}
	rows, _ := migrate.Status(ctx, c, tampered)
	if rows[2].State != migrate.StateChanged {
		t.Errorf("status: %+v", rows[2])
	}
}

func TestDatabaseAhead(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	c := e.Migrator(t)
	if _, err := c.Exec(ctx, `INSERT INTO deedglass.schema_migrations (version, name, checksum, execution_ms, app_version)
	    VALUES (99, 'future', 'sha256:' || pg_catalog.repeat('a', 64), 1, 'v9')`); err != nil {
		t.Fatal(err)
	}
	_, err := migrate.Up(ctx, c, e.Migrations, migrate.UpOptions{Env: config.Dev})
	var ae *migrate.AheadError
	if !errors.Is(err, migrate.ErrDatabaseAhead) || !errors.As(err, &ae) || ae.Version != 99 || ae.Known != 21 {
		t.Fatalf("up: %v", err)
	}
	rows, _ := migrate.Status(ctx, c, e.Migrations)
	if last := rows[len(rows)-1]; last.State != migrate.StateUnknown || last.Version != 99 {
		t.Errorf("status: %+v", last)
	}
}

func TestConcurrentUp(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	applied := make([]int, 2)
	for i := range 2 {
		c := e.Migrator(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := migrate.Up(ctx, c, e.Migrations, migrate.UpOptions{Env: config.Dev})
			errs[i], applied[i] = err, len(res.Applied)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || applied[0]+applied[1] != 21 {
		t.Fatalf("errs %v applied %v", errs, applied)
	}
	var n int
	if err := e.Migrator(t).QueryRow(ctx, `SELECT count(*) FROM deedglass.schema_migrations`).Scan(&n); err != nil || n != 21 {
		t.Fatalf("%d rows", n)
	}
}

func TestLockTimeout(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	holder := e.Migrator(t)
	if _, err := holder.Exec(ctx, `SELECT pg_catalog.pg_advisory_lock(x'646565646d696731'::bigint)`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := migrate.Up(ctx, e.Migrator(t), e.Migrations, migrate.UpOptions{Env: config.Dev, LockTimeout: time.Second})
	if !errors.Is(err, migrate.ErrLockTimeout) || time.Since(start) < time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

func TestNotControlPlane(t *testing.T) {
	for _, tc := range []struct{ name, sql, found string }{
		{"ledger", `CREATE SCHEMA deedglass; CREATE TABLE deedglass.ledger (action_id text)`, "deedglass.ledger"},
		{"publication", `CREATE TABLE public.t (a int); CREATE PUBLICATION deedglass_pub FOR TABLE public.t`, "the deedglass_pub publication"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := storetest.New(t, storetest.Options{NoMigrate: true})
			if _, err := e.Admin(t).Exec(ctx, tc.sql); err != nil {
				t.Fatal(err)
			}
			_, err := migrate.Up(ctx, e.Migrator(t), e.Migrations, migrate.UpOptions{Env: config.Dev})
			var ne *migrate.NotControlPlaneError
			if !errors.Is(err, migrate.ErrNotControlPlane) || !errors.As(err, &ne) || ne.Found != tc.found {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestMissingRole(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	restore := migrate.SetAppRoleForTest("deedglass_app_missing_for_test")
	defer restore()
	_, err := migrate.Up(ctx, e.Migrator(t), e.Migrations, migrate.UpOptions{Env: config.Dev})
	if !errors.Is(err, migrate.ErrMissingRole) {
		t.Fatal(err)
	}
}

func TestSuperuser(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	admin := e.Admin(t)
	if _, err := migrate.Up(ctx, admin, e.Migrations, migrate.UpOptions{Env: config.Prod}); !errors.Is(err, migrate.ErrSuperuser) {
		t.Fatalf("prod: %v", err)
	}
	res, err := migrate.Up(ctx, admin, e.Migrations, migrate.UpOptions{Env: config.Dev, DryRun: true})
	if err != nil || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "superuser") {
		t.Fatalf("dev: %+v %v", res, err)
	}
}

// A failing migration rolls back alone: earlier ones stay applied, and the error names the file.
func TestFailingMigrationRollsBack(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	c := e.Migrator(t)
	ms := append([]migrate.Migration(nil), e.Migrations[:2]...)
	ms = append(ms, migrate.Migration{Version: 3, Name: "broken", SQL: "CREATE TABLE deedglass.half (a int);\nSELECT unqualified_function();", Checksum: "sha256:" + strings.Repeat("b", 64)})
	_, err := migrate.Up(ctx, c, ms, migrate.UpOptions{Env: config.Dev})
	if err == nil || !strings.Contains(err.Error(), "migration 0003_broken.sql") {
		t.Fatalf("%v", err)
	}
	var n int
	var half bool
	if err := c.QueryRow(ctx, `SELECT count(*), pg_catalog.to_regclass('deedglass.half') IS NOT NULL FROM deedglass.schema_migrations`).Scan(&n, &half); err != nil || n != 2 || half {
		t.Errorf("after the failure: %d applied, half table %v", n, half)
	}
}
