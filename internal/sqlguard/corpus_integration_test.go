//go:build integration

package sqlguard_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.yaml.in/yaml/v3"

	"github.com/deedglass/deedglass/internal/sqlguard"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// TestCorpusDifferential runs every `differential: true` case of testdata/corpus.yaml on a real,
// throwaway Postgres and checks that Analyze agrees with what Postgres did:
//
//   - the number of command results equals Facts.Statements (a comment-only query gives Postgres's
//     EmptyQueryResponse, which pgconn reports as a result with an empty tag; it is not counted);
//   - each result's command tag, normalised by tagKind, agrees with Facts.Kinds[i];
//   - when Postgres rejects the SQL with a syntax error (42601), Analyze reports Ambiguous or kind
//     "other". Any other Postgres error fails the case: fix it, or mark it differential: false with
//     a note.
//
// Each case runs on one superuser connection, with the simple query protocol, after a reset
// (ROLLBACK, DISCARD ALL, schema public dropped and recreated, the corpus `setup`), inside
// BEGIN ... ROLLBACK. Cases that would run server code, dangerous functions, COPY ... PROGRAM,
// role statements or ALTER SYSTEM are refused (t.Fatalf), whatever the YAML says.
//
// Every disagreement is collected and reported at the end, one t.Errorf per case.
func TestCorpusDifferential(t *testing.T) {
	c := loadDiffCorpus(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	env := storetest.New(t, storetest.Options{NoMigrate: true})
	conn, err := pgx.Connect(ctx, env.AdminURL)
	if err != nil {
		t.Fatalf("connect as admin: %v", err)
	}
	defer conn.Close(context.Background())
	pc := conn.PgConn()

	version, major := serverVersion(t, ctx, pc)
	t.Logf("differential corpus on PostgreSQL %s (major %d)", version, major)

	var (
		disagreements []string
		ran, skipped  int
	)
	for _, k := range c.Cases {
		if !k.Differential || k.Gen != "" {
			skipped++
			continue
		}
		if why := refuseDifferential(k); why != "" {
			t.Fatalf("%s is marked differential: true but %s; set differential: false", k.ID, why)
		}
		if k.MinPG > major {
			t.Logf("%s: skipped, needs PostgreSQL %d (server is %d)", k.ID, k.MinPG, major)
			skipped++
			continue
		}
		sql := *k.SQL
		if d := runDiffCase(t, ctx, pc, c.Setup, k, sql); d != "" {
			disagreements = append(disagreements, d)
		}
		ran++
	}

	t.Logf("differential corpus: %d cases ran, %d skipped, %d disagreements", ran, skipped, len(disagreements))
	for _, d := range disagreements {
		t.Error(d)
	}
}

// runDiffCase resets the database, runs one case inside BEGIN ... ROLLBACK and returns a description
// of any disagreement ("" when Postgres and Analyze agree).
func runDiffCase(t *testing.T, ctx context.Context, pc *pgconn.PgConn, setup string, k diffCase, sql string) string {
	t.Helper()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	resetDB(t, cctx, pc, setup, k.ID)
	if _, err := pc.Exec(cctx, "BEGIN").ReadAll(); err != nil {
		t.Fatalf("%s: BEGIN: %v", k.ID, err)
	}
	results, runErr := pc.Exec(cctx, sql).ReadAll()
	_, _ = pc.Exec(cctx, "ROLLBACK").ReadAll() // a case may have committed or rolled back already
	if pc.IsClosed() {
		t.Fatalf("%s: the connection closed while running %q (last error: %v)", k.ID, sql, runErr)
	}

	f := sqlguard.Analyze(sql, sqlguard.Options{})
	head := fmt.Sprintf("%s (%s)\n  sql:        %q\n", k.ID, k.Family, sql)

	if runErr != nil {
		var pgErr *pgconn.PgError
		if errors.As(runErr, &pgErr) && pgErr.Code == "42601" {
			if f.Ambiguous || f.Kind == "other" {
				return ""
			}
			return head + fmt.Sprintf("  postgres:   syntax error (42601): %s\n  classifier: kind %q, kinds %v, ambiguous false\n  want:       ambiguous, or kind \"other\"",
				pgErr.Message, f.Kind, f.Kinds)
		}
		return head + fmt.Sprintf("  postgres:   %v\n  only syntax errors (42601) are expected: fix the case or mark it differential: false with a note", runErr)
	}

	var tags []string
	for _, r := range results {
		if s := r.CommandTag.String(); s != "" {
			tags = append(tags, s)
		}
	}
	var problems []string
	if len(tags) != f.Statements {
		problems = append(problems, fmt.Sprintf("postgres ran %d statements, classifier counted %d", len(tags), f.Statements))
	}
	for i, tag := range tags {
		if i >= len(f.Kinds) {
			break
		}
		if !kindMatchesTag(f.Kinds[i], tag) {
			problems = append(problems, fmt.Sprintf("statement %d: postgres tag %q (kind %s), classifier kind %q", i+1, tag, tagKind(tag), f.Kinds[i]))
		}
	}
	if len(problems) == 0 {
		return ""
	}
	return head + fmt.Sprintf("  postgres:   tags %q\n  classifier: kinds %q, statements %d\n  - %s",
		tags, f.Kinds, f.Statements, strings.Join(problems, "\n  - "))
}

// resetDB clears whatever the previous case left (an open or failed transaction, session settings,
// prepared statements, LISTENs) and recreates schema public from setup. Everything runs outside a
// transaction with the simple protocol.
func resetDB(t *testing.T, ctx context.Context, pc *pgconn.PgConn, setup, id string) {
	t.Helper()
	_, _ = pc.Exec(ctx, "ROLLBACK").ReadAll() // only a warning when no transaction is open
	for _, q := range []string{
		"DISCARD ALL",
		"SET statement_timeout = '5s'; SET lock_timeout = '2s'",
		"DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public",
		setup,
	} {
		if _, err := pc.Exec(ctx, q).ReadAll(); err != nil {
			t.Fatalf("%s: reset: %q: %v", id, q, err)
		}
	}
}

// tagKind turns a Postgres command tag into a classifier kind: its first word, lowercased, except
// where the two disagree on the name.
//
//	"SELECT 5"                   → select   (SELECT, TABLE, VALUES, WITH ... SELECT and SELECT ... INTO)
//	"INSERT 0 1", "UPDATE 1",
//	"DELETE 3", "MERGE 2"        → insert, update, delete, merge
//	"COPY 0"                     → copy
//	"CREATE TABLE", "DROP TABLE",
//	"ALTER TABLE", "TRUNCATE TABLE",
//	"COMMENT", "GRANT", "REVOKE" → create, drop, alter, truncate, comment, grant, revoke
//	"SECURITY LABEL"             → security_label
//	"IMPORT FOREIGN SCHEMA"      → import
//	"EXPLAIN", "SHOW", "SET",
//	"RESET", "LISTEN", "NOTIFY"  → explain, show, set, reset, listen, notify
//	"DECLARE CURSOR", "FETCH 1",
//	"MOVE 1", "CLOSE CURSOR",
//	"PREPARE", "DEALLOCATE ALL"  → declare, fetch, move, close, prepare, deallocate
//	"BEGIN", "START TRANSACTION" → begin, start
//	"COMMIT", "ROLLBACK"         → commit, rollback   (END reports COMMIT; ABORT reports ROLLBACK)
//	"SAVEPOINT", "RELEASE"       → savepoint, release
//	"LOCK TABLE", "REFRESH MATERIALIZED VIEW", "ANALYZE",
//	"VACUUM", "CLUSTER", "REINDEX", "CHECKPOINT" → lock, refresh, analyze, vacuum, cluster, reindex, checkpoint
//
// kindMatchesTag then treats the transaction synonyms as one (start = begin, end = commit,
// abort = rollback), and accepts any tag for kind "execute": Postgres reports the prepared
// statement's own tag (EXECUTE of a DELETE says "DELETE 3"), which is exactly why the classifier
// calls EXECUTE ambiguous.
func tagKind(tag string) string {
	w, _, _ := strings.Cut(strings.TrimSpace(tag), " ")
	w = strings.ToLower(w)
	if k, ok := tagFirstWord[w]; ok {
		return k
	}
	return w
}

var tagFirstWord = map[string]string{
	"security": "security_label",
}

// synonyms are kinds that Postgres reports under another tag.
var synonyms = map[string]string{
	"start": "begin",
	"end":   "commit",
	"abort": "rollback",
}

func canonKind(k string) string {
	if s, ok := synonyms[k]; ok {
		return s
	}
	return k
}

func kindMatchesTag(kind, tag string) bool {
	if kind == "execute" {
		return true
	}
	return canonKind(kind) == canonKind(tagKind(tag))
}

// forbiddenSQL backs up refuseDifferential when a case's expectations are wrong: role and
// ALTER SYSTEM statements, COPY ... PROGRAM, and statements that run server code.
var forbiddenSQL = regexp.MustCompile(`(?is)\balter\s+system\b` +
	`|\b(create|alter|drop)\s+(role|user|group)\b` +
	`|\b(set|reset)\s+role\b` +
	`|\bsession\s+authorization\b` +
	`|\b(reassign|drop)\s+owned\b` +
	`|\bprogram\b` +
	`|(^|;)\s*(do|call|load)\b`)

var alterDefaultPrivileges = regexp.MustCompile(`(?is)^\s*alter\s+default\s+privileges\b`)

// refuseDifferential says why a case must never run on Postgres, or "" when it may.
func refuseDifferential(k diffCase) string {
	e := k.Expect
	switch {
	case k.SQL == nil:
		return "it has no sql"
	case strings.ContainsRune(*k.SQL, 0):
		return "it contains a NUL byte, which the wire protocol can't carry"
	case e.Category != nil && *e.Category == sqlguard.CatServerCode:
		return "its category is server_code"
	case len(e.DangerousFunctions) > 0:
		return "it calls dangerous functions " + strings.Join(e.DangerousFunctions, ", ")
	case e.CopyProgram != nil && *e.CopyProgram:
		return "it is COPY ... PROGRAM"
	case e.Category != nil && *e.Category == sqlguard.CatPrivilege &&
		(e.Kind == nil || (*e.Kind != "grant" && *e.Kind != "revoke")) && !alterDefaultPrivileges.MatchString(*k.SQL):
		return "it is a privilege statement other than GRANT, REVOKE or ALTER DEFAULT PRIVILEGES"
	case forbiddenSQL.MatchString(*k.SQL):
		return "its SQL matches " + forbiddenSQL.FindString(*k.SQL)
	}
	return ""
}

// serverVersion returns server_version and the major version number.
func serverVersion(t *testing.T, ctx context.Context, pc *pgconn.PgConn) (string, int) {
	t.Helper()
	rs, err := pc.Exec(ctx, "SHOW server_version; SHOW server_version_num").ReadAll()
	if err != nil {
		t.Fatalf("server version: %v", err)
	}
	if len(rs) != 2 || len(rs[0].Rows) != 1 || len(rs[1].Rows) != 1 {
		t.Fatalf("server version: unexpected results %v", rs)
	}
	num, err := strconv.Atoi(string(rs[1].Rows[0][0]))
	if err != nil {
		t.Fatalf("server_version_num: %v", err)
	}
	return string(rs[0].Rows[0][0]), num / 10000
}

// The corpus, as this test reads it. The unit runner (TestCorpus) has its own types; both decode
// with KnownFields, so a misspelt key fails either.
type diffCorpus struct {
	Version int        `yaml:"version"`
	Setup   string     `yaml:"setup"`
	Cases   []diffCase `yaml:"cases"`
}

type diffCase struct {
	ID           string     `yaml:"id"`
	Family       string     `yaml:"family"`
	SQL          *string    `yaml:"sql"`
	Gen          string     `yaml:"gen"`
	Note         string     `yaml:"note"`
	Differential bool       `yaml:"differential"`
	MinPG        int        `yaml:"min_pg"`
	Expect       diffExpect `yaml:"expect"`
}

type diffExpect struct {
	Kind               *string  `yaml:"kind"`
	Category           *string  `yaml:"category"`
	Kinds              []string `yaml:"kinds"`
	Statements         *int     `yaml:"statements"`
	Multi              *bool    `yaml:"multi"`
	Writes             *bool    `yaml:"writes"`
	WriteKinds         []string `yaml:"write_kinds"`
	HasWhere           *bool    `yaml:"has_where"`
	UnboundedWrite     *bool    `yaml:"unbounded_write"`
	WhereTrivial       *bool    `yaml:"where_trivial"`
	HasReturning       *bool    `yaml:"has_returning"`
	CTEWrite           *bool    `yaml:"cte_write"`
	SelectInto         *bool    `yaml:"select_into"`
	CopyProgram        *bool    `yaml:"copy_program"`
	ExplainAnalyze     *bool    `yaml:"explain_analyze"`
	LocksRows          *bool    `yaml:"locks_rows"`
	Tables             []string `yaml:"tables"`
	TablesComplete     *bool    `yaml:"tables_complete"`
	Functions          []string `yaml:"functions"`
	DangerousFunctions []string `yaml:"dangerous_functions"`
	WriteFunctions     []string `yaml:"write_functions"`
	UnknownFunctions   []string `yaml:"unknown_functions"`
	Ambiguous          *bool    `yaml:"ambiguous"`
	Reasons            []string `yaml:"reasons"`
	Bytes              *int     `yaml:"bytes"`
}

var diffFamilies = map[string]bool{
	"read": true, "write": true, "cte": true, "ddl": true, "privilege": true, "server_code": true,
	"functions": true, "multi": true, "obfuscation": true, "session": true, "transaction": true,
	"maintenance": true, "limits": true,
}

var diffGens = map[string]bool{"too_large": true, "too_deep": true, "too_many_statements": true}

// loadDiffCorpus decodes testdata/corpus.yaml strictly and checks the shape this test relies on.
func loadDiffCorpus(t *testing.T) diffCorpus {
	t.Helper()
	f, err := os.Open("testdata/corpus.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var c diffCorpus
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("corpus.yaml: %v", err)
	}
	if c.Version != 1 {
		t.Fatalf("corpus.yaml: version %d, want 1", c.Version)
	}
	if strings.TrimSpace(c.Setup) == "" {
		t.Fatal("corpus.yaml: setup is empty")
	}
	seen := map[string]bool{}
	for i, k := range c.Cases {
		switch {
		case k.ID == "":
			t.Fatalf("corpus.yaml: case %d has no id", i)
		case seen[k.ID]:
			t.Fatalf("corpus.yaml: duplicate id %s", k.ID)
		case !diffFamilies[k.Family]:
			t.Fatalf("corpus.yaml: %s: unknown family %q", k.ID, k.Family)
		case (k.SQL == nil) == (k.Gen == ""):
			t.Fatalf("corpus.yaml: %s: set exactly one of sql and gen", k.ID)
		case k.Gen != "" && !diffGens[k.Gen]:
			t.Fatalf("corpus.yaml: %s: unknown gen %q", k.ID, k.Gen)
		case k.Expect.Kind == nil || k.Expect.Category == nil || k.Expect.Statements == nil || k.Expect.Ambiguous == nil:
			t.Fatalf("corpus.yaml: %s: expect must set kind, category, statements and ambiguous", k.ID)
		}
		seen[k.ID] = true
	}
	return c
}
