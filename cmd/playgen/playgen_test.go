package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	demosql "github.com/deedglass/deedglass/examples/demo/sql"
)

// TestPlayGenFresh keeps examples/play equal to what playgen writes (go run ./cmd/playgen).
func TestPlayGenFresh(t *testing.T) {
	files, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		old, err := os.ReadFile(filepath.Join("..", "..", "examples", "play", f.Name))
		if err != nil {
			t.Fatalf("%s: %v (run go run ./cmd/playgen)", f.Name, err)
		}
		if !bytes.Equal(old, f.Data) {
			t.Errorf("examples/play/%s is stale: run go run ./cmd/playgen", f.Name)
		}
	}
}

// TestAcmeSQLOnlyPlannedChanges: acme.sql is the product SQL with only psql meta-commands and the
// role clause changed; every other line is byte-equal.
func TestAcmeSQLOnlyPlannedChanges(t *testing.T) {
	kinds := map[string]int{}
	for _, name := range []string{"01_schema.sql", "02_seed.sql", "10_trigger_on.sql"} {
		raw, err := demosql.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		out, changes := TransformSQL(string(raw))
		for _, c := range changes {
			k, _, _ := strings.Cut(c, ":")
			kinds[k]++
		}
		kept := 0
		for _, l := range src {
			if strings.HasPrefix(l, `\set `) {
				continue
			}
			if out[kept] != l && !strings.HasSuffix(l, `\gset`) && !strings.Contains(l, "AUTHORIZATION acme_owner") {
				t.Errorf("%s: line changed unexpectedly: %q → %q", name, l, out[kept])
			}
			kept++
		}
		if kept != len(out) {
			t.Errorf("%s: %d lines kept, %d written", name, kept, len(out))
		}
	}
	if kinds["drop"] != 3 || kinds["gset"] != 1 || kinds["role"] != 1 {
		t.Errorf("changes = %v, want 3 \\set lines, 1 \\gset, 1 role clause", kinds)
	}
	all, _ := AcmeSQL()
	for _, bad := range []string{`\set`, `\gset`, "acme_owner", "CREATE ROLE", "ALTER ROLE"} {
		if strings.Contains(all, bad) {
			t.Errorf("acme.sql still holds %q", bad)
		}
	}
}

// TestParityCount: the site's parity test needs exactly 40 cases covering every decision.
func TestParityCount(t *testing.T) {
	files, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	var golden []byte
	for _, f := range files {
		if f.Name == "parity.golden.json" {
			golden = f.Data
		}
	}
	if n := bytes.Count(golden, []byte(`"fixture":`)); n != 40 {
		t.Fatalf("parity cases = %d, want 40", n)
	}
	for _, d := range []string{`"decision": "allow"`, `"decision": "deny"`, `"decision": "require_approval"`} {
		if !bytes.Contains(golden, []byte(d)) {
			t.Errorf("no case with %s", d)
		}
	}
}
