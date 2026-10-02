package postgres

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/postgres/setup"
)

// Type is the connector type.
const Type = "postgres"

// Modes.
const (
	ModeSQL                 = "sql"                  // query, execute, call, describe
	ModeControlledFunctions = "controlled_functions" // no postgres.execute: writes only through allow-listed functions
)

// Limits and defaults (S17 §3.4).
const (
	DefaultRowLimit         = 500
	MaxRowLimit             = 10_000
	DefaultMaxResultBytes   = 4 << 20
	MaxMaxResultBytes       = 16 << 20
	DefaultMaxRowsAffected  = 1000
	MaxMaxRowsAffected      = 1_000_000
	DefaultStatementTimeout = 5000
	MaxStatementTimeout     = 60_000
	DefaultLockTimeout      = 2000
	DefaultMaxConnections   = 4
	MaxMaxConnections       = 16
	MaxSQLBytes             = 64 << 10
	MaxParams               = 100
	maxListLen              = 256
)

// FunctionArg is one named, typed argument of a controlled function.
type FunctionArg struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Function is an allow-listed function postgres.call may run.
type Function struct {
	Name        string        `json:"name"` // schema.function
	Args        []FunctionArg `json:"args"`
	SideEffect  string        `json:"side_effect"` // read, write or destructive (default write)
	Description string        `json:"description,omitempty"`
	// Writes is the function's contract for L2 (S21): the tables it writes, with which operations,
	// at most how many rows, and which columns. Without it a call's footprint is unknown.
	Writes []FunctionWrite `json:"writes,omitempty"`
}

// FunctionWrite is one table a controlled function declares it writes.
type FunctionWrite struct {
	Table   string   `json:"table"`
	Ops     []string `json:"ops"`
	MaxRows *int64   `json:"max_rows,omitempty"`
	Columns []string `json:"columns,omitempty"`
}

// Signature is "public.ship_order(bigint)".
func (f Function) Signature() string {
	types := make([]string, len(f.Args))
	for i, a := range f.Args {
		types[i] = a.Type
	}
	return f.Name + "(" + strings.Join(types, ", ") + ")"
}

// Witness is witness.*: validated here, read by S20 (the decoder) and S21 (L2).
type Witness struct {
	Enabled          bool     `json:"enabled"`
	WatchedTables    []string `json:"watched_tables"`
	SensitiveColumns []string `json:"sensitive_columns"`
	// S21: L2's settings. Every one is optional and left out when unset, so older configs keep
	// their config_hash.
	AutoColumns    map[string][]string `json:"auto_columns,omitempty"`    // table → columns a trigger maintains (updated_at)
	MaxRows        map[string]int64    `json:"max_rows,omitempty"`        // table → the most rows one action may change
	StrictTriggers bool                `json:"strict_triggers,omitempty"` // triggers that may write unwatched tables are a gap
	L2Timeout      string              `json:"l2_timeout,omitempty"`      // how long an action waits for its frame (1s to 60s; 10s)
	DiffRows       int                 `json:"diff_rows,omitempty"`       // rows the diff shows (1 to 100; 20)
}

// L2 defaults and limits (S21 §3.12).
const (
	DefaultL2Timeout = 10 * time.Second
	MinL2Timeout     = time.Second
	MaxL2Timeout     = 60 * time.Second
	DefaultDiffRows  = 20
	MaxDiffRows      = 100
)

// L2TimeoutOrDefault is witness.l2_timeout, parsed (the default when unset).
func (w Witness) L2TimeoutOrDefault() time.Duration {
	if d, err := time.ParseDuration(w.L2Timeout); err == nil && d >= MinL2Timeout && d <= MaxL2Timeout {
		return d
	}
	return DefaultL2Timeout
}

// Config is a Postgres connector's config (S17 §3.4), as stored after normalization.
type Config struct {
	Mode               string     `json:"mode"`
	AllowedSchemas     []string   `json:"allowed_schemas"`
	WritableTables     []string   `json:"writable_tables"`
	RowLimit           int        `json:"row_limit"`
	MaxResultBytes     int        `json:"max_result_bytes"`
	MaxRowsAffected    int        `json:"max_rows_affected"`
	StatementTimeoutMS int        `json:"statement_timeout_ms"`
	LockTimeoutMS      int        `json:"lock_timeout_ms"`
	MaxConnections     int        `json:"max_connections"`
	Witness            Witness    `json:"witness"`
	Functions          []Function `json:"functions"`
	Throwaway          bool       `json:"throwaway"`
}

