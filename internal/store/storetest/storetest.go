//go:build integration || attack

// Package storetest gives integration tests a fresh control-plane database on a THROWAWAY cluster.
//
// It refuses to run unless DEEDGLASS_IT_THROWAWAY=1, and connects with DEEDGLASS_IT_ADMIN_URL (a
// superuser on that throwaway cluster; `make integration` sets both). It creates the roles
// deedglass_migrator and deedglass_app when they are missing (password deedglass-it-throwaway),
// creates a database deedglass_it_<random> owned by the migrator, and drops it in t.Cleanup.
package storetest

//deedglass:allow-sql-build test helper: the database name is generated here (deedglass_it_ plus random hex) and the role names are fixed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/buildinfo"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/migrate"
	"github.com/deedglass/deedglass/internal/store/migrations"
)

// Password is the throwaway password of both test roles.
const Password = "deedglass-it-throwaway"

// Env is one fresh database.
type Env struct {
	Name        string    // deedglass_it_<random>
	DB          *store.DB // the pool, as deedglass_app
	AdminURL    string    // superuser, this database
	MigratorURL string
	AppURL      string
	Migrations  []migrate.Migration
}

// Options for New.
type Options struct {
	NoMigrate bool // leave the database empty (runner tests)
	MaxConns  int32
}

// New creates a database and, unless o.NoMigrate, applies every migration as the migrator.
func New(t testing.TB, o Options) *Env {
	t.Helper()
	if os.Getenv("DEEDGLASS_IT_THROWAWAY") != "1" {
		t.Fatal("refusing: set DEEDGLASS_IT_THROWAWAY=1 (throwaway clusters only; run make integration)")
	}
	admin := os.Getenv("DEEDGLASS_IT_ADMIN_URL")
	if admin == "" {
		t.Fatal("refusing: DEEDGLASS_IT_ADMIN_URL isn't set (make integration sets it for the cluster it starts)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ac, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer ac.Close(context.Background())
	ensureRoles(t, ctx, ac)

	var b [4]byte
	_, _ = rand.Read(b[:])
	name := "deedglass_it_" + hex.EncodeToString(b[:])
	for _, q := range []string{
		"CREATE DATABASE " + name + " OWNER deedglass_migrator",
		"REVOKE ALL ON DATABASE " + name + " FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + name + " TO deedglass_app",
	} {
		if _, err := ac.Exec(ctx, q); err != nil { // name is generated here, never user input
			t.Fatalf("%s: %v", q, err)
		}
	}
	e := &Env{
		Name:        name,
		AdminURL:    withDB(t, admin, name, "", ""),
		MigratorURL: withDB(t, admin, name, "deedglass_migrator", Password),
		AppURL:      withDB(t, admin, name, "deedglass_app", Password),
	}
	t.Cleanup(func() {
		if e.DB != nil {
			e.DB.Close()
		}
		if os.Getenv("DEEDGLASS_IT_KEEP") == "1" {
			t.Logf("keeping database %s", name)
			return
		}
		cctx, cc := context.WithTimeout(context.Background(), 30*time.Second)
		defer cc()
		c, err := pgx.Connect(cctx, admin)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer c.Close(cctx)
		if _, err := c.Exec(cctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})

	ms, err := migrate.Load(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	e.Migrations = ms
	if !o.NoMigrate {
		mc := e.Conn(t, e.MigratorURL)
		if _, err := migrate.Up(ctx, mc, ms, migrate.UpOptions{Env: config.Dev, AppVersion: buildinfo.Get().Version}); err != nil {
			t.Fatalf("migrate up: %v", err)
		}
	}
	e.DB, err = store.Open(ctx, config.NewSecret(e.AppURL), store.Options{MaxConns: o.MaxConns})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return e
}

// Conn opens a single connection with url and closes it at cleanup.
func (e *Env) Conn(t testing.TB, u string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, u)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

// Admin, Migrator and App open one connection each as that role, on this database.
func (e *Env) Admin(t testing.TB) *pgx.Conn    { return e.Conn(t, e.AdminURL) }
func (e *Env) Migrator(t testing.TB) *pgx.Conn { return e.Conn(t, e.MigratorURL) }
func (e *Env) App(t testing.TB) *pgx.Conn      { return e.Conn(t, e.AppURL) }

// ensureRoles creates the control-plane roles when missing, under an advisory lock so parallel
// test packages don't race. The same statements as deploy/postgres/control-plane-roles.sql.
func ensureRoles(t testing.TB, ctx context.Context, c *pgx.Conn) {
	t.Helper()
	if _, err := c.Exec(ctx, "SELECT pg_catalog.pg_advisory_lock(7297)"); err != nil {
		t.Fatal(err)
	}
	defer c.Exec(context.Background(), "SELECT pg_catalog.pg_advisory_unlock(7297)")
	for _, r := range []string{"deedglass_migrator", "deedglass_app"} {
		var ok bool
		if err := c.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = $1)", r).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		q := "ALTER ROLE " + r + " PASSWORD '" + Password + "'"
		if !ok {
			q = "CREATE ROLE " + r + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + Password + "'"
		}
		if _, err := c.Exec(ctx, q); err != nil { // fixed role names and a fixed test password
			t.Fatalf("%s: %v", r, err)
		}
	}
}

func withDB(t testing.TB, admin, db, user, pass string) string {
	t.Helper()
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("DEEDGLASS_IT_ADMIN_URL: %v", err)
	}
	u.Path = "/" + db
	if user != "" {
		u.User = url.UserPassword(user, pass)
	}
	return u.String()
}
