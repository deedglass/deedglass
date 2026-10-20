// Package bench scores gateways and checkers against the intent-vs-effect corpus (corpus/ at the
// repo root, corpus/BENCHMARK.md). Each scenario is played through a Subject on a fresh database;
// the runner reads what committed from Postgres's own change log, never from the subject, and
// scores three things:
//
//   - caught: when what the agent reports differs from what the database commits, did the
//     subject flag it, before the commit (a refusal or a hold) or after it (a verdict that says
//     the effect doesn't match, or that it can't tell)?
//   - effect: did the subject's own account of what committed equal what committed?
//   - record: did the subject leave a record that a third party can check offline?
//
// A Subject is an adapter. This package has two: None (the baseline: the agent's own report, and
// nothing else) and Command (any tool driven by a shell command, as make corpus's CORPUS_TOOL).
// bench/cmd/corpus-bench adds the Deedglass Gate. Write your own by implementing Subject.
package bench

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/corpus"
)

// Flag is when a subject raised a flag in a scenario.
type Flag string

// The flags, strongest first. Before means the subject stopped or held an agent action before it
// ran (a policy refusal or an approval hold). After means an action ran and the subject's verdict
// says what committed doesn't match what was asked. Unverifiable means the subject said it can't
// tell, which a person should look at but which isn't a caught mismatch. None means none of these.
const (
	FlagNone         Flag = "none"
	FlagBefore       Flag = "before"
	FlagAfter        Flag = "after"
	FlagUnverifiable Flag = "unverifiable"
)

// Caught reports whether the flag counts as catching a mismatch: before or after.
func (f Flag) Caught() bool { return f == FlagBefore || f == FlagAfter }

// Stronger reports whether f outranks g (before, after, unverifiable, none).
func (f Flag) Stronger(g Flag) bool { return rank(f) > rank(g) }

func rank(f Flag) int {
	switch f {
	case FlagBefore:
		return 3
	case FlagAfter:
		return 2
	case FlagUnverifiable:
		return 1
	}
	return 0
}

// Effect is what committed on the agent's behalf: row changes per table and operation, rows that
// vanished with no row change in the change log (a DROP), and NOTIFY messages per channel.
type Effect struct {
	Changes       corpus.Changes `json:"changes"`
	Vanished      map[string]int `json:"vanished,omitempty"`
	Notifications map[string]int `json:"notifications,omitempty"`
}

// Normal drops zero counts and empty maps, so two effects compare by content.
func (e Effect) Normal() Effect {
	out := Effect{Changes: corpus.Changes{}}
	for t, ops := range e.Changes {
		for op, n := range ops {
			if n != 0 {
				if out.Changes[t] == nil {
					out.Changes[t] = map[string]int{}
				}
				out.Changes[t][op] = n
			}
		}
	}
	for _, src := range []struct {
		in  map[string]int
		out *map[string]int
	}{{e.Vanished, &out.Vanished}, {e.Notifications, &out.Notifications}} {
		for k, n := range src.in {
			if n != 0 {
				if *src.out == nil {
					*src.out = map[string]int{}
				}
				(*src.out)[k] = n
			}
		}
	}
	return out
}

// Equal reports whether two effects have the same content.
func Equal(a, b Effect) bool {
	return a.Normal().String() == b.Normal().String()
}

