// Package corpus runs the intent-vs-effect corpus (TY-13, corpus/ at the repo root): scenarios in
// which what an agent reports, what Postgres commits and what Deedglass says can differ. It loads
// each scenario, runs it against a throwaway Postgres, reads what committed from Postgres's own
// change log (test_decoding, per transaction) and compares the database facts with the scenario's
// expected.json.
//
// The runner checks the facts any tool can reproduce: what the agent saw, which transactions
// committed which row changes, row counts and probe values. The Deedglass verdict in each
// expected.json is stated with the test that pins it; the runner checks its shape, not by running
// Deedglass. scripts/corpus.sh starts Postgres in Docker and calls cmd/corpus.
package corpus

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Scenario is scenario.json.
type Scenario struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Tables []string `json:"tables"`
	Steps  []Step   `json:"steps"`

	Dir      string   `json:"-"`
	Expected Expected `json:"-"`
}

// Step runs one SQL file in one session, statement by statement, each in its own transaction
// (autocommit, as psql runs a file).
type Step struct {
	Session string `json:"session"` // "agent" or "other"
	File    string `json:"file"`
	// StopOnError stops the file at the first failed statement (psql -v ON_ERROR_STOP=1).
	StopOnError bool `json:"stop_on_error,omitempty"`
	// ClientTimeoutMS drops the connection at this deadline without cancelling the statement on
	// the server, as a client that times out does. 0 means 30 s.
	ClientTimeoutMS int `json:"client_timeout_ms,omitempty"`
	// WaitForServer waits for the dropped session's statement to finish on the server before the
	// next step, so what it committed is read in this step.
	WaitForServer bool `json:"wait_for_server,omitempty"`
}

// Changes are row changes per table and operation: {"public.orders": {"update": 301}}.
type Changes map[string]map[string]int

// Tx is one committed transaction with row changes, and the session that wrote it.
type Tx struct {
	Session string  `json:"session"`
	Changes Changes `json:"changes"`
}

// Count is a table's row count before and after the steps; -1 when the table doesn't exist.
type Count struct {
	Before int `json:"before"`
	After  int `json:"after"`
}

// Probe is a query run after the steps and the single value it must return.
type Probe struct {
	SQL   string `json:"sql"`
	Value string `json:"value"`
}

// Commits is what the database committed: the facts the runner checks.
type Commits struct {
	Transactions []Tx             `json:"transactions"`
	State        map[string]Count `json:"state"`
	Probes       []Probe          `json:"probes"`
}

// DeedglassAction is the Deedglass outcome expected for one action of the scenario.
type DeedglassAction struct {
	Statement string   `json:"statement"`
	Decision  string   `json:"decision"`
	Rule      string   `json:"rule,omitempty"`
	Execution string   `json:"execution"`
	Verdict   string   `json:"verdict"`
	Level     string   `json:"level,omitempty"`
	Reasons   []string `json:"reasons"`
}

// Deedglass is the expected Deedglass outcome, with what it assumes and what pins it.
type Deedglass struct {
	Assumes     string            `json:"assumes"`
	Actions     []DeedglassAction `json:"actions"`
	OutsideGate string            `json:"outside_gate,omitempty"`
	Note        string            `json:"note,omitempty"`
	Basis       []string          `json:"basis"`
}

// Report is what the agent says it did.
type Report struct {
	Summary string  `json:"summary"`
	Changes Changes `json:"changes"`
}

// Expected is expected.json.
type Expected struct {
	AgentReports    Report     `json:"agent_reports"`
	AgentSaw        [][]string `json:"agent_saw"`
	DatabaseCommits Commits    `json:"database_commits"`
	Deedglass       Deedglass  `json:"deedglass"`
}

var reID = regexp.MustCompile(`^[0-9]{2}-[a-z0-9-]+$`)

// Load reads every scenario under dir/scenarios, in name order.
func Load(dir string) ([]*Scenario, error) {
	ents, err := os.ReadDir(filepath.Join(dir, "scenarios"))
	if err != nil {
		return nil, err
	}
	var out []*Scenario
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		s, err := LoadOne(filepath.Join(dir, "scenarios", e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) == 0 {
		return nil, errors.New("corpus: no scenarios")
	}
	return out, nil
}

// LoadOne reads one scenario directory and checks it is complete.
func LoadOne(dir string) (*Scenario, error) {
	s := &Scenario{Dir: dir}
	if err := readJSON(filepath.Join(dir, "scenario.json"), s); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(dir, "expected.json"), &s.Expected); err != nil {
		return nil, err
	}
	if s.ID != filepath.Base(dir) || !reID.MatchString(s.ID) {
		return nil, fmt.Errorf("corpus: %s: id %q must equal the directory name (NN-name)", dir, s.ID)
	}
	if s.Title == "" || len(s.Tables) == 0 || len(s.Steps) == 0 {
		return nil, fmt.Errorf("corpus: %s needs a title, tables and steps", s.ID)
	}
	if _, err := os.Stat(filepath.Join(dir, "setup.sql")); err != nil {
		return nil, fmt.Errorf("corpus: %s has no setup.sql", s.ID)
	}
	for _, st := range s.Steps {
		if st.Session != "agent" && st.Session != "other" {
			return nil, fmt.Errorf("corpus: %s: session %q must be agent or other", s.ID, st.Session)
		}
		if _, err := os.Stat(filepath.Join(dir, st.File)); err != nil || strings.Contains(st.File, "/") {
			return nil, fmt.Errorf("corpus: %s: step file %q must be a file in the scenario directory", s.ID, st.File)
		}
	}
	x := s.Expected
	if len(x.AgentSaw) != len(s.Steps) {
		return nil, fmt.Errorf("corpus: %s: agent_saw has %d entries for %d steps", s.ID, len(x.AgentSaw), len(s.Steps))
	}
	for _, t := range s.Tables {
		if _, ok := x.DatabaseCommits.State[t]; !ok {
			return nil, fmt.Errorf("corpus: %s: database_commits.state lacks %s", s.ID, t)
		}
	}
	if len(x.DatabaseCommits.State) != len(s.Tables) {
		return nil, fmt.Errorf("corpus: %s: database_commits.state names tables not in scenario.json", s.ID)
	}
	if x.AgentReports.Summary == "" || x.Deedglass.Assumes == "" || len(x.Deedglass.Actions) == 0 || len(x.Deedglass.Basis) == 0 {
		return nil, fmt.Errorf("corpus: %s: expected.json needs agent_reports.summary and deedglass assumes, actions and basis", s.ID)
	}
	return s, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("corpus: %s: %w", path, err)
	}
	return nil
}