// Desired is what the config asks of the database, for the planner and the checks.
func (c Config) Desired(p setup.Provider) setup.Desired {
	d := setup.Desired{AllowedSchemas: append([]string{}, c.AllowedSchemas...), WritableTables: append([]string{}, c.WritableTables...), Provider: p}
	if c.Witness.Enabled {
		d.WatchedTables = append([]string{}, c.Witness.WatchedTables...)
	}
	for _, f := range c.Functions {
		d.Functions = append(d.Functions, f.Signature())
	}
	return d
}

// Function returns the allow-listed function by name.
func (c Config) Function(name string) (Function, bool) {
	for _, f := range c.Functions {
		if f.Name == name {
			return f, true
		}
	}
	return Function{}, false
}

// IsWritable reports whether schema.table is in writable_tables.
func (c Config) IsWritable(t string) bool { return slices.Contains(c.WritableTables, t) }

// IsWatched reports whether schema.table is watched (witness enabled).
func (c Config) IsWatched(t string) bool {
	return c.Witness.Enabled && slices.Contains(c.Witness.WatchedTables, t)
}

// reserved schemas a config may never name.
var reservedSchemas = []string{"deedglass", "pg_catalog", "information_schema", "pg_toast"}

// ParseConfig decodes and validates raw, filling defaults. Unknown keys anywhere (witness.*
// included) are errors, so a typo never silently loosens a limit.
func ParseConfig(raw json.RawMessage) (Config, []connector.FieldError) {
	var c Config
	var errs []connector.FieldError
	bad := func(param, msg string) {
		errs = append(errs, connector.FieldError{Param: "config." + param, Message: msg})
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		msg := err.Error()
		if i := strings.Index(msg, "unknown field "); i >= 0 {
			msg = strings.TrimPrefix(msg[i:], "unknown field ") + " isn't a postgres config key. Check the spelling against docs/POSTGRES.md."
		} else {
			msg = "can't be read: " + strings.TrimPrefix(msg, "json: ") + "."
		}
		return c, []connector.FieldError{{Param: "config", Message: msg}}
	}
	if dec.More() {
		return c, []connector.FieldError{{Param: "config", Message: "holds more than one JSON value."}}
	}
	switch c.Mode {
	case "":
		c.Mode = ModeSQL
	case ModeSQL, ModeControlledFunctions:
	default:
		bad("mode", "must be sql or controlled_functions.")
	}
	// allowed_schemas: the read scope, in search_path order.
	switch n := len(c.AllowedSchemas); {
	case n == 0:
		bad("allowed_schemas", "needs at least one schema, like [public].")
	case n > 16:
		bad("allowed_schemas", "lists at most 16 schemas.")
	}
	seen := map[string]bool{}
	for i, s := range c.AllowedSchemas {
		switch {
		case !setup.ValidIdent(s):
			bad(fmt.Sprintf("allowed_schemas[%d]", i), "must be a lowercase schema name: letters, digits and underscores, at most 63.")
		case slices.Contains(reservedSchemas, s):
			bad(fmt.Sprintf("allowed_schemas[%d]", i), fmt.Sprintf("can't be %s. Deedglass never lets agents read it.", s))
		case seen[s]:
			bad(fmt.Sprintf("allowed_schemas[%d]", i), s+" is listed twice.")
		}
		seen[s] = true
	}
	tableList := func(param string, list []string, needAllowed bool) []string {
		if len(list) > maxListLen {
			bad(param, fmt.Sprintf("lists at most %d tables.", maxListLen))
			return list
		}
		dup := map[string]bool{}
		for i, t := range list {
			p, ok := setup.SplitQualified(t, 2)
			switch {
			case !ok:
				bad(fmt.Sprintf("%s[%d]", param, i), "must be schema.table in lowercase, like public.orders.")
			case slices.Contains(reservedSchemas, p[0]):
				bad(fmt.Sprintf("%s[%d]", param, i), fmt.Sprintf("can't be in schema %s.", p[0]))
			case needAllowed && !seen[p[0]]:
				bad(fmt.Sprintf("%s[%d]", param, i), fmt.Sprintf("is in schema %s, which isn't in allowed_schemas.", p[0]))
			case dup[t]:
				bad(fmt.Sprintf("%s[%d]", param, i), t+" is listed twice.")
			}
			dup[t] = true
		}
		out := append([]string{}, list...)
		sort.Strings(out)
		return out
	}
	c.WritableTables = tableList("writable_tables", c.WritableTables, true)
	intIn := func(param string, v *int, def, lo, hi int) {
		if *v == 0 {
			*v = def
			return
		}
		if *v < lo || *v > hi {
			bad(param, fmt.Sprintf("must be between %d and %d.", lo, hi))
		}
	}
	intIn("row_limit", &c.RowLimit, DefaultRowLimit, 1, MaxRowLimit)
	intIn("max_result_bytes", &c.MaxResultBytes, DefaultMaxResultBytes, 1024, MaxMaxResultBytes)
	intIn("max_rows_affected", &c.MaxRowsAffected, DefaultMaxRowsAffected, 1, MaxMaxRowsAffected)
	intIn("statement_timeout_ms", &c.StatementTimeoutMS, DefaultStatementTimeout, 100, MaxStatementTimeout)
	intIn("lock_timeout_ms", &c.LockTimeoutMS, DefaultLockTimeout, 100, MaxStatementTimeout)
	intIn("max_connections", &c.MaxConnections, DefaultMaxConnections, 1, MaxMaxConnections)
	if c.LockTimeoutMS > c.StatementTimeoutMS {
		bad("lock_timeout_ms", "must be at most statement_timeout_ms.")
	}
	// witness
	c.Witness.WatchedTables = tableList("witness.watched_tables", c.Witness.WatchedTables, false)
	if c.Witness.Enabled {
		for _, t := range c.WritableTables {
			if !slices.Contains(c.Witness.WatchedTables, t) {
				bad("witness.watched_tables", fmt.Sprintf("must include every writable table; %s is missing. Deedglass witnesses every table an agent can write.", t))
			}
		}
	}
	if c.Witness.WatchedTables == nil {
		c.Witness.WatchedTables = []string{}
	}
	sens := append([]string{}, c.Witness.SensitiveColumns...)
	for i, col := range sens {
		p, ok := setup.SplitQualified(col, 3)
		switch {
		case !ok:
			bad(fmt.Sprintf("witness.sensitive_columns[%d]", i), "must be schema.table.column in lowercase, like public.customers.email.")
		case !slices.Contains(c.Witness.WatchedTables, p[0]+"."+p[1]):
			bad(fmt.Sprintf("witness.sensitive_columns[%d]", i), fmt.Sprintf("is in %s.%s, which isn't a watched table.", p[0], p[1]))
		}
	}
	sort.Strings(sens)
	c.Witness.SensitiveColumns = sens
	watchedTable := func(param, t string) bool {
		if !slices.Contains(c.Witness.WatchedTables, t) {
			bad(param, fmt.Sprintf("names %s, which isn't a watched table.", t))
			return false
		}
		return true
	}
	for _, t := range sortedMapKeys(c.Witness.AutoColumns) {
		param := "witness.auto_columns." + t
		if !watchedTable(param, t) {
			continue
		}
		cols := c.Witness.AutoColumns[t]
		if len(cols) == 0 || len(cols) > 32 {
			bad(param, "lists 1 to 32 columns.")
		}
		for j, col := range cols {
			if !setup.ValidIdent(col) {
				bad(fmt.Sprintf("%s[%d]", param, j), "must be a lowercase column name.")
			}
		}
	}
	for _, t := range sortedMapKeys(c.Witness.MaxRows) {
		param := "witness.max_rows." + t
		if watchedTable(param, t) && (c.Witness.MaxRows[t] < 1 || c.Witness.MaxRows[t] > MaxMaxRowsAffected) {
			bad(param, fmt.Sprintf("must be between 1 and %d.", MaxMaxRowsAffected))
		}
	}
	if c.Witness.L2Timeout != "" {
		if d, err := time.ParseDuration(c.Witness.L2Timeout); err != nil || d < MinL2Timeout || d > MaxL2Timeout {
			bad("witness.l2_timeout", "must be a duration between 1s and 60s, like 10s.")
		}
	}
	if c.Witness.DiffRows != 0 && (c.Witness.DiffRows < 1 || c.Witness.DiffRows > MaxDiffRows) {
		bad("witness.diff_rows", fmt.Sprintf("must be between 1 and %d.", MaxDiffRows))
	}
	if !c.Witness.Enabled && (len(c.Witness.AutoColumns) > 0 || len(c.Witness.MaxRows) > 0 || c.Witness.StrictTriggers || c.Witness.L2Timeout != "" || c.Witness.DiffRows != 0) {
		bad("witness.enabled", "must be true for the L2 settings to apply. Turn the witness on, or remove them.")
	}
	// functions
	if len(c.Functions) > 64 {
		bad("functions", "lists at most 64 functions.")
	}
	fnames := map[string]bool{}
	for i, f := range c.Functions {
		param := fmt.Sprintf("functions[%d]", i)
		p, ok := setup.SplitQualified(f.Name, 2)
		switch {
		case !ok:
			bad(param+".name", "must be schema.function in lowercase, like public.ship_order.")
		case !seen[p[0]]:
			bad(param+".name", fmt.Sprintf("is in schema %s, which isn't in allowed_schemas.", p[0]))
		case fnames[f.Name]:
			bad(param+".name", f.Name+" is listed twice. Overloads aren't supported; give each function its own name.")
		}
		fnames[f.Name] = true
		if len(f.Args) > 32 {
			bad(param+".args", "lists at most 32 arguments.")
		}
		anames := map[string]bool{}
		for j, a := range f.Args {
			switch {
			case !setup.ValidIdent(a.Name):
				bad(fmt.Sprintf("%s.args[%d].name", param, j), "must be a lowercase identifier.")
			case anames[a.Name]:
				bad(fmt.Sprintf("%s.args[%d].name", param, j), a.Name+" is listed twice.")
			}
			anames[a.Name] = true
			if !setup.ValidType(a.Type) {
				bad(fmt.Sprintf("%s.args[%d].type", param, j), "must be a Postgres type name, like bigint, text or numeric(10,2).")
			}
		}
		switch f.SideEffect {
		case "":
			c.Functions[i].SideEffect = string(connector.SideEffectWrite)
		case "read", "write", "destructive":
		default:
			bad(param+".side_effect", "must be read, write or destructive.")
		}
		if len(f.Description) > 1024 {
			bad(param+".description", "is at most 1,024 characters.")
		}
		if f.Args == nil {
			c.Functions[i].Args = []FunctionArg{}
		}
		for j, w := range f.Writes {
			wp := fmt.Sprintf("%s.writes[%d]", param, j)
			if _, ok := setup.SplitQualified(w.Table, 2); !ok {
				bad(wp+".table", "must be schema.table in lowercase, like public.orders.")
			}
			if len(w.Ops) == 0 {
				bad(wp+".ops", "lists insert, update or delete.")
			}
			for _, op := range w.Ops {
				if op != "insert" && op != "update" && op != "delete" {
					bad(wp+".ops", "lists only insert, update and delete.")
					break
				}
			}
			if w.MaxRows != nil && (*w.MaxRows < 1 || *w.MaxRows > MaxMaxRowsAffected) {
				bad(wp+".max_rows", fmt.Sprintf("must be between 1 and %d.", MaxMaxRowsAffected))
			}
			for k, col := range w.Columns {
				if !setup.ValidIdent(col) {
					bad(fmt.Sprintf("%s.columns[%d]", wp, k), "must be a lowercase column name.")
				}
			}
		}
		if len(f.Writes) > 16 {
			bad(param+".writes", "lists at most 16 tables.")
		}
	}
	if c.Functions == nil {
		c.Functions = []Function{}
	}
	if c.WritableTables == nil {
		c.WritableTables = []string{}
	}
	return c, errs
}

// Normalize returns the canonical JSON of c.
func (c Config) Normalize() json.RawMessage {
	b, _ := json.Marshal(c)
	return b
}

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
