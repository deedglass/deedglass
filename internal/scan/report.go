// Package scan is `deedglass scan` (S32 §3.13): a local, offline, read-only report of what an agent
// setup can change. It finds MCP client configs, agent and database dependencies, database URL
// variable names and write-capable tools; it flags the archived Postgres MCP server; with a DSN it
// reads the Postgres catalog in a read-only transaction and ranks the tables the role can change;
// and it grades the setup with the Agent Blast-Radius Score (A to F).
//
// Every function returns data; nothing here prints. The scan never sends a write, never starts an
// MCP server, never runs a stacked statement, and opens no network connection except to the one
// database the user named. No DSN or environment value is kept anywhere in a Report.
package scan

import (
	"context"
	"errors"
	"time"
)

// FooterLine is the one footer every output carries (S32 §3.13.4). S48 may change its wording
// (the 30-day Pro pass); the link stays deedglass.com/pro.
const FooterLine = "Full report with fixes: deedglass.com/pro ($29 once)."

// SmallPrint always follows the grade (§3.13.6).
const SmallPrint = "A means a small blast radius, not a safe agent."

// Version is the scan_report schema version; S37's renderer reads exactly this shape.
const Version = 1

// FormulaVersion is the score formula's version (A8 v1). A different reading is v2, never an edit.
const FormulaVersion = 1

// Report is the scan_report object (`--json`, deedglass-scan.json).
type Report struct {
	Object           string        `json:"object"`
	Version          int           `json:"version"`
	ScannedAt        string        `json:"scanned_at"`
	DeedglassVersion string        `json:"deedglass_version"`
	Dir              string        `json:"dir"`
	Offline          bool          `json:"offline"`
	Score            Score         `json:"score"`
	Summary          Summary       `json:"summary"`
	Configs          []Config      `json:"configs"`
	Dependencies     []Dependency  `json:"dependencies"`
	EnvNames         []EnvNames    `json:"env_names"`
	Tools            []Tool        `json:"tools"`
	Findings         []Finding     `json:"findings"`
	Postgres         *Postgres     `json:"postgres"`
	Policies         []Suggestion  `json:"policies"`
	Notes            []string      `json:"notes"`
	Footer           string        `json:"footer"`
	TookMS           int64         `json:"took_ms"`
	Walk             WalkStats     `json:"walk"`
	PostgresError    *PostgresFail `json:"postgres_error,omitempty"`
}

// Score is the Agent Blast-Radius Score.
type Score struct {
	Grade          string      `json:"grade"`
	Points         int         `json:"points"`
	Deductions     []Deduction `json:"deductions"`
	FormulaVersion int         `json:"formula_version"`
	// UncheckedPaths is how many write paths have no check (the score line's number).
	UncheckedPaths int `json:"unchecked_write_paths"`
	// DatabaseChecked is false without --postgres-dsn-env (the line adds "database not checked").
	DatabaseChecked bool   `json:"database_checked"`
	FixFirst        string `json:"fix_first"`
}

// Deduction is one line of the score.
type Deduction struct {
	ID      string `json:"id"` // role_superuser_or_bypassrls · high_finding · medium_finding · unchecked_write_path
	Points  int    `json:"points"`
	Subject string `json:"subject"`
	Detail  string `json:"detail"`
}

// Summary is the counts line.
type Summary struct {
	Configs           int            `json:"configs"`
	MCPServers        int            `json:"mcp_servers"`
	WriteCapableTools int            `json:"write_capable_tools"`
	TablesCanChange   int            `json:"tables_can_change"`
	Findings          map[string]int `json:"findings"`
}

// Config is one MCP client config file.
type Config struct {
	Path    string   `json:"path"` // relative to --dir, or ~/… for user configs
	Client  string   `json:"client"`
	User    bool     `json:"user"`
	Servers []Server `json:"servers"`
	Error   string   `json:"error,omitempty"`
}

// Server is one MCP server entry: never an env value or a secret-looking argument.
type Server struct {
	Name              string   `json:"name"`
	Package           string   `json:"package,omitempty"`
	Version           string   `json:"version,omitempty"`
	Transport         string   `json:"transport"`
	Command           string   `json:"command,omitempty"` // the base name of the command
	EnvNames          []string `json:"env_names"`
	DatabaseURLInArgs bool     `json:"database_url_in_args"`
	Routed            bool     `json:"routed"` // deedglass mcp wrap … -- <cmd>, or the Deedglass server itself
	CatalogID         string   `json:"catalog_id,omitempty"`
}

// Dependency is one dependency line that the catalog knows, or an agent framework.
type Dependency struct {
	File      string `json:"file"`
	Name      string `json:"name"`
	Spec      string `json:"spec"`
	CatalogID string `json:"catalog_id,omitempty"`
}

// EnvNames lists database URL variable names in one .env file (never values).
type EnvNames struct {
	File  string   `json:"file"`
	Names []string `json:"names"`
}

// Tool is a tool the catalog says a discovered server or package exposes.
type Tool struct {
	Server     string `json:"server"`
	Tool       string `json:"tool"`
	SideEffect string `json:"side_effect"`
	Source     string `json:"source"`
	Routed     bool   `json:"routed"`
	Where      string `json:"where"`
}

// Finding is one problem the scan found.
type Finding struct {
	ID       string   `json:"id"`
	Severity string   `json:"severity"` // high · medium · low · info
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Checked  string   `json:"checked,omitempty"` // which role the database part checked
	Fix      []string `json:"fix"`
	Paths    []string `json:"paths"`
	Sources  []string `json:"sources"`
}

