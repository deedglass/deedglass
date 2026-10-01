// Package pgtest gives integration tests a throwaway customer database set up for the Postgres
// connector: the Acme schema (customers, orders, refunds, order_items, products, a bulk table),
// public.ship_order owned by a non-superuser role, and init applied with fresh passwords. Tests
// only: nothing in the binary imports it.
//
// The deedglass_* roles are cluster-wide, so every package that sets them up takes the same
// advisory lock first (Lock): two packages rotating deedglass_agent's password at once would
// break each other's logins.
package pgtest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector/postgres/setup"
)

// AdminURL is the throwaway cluster's superuser URL (make integration sets it), or "" to skip.
func AdminURL(t testing.TB) string {
	t.Helper()
	u := os.Getenv("DEEDGLASS_IT_ADMIN_URL")
	if u == "" || os.Getenv("DEEDGLASS_IT_THROWAWAY") != "1" {
		t.Skip("set DEEDGLASS_IT_ADMIN_URL and DEEDGLASS_IT_THROWAWAY=1 (make integration) to run the Postgres connector against a throwaway cluster")
	}
	return u
}

// WithDB returns u pointing at database db.
func WithDB(u, db string) string {
	p, err := url.Parse(u)
	if err != nil {
		panic(err)
	}
	p.Path = "/" + db
	return p.String()
}

// lockKey is the advisory lock every package takes before touching the deedglass_* roles.
const lockKey = 7_317_017

// Lock takes the cluster-wide setup lock for the rest of the test (or TestMain, with Unlock).
func Lock(ctx context.Context, adminURL string) (unlock func(), err error) {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "SELECT pg_catalog.pg_advisory_lock($1)", int64(lockKey)); err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	return func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(c, "SELECT pg_catalog.pg_advisory_unlock($1)", int64(lockKey))
		_ = conn.Close(c)
	}, nil
}

// DB is one throwaway customer database.
type DB struct {
	Name       string
	AdminURL   string // superuser, this database
	AgentDSN   string
	WitnessDSN string
	Host       string
	Port       string
}

// schema is the Acme store, smaller than examples/demo but the same shape, plus bulk (1,204 rows
// for the rows cap) and wide (600 rows for the row cap).
const schema = `
CREATE ROLE deedglass_it_acme NOLOGIN;
CREATE TABLE public.customers (id integer PRIMARY KEY, name text NOT NULL, email text NOT NULL UNIQUE, created_at timestamptz NOT NULL);
CREATE TABLE public.products (id integer PRIMARY KEY, name text NOT NULL, price_cents integer NOT NULL);
CREATE TABLE public.orders (id integer PRIMARY KEY, customer_id integer NOT NULL REFERENCES public.customers (id),
  status text NOT NULL CHECK (status IN ('pending', 'paid', 'shipped', 'cancelled', 'refunded')),
  total_cents integer NOT NULL DEFAULT 0, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL);
CREATE TABLE public.order_items (id integer PRIMARY KEY, order_id integer NOT NULL REFERENCES public.orders (id), product_id integer NOT NULL REFERENCES public.products (id), quantity integer NOT NULL);
CREATE TABLE public.refunds (id bigserial PRIMARY KEY, order_id integer NOT NULL REFERENCES public.orders (id), amount_cents integer NOT NULL CHECK (amount_cents > 0), reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE public.bulk (id integer PRIMARY KEY, flag boolean NOT NULL DEFAULT false);
CREATE TABLE public.wide (id integer PRIMARY KEY, label text NOT NULL);
INSERT INTO public.customers SELECT g, 'Customer ' || g, 'c' || g || '@acme.example', '2026-09-01T00:00:00Z' FROM generate_series(1, 20) AS g;
INSERT INTO public.products SELECT g, 'Product ' || g, 100 * g FROM generate_series(1, 5) AS g;
INSERT INTO public.orders SELECT g, 1 + g % 20, 'paid', 1000 + g, '2026-09-01T00:00:00Z'::timestamptz + g * interval '1 hour', '2026-09-01T00:00:00Z' FROM generate_series(1, 60) AS g;
INSERT INTO public.order_items SELECT g, g, 1 + g % 5, 1 FROM generate_series(1, 60) AS g;
INSERT INTO public.bulk SELECT g FROM generate_series(1, 1204) AS g;
INSERT INTO public.wide SELECT g, 'row ' || g FROM generate_series(1, 600) AS g;
ALTER TABLE public.customers OWNER TO deedglass_it_acme;
ALTER TABLE public.products OWNER TO deedglass_it_acme;
ALTER TABLE public.orders OWNER TO deedglass_it_acme;
ALTER TABLE public.order_items OWNER TO deedglass_it_acme;
ALTER TABLE public.refunds OWNER TO deedglass_it_acme;
ALTER TABLE public.bulk OWNER TO deedglass_it_acme;
ALTER TABLE public.wide OWNER TO deedglass_it_acme;
CREATE FUNCTION public.ship_order(p_order_id bigint) RETURNS TABLE (id integer, status text)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  UPDATE public.orders AS o SET status = 'shipped', updated_at = pg_catalog.now()
   WHERE o.id = p_order_id AND o.status = 'paid' RETURNING o.id, o.status
$fn$;
ALTER FUNCTION public.ship_order(bigint) OWNER TO deedglass_it_acme;
REVOKE ALL ON FUNCTION public.ship_order(bigint) FROM PUBLIC;
`

