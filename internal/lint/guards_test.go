package lint

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// repoRoot walks up from the test's directory to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

func TestRepoClean(t *testing.T) {
	findings, err := Check(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Errorf("%s", f)
	}
}

// module writes files (relative path to source) into a fresh module directory and returns it.
func module(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["go.mod"] = "module example.com/m\n\ngo 1.27\n"
	for rel, src := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// want runs one guard over files and checks the findings against want, given as "file:line" lines.
func want(t *testing.T, g Guard, files map[string]string, want ...string) {
	t.Helper()
	got, err := Check(module(t, files), g)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, f := range got {
		if f.Guard != string(g) {
			t.Errorf("finding from %s while running only %s: %s", f.Guard, g, f)
		}
		lines = append(lines, f.File+":"+strconv.Itoa(f.Line))
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		var all []string
		for _, f := range got {
			all = append(all, f.String())
		}
		t.Errorf("%s findings:\n%s\nwant at:\n%s", g, strings.Join(all, "\n"), strings.Join(want, "\n"))
	}
}

const g1Bad = `package store

import (
	"context"
	"fmt"
)

type db interface {
	Exec(ctx context.Context, sql string, args ...any) (any, error)
	QueryRow(ctx context.Context, sql string, args ...any) any
}

func byName(ctx context.Context, c db, name string) {
	c.QueryRow(ctx, "SELECT id FROM accounts WHERE name = '"+name+"'")
}

func drop(ctx context.Context, c db, table string) {
	q := fmt.Sprintf("DROP TABLE %s", table)
	c.Exec(ctx, q)
}

func filter(ctx context.Context, c db, where string) {
	q := "SELECT id FROM accounts"
	q += " WHERE " + where
	c.QueryRow(ctx, q)
}

const selectByName = "SELECT id FROM accounts WHERE name = %s"

func viaConstFormat(ctx context.Context, c db, name string) {
	c.QueryRow(ctx, fmt.Sprintf(selectByName, name))
}
`

const g1NearMiss = `package store

import (
	"context"
	"fmt"
)

type db interface {
	Exec(ctx context.Context, sql string, args ...any) (any, error)
	QueryRow(ctx context.Context, sql string, args ...any) any
}

const cols = "id, name"

func byName(ctx context.Context, c db, name string) {
	c.QueryRow(ctx, "SELECT "+cols+" FROM accounts WHERE name = $1", name)
	q := "SELECT " + cols + " FROM accounts"
	q += " WHERE name = $1"
	c.QueryRow(ctx, q, name)
	c.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM accounts", cols))
}

func message(name string) string {
	// Built from a value but never run: an error message, not SQL.
	return fmt.Sprintf("could not update %s", name)
}

func notRun(ctx context.Context, c db, name string) {
	s := fmt.Sprintf("DELETE FROM accounts WHERE name = '%s'", name)
	_ = s
	c.Exec(ctx, "SELECT 1")
}
`

func TestG1(t *testing.T) {
	want(t, G1, map[string]string{"internal/store/bad.go": g1Bad},
		"internal/store/bad.go:14", "internal/store/bad.go:18", "internal/store/bad.go:24", "internal/store/bad.go:31")
	want(t, G1, map[string]string{"internal/store/ok.go": g1NearMiss})
	// The marker exempts a file, but only with a reason.
	marked := strings.Replace(g1Bad, "package store\n", "package store\n\n//deedglass:allow-sql-build table names come from the catalog\n", 1)
	want(t, G1, map[string]string{"internal/store/bad.go": marked})
	bare := strings.Replace(g1Bad, "package store\n", "package store\n\n//deedglass:allow-sql-build\n", 1)
	want(t, G1, map[string]string{"internal/store/bad.go": bare},
		"internal/store/bad.go:3", "internal/store/bad.go:16", "internal/store/bad.go:20", "internal/store/bad.go:26", "internal/store/bad.go:33")
	// Test files are not scanned.
	want(t, G1, map[string]string{"internal/store/bad_test.go": g1Bad})
}

const g2Bad = `package fetch

import (
	"crypto/tls"
	"net"
	"net/http"
)

func get(u string) {
	c := &http.Client{}
	_, _ = c.Get(u)
	_, _ = http.Get(u)
	_ = http.DefaultClient
	_ = new(http.Transport)
	_, _ = net.Dial("tcp", u)
	_, _ = (&net.Dialer{}).Dial("tcp", u)
	_, _ = tls.Dial("tcp", u, nil)
}
`

const g2NearMiss = `package fetch

import "net/http"

type caller struct {
	client *http.Client
	tr     http.RoundTripper
}

func (c caller) do(r *http.Request) (*http.Response, error) { return c.client.Do(r) }
`

func TestG2(t *testing.T) {
	want(t, G2, map[string]string{"internal/fetch/fetch.go": g2Bad},
		"internal/fetch/fetch.go:10", "internal/fetch/fetch.go:12", "internal/fetch/fetch.go:13",
		"internal/fetch/fetch.go:14", "internal/fetch/fetch.go:15", "internal/fetch/fetch.go:16", "internal/fetch/fetch.go:17")
	want(t, G2, map[string]string{"internal/fetch/fetch.go": g2NearMiss})
	// The egress package itself may build clients and dial; its subpackages may not.
	want(t, G2, map[string]string{"internal/connector/egress/dial.go": strings.Replace(g2Bad, "package fetch", "package egress", 1)})
	want(t, G2, map[string]string{"internal/connector/egress/sub/dial.go": "package sub\n\nimport \"net\"\n\nvar _ = net.Dial\n"},
		"internal/connector/egress/sub/dial.go:5")
}

const g3Bad = `package serve

import (
	"context"
	"log/slog"
)

func run(ctx context.Context, log *slog.Logger, tok, pw string) {
	log.Info("signed in", "user", "u1", "token", tok)
	log.WarnContext(ctx, "retry", "Authorization", tok)
	slog.Error("open failed", slog.String("dsn", pw))
	log.With("API_KEY", tok).Debug("call")
	log.Log(ctx, slog.LevelInfo, "rotated", "refresh_token", tok)
	_ = slog.Any("client_secret", pw)
	slog.Info("x", "n", 1, "db_password", pw)
}
`

const g3NearMiss = `package serve

import (
	"context"
	"log/slog"
)

func run(ctx context.Context, log *slog.Logger, id string) {
	log.Info("token", "token_id", id)
	log.InfoContext(ctx, "secret rotated", "secret_id", id, "tokens", 3)
	slog.String("token_kind", "api")
	log.Info("x", "k", "password")
}
`

func TestG3(t *testing.T) {
	want(t, G3, map[string]string{"cmd/serve/serve.go": g3Bad},
		"cmd/serve/serve.go:9", "cmd/serve/serve.go:10", "cmd/serve/serve.go:11", "cmd/serve/serve.go:12",
		"cmd/serve/serve.go:13", "cmd/serve/serve.go:14", "cmd/serve/serve.go:15")
	want(t, G3, map[string]string{"cmd/serve/serve.go": g3NearMiss})
	want(t, G3, map[string]string{"internal/vault/log.go": strings.Replace(g3Bad, "package serve", "package vault", 1)})
}

const g4Bad = `package tlsconf

import "crypto/tls"

func conf() *tls.Config {
	c := &tls.Config{InsecureSkipVerify: true}
	c.InsecureSkipVerify = false
	return c
}
`

const g4NearMiss = `package tlsconf

import "crypto/tls"

func conf() *tls.Config { return &tls.Config{ServerName: "example.com", MinVersion: tls.VersionTLS12} }
`

func TestG4(t *testing.T) {
	want(t, G4, map[string]string{"internal/tlsconf/conf.go": g4Bad},
		"internal/tlsconf/conf.go:6", "internal/tlsconf/conf.go:7")
	want(t, G4, map[string]string{"internal/tlsconf/conf.go": g4NearMiss, "internal/tlsconf/conf_test.go": g4Bad})
}

func TestG5(t *testing.T) {
	want(t, G5, map[string]string{
		"internal/a/a.go": "package a\n\nimport \"math/rand\"\n\nvar _ = rand.Int\n",
		"internal/b/b.go": "package b\n\nimport (\n\t\"fmt\"\n\tmr \"math/rand/v2\"\n)\n\nvar _, _ = fmt.Sprint, mr.Int\n",
	}, "internal/a/a.go:3", "internal/b/b.go:5")
	want(t, G5, map[string]string{
		"internal/a/a.go":      "package a\n\nimport \"crypto/rand\"\n\nvar _ = rand.Read\n",
		"internal/a/a_test.go": "package a\n\nimport \"math/rand\"\n\nvar _ = rand.Int\n",
		"testdata/x/x.go":      "package x\n\nimport \"math/rand\"\n\nvar _ = rand.Int\n",
		"web/x/x.go":           "package x\n\nimport \"math/rand\"\n\nvar _ = rand.Int\n",
		"internal/gen/gen.go":  "// Code generated by gen; DO NOT EDIT.\n\npackage gen\n\nimport \"math/rand\"\n\nvar _ = rand.Int\n",
		"internal/demo/d.go":   "package demo\n\n//deedglass:allow-math-rand seeded demo data, never a secret\n\nimport \"math/rand/v2\"\n\nvar _ = rand.Int\n",
	})
	// A bare marker exempts nothing and is itself reported.
	want(t, G5, map[string]string{
		"internal/demo/d.go": "package demo\n\n//deedglass:allow-math-rand\n\nimport \"math/rand/v2\"\n\nvar _ = rand.Int\n",
	}, "internal/demo/d.go:3", "internal/demo/d.go:5")
}

func TestUnknownGuard(t *testing.T) {
	if _, err := Check(t.TempDir(), "G9"); err == nil {
		t.Fatal("Check accepted an unknown guard")
	}
}
