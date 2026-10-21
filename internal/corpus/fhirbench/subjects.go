package fhirbench

//deedglass:allow-exec the benchmark's Command adapter: runs the checker command the person running make fhir-bench names, and the record check it prints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/deedglass/deedglass/internal/corpus/bench"
)

// Report is what a subject says about one scenario after its steps. The flags, the record and the
// per-action outcome are the corpus benchmark's (internal/corpus/bench).
type Report struct {
	Flag    bench.Flag            `json:"flag"`
	Notes   []string              `json:"notes"`
	Effect  *Effect               `json:"effect"` // nil: it doesn't say what was stored
	Record  *bench.Record         `json:"record"`
	Actions []bench.ActionOutcome `json:"actions,omitempty"`
}

// Env is what a session gets for one scenario on one server.
type Env struct {
	Server   string // "medplum"
	BaseURL  string // the FHIR base, like http://127.0.0.1:8103/fhir/R4
	Token    string // the agent's client's bearer token
	ClientID string // the agent's client (Medplum: the ClientApplication id)
	// WitnessDSN reaches the server's database as a role with REPLICATION (for Proven), and
	// AdminDSN as its owner (for an observer that reads the database itself).
	WitnessDSN string
	AdminDSN   string
}

// Subject is a gateway or checker under test.
type Subject interface {
	Info() bench.Info
	Start(ctx context.Context, s *Scenario, env Env) (Session, error)
	Close() error
}

// Session is one scenario played through a subject.
type Session interface {
	// Agent carries one agent call and returns what the agent's client saw. handled false hands
	// the call to the runner, which sends it straight to the server with the agent's token.
	Agent(ctx context.Context, step int, st Step, req Request) (saw Exchange, handled bool, err error)
	// Finish returns the subject's report, after the facts were read.
	Finish(ctx context.Context, run *Run) (Report, error)
}

// None is the baseline: no gateway and no checker; the agent's report is the only account.
type None struct{}

// Info implements Subject.
func (None) Info() bench.Info {
	return bench.Info{Name: "none", Kind: "baseline", Config: "No gateway or checker: the calls go straight to the FHIR server and the agent's report is the only account of what was stored."}
}

// Start implements Subject.
func (None) Start(_ context.Context, s *Scenario, _ Env) (Session, error) { return noneSession{s}, nil }

// Close implements Subject.
func (None) Close() error { return nil }

type noneSession struct{ s *Scenario }

func (noneSession) Agent(context.Context, int, Step, Request) (Exchange, bool, error) {
	return Exchange{}, false, nil
}

func (n noneSession) Finish(context.Context, *Run) (Report, error) {
	e := n.s.AgentReports.Effect.Normal()
	return Report{Flag: bench.FlagNone, Notes: []string{"the agent reports: " + n.s.AgentReports.Summary}, Effect: &e}, nil
}

// HTTPStatus is a checker that trusts the status code: every 2xx answer the agent's client saw
// means the call did what it asked (a create created one resource, an update updated one, a
// Bundle did every entry), and anything else did nothing. It never raises a flag. It stands for
// the common orchestration check "the API call succeeded".
type HTTPStatus struct{}

// Info implements Subject.
func (HTTPStatus) Info() bench.Info {
	return bench.Info{Name: "http-status", Kind: "observer", Config: "Trusts the status code: a 2xx answer means the call did what it asked (a Bundle, every entry), anything else did nothing. It reads nothing back and raises no flag."}
}

// Start implements Subject.
func (HTTPStatus) Start(context.Context, *Scenario, Env) (Session, error) {
	return statusSession{}, nil
}

// Close implements Subject.
func (HTTPStatus) Close() error { return nil }

type statusSession struct{}

func (statusSession) Agent(context.Context, int, Step, Request) (Exchange, bool, error) {
	return Exchange{}, false, nil
}

func (statusSession) Finish(_ context.Context, run *Run) (Report, error) {
	e, notes := StatusAccount(run)
	return Report{Flag: bench.FlagNone, Notes: notes, Effect: &e}, nil
}

// StatusAccount is HTTPStatus's account of a run: the nominal effect of every agent call whose
// answer the agent saw with a 2xx status.
func StatusAccount(run *Run) (Effect, []string) {
	e := Effect{Changes: map[string]map[string]int{}}
	var notes []string
	for _, st := range run.Steps {
		if st.Session != "agent" {
			continue
		}
		r := st.Request
		if st.Saw.Lost {
			notes = append(notes, fmt.Sprintf("%s %s: no answer seen", r.Method, display(r.Path)))
			continue
		}
		notes = append(notes, fmt.Sprintf("%s %s: HTTP %d", r.Method, display(r.Path), st.Saw.Status))
		if st.Saw.Status < 200 || st.Saw.Status > 299 {
			continue
		}
		for _, n := range Nominal(r) {
			add(e.Changes, n[0], n[1], 1)
		}
	}
	return e.Normal(), notes
}

func display(p string) string {
	if p == "" {
		return "(base)"
	}
	return p
}

// Nominal is what a request asks for, as [type, op] pairs: POST Type is a create, PUT and PATCH
// an update, DELETE a delete, and a Bundle each of its entries.
func Nominal(r Request) [][2]string {
	typ := strings.SplitN(strings.SplitN(r.Path, "?", 2)[0], "/", 2)[0]
	op := map[string]string{"POST": "create", "PUT": "update", "PATCH": "update", "DELETE": "delete"}[r.Method]
	if typ != "" {
		if op == "" {
			return nil
		}
		return [][2]string{{typ, op}}
	}
	var b struct {
		Entry []struct {
			Request struct {
				Method string `json:"method"`
				URL    string `json:"url"`
			} `json:"request"`
		} `json:"entry"`
	}
	if json.Unmarshal(r.Body, &b) != nil {
		return nil
	}
	var out [][2]string
	for _, en := range b.Entry {
		out = append(out, Nominal(Request{Method: en.Request.Method, Path: en.Request.URL})...)
	}
	return out
}

