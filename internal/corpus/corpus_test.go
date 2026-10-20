package corpus

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/witness"
)

const dir = "../../corpus"

// TestScenarios: every scenario loads, covers the six cases TY-13 names, and states a Deedglass
// outcome in the product's own words: verdicts, levels and reason codes from the witness catalog.
func TestScenarios(t *testing.T) {
	ss, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range ss {
		ids = append(ids, s.ID)
		for i, a := range s.Expected.Deedglass.Actions {
			switch a.Verdict {
			case "VERIFIED", "MISMATCH", "UNVERIFIABLE":
				if a.Level != "L0" && a.Level != "L1" && a.Level != "L2" {
					t.Errorf("%s action %d: level %q", s.ID, i+1, a.Level)
				}
			case "none":
				if a.Decision != "deny" && a.Execution != "failed" {
					t.Errorf("%s action %d: no verdict needs a deny or a failed execution", s.ID, i+1)
				}
			default:
				t.Errorf("%s action %d: verdict %q", s.ID, i+1, a.Verdict)
			}
			if a.Decision != "allow" && a.Decision != "deny" && a.Decision != "require_approval" {
				t.Errorf("%s action %d: decision %q", s.ID, i+1, a.Decision)
			}
			for _, r := range a.Reasons {
				if _, ok := witness.Lookup(witness.Code(r)); !ok {
					t.Errorf("%s action %d: %q isn't a witness reason code (docs/witness/REASONS.md)", s.ID, i+1, r)
				}
			}
			if a.Verdict == "MISMATCH" && len(a.Reasons) == 0 {
				t.Errorf("%s action %d: a MISMATCH names its reasons", s.ID, i+1)
			}
		}
		// The agent's report and the agent's committed changes differ, or the scenario says why
		// a check that compares them would be fooled.
		agent := Sum(s.Expected.DatabaseCommits.Transactions, "agent")
		outside := len(s.Expected.DatabaseCommits.Notifications) > 0
		if reflect.DeepEqual(agent, s.Expected.AgentReports.Changes) && !outside && s.Expected.Deedglass.Note == "" && s.Expected.Deedglass.OutsideGate == "" {
			t.Errorf("%s: the report equals what committed, and nothing says why the scenario is here", s.ID)
		}
		readme, err := os.ReadFile(filepath.Join(s.Dir, "README.md"))
		if err != nil || !strings.HasPrefix(string(readme), "# "+s.ID) {
			t.Errorf("%s: README.md must start with # %s", s.ID, s.ID)
		}
	}
	want := []string{"01-trigger", "02-cascade", "03-orm-reset", "04-another-writer", "05-partial-failure", "06-timeout-unknown",
		"07-missing-where", "08-rule-instead", "09-unwatched-table", "10-notify-outside", "11-api-claimed-only", "12-upsert-overwrite"}
	for _, w := range want {
		found := false
		for _, id := range ids {
			found = found || id == w
		}
		if !found {
			t.Errorf("scenario %s is missing", w)
		}
	}
	top, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if !strings.Contains(string(top), "scenarios/"+id) {
			t.Errorf("corpus/README.md doesn't list %s", id)
		}
	}
}

