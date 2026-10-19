package corpus

//deedglass:allow-sql-build a throwaway-server test runner: database and slot names come from scenario ids checked against ^[0-9]{2}-[a-z0-9-]+$, table names from ^schema.table$ in lowercase, the pid is an integer; the scenario files themselves are the SQL under test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// Observed is what a run saw: the same shape as the database half of expected.json.
type Observed struct {
	ID       string     `json:"id"`
	AgentSaw [][]string `json:"agent_saw"`
	Commits  Commits    `json:"database_commits"`
	// ToolSaid is the last line a CORPUS_TOOL printed after the steps, when one is set.
	ToolSaid string `json:"tool_said,omitempty"`
}

// Options configure a run.
type Options struct {
	// DSN reaches the throwaway server as a superuser (a logical slot needs wal_level=logical).
	DSN string
	// Keep leaves each scenario's database in place after the run.
	Keep bool
	// Tool is a shell command run before the steps (argument "before") and after them ("after"),
	// with CORPUS_SCENARIO, CORPUS_DIR, CORPUS_DSN and CORPUS_REPORT set. Its last line of output
	// after the steps is recorded as what it said.
	Tool string
}

var reTable = regexp.MustCompile(`^[a-z_][a-z0-9_]*\.[a-z_][a-z0-9_]*$`)

// dbName is the scenario's database (and slot) name: corpus_01_trigger.
func dbName(id string) string { return "corpus_" + strings.ReplaceAll(id, "-", "_") }

// Run runs one scenario on a fresh database and returns what it saw.
func Run(ctx context.Context, s *Scenario, o Options) (*Observed, error) {
	for _, t := range s.Tables {
		if !reTable.MatchString(t) {
			return nil, fmt.Errorf("corpus: %s: table %q must be schema.table in lowercase", s.ID, t)
		}
	}
	admin, err := pgconn.Connect(ctx, o.DSN)
	if err != nil {
		return nil, fmt.Errorf("corpus: can't reach Postgres: %w", err)
	}
	defer admin.Close(context.Background())
	name := dbName(s.ID) // from a checked id: [0-9a-z_] only
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)").ReadAll(); err != nil {
		return nil, err
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name).ReadAll(); err != nil {
		return nil, err
	}
	cfg, err := pgconn.ParseConfig(o.DSN)
	if err != nil {
		return nil, err
	}
	cfg.Database = name
	dsn := dsnFor(o.DSN, name)
	if !o.Keep {
		defer func() {
			bg := context.Background()
			_, _ = admin.Exec(bg, "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE slot_name = '"+name+"'").ReadAll()
			_, _ = admin.Exec(bg, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)").ReadAll()
		}()
	}

	obs, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer obs.Close(context.Background())
	setup, err := os.ReadFile(filepath.Join(s.Dir, "setup.sql"))
	if err != nil {
		return nil, err
	}
	if _, err := obs.Exec(ctx, string(setup)).ReadAll(); err != nil {
		return nil, fmt.Errorf("corpus: %s: setup.sql: %w", s.ID, err)
	}
	before, err := counts(ctx, obs, s.Tables)
	if err != nil {
		return nil, err
	}
	if _, err := obs.Exec(ctx, "SELECT pg_create_logical_replication_slot('"+name+"', 'test_decoding')").ReadAll(); err != nil {
		return nil, fmt.Errorf("corpus: can't create the logical slot (is wal_level logical?): %w", err)
	}
	report := filepath.Join(os.TempDir(), name+"-report.json")
	if o.Tool != "" {
		if err := writeReport(report, s.Expected.AgentReports); err != nil {
			return nil, err
		}
		defer os.Remove(report)
		if _, err := runTool(ctx, o.Tool, "before", s, dsn, report); err != nil {
			return nil, err
		}
	}

	out := &Observed{ID: s.ID, Commits: Commits{State: map[string]Count{}}}
	for i, st := range s.Steps {
		saw, err := runStep(ctx, cfg, admin, s, st)
		if err != nil {
			return nil, fmt.Errorf("corpus: %s step %d: %w", s.ID, i+1, err)
		}
		out.AgentSaw = append(out.AgentSaw, saw)
		txs, err := drain(ctx, obs, name)
		if err != nil {
			return nil, err
		}
		for _, c := range txs {
			out.Commits.Transactions = append(out.Commits.Transactions, Tx{Session: st.Session, Changes: c})
		}
	}
	after, err := counts(ctx, obs, s.Tables)
	if err != nil {
		return nil, err
	}
	for _, t := range s.Tables {
		out.Commits.State[t] = Count{Before: before[t], After: after[t]}
	}
	for _, p := range s.Expected.DatabaseCommits.Probes {
		v, err := scalar(ctx, obs, p.SQL)
		if err != nil {
			return nil, fmt.Errorf("corpus: %s: probe %q: %w", s.ID, p.SQL, err)
		}
		out.Commits.Probes = append(out.Commits.Probes, Probe{SQL: p.SQL, Value: v})
	}
	if o.Tool != "" {
		said, err := runTool(ctx, o.Tool, "after", s, dsn, report)
		if err != nil {
			return nil, err
		}
		out.ToolSaid = said
	}
	return out, nil
}