// Postgres is the read-only catalog report (§3.13.3).
type Postgres struct {
	Role            string         `json:"role"`
	Database        string         `json:"database"`
	ServerVersion   string         `json:"server_version"`
	WALLevel        string         `json:"wal_level"`
	EnvVar          string         `json:"env_var"`
	RoleAttributes  RoleAttributes `json:"role_attributes"`
	Tables          []Table        `json:"tables"`
	TablesReadOnly  int            `json:"tables_read_only"`
	SecurityDefiner []SecDefFunc   `json:"security_definer"`
	Statements      []string       `json:"-"` // the SQL sent, for the read-only test
}

// RoleAttributes are pg_roles' flags for the connected role.
type RoleAttributes struct {
	Superuser   bool `json:"superuser"`
	BypassRLS   bool `json:"bypassrls"`
	Replication bool `json:"replication"`
	CreateRole  bool `json:"createrole"`
	CreateDB    bool `json:"createdb"`
}

// Table is one table the role can change.
type Table struct {
	Schema     string    `json:"schema"`
	Table      string    `json:"table"`
	Can        []string  `json:"can"`
	EstRows    int64     `json:"est_rows"` // -1: never analysed
	CascadesTo []string  `json:"cascades_to"`
	Triggers   []Trigger `json:"triggers"`
	RLS        bool      `json:"rls"`
	UndoReady  bool      `json:"undo_ready"`
	L2Ready    bool      `json:"l2_ready"`
	Score      float64   `json:"score"`
	HasPK      bool      `json:"has_pk"`
}

// Name is schema.table.
func (t Table) Name() string { return t.Schema + "." + t.Table }

// CanDelete reports DELETE or TRUNCATE.
func (t Table) CanDelete() bool {
	for _, c := range t.Can {
		if c == "DELETE" || c == "TRUNCATE" {
			return true
		}
	}
	return false
}

// Trigger is an enabled, non-internal trigger and the tables its function body names.
type Trigger struct {
	Name        string   `json:"name"`
	Enabled     bool     `json:"enabled"`
	NamesTables []string `json:"names_tables"`
}

// SecDefFunc is a SECURITY DEFINER function the role can execute.
type SecDefFunc struct {
	Signature string `json:"signature"`
	Owner     string `json:"owner"`
}

// Suggestion is one of the three policies to add first.
type Suggestion struct {
	Starter string `json:"starter"`
	Rule    string `json:"rule"`
	Why     string `json:"why"`
	YAML    string `json:"yaml"`
}

// WalkStats says what the file walk saw.
type WalkStats struct {
	Files      int  `json:"files"`
	SkippedBig int  `json:"skipped_big"`
	Stopped    bool `json:"stopped"`
}

// PostgresFail is why the database part didn't run (the file part was still written).
type PostgresFail struct {
	EnvVar   string `json:"env_var"`
	Reason   string `json:"reason"` // "unset" · "connect" · "query"
	SQLState string `json:"sqlstate,omitempty"`
	Class    string `json:"class,omitempty"`
}

// Options are the scan's inputs.
type Options struct {
	Dir         string // the directory to scan
	DirLabel    string // how --dir is shown (~ for home)
	Home        string // the user's home ("" disables user configs)
	UserConfigs bool
	MaxFiles    int
	GOOS        string
	// DSN is the database URL read once from the environment; "" skips the Postgres part.
	DSN    string
	DSNVar string
	// Connect opens the read-only session (tests replace it); nil uses pgx.
	Connect Connector
	Now     func() time.Time
	Version string
}

// ErrDSNUnset is the usage error for --postgres-dsn-env naming an empty variable.
var ErrDSNUnset = errors.New("scan: the DSN variable is unset")

// Scan runs the file part, then (with a DSN) the database part, then the findings, the score and
// the policies.
func Scan(ctx context.Context, o Options) (*Report, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	start := now()
	if o.MaxFiles <= 0 {
		o.MaxFiles = 20000
	}
	r := &Report{Object: "scan_report", Version: Version, ScannedAt: start.UTC().Format(time.RFC3339),
		DeedglassVersion: o.Version, Dir: o.DirLabel, Offline: o.DSN == "", Footer: FooterLine,
		Configs: []Config{}, Dependencies: []Dependency{}, EnvNames: []EnvNames{}, Tools: []Tool{},
		Findings: []Finding{}, Policies: []Suggestion{}, Notes: []string{}}
	d, err := discover(o)
	if err != nil {
		return nil, err
	}
	r.Configs, r.Dependencies, r.EnvNames, r.Walk = d.configs, d.deps, d.env, d.walk
	r.Tools = toolsFor(r.Configs, r.Dependencies)
	if o.DSN != "" {
		pg, fail := readPostgres(ctx, o)
		r.Postgres, r.PostgresError = pg, fail
		if pg != nil {
			pg.EnvVar = o.DSNVar
			rank(pg.Tables)
		}
	}
	r.Findings = findings(r, o)
	r.Score = score(r, d.policyFiles > 0)
	r.Policies = suggest(r)
	r.Summary = summarize(r)
	r.TookMS = now().Sub(start).Milliseconds()
	return r, nil
}

func summarize(r *Report) Summary {
	s := Summary{Configs: len(r.Configs), Findings: map[string]int{"high": 0, "medium": 0, "low": 0, "info": 0}}
	for _, c := range r.Configs {
		s.MCPServers += len(c.Servers)
	}
	for _, t := range r.Tools {
		if writeLike(t.SideEffect) {
			s.WriteCapableTools++
		}
	}
	if r.Postgres != nil {
		s.TablesCanChange = len(r.Postgres.Tables)
	}
	for _, f := range r.Findings {
		s.Findings[f.Severity]++
	}
	return s
}

func writeLike(se string) bool { return se == "write" || se == "destructive" || se == "money" }
