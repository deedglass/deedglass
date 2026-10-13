package undo

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/witness/l2"
)

var updateScript = flag.Bool("update-script", false, "rewrite testdata/script/*.sql")

func sessionPlan(t *testing.T, frames ...l2.Frame) *Plan {
	t.Helper()
	ev := FromSessionFrames(SessionInput{Session: obsID, ConnectorID: connID, Frames: frames})
	p, fs := Build(BuildInput{Undoes: obsID, ConnectorID: connID, Evidence: ev, Catalog: defaultCatalog()})
	if p == nil {
		t.Fatalf("no plan: %+v", fs)
	}
	return p
}

func TestScriptGolden(t *testing.T) {
	tx1 := sframe("dtx_01m3x71p6wesj7xewec6bwbw01", t0, "0/16B3748",
		lu(1, "public.orders", order("1", "paid"), order("1", "shipped")),
		l2.Change{Seq: 2, Op: "I", Table: "public.order_items", New: item("9", "1")})
	tx2 := sframe("dtx_01m3x71p6wesj7xewec6bwbw02", t0.Add(time.Second), "0/16B3900",
		l2.Change{Seq: 1, Op: "D", Table: "public.orders", Old: with(order("2", "paid"), "note", "it's a 'quoted' note\\n")})
	p := sessionPlan(t, tx1, tx2)
	got, err := Script(p, ScriptOptions{Subject: "watched session " + obsID, Connector: "pg-demo", Generated: "2026-10-17 14:05 UTC",
		Excluded: []string{"dtx_01m3x71p6wesj7xewec6bwbw03"}, Warnings: []string{"Triggers: public.orders has a trigger\nthat fires again."}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "script", "session.sql")
	if *updateScript {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("script drifted; run go test ./internal/witness/undo -run TestScriptGolden -update-script\n%s", got)
	}
	for _, must := range []string{"BEGIN;\n", "COMMIT;\n", "SET LOCAL TimeZone = 'UTC';", "'it''s a ''quoted'' note\\n'", "IF NOT FOUND THEN RAISE EXCEPTION",
		"\"status\" IS NOT NULL AND pg_catalog.format('%s', \"status\") = 'shipped'", "-- Check: Triggers: public.orders has a trigger that fires again."} {
		if !strings.Contains(got, must) {
			t.Errorf("script lacks %q", must)
		}
	}
	// Every non-comment line outside a DO block is a statement we wrote: no value ever starts a line.
	for _, line := range strings.Split(got, "\n") {
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		ok := false
		for _, pre := range []string{"BEGIN;", "COMMIT;", "SET LOCAL ", "DO $dg$ BEGIN", "  ", "END $dg$;", "INSERT INTO "} {
			ok = ok || strings.HasPrefix(line, pre)
		}
		if !ok {
			t.Errorf("unexpected line %q", line)
		}
	}
}

func TestScriptCommentsCantBreakOut(t *testing.T) {
	evil := "1\nDROP TABLE public.orders; --"
	tx := sframe("dtx_01m3x71p6wesj7xewec6bwbw01", t0, "0/1", lu(1, "public.orders", order(evil, "a"), order(evil, "b")))
	p := sessionPlan(t, tx)
	got, err := Script(p, ScriptOptions{Subject: "watched session\n" + obsID, Connector: "pg\r\ndemo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "-- restore id=1?DROP TABLE public.orders; -- in public.orders\n") ||
		!strings.Contains(got, "-- Deedglass undo SQL for watched session?"+obsID+" (connector pg??demo).\n") {
		t.Fatalf("a value escaped its comment line:\n%s", got)
	}
	if !strings.Contains(got, `"id" = '1`+"\n"+`DROP TABLE public.orders; --'::bigint`) {
		t.Fatal("the value itself stays a quoted literal")
	}
}

func TestScriptDollarTagAndRefusals(t *testing.T) {
	tx := sframe("dtx_01m3x71p6wesj7xewec6bwbw01", t0, "0/1", lu(1, "public.orders", order("1", "a"), with(order("1", "b"), "note", "x $dg$ y")))
	p := sessionPlan(t, tx)
	got, err := Script(p, ScriptOptions{Subject: "s", Connector: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "DO $dg1$ BEGIN") || strings.Contains(got, "DO $dg$ BEGIN") {
		t.Fatalf("the tag must avoid the body:\n%s", got)
	}
	if _, err := Script(nil, ScriptOptions{}); err == nil {
		t.Fatal("no plan, no script")
	}
	var r *ScriptRefusal
	if _, err := Script(&Plan{}, ScriptOptions{}); !errors.As(err, &r) || r.Error() == "" {
		t.Fatal("an empty plan is a refusal")
	}
	// Guard predicates for each value shape.
	for _, tc := range []struct {
		v    Value
		want string
	}{
		{nv, `"c" IS NULL`}, {uv, ""}, {tv("x"), `"c" IS NOT NULL AND pg_catalog.format('%s', "c") = 'x'`},
		{dv("secret"), `"c" IS NOT NULL AND pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(pg_catalog.format('%s', "c"), 'UTF8')), 'hex') = '` + strings.TrimPrefix(digestOf("secret"), "sha256:") + `'`},
	} {
		if got, err := guardPredicate("c", tc.v); err != nil || got != tc.want {
			t.Errorf("guard %+v = %q, %v", tc.v, got, err)
		}
	}
	if _, err := guardPredicate("c", xv([]byte{0xff})); !errors.As(err, &r) {
		t.Error("non-UTF-8 guard is a refusal")
	}
	if _, err := guardPredicate("c", Value{}); err == nil {
		t.Error("an empty value")
	}
	if _, err := literal(xv([]byte{0xff})); !errors.As(err, &r) {
		t.Error("non-UTF-8 literal is a refusal")
	}
	if s, _ := literal(nv); s != "NULL" {
		t.Error("NULL literal")
	}
	if _, err := literal(dv("x")); err == nil {
		t.Error("a sensitive value is never written")
	}
}