// SplitSQL splits a file into statements the way psql sends them: at a semicolon outside quotes,
// dollar quotes and comments. Comment-only pieces are dropped.
func SplitSQL(src string) []string {
	var (
		out    []string
		cur    strings.Builder
		dollar string
	)
	flush := func() {
		s := strings.TrimSpace(cur.String())
		cur.Reset()
		if stripComments(s) != "" {
			out = append(out, s)
		}
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case dollar != "":
			if strings.HasPrefix(src[i:], dollar) {
				cur.WriteString(dollar)
				i += len(dollar) - 1
				dollar = ""
				continue
			}
		case c == '-' && strings.HasPrefix(src[i:], "--"):
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				j = len(src) - i
			}
			cur.WriteString(src[i : i+j])
			i += j - 1
			continue
		case c == '\'':
			j := i + 1
			for j < len(src) {
				if src[j] == '\'' {
					if j+1 < len(src) && src[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			cur.WriteString(src[i:min(j+1, len(src))])
			i = j
			continue
		case c == '$':
			if m := reDollar.FindString(src[i:]); m != "" {
				dollar = m
				cur.WriteString(m)
				i += len(m) - 1
				continue
			}
		case c == ';':
			flush()
			continue
		}
		cur.WriteByte(c)
	}
	flush()
	return out
}

var reDollar = regexp.MustCompile(`^\$[A-Za-z_]*\$`)

func stripComments(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "--") {
			b.WriteString(t)
		}
	}
	return b.String()
}

// ParseChanges reads test_decoding output (include-xids, skip-empty-xacts) into transactions with
// row changes, in commit order.
func ParseChanges(lines []string) ([]Changes, error) {
	var (
		out []Changes
		cur Changes
	)
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "BEGIN"):
			cur = Changes{}
		case strings.HasPrefix(l, "COMMIT"):
			if cur == nil {
				return nil, errors.New("corpus: COMMIT without BEGIN in the change log")
			}
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur = nil
		case strings.HasPrefix(l, "table "):
			m := reChange.FindStringSubmatch(l)
			if m == nil || cur == nil {
				return nil, fmt.Errorf("corpus: unreadable change-log line %q", l)
			}
			op := strings.ToLower(m[2])
			for _, t := range strings.Split(m[1], ", ") {
				if cur[t] == nil {
					cur[t] = map[string]int{}
				}
				cur[t][op]++
			}
		case strings.HasPrefix(l, "message:"):
			// logical messages carry no row change
		default:
			return nil, fmt.Errorf("corpus: unexpected change-log line %q", l)
		}
	}
	if cur != nil {
		return nil, errors.New("corpus: the change log ends inside a transaction")
	}
	return out, nil
}

var reChange = regexp.MustCompile(`^table ([^:]+(?:, [^:]+)*): (INSERT|UPDATE|DELETE|TRUNCATE):`)

// Describe is one line about a set of changes: "update 301 in public.orders".
func Describe(c Changes) string {
	if len(c) == 0 {
		return "no row changes"
	}
	var parts []string
	tables := make([]string, 0, len(c))
	for t := range c {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		ops := make([]string, 0, len(c[t]))
		for op := range c[t] {
			ops = append(ops, op)
		}
		sort.Strings(ops)
		for _, op := range ops {
			parts = append(parts, fmt.Sprintf("%s %d in %s", op, c[t][op], t))
		}
	}
	return strings.Join(parts, ", ")
}

// Sum adds up the changes of the transactions written by one session.
func Sum(txs []Tx, session string) Changes {
	out := Changes{}
	for _, tx := range txs {
		if tx.Session != session {
			continue
		}
		for t, ops := range tx.Changes {
			if out[t] == nil {
				out[t] = map[string]int{}
			}
			for op, n := range ops {
				out[t][op] += n
			}
		}
	}
	return out
}