// String is one line: "update 301 in public.orders; 240 NOTIFY on send_invoice".
func (e Effect) String() string {
	n := e.Normal()
	var parts []string
	if len(n.Changes) > 0 {
		parts = append(parts, corpus.Describe(n.Changes))
	}
	for _, k := range sortedKeys(n.Vanished) {
		parts = append(parts, fmt.Sprintf("%d rows of %s gone with no row change logged", n.Vanished[k], k))
	}
	for _, k := range sortedKeys(n.Notifications) {
		parts = append(parts, fmt.Sprintf("%d NOTIFY on %s", n.Notifications[k], k))
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, "; ")
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TrueEffect is what committed on the agent's behalf in a run: the agent's transactions, the
// rows that vanished from each table with no row change logged (counted over every session's
// changes; only agents run DDL in the corpus) and the notifications.
func TrueEffect(c corpus.Commits) Effect {
	e := Effect{Changes: corpus.Sum(c.Transactions, "agent"), Notifications: c.Notifications}
	all := corpus.Changes{}
	for _, s := range []string{"agent", "other"} {
		for t, ops := range corpus.Sum(c.Transactions, s) {
			if all[t] == nil {
				all[t] = map[string]int{}
			}
			for op, n := range ops {
				all[t][op] += n
			}
		}
	}
	for t, n := range c.State {
		if n.Before < 0 {
			continue
		}
		want := n.Before + all[t]["insert"] - all[t]["delete"]
		after := n.After
		if after < 0 {
			after = 0
		}
		if after < want {
			if e.Vanished == nil {
				e.Vanished = map[string]int{}
			}
			e.Vanished[t] = want - after
		}
	}
	return e.Normal()
}

// Claim is what the agent says it did, as an effect.
func Claim(s *corpus.Scenario) Effect {
	return Effect{Changes: s.Expected.AgentReports.Changes}.Normal()
}

// Diverges reports whether the scenario's agent report differs from what the database commits
// without any gateway in front (its expected.json facts, which make corpus reproduces).
func Diverges(s *corpus.Scenario) bool {
	return !Equal(Claim(s), TrueEffect(s.Expected.DatabaseCommits))
}

// Record is a subject's record of a scenario, as the bench checked it.
type Record struct {
	Kind    string `json:"kind"`              // what it is: "deed bundle", "log file"
	Check   string `json:"check"`             // how a third party checks it (a command)
	Checked bool   `json:"checked"`           // the check passed and the record covers every action
	Problem string `json:"problem,omitempty"` // why not, when Checked is false
}

// Report is what a subject says about one scenario after its steps.
type Report struct {
	Flag Flag `json:"flag"`
	// Notes are the subject's own words per action, for the results file.
	Notes []string `json:"notes"`
	// Effect is what the subject says committed for the agent; nil when it doesn't say.
	Effect *Effect `json:"effect"`
	// Record is the subject's record; nil when it leaves none.
	Record *Record `json:"record"`
	// Actions are the subject's own per-action outcome, in its own words (optional).
	Actions []ActionOutcome `json:"actions,omitempty"`
}

// ActionOutcome is what a gateway decided and saw for one agent action.
type ActionOutcome struct {
	Tool      string   `json:"tool"`
	Decision  string   `json:"decision"`
	Rule      string   `json:"rule,omitempty"`
	Execution string   `json:"execution,omitempty"`
	Verdict   string   `json:"verdict,omitempty"`
	Level     string   `json:"level,omitempty"`
	Reasons   []string `json:"reasons,omitempty"`
	Summary   string   `json:"summary,omitempty"`
}

// Info describes a subject in the results file.
type Info struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // gateway | observer | baseline
	Version string `json:"version,omitempty"`
	Config  string `json:"config"` // how it was set up, in one or two sentences
}

// Subject is a gateway or checker under test.
type Subject interface {
	Info() Info
	// Start begins one scenario. The runner then calls the session's Prepare once, Agent for
	// every agent step, and Finish once after the facts are read.
	Start(ctx context.Context, s *corpus.Scenario) (Session, error)
	// Close releases what the subject started.
	Close() error
}

// Session is one scenario played through a subject.
type Session interface {
	// Prepare runs after setup.sql, before the change log starts, with a superuser DSN for the
	// scenario's fresh database.
	Prepare(ctx context.Context, dsn string) error
	// Agent runs one agent step (stmts are its statements, in order) and returns what the
	// agent's client saw. A gateway runs the statements itself. handled false hands the step to
	// the runner, which runs it on a direct connection (an observer, or the baseline).
	Agent(ctx context.Context, step int, st corpus.Step, stmts []string) (saw []string, handled bool, err error)
	// Finish returns the subject's report. It runs before the database is dropped.
	Finish(ctx context.Context) (Report, error)
	// IgnoreSchemas are schemas the subject writes its own bookkeeping to in the scenario's
	// database; their row changes aren't facts of the scenario.
	IgnoreSchemas() []string
}