func TestLoadRefusals(t *testing.T) {
	good, err := os.ReadFile(filepath.Join(dir, "scenarios", "01-trigger", "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ scenario, expected string }{
		"unknown key":         {`{"id":"01-x","title":"t","tables":["public.t"],"steps":[{"session":"agent","file":"agent.sql"}],"extra":1}`, string(good)},
		"wrong id":            {`{"id":"02-y","title":"t","tables":["public.t"],"steps":[{"session":"agent","file":"agent.sql"}]}`, string(good)},
		"bad session":         {`{"id":"01-x","title":"t","tables":["public.t"],"steps":[{"session":"root","file":"agent.sql"}]}`, string(good)},
		"file outside":        {`{"id":"01-x","title":"t","tables":["public.t"],"steps":[{"session":"agent","file":"../agent.sql"}]}`, string(good)},
		"state mismatch":      {`{"id":"01-x","title":"t","tables":["public.t"],"steps":[{"session":"agent","file":"agent.sql"}]}`, string(good)},
		"api without call":    {`{"id":"01-x","title":"t","tables":["public.orders"],"steps":[{"session":"agent","file":"agent.sql","via":"api"}]}`, string(good)},
		"api for other":       {`{"id":"01-x","title":"t","tables":["public.orders"],"steps":[{"session":"other","file":"agent.sql","via":"api","api":{"method":"POST","path":"/x"}}]}`, string(good)},
		"api bad method":      {`{"id":"01-x","title":"t","tables":["public.orders"],"steps":[{"session":"agent","file":"agent.sql","via":"api","api":{"method":"TRACE","path":"/x"}}]}`, string(good)},
		"watched not a table": {`{"id":"01-x","title":"t","tables":["public.orders"],"watched":["public.audit"],"steps":[{"session":"agent","file":"agent.sql"}]}`, string(good)},
		"listen, no count":    {`{"id":"01-x","title":"t","tables":["public.orders"],"listen":["mail"],"steps":[{"session":"agent","file":"agent.sql"}]}`, string(good)},
		"bad channel":         {`{"id":"01-x","title":"t","tables":["public.orders"],"listen":["Mail-Out"],"steps":[{"session":"agent","file":"agent.sql"}]}`, string(good)},
		"agent_saw length":    {`{"id":"01-x","title":"t","tables":["public.orders"],"steps":[{"session":"agent","file":"agent.sql"},{"session":"agent","file":"agent.sql"}]}`, string(good)},
	} {
		d := filepath.Join(t.TempDir(), "01-x")
		_ = os.MkdirAll(d, 0o755)
		for f, b := range map[string]string{"scenario.json": c.scenario, "expected.json": c.expected, "setup.sql": "", "agent.sql": "SELECT 1;"} {
			_ = os.WriteFile(filepath.Join(d, f), []byte(b), 0o600)
		}
		if _, err := LoadOne(d); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestSplitSQL(t *testing.T) {
	src := `-- a comment; with a semicolon
UPDATE t SET a = 'x;y' WHERE id = 1;
INSERT INTO t VALUES ('it''s; fine');
CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END; $$;
CREATE FUNCTION g() RETURNS int LANGUAGE sql AS $body$ SELECT 1; $body$;
-- trailing comment only
`
	got := SplitSQL(src)
	if len(got) != 4 {
		t.Fatalf("%d statements: %q", len(got), got)
	}
	if !strings.HasSuffix(got[0], "WHERE id = 1") || !strings.Contains(got[1], "'it''s; fine'") || !strings.HasSuffix(got[2], "END; $$") || !strings.HasSuffix(got[3], "$body$") {
		t.Errorf("%q", got)
	}
	for _, f := range []string{"01-trigger/setup.sql", "05-partial-failure/agent.sql", "03-orm-reset/agent.sql"} {
		b, _ := os.ReadFile(filepath.Join(dir, "scenarios", f))
		if len(SplitSQL(string(b))) == 0 {
			t.Errorf("%s: no statements", f)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "scenarios", "05-partial-failure", "agent.sql"))
	if n := len(SplitSQL(string(b))); n != 3 {
		t.Errorf("05 agent.sql: %d statements, want 3", n)
	}
}

func TestParseChanges(t *testing.T) {
	got, err := ParseChanges([]string{
		"BEGIN 745", "table public.orders: UPDATE: old-key: id[integer]:42 new-tuple: id[integer]:42 status[text]:'shipped'",
		"table public.orders: UPDATE: id[integer]:501", "COMMIT 745",
		"BEGIN 746", "table public.customers: DELETE: id[integer]:7", "table public.orders: DELETE: id[integer]:1", "COMMIT 746",
		"BEGIN 747", "table public.a, public.b: TRUNCATE: (no-flags)", "COMMIT 747",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Changes{
		{"public.orders": {"update": 2}},
		{"public.customers": {"delete": 1}, "public.orders": {"delete": 1}},
		{"public.a": {"truncate": 1}, "public.b": {"truncate": 1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	for name, l := range map[string][]string{
		"no begin":   {"table public.t: INSERT: id[integer]:1"},
		"open tx":    {"BEGIN 1"},
		"commit":     {"COMMIT 1"},
		"unknown":    {"BEGIN 1", "something else", "COMMIT 1"},
		"bad change": {"BEGIN 1", "table public.t: MERGE: x", "COMMIT 1"},
	} {
		if _, err := ParseChanges(l); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDiff(t *testing.T) {
	s, err := LoadOne(filepath.Join(dir, "scenarios", "01-trigger"))
	if err != nil {
		t.Fatal(err)
	}
	o := &Observed{ID: s.ID, AgentSaw: s.Expected.AgentSaw, Commits: s.Expected.DatabaseCommits}
	if d := Diff(s, o); len(d) != 0 {
		t.Fatalf("expected facts differ from themselves: %v", d)
	}
	o.Commits = Commits{Transactions: []Tx{{Session: "agent", Changes: Changes{"public.orders": {"update": 1}}}}, State: s.Expected.DatabaseCommits.State,
		Probes: []Probe{{SQL: s.Expected.DatabaseCommits.Probes[0].SQL, Value: "1"}}}
	if d := Diff(s, o); len(d) != 2 {
		t.Errorf("want the transaction and the probe to differ: %v", d)
	}
}

func TestWithoutSchemas(t *testing.T) {
	c := Changes{"public.orders": {"update": 1}, "deedglass.ledger": {"insert": 1}}
	if got := withoutSchemas(c, []string{"deedglass"}); !reflect.DeepEqual(got, Changes{"public.orders": {"update": 1}}) {
		t.Fatalf("%v", got)
	}
	if got := withoutSchemas(c, nil); !reflect.DeepEqual(got, c) {
		t.Fatalf("%v", got)
	}
}

func TestDSNFor(t *testing.T) {
	for in, want := range map[string]string{
		"postgres://postgres@127.0.0.1:5433/postgres?sslmode=disable": "postgres://postgres@127.0.0.1:5433/corpus_01_trigger?sslmode=disable",
		"postgres://postgres@127.0.0.1:5433":                          "postgres://postgres@127.0.0.1:5433/corpus_01_trigger",
		"host=127.0.0.1 port=5433 user=postgres":                      "host=127.0.0.1 port=5433 user=postgres dbname=corpus_01_trigger",
	} {
		if got := dsnFor(in, dbName("01-trigger")); got != want {
			t.Errorf("dsnFor(%q) = %q", in, got)
		}
	}
}