// runStep runs a file statement by statement on a new session and returns what that session saw.
// It speaks the simple-query protocol itself on the raw connection, so a client timeout is only a
// closed socket: nothing asks the server to cancel, as with an agent that gives up waiting.
func runStep(ctx context.Context, cfg *pgconn.Config, admin *pgconn.PgConn, s *Scenario, st Step) ([]string, error) {
	src, err := os.ReadFile(filepath.Join(s.Dir, st.File))
	if err != nil {
		return nil, err
	}
	c, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := c.SyncConn(ctx); err != nil {
		return nil, err
	}
	hc, err := c.Hijack()
	if err != nil {
		return nil, err
	}
	conn, fe := hc.Conn, hc.Frontend
	defer conn.Close()
	timeout := 30 * time.Second
	if st.ClientTimeoutMS > 0 {
		timeout = time.Duration(st.ClientTimeoutMS) * time.Millisecond
	}
	var saw []string
	dropped := false
statements:
	for _, stmt := range SplitSQL(string(src)) {
		fe.Send(&pgproto3.Query{String: stmt})
		if err := fe.Flush(); err != nil {
			return nil, err
		}
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		var tag, code string
		for {
			msg, err := fe.Receive()
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					saw = append(saw, "no reply (client timeout)")
					dropped = true
					break statements
				}
				return nil, err
			}
			switch m := msg.(type) {
			case *pgproto3.CommandComplete:
				tag = string(m.CommandTag)
			case *pgproto3.ErrorResponse:
				code = m.Code
			case *pgproto3.ReadyForQuery:
				if code != "" {
					saw = append(saw, "ERROR "+code)
					if st.StopOnError {
						break statements
					}
				} else {
					saw = append(saw, tag)
				}
				continue statements
			}
		}
	}
	if !dropped {
		fe.Send(&pgproto3.Terminate{})
		_ = fe.Flush()
	}
	_ = conn.Close()
	if st.WaitForServer {
		deadline := time.Now().Add(30 * time.Second)
		for {
			v, err := scalar(ctx, admin, "SELECT count(*) FROM pg_stat_activity WHERE pid = "+strconv.FormatUint(uint64(hc.PID), 10))
			if err != nil {
				return nil, err
			}
			if v == "0" {
				break
			}
			if time.Now().After(deadline) {
				return nil, errors.New("the dropped session's statement didn't finish within 30 s")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return saw, nil
}

// drain reads and consumes the slot's committed transactions with row changes.
func drain(ctx context.Context, c *pgconn.PgConn, slot string) ([]Changes, error) {
	res, err := c.Exec(ctx, "SELECT data FROM pg_logical_slot_get_changes('"+slot+"', NULL, NULL, 'include-xids', '1', 'skip-empty-xacts', '1')").ReadAll()
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, row := range res[0].Rows {
		lines = append(lines, string(row[0]))
	}
	return ParseChanges(lines)
}

// counts reads each table's row count; -1 for a table that doesn't exist.
func counts(ctx context.Context, c *pgconn.PgConn, tables []string) (map[string]int, error) {
	out := map[string]int{}
	for _, t := range tables { // checked against reTable
		exists, err := scalar(ctx, c, "SELECT to_regclass('"+t+"') IS NOT NULL")
		if err != nil {
			return nil, err
		}
		if exists != "t" {
			out[t] = -1
			continue
		}
		v, err := scalar(ctx, c, "SELECT count(*) FROM "+t)
		if err != nil {
			return nil, err
		}
		out[t], _ = strconv.Atoi(v)
	}
	return out, nil
}

func scalar(ctx context.Context, c *pgconn.PgConn, sql string) (string, error) {
	res, err := c.Exec(ctx, sql).ReadAll()
	if err != nil {
		return "", err
	}
	if len(res) != 1 || len(res[0].Rows) != 1 || len(res[0].Rows[0]) != 1 {
		return "", errors.New("expected one value")
	}
	return string(res[0].Rows[0][0]), nil
}

// dsnFor is the DSN with the database name swapped, for a tool to connect with.
func dsnFor(dsn, db string) string {
	if i := strings.Index(dsn, "://"); i >= 0 {
		rest := dsn[i+3:]
		q := ""
		if j := strings.IndexByte(rest, '?'); j >= 0 {
			rest, q = rest[:j], rest[j:]
		}
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			rest = rest[:j]
		}
		return dsn[:i+3] + rest + "/" + db + q
	}
	return dsn + " dbname=" + db
}

func writeReport(path string, r Report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func runTool(ctx context.Context, tool, phase string, s *Scenario, dsn, report string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", tool+" "+phase) //deedglass:allow-exec the corpus runner's documented hook: a command the person running make corpus passes in CORPUS_TOOL
	cmd.Env = append(os.Environ(), "CORPUS_SCENARIO="+s.ID, "CORPUS_DIR="+s.Dir, "CORPUS_DSN="+dsn, "CORPUS_REPORT="+report)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("corpus: CORPUS_TOOL %s failed for %s: %w", phase, s.ID, err)
	}
	ls := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(ls[len(ls)-1]), nil
}

// Diff lists every way what a run saw differs from the scenario's expected database facts.
func Diff(s *Scenario, o *Observed) []string {
	x := s.Expected
	var d []string
	if !reflect.DeepEqual(x.AgentSaw, o.AgentSaw) {
		d = append(d, fmt.Sprintf("the agent saw %q, expected %q", o.AgentSaw, x.AgentSaw))
	}
	if !equalTxs(x.DatabaseCommits.Transactions, o.Commits.Transactions) {
		d = append(d, fmt.Sprintf("committed %s, expected %s", describeTxs(o.Commits.Transactions), describeTxs(x.DatabaseCommits.Transactions)))
	}
	if !reflect.DeepEqual(x.DatabaseCommits.State, o.Commits.State) {
		d = append(d, fmt.Sprintf("row counts %v, expected %v", o.Commits.State, x.DatabaseCommits.State))
	}
	for i, p := range x.DatabaseCommits.Probes {
		if i >= len(o.Commits.Probes) || o.Commits.Probes[i].Value != p.Value {
			got := "nothing"
			if i < len(o.Commits.Probes) {
				got = o.Commits.Probes[i].Value
			}
			d = append(d, fmt.Sprintf("%s returned %s, expected %s", p.SQL, got, p.Value))
		}
	}
	return d
}

func equalTxs(a, b []Tx) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Session != b[i].Session || !reflect.DeepEqual(a[i].Changes, b[i].Changes) {
			return false
		}
	}
	return true
}

func describeTxs(txs []Tx) string {
	if len(txs) == 0 {
		return "no row changes"
	}
	var parts []string
	for _, t := range txs {
		parts = append(parts, t.Session+": "+Describe(t.Changes))
	}
	return strings.Join(parts, "; ")
}