// Command is an observer driven by a shell command (corpus/fhir/BENCHMARK.md): it is called with
// "before" ahead of the steps and with "after" once the facts were read, with FHIRBENCH_SCENARIO,
// FHIRBENCH_DIR, FHIRBENCH_SERVER, FHIRBENCH_BASE_URL, FHIRBENCH_TOKEN, FHIRBENCH_DSN (the
// server's database, as its owner), FHIRBENCH_REPORT (the agent's report as JSON) and
// FHIRBENCH_RUN (the run so far as JSON: the calls and what the client saw, never the facts) set.
// The last line it prints after the steps is its answer:
//
//	{"flag": "none|before|after|unverifiable", "notes": ["…"],
//	 "effect": {"changes": {"Observation": {"create": 1}}, "differs": ["Observation.note"]},
//	 "record": {"kind": "log file", "check": "my-checker verify run.log"}}
type Command struct {
	Name string
	Cmd  string
}

// Info implements Subject.
func (c Command) Info() bench.Info {
	return bench.Info{Name: c.Name, Kind: "observer", Config: "An observer run as a command (" + c.Cmd + "); the calls go straight to the FHIR server."}
}

// Start implements Subject.
func (c Command) Start(ctx context.Context, s *Scenario, env Env) (Session, error) {
	cs := &cmdSession{c: c, s: s, env: env}
	dir, err := os.MkdirTemp("", "fhir-bench-cmd-*")
	if err != nil {
		return nil, err
	}
	cs.dir = dir
	b, _ := json.MarshalIndent(s.AgentReports, "", "  ")
	if err := os.WriteFile(cs.path("report.json"), b, 0o600); err != nil {
		return nil, err
	}
	if _, err := cs.run(ctx, "before", nil); err != nil {
		return nil, err
	}
	return cs, nil
}

// Close implements Subject.
func (Command) Close() error { return nil }

type cmdSession struct {
	c   Command
	s   *Scenario
	env Env
	dir string
}

func (cs *cmdSession) path(n string) string { return cs.dir + string(os.PathSeparator) + n }

func (cs *cmdSession) Agent(context.Context, int, Step, Request) (Exchange, bool, error) {
	return Exchange{}, false, nil
}

func (cs *cmdSession) Finish(ctx context.Context, run *Run) (Report, error) {
	defer os.RemoveAll(cs.dir)
	calls := &Run{}
	for _, st := range run.Steps {
		calls.Steps = append(calls.Steps, StepRecord{Session: st.Session, Request: st.Request, Saw: st.Saw, Via: st.Via})
	}
	line, err := cs.run(ctx, "after", calls)
	if err != nil {
		return Report{}, err
	}
	var ans struct {
		Flag   bench.Flag `json:"flag"`
		Notes  []string   `json:"notes"`
		Effect *Effect    `json:"effect"`
		Record *struct {
			Kind  string `json:"kind"`
			Check string `json:"check"`
		} `json:"record"`
	}
	if err := json.Unmarshal([]byte(line), &ans); err != nil {
		return Report{}, fmt.Errorf("%s printed %q after the steps, not the answer object (corpus/fhir/BENCHMARK.md): %w", cs.c.Name, line, err)
	}
	switch ans.Flag {
	case bench.FlagNone, bench.FlagBefore, bench.FlagAfter, bench.FlagUnverifiable:
	default:
		return Report{}, fmt.Errorf("%s answered flag %q; use none, before, after or unverifiable", cs.c.Name, ans.Flag)
	}
	rep := Report{Flag: ans.Flag, Notes: ans.Notes, Effect: ans.Effect}
	if ans.Record != nil {
		r := &bench.Record{Kind: ans.Record.Kind, Check: ans.Record.Check}
		cmd := exec.CommandContext(ctx, "sh", "-c", ans.Record.Check)
		cmd.Dir = cs.s.Dir
		cmd.Env = cs.environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			r.Problem = strings.TrimSpace(lastLine(string(out)) + " (" + err.Error() + ")")
		} else {
			r.Checked = true
		}
		rep.Record = r
	}
	return rep, nil
}

func (cs *cmdSession) environ() []string {
	return append(os.Environ(), "FHIRBENCH_SCENARIO="+cs.s.ID, "FHIRBENCH_DIR="+cs.s.Dir, "FHIRBENCH_SERVER="+cs.env.Server,
		"FHIRBENCH_BASE_URL="+cs.env.BaseURL, "FHIRBENCH_TOKEN="+cs.env.Token, "FHIRBENCH_DSN="+cs.env.AdminDSN,
		"FHIRBENCH_REPORT="+cs.path("report.json"), "FHIRBENCH_RUN="+cs.path("run.json"))
}

func (cs *cmdSession) run(ctx context.Context, phase string, calls *Run) (string, error) {
	if calls != nil {
		b, _ := json.MarshalIndent(calls, "", "  ")
		if err := os.WriteFile(cs.path("run.json"), b, 0o600); err != nil {
			return "", err
		}
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", cs.c.Cmd+" "+phase)
	cmd.Env = cs.environ()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s failed for %s: %w: %s", cs.c.Name, phase, cs.s.ID, err, lastLine(errb.String()))
	}
	l := lastLine(out.String())
	if phase == "after" && l == "" {
		return "", errors.New(cs.c.Name + " printed nothing after the steps")
	}
	return l, nil
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(ls[len(ls)-1])
}