// NewDB creates database name (dropping an old one) with the Acme schema.
func NewDB(t testing.TB, adminURL, name string) *DB {
	t.Helper()
	if !strings.HasPrefix(name, "deedglass_it_") {
		t.Fatalf("pgtest: database %s must start with deedglass_it_ (make integration drops those)", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	ident := pgx.Identifier{name}.Sanitize()
	var slots []string
	rows, _ := conn.Query(ctx, "SELECT slot_name::text FROM pg_catalog.pg_replication_slots WHERE database = $1", name)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		slots = append(slots, s)
	}
	rows.Close()
	for _, s := range slots {
		_, _ = conn.Exec(ctx, "SELECT pg_catalog.pg_drop_replication_slot($1)", s)
	}
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		t.Fatal(err)
	}
	u := WithDB(adminURL, name)
	db, err := pgx.Connect(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	var exists bool
	_ = db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'deedglass_it_acme')").Scan(&exists)
	s := schema
	if exists {
		s = strings.Replace(s, "CREATE ROLE deedglass_it_acme NOLOGIN;", "", 1)
	}
	if _, err := db.Exec(ctx, s); err != nil {
		t.Fatalf("pgtest: loading the Acme schema: %v", err)
	}
	p, _ := url.Parse(adminURL)
	return &DB{Name: name, AdminURL: u, Host: p.Hostname(), Port: p.Port()}
}

// Desired is the Acme connector's config as the planner reads it.
func Desired() setup.Desired {
	return setup.Desired{AllowedSchemas: []string{"public"}, WritableTables: []string{"public.bulk", "public.orders", "public.refunds"},
		WatchedTables: []string{"public.bulk", "public.customers", "public.orders", "public.refunds"},
		Functions:     []string{"public.ship_order(bigint)"}, Provider: setup.ProviderLocal}
}

// ConfigJSON is the matching connector config.
const ConfigJSON = `{"allowed_schemas":["public"],"writable_tables":["public.bulk","public.orders","public.refunds"],
"witness":{"enabled":true,"watched_tables":["public.bulk","public.customers","public.orders","public.refunds"],"sensitive_columns":["public.customers.email"]},
"functions":[{"name":"public.ship_order","args":[{"name":"p_order_id","type":"bigint"}],"side_effect":"write","description":"Mark a paid order shipped."}],
"throwaway":true}`

// Init plans and applies setup as the superuser, then sets fresh passwords on deedglass_agent and
// deedglass_witness (they are cluster-wide; Lock first) and fills the DSNs. It returns the plan.
func (d *DB) Init(t testing.TB, want setup.Desired) *setup.Plan {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, d.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	st, err := setup.Introspect(ctx, conn, want.Want())
	if err != nil {
		t.Fatal(err)
	}
	p := setup.Build(st, want)
	if len(p.Blockers) > 0 {
		t.Fatalf("pgtest: plan blockers: %v", p.Blockers)
	}
	agentPW, _ := setup.GeneratePassword(nil)
	witnessPW, _ := setup.GeneratePassword(nil)
	pw := map[string]string{setup.RoleAgent: agentPW, setup.RoleWitness: witnessPW}
	if err := setup.Apply(ctx, conn, p, want, pw); err != nil {
		t.Fatalf("pgtest: apply: %v", err)
	}
	if err := setup.SetPasswords(ctx, conn, pw); err != nil {
		t.Fatal(err)
	}
	d.AgentDSN = fmt.Sprintf("postgres://deedglass_agent:%s@%s:%s/%s?sslmode=disable", agentPW, d.Host, d.Port, d.Name)
	d.WitnessDSN = fmt.Sprintf("postgres://deedglass_witness:%s@%s:%s/%s?sslmode=disable", witnessPW, d.Host, d.Port, d.Name)
	return p
}

// Admin connects to the database as the superuser.
func (d *DB) Admin(t testing.TB) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), d.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}
