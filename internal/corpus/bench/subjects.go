package bench

//deedglass:allow-exec the benchmark's Command adapter: runs the checker command the person running make corpus-bench names, and the record check it prints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/deedglass/deedglass/internal/corpus"
)

// None is the baseline: no gateway and no checker. The agent's statements run on a direct
// connection, and the only account of what happened is the agent's own report.
type None struct{}

// Info implements Subject.
func (None) Info() Info {
	return Info{Name: "none", Kind: "baseline", Config: "No gateway or checker: the statements run on a direct connection and the agent's report is the only account of what happened."}
}

// Start implements Subject: the account of what committed is the scenario's agent report.
func (None) Start(_ context.Context, s *corpus.Scenario) (Session, error) {
	return claimSession{s: s}, nil
}

// Close implements Subject.
func (None) Close() error { return nil }

type claimSession struct{ s *corpus.Scenario }

func (claimSession) Prepare(context.Context, string) error { return nil }
func (claimSession) Agent(context.Context, int, corpus.Step, []string) ([]string, bool, error) {
	return nil, false, nil
}
func (claimSession) IgnoreSchemas() []string { return nil }
func (c claimSession) Finish(context.Context) (Report, error) {
	e := Claim(c.s)
	return Report{Flag: FlagNone, Notes: []string{"the agent reports: " + c.s.Expected.AgentReports.Summary}, Effect: &e}, nil
}

// Command is an observer driven by a shell command, the way make corpus's CORPUS_TOOL is. The
// runner runs the agent's statements itself on a direct connection. The command is called with
// "before" after setup.sql, and with "after" once the steps ran and the facts were read, with
// CORPUS_SCENARIO, CORPUS_DIR, CORPUS_DSN and CORPUS_REPORT set (corpus/README.md). The last line
// it prints after the steps is its answer, one JSON object (corpus/BENCHMARK.md):
//
//	{"flag": "none|before|after|unverifiable", "notes": ["…"], "effect": {"changes": {"public.orders": {"update": 301}}},
//	 "record": {"kind": "log file", "check": "my-checker verify run.log"}}
//
// effect and record may be null. The runner runs record.check itself, in the scenario directory;
// exit 0 means the record checks.
type Command struct {
	Name    string // the subject's name in the results
	Cmd     string // the shell command
	Version string
	Config  string
}

// Info implements Subject.
func (c Command) Info() Info {
	cfg := c.Config
	if cfg == "" {
		cfg = "An observer run as a command (" + c.Cmd + "); the statements run on a direct connection."
	}
	return Info{Name: c.Name, Kind: "observer", Version: c.Version, Config: cfg}
}

// Start implements Subject.
func (c Command) Start(_ context.Context, s *corpus.Scenario) (Session, error) {
	return &cmdSession{c: c, s: s}, nil
}

// Close implements Subject.
func (Command) Close() error { return nil }

type cmdSession struct {
	c      Command
	s      *corpus.Scenario
	dsn    string
	report string
}

func (cs *cmdSession) IgnoreSchemas() []string { return nil }

func (cs *cmdSession) Prepare(ctx context.Context, dsn string) error {
	cs.dsn = dsn
	f, err := os.CreateTemp("", "corpus-bench-report-*.json")
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(cs.s.Expected.AgentReports, "", "  ")
	_, err = f.Write(append(b, '\n'))
	f.Close()
	cs.report = f.Name()
	if err != nil {
		return err
	}
	_, err = cs.run(ctx, "before")
	return err
}

func (cs *cmdSession) Agent(context.Context, int, corpus.Step, []string) ([]string, bool, error) {
	return nil, false, nil
}

func (cs *cmdSession) Finish(ctx context.Context) (Report, error) {
	defer os.Remove(cs.report)
	line, err := cs.run(ctx, "after")
	if err != nil {
		return Report{}, err
	}
	var ans struct {
		Flag   Flag     `json:"flag"`
		Notes  []string `json:"notes"`
		Effect *Effect  `json:"effect"`
		Record *struct {
			Kind  string `json:"kind"`
			Check string `json:"check"`
		} `json:"record"`
	}
	if err := json.Unmarshal([]byte(line), &ans); err != nil {
		return Report{}, fmt.Errorf("%s printed %q after the steps, not the answer object (corpus/BENCHMARK.md): %w", cs.c.Name, line, err)
	}
	switch ans.Flag {
	case FlagNone, FlagBefore, FlagAfter, FlagUnverifiable:
	default:
		return Report{}, fmt.Errorf("%s answered flag %q; use none, before, after or unverifiable", cs.c.Name, ans.Flag)
	}
	rep := Report{Flag: ans.Flag, Notes: ans.Notes, Effect: ans.Effect}
	if ans.Record != nil {
		r := &Record{Kind: ans.Record.Kind, Check: ans.Record.Check}
		cmd := exec.CommandContext(ctx, "sh", "-c", ans.Record.Check)
		cmd.Dir = cs.s.Dir
		cmd.Env = cs.env()
		if out, err := cmd.CombinedOutput(); err != nil {
			r.Problem = strings.TrimSpace(lastLine(string(out)) + " (" + err.Error() + ")")
		} else {
			r.Checked = true
		}
		rep.Record = r
	}
	return rep, nil
}

func (cs *cmdSession) env() []string {
	return append(os.Environ(), "CORPUS_SCENARIO="+cs.s.ID, "CORPUS_DIR="+cs.s.Dir, "CORPUS_DSN="+cs.dsn, "CORPUS_REPORT="+cs.report)
}

func (cs *cmdSession) run(ctx context.Context, phase string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", cs.c.Cmd+" "+phase)
	cmd.Env = cs.env()
	cmd.Dir = filepath.Clean(".")
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
