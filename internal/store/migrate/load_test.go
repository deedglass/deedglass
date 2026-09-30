package migrate

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func file(name, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("-- " + name + ": test file.\n" + body)}
}

func TestLoad(t *testing.T) {
	good := func() fstest.MapFS {
		return fstest.MapFS{
			"0001_foundation.sql": file("0001_foundation.sql", "CREATE SCHEMA IF NOT EXISTS deedglass;\n"),
			"0002_identity.sql": file("0002_identity.sql", `CREATE FUNCTION deedglass.f() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  RETURN NEW;
END $fn$;
COMMENT ON FUNCTION deedglass.f() IS 'Begin
commit';
/* a block comment
BEGIN
*/
`),
			"embed.go": {Data: []byte("package migrations\n")},
		}
	}
	cases := []struct {
		name   string
		mutate func(fstest.MapFS)
		err    string
	}{
		{name: "good", mutate: func(fstest.MapFS) {}},
		{name: "a gap", mutate: func(f fstest.MapFS) {
			f["0003_policy.sql"] = file("0003_policy.sql", "SELECT 1;\n")
			delete(f, "0002_identity.sql")
		}, err: "migration 0002 is missing (found 0003_policy.sql next)"},
		{name: "duplicate 0002", mutate: func(f fstest.MapFS) { f["0002_other.sql"] = file("0002_other.sql", "SELECT 1;\n") },
			err: "version 0002 is also used by"},
		{name: "uppercase name", mutate: func(f fstest.MapFS) { f["0003_Identity.sql"] = file("0003_Identity.sql", "") },
			err: "migration 0003_Identity.sql: the name must be NNNN_name.sql"},
		{name: "up.sql name", mutate: func(f fstest.MapFS) { f["0003_policy.up.sql"] = file("0003_policy.up.sql", "") },
			err: "the name must be NNNN_name.sql"},
		{name: "version zero", mutate: func(f fstest.MapFS) { f["0000_zero.sql"] = file("0000_zero.sql", "") },
			err: "versions start at 0001"},
		{name: "BEGIN line", mutate: func(f fstest.MapFS) { f["0003_policy.sql"] = file("0003_policy.sql", "SELECT 1;\nBEGIN;\nSELECT 2;\n") },
			err: "migration 0003_policy.sql line 3: BEGIN: the runner owns transactions"},
		{name: "indented commit", mutate: func(f fstest.MapFS) { f["0003_policy.sql"] = file("0003_policy.sql", "SELECT 1;\n   commit;\n") },
			err: "line 3: COMMIT"},
		{name: "rollback", mutate: func(f fstest.MapFS) { f["0003_policy.sql"] = file("0003_policy.sql", "ROLLBACK;\n") },
			err: "line 2: ROLLBACK"},
		{name: "start transaction", mutate: func(f fstest.MapFS) { f["0003_policy.sql"] = file("0003_policy.sql", "START TRANSACTION;\n") },
			err: "line 2: START TRANSACTION"},
		{name: "concurrently", mutate: func(f fstest.MapFS) {
			f["0003_policy.sql"] = file("0003_policy.sql", "SELECT 1;\n\nCREATE INDEX CONCURRENTLY x ON deedglass.t (a);\n")
		}, err: "migration 0003_policy.sql line 4: CONCURRENTLY"},
		{name: "a CR byte", mutate: func(f fstest.MapFS) { f["0003_policy.sql"] = file("0003_policy.sql", "SELECT 1;\r\n") },
			err: "line 2: the file has a carriage return"},
		{name: "a BOM", mutate: func(f fstest.MapFS) {
			f["0003_policy.sql"] = &fstest.MapFile{Data: append([]byte{0xEF, 0xBB, 0xBF}, file("0003_policy.sql", "").Data...)}
		}, err: "byte-order mark"},
		{name: "no header", mutate: func(f fstest.MapFS) { f["0003_policy.sql"] = &fstest.MapFile{Data: []byte("SELECT 1;\n")} },
			err: `line 1: the first line must be the header comment "-- 0003_policy.sql: …"`},
		{name: "header names another file", mutate: func(f fstest.MapFS) { f["0003_policy.sql"] = file("0003_other.sql", "") },
			err: "line 1: the first line must be the header"},
		{name: "header without a purpose", mutate: func(f fstest.MapFS) {
			f["0003_policy.sql"] = &fstest.MapFile{Data: []byte("-- 0003_policy.sql: \nSELECT 1;\n")}
		}, err: "line 1: the first line must be the header"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := good()
			tc.mutate(f)
			ms, err := Load(f)
			if tc.err == "" {
				if err != nil {
					t.Fatal(err)
				}
				if len(ms) != 2 || ms[0].Version != 1 || ms[1].Name != "identity" || ms[1].File() != "0002_identity.sql" {
					t.Fatalf("%+v", ms)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("err = %v\nwant %q", err, tc.err)
			}
			var le *LoadError
			if !errors.As(err, &le) {
				t.Fatalf("%T is not a *LoadError", err)
			}
		})
	}
}

func TestChecksumStable(t *testing.T) {
	f := fstest.MapFS{"0001_foundation.sql": file("0001_foundation.sql", "SELECT 1;\n")}
	a, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Load(f)
	if a[0].Checksum != b[0].Checksum || !strings.HasPrefix(a[0].Checksum, "sha256:") || len(a[0].Checksum) != 71 {
		t.Fatalf("%s vs %s", a[0].Checksum, b[0].Checksum)
	}
	f["0001_foundation.sql"] = file("0001_foundation.sql", "SELECT 2;\n")
	c, _ := Load(f)
	if c[0].Checksum == a[0].Checksum {
		t.Fatal("different bytes, same checksum")
	}
}

func TestLoadEmptyAndErrors(t *testing.T) {
	ms, err := Load(fstest.MapFS{})
	if err != nil || len(ms) != 0 {
		t.Fatalf("%v %v", ms, err)
	}
	e := &LoadError{Reason: "r"}
	if e.Error() != "migrations: r" {
		t.Fatal(e.Error())
	}
}

func TestStatementLineStarts(t *testing.T) {
	src := "SELECT 'a\nBEGIN'; -- c\n$x$\nBEGIN\n$x$;\n/* /* nested */\nBEGIN */ COMMIT;\n  END;\n"
	var got []string
	for _, s := range statementLineStarts(src) {
		got = append(got, s.text)
	}
	// Line 2 is inside a string, lines 4-5 inside a dollar body, line 7 inside the block comment.
	want := []string{"SELECT 'a", "$x$", "/* /* nested */", "END;"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", got, want)
	}
}
