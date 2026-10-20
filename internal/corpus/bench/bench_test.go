package bench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/corpus"
)

const dir = "../../../corpus"

func load(t *testing.T) []*corpus.Scenario {
	t.Helper()
	ss, err := corpus.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

// TestDiverges: in every scenario but 04 the agent's report differs from what commits without a
// gateway; 04 is there so a subject that flags everything shows a false alarm.
func TestDiverges(t *testing.T) {
	for _, s := range load(t) {
		want := s.ID != "04-another-writer"
		if got := Diverges(s); got != want {
			t.Errorf("%s: diverges %t, want %t (claim %s, commits %s)", s.ID, got, want, Claim(s), TrueEffect(s.Expected.DatabaseCommits))
		}
	}
}

func TestTrueEffect(t *testing.T) {
	byID := map[string]*corpus.Scenario{}
	for _, s := range load(t) {
		byID[s.ID] = s
	}
	for id, want := range map[string]string{
		"01-trigger":          "update 301 in public.orders",
		"03-orm-reset":        "insert 3 in public.customers; 500 rows of public.customers gone with no row change logged",
		"04-another-writer":   "update 1 in public.orders",
		"05-partial-failure":  "update 1 in public.accounts",
		"06-timeout-unknown":  "update 2 in public.accounts",
		"09-unwatched-table":  "delete 5 in public.api_keys, update 1 in public.customers",
		"10-notify-outside":   "update 1 in public.invoice_batches; 240 NOTIFY on send_invoice",
		"12-upsert-overwrite": "insert 7 in public.products, update 5 in public.products",
	} {
		if got := TrueEffect(byID[id].Expected.DatabaseCommits).String(); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	if got := Claim(byID["05-partial-failure"]).String(); got != "nothing" {
		t.Errorf("05 claim %q", got)
	}
}

func TestEffectEqualIgnoresZeros(t *testing.T) {
	a := Effect{Changes: corpus.Changes{"public.t": {"update": 1, "delete": 0}, "public.u": {"insert": 0}}, Notifications: map[string]int{"c": 0}}
	b := Effect{Changes: corpus.Changes{"public.t": {"update": 1}}}
	if !Equal(a, b) {
		t.Fatalf("%s != %s", a, b)
	}
	if Equal(b, Effect{Changes: corpus.Changes{"public.t": {"update": 2}}}) {
		t.Fatal("different counts compared equal")
	}
	if Equal(b, Effect{Changes: b.Changes, Vanished: map[string]int{"public.t": 1}}) {
		t.Fatal("vanished rows ignored")
	}
}

func TestScore(t *testing.T) {
	ss := load(t)
	var div, same *corpus.Scenario
	for _, s := range ss {
		if s.ID == "01-trigger" {
			div = s
		}
		if s.ID == "04-another-writer" {
			same = s
		}
	}
	committed := TrueEffect(div.Expected.DatabaseCommits)
	right := committed
	wrong := Claim(div)
	for name, c := range map[string]struct {
		s    *corpus.Scenario
		rep  Report
		want Score
	}{
		"caught after, right effect, record": {div, Report{Flag: FlagAfter, Effect: &right, Record: &Record{Checked: true}},
			Score{Diverges: true, Flag: FlagAfter, Caught: true, Effect: true, Record: true}},
		"caught before":             {div, Report{Flag: FlagBefore}, Score{Diverges: true, Flag: FlagBefore, Caught: true}},
		"unverifiable isn't caught": {div, Report{Flag: FlagUnverifiable}, Score{Diverges: true, Flag: FlagUnverifiable}},
		"silent, wrong effect":      {div, Report{Effect: &wrong}, Score{Diverges: true, Flag: FlagNone}},
		"record that failed":        {div, Report{Record: &Record{Problem: "x"}}, Score{Diverges: true, Flag: FlagNone}},
		"false alarm":               {same, Report{Flag: FlagAfter}, Score{Flag: FlagAfter, FalseAlarm: true}},
		"quiet when right":          {same, Report{Flag: FlagNone}, Score{Flag: FlagNone}},
	} {
		if got := score(c.s, committed, c.rep); got != c.want {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
	}
}

func TestFlagOrder(t *testing.T) {
	order := []Flag{FlagNone, FlagUnverifiable, FlagAfter, FlagBefore}
	for i := range order {
		for j := range order {
			if got := order[i].Stronger(order[j]); got != (i > j) {
				t.Errorf("%s stronger than %s: %t", order[i], order[j], got)
			}
		}
	}
}

func TestDBName(t *testing.T) {
	if got := dbName("cmd:My-Tool", "01-trigger"); got != "bench_cmd_my_tool_01_trigger" {
		t.Fatal(got)
	}
	if got := dbName(strings.Repeat("x", 80), "01-trigger"); len(got) != 63 {
		t.Fatal(len(got))
	}
}

// TestCommandSubject runs the Command adapter's answer parsing and record check with a shell
// script standing in for a checker (no database: Prepare and Finish only).
func TestCommandSubject(t *testing.T) {
	s := load(t)[0]
	tmp := t.TempDir()
	script := filepath.Join(tmp, "checker.sh")
	body := `#!/bin/sh
if [ "$1" = after ]; then
  test -s "$CORPUS_REPORT" || exit 3
  echo 'some log line'
  echo '{"flag":"after","notes":["301 rows"],"effect":{"changes":{"public.orders":{"update":301}}},"record":{"kind":"log","check":"test -f scenario.json"}}'
fi
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	c := Command{Name: "mine", Cmd: script}
	sess, err := c.Start(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Prepare(context.Background(), "postgres://unused"); err != nil {
		t.Fatal(err)
	}
	if _, handled, _ := sess.Agent(context.Background(), 0, s.Steps[0], nil); handled {
		t.Fatal("an observer must hand the step to the runner")
	}
	rep, err := sess.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Flag != FlagAfter || rep.Effect == nil || rep.Effect.String() != "update 301 in public.orders" || rep.Record == nil || !rep.Record.Checked {
		t.Fatalf("%+v %+v", rep, rep.Record)
	}
	// A bad answer is an error, not a silent pass.
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '{\"flag\":\"maybe\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sess, _ = c.Start(context.Background(), s)
	_ = sess.Prepare(context.Background(), "postgres://unused")
	if _, err := sess.Finish(context.Background()); err == nil {
		t.Fatal("flag maybe accepted")
	}
}

func TestMarkdown(t *testing.T) {
	ss := load(t)
	f := &File{StartedAt: "2026-10-20T00:00:00Z", Commit: "0123456789abcdef", Postgres: "18.6",
		Subjects: []Info{None{}.Info()}}
	for _, s := range ss {
		f.Scenarios = append(f.Scenarios, Describe(s))
		e := Claim(s)
		f.Results = append(f.Results, &Result{Scenario: s.ID, Subject: "none", Committed: TrueEffect(s.Expected.DatabaseCommits),
			Report: Report{Flag: FlagNone, Effect: &e}, Score: score(s, TrueEffect(s.Expected.DatabaseCommits), Report{Flag: FlagNone, Effect: &e})})
	}
	f.Totals = Tally(f.Subjects, f.Results)
	if f.Totals[0].Diverging != len(ss)-1 || f.Totals[0].Caught != 0 || f.Totals[0].Effect != 1 {
		t.Fatalf("%+v", f.Totals[0])
	}
	md := f.Markdown()
	for _, s := range ss {
		if !strings.Contains(md, "`"+s.ID+"`") {
			t.Errorf("the table lacks %s", s.ID)
		}
	}
	if !strings.Contains(md, "| Caught (of the scenarios where the report and the database differ) | 0 of 11 |") {
		t.Errorf("totals row missing:\n%s", md)
	}
}
