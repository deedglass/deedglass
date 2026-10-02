//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
	"github.com/deedglass/deedglass/internal/connector/postgres/setup"
)

// TestGeneratedColumnsPG18 is DL-037's carry-over: on PG 18, a watched table with a stored
// generated column must keep accepting the customer's own UPDATE and DELETE after init. PG 18
// refuses them (42P10) on a REPLICA IDENTITY FULL table in a publication that doesn't publish
// generated columns, so init sets publish_generated_columns = stored, upgrades an S17-era
// publication, and the check refuses a watched table with a virtual generated column (which PG 18
// refuses whatever the publication says).
func TestGeneratedColumnsPG18(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	conn := d.Admin(t)
	var ver int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver < 180000 {
		// Before 18 there is no publish_generated_columns: the publication is unchanged and
		// stored generated columns are always fine.
		st, err := setup.Introspect(ctx, conn, pgtest.Desired().Want())
		if err != nil {
			t.Fatal(err)
		}
		if st.Publication.GenCols != "" {
			t.Fatalf("PG %d reported generated columns %q", ver, st.Publication.GenCols)
		}
		t.Skipf("PG %d: publish_generated_columns is PG 18+", ver)
	}

	want := pgtest.Desired()
	want.WritableTables = append(append([]string{}, want.WritableTables...), "public.gencols_it")
	want.WatchedTables = append(append([]string{}, want.WatchedTables...), "public.gencols_it")
	apply := func(w setup.Desired) *setup.Plan {
		t.Helper()
		st, err := setup.Introspect(ctx, conn, w.Want())
		if err != nil {
			t.Fatal(err)
		}
		p := setup.Build(st, w)
		if len(p.Blockers) > 0 {
			t.Fatalf("blockers: %v", p.Blockers)
		}
		if err := setup.Apply(ctx, conn, p, w, nil); err != nil {
			t.Fatalf("apply: %v", err)
		}
		return p
	}
	exec := func(sql string) error {
		_, err := conn.Exec(ctx, sql)
		return err
	}
	must := func(sql string) {
		t.Helper()
		if err := exec(sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	refused := func(err error) bool {
		var pe *pgconn.PgError
		return errors.As(err, &pe) && pe.Code == "42P10"
	}
	t.Cleanup(func() {
		_ = exec("ALTER PUBLICATION deedglass_pub SET (publish_generated_columns = stored)")
		apply(pgtest.Desired())
		_ = exec("DROP TABLE IF EXISTS public.gencols_it, public.gencols_virtual_it")
	})

	// 1. A watched table with a stored generated column, after init: the customer's own writes work.
	must(`CREATE TABLE public.gencols_it (id bigint PRIMARY KEY, amount_cents bigint NOT NULL,
		amount_with_tax bigint GENERATED ALWAYS AS (amount_cents * 118 / 100) STORED)`)
	must("ALTER TABLE public.gencols_it OWNER TO deedglass_it_acme")
	must("INSERT INTO public.gencols_it (id, amount_cents) VALUES (1, 1000), (2, 2000)")
	apply(want)
	st, err := setup.Introspect(ctx, conn, want.Want())
	if err != nil {
		t.Fatal(err)
	}
	if st.Publication.GenCols != setup.GenColsStored {
		t.Fatalf("after init the publication publishes generated columns %q, want stored", st.Publication.GenCols)
	}
	if err := exec("UPDATE public.gencols_it SET amount_cents = 1500 WHERE id = 1"); err != nil {
		t.Fatalf("the customer's own UPDATE on a watched table with a stored generated column: %v", err)
	}
	if err := exec("DELETE FROM public.gencols_it WHERE id = 2"); err != nil {
		t.Fatalf("the customer's own DELETE: %v", err)
	}
	if p := setup.Build(st, want); !p.Empty() {
		t.Fatalf("a second init isn't empty:\n%s", p.Body())
	}

	// 2. An S17-era publication (generated columns not published): Postgres refuses the update, the
	// check fails with the init fix, and init plans exactly the one ALTER that repairs it.
	must("ALTER PUBLICATION deedglass_pub SET (publish_generated_columns = none)")
	if err := exec("UPDATE public.gencols_it SET amount_cents = 1600 WHERE id = 1"); !refused(err) {
		t.Fatalf("with generated columns unpublished, PG 18 should refuse the update with 42P10; got %v", err)
	}
	st, err = setup.Introspect(ctx, conn, want.Want())
	if err != nil {
		t.Fatal(err)
	}
	pc := statusOf(setup.Checks(setup.CheckInput{State: st, Desired: want, Witness: true}), "publication")
	if pc.Status != connector.CheckFail || !strings.Contains(pc.Detail, "stored generated columns") || pc.Fix == "" {
		t.Fatalf("publication check: %s %q (fix %q)", pc.Status, pc.Detail, pc.Fix)
	}
	p := apply(want)
	if body := p.Body(); !strings.Contains(body, "ALTER PUBLICATION deedglass_pub SET (publish_generated_columns = stored);") {
		t.Fatalf("init didn't upgrade the publication:\n%s", body)
	}
	if err := exec("UPDATE public.gencols_it SET amount_cents = 1600 WHERE id = 1"); err != nil {
		t.Fatalf("after init the update still fails: %v", err)
	}

	// 3. A virtual generated column: PG 18 refuses updates whatever the publication says, so the
	// check refuses to watch the table.
	must(`CREATE TABLE public.gencols_virtual_it (id bigint PRIMARY KEY, a bigint, doubled bigint GENERATED ALWAYS AS (a * 2) VIRTUAL)`)
	must("ALTER TABLE public.gencols_virtual_it OWNER TO deedglass_it_acme")
	wantV := want
	wantV.WatchedTables = append(append([]string{}, want.WatchedTables...), "public.gencols_virtual_it")
	st, err = setup.Introspect(ctx, conn, wantV.Want())
	if err != nil {
		t.Fatal(err)
	}
	wc := statusOf(setup.Checks(setup.CheckInput{State: st, Desired: wantV, Witness: true}), "witness.watched_tables")
	if wc.Status != connector.CheckFail || !strings.Contains(wc.Detail, "virtual generated columns (doubled)") || !strings.Contains(wc.Fix, "STORED") {
		t.Fatalf("watched tables check: %s %q (fix %q)", wc.Status, wc.Detail, wc.Fix)
	}
}

func statusOf(cs []connector.Check, id string) connector.Check {
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	return connector.Check{ID: id, Status: "missing"}
}
