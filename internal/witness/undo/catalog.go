package undo

import (
	"regexp"
	"time"
)

// Catalog is what the customer database's catalog says now about the tables an undo touches, read
// by the Postgres connector as deedglass_agent with constant queries by relid (S23 §4 step 4).
type Catalog struct {
	Role      string // current_user: deedglass_agent
	BypassRLS bool   // the role has BYPASSRLS
	Tables    map[uint32]*TableCatalog
}

// TableCatalog is one relation by relid. Exists is false when the relid names nothing now.
type TableCatalog struct {
	RelID     uint32
	Exists    bool
	Schema    string
	Name      string
	Kind      string // pg_class.relkind: r, p
	Columns   []ColumnCatalog
	PK        []string // the primary key's columns, in index order; empty when none
	ReplIdent string   // pg_class.relreplident: d, n, f, i
	Triggers  []string // enabled user triggers (NOT tgisinternal AND tgenabled <> 'D'), sorted
	Rules     []string // rules other than ON SELECT, sorted
	Referrers []ForeignKey
	RLS       bool // relrowsecurity
	ForceRLS  bool // relforcerowsecurity
	Select    bool // has_table_privilege(current_user, relid, …)
	Insert    bool
	Update    bool
	Delete    bool
}

// String is "schema.name".
func (t *TableCatalog) String() string { return t.Schema + "." + t.Name }

// Column returns the named live column.
func (t *TableCatalog) Column(name string) (ColumnCatalog, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return ColumnCatalog{}, false
}

// ColumnCatalog is one live column (attisdropped is false; attnum > 0).
type ColumnCatalog struct {
	Name      string
	Type      string // format_type(atttypid, atttypmod)
	TypeOID   uint32
	Generated string // attgenerated: "" none, "s" stored, "v" virtual (PG 18)
	Identity  string // attidentity: "" none, "a" always, "d" by default
}

// ForeignKey is a constraint in another table (or this one) that references this table.
type ForeignKey struct {
	Name        string
	From        string   // the referencing table, "schema.name"
	FromColumns []string // the referencing columns in that table, in the constraint's order
	Columns     []string // the referenced columns in this table, in the same order
	OnDelete    string   // confdeltype: a no action, r restrict, c cascade, n set null, d set default
	OnUpdate    string   // confupdtype
}

// Acts reports whether the action does something to the referencing rows (cascade, set null, set
// default). No action and restrict only make a statement fail, which rolls the undo back cleanly.
func Acts(action string) bool { return action == "c" || action == "n" || action == "d" }

// TypePattern is what a type name from format_type must match before it is used in a cast
// (S23 §3.4). Anything else refuses the plan.
var TypePattern = regexp.MustCompile(`^[A-Za-z0-9_ ."(),\[\]]{1,128}$`)

// ValidTypeName reports whether s may be used in a cast.
func ValidTypeName(s string) bool { return TypePattern.MatchString(s) }

// Current is the current state of the rows the plan's guards name, read in one statement per table
// (preview: without locks; RunUndo: FOR UPDATE, keys sorted): table relid → key → the row as text,
// with every guard column. A key with no row is absent from the map.
type Current map[uint32]map[string]Image

// RunOptions are an undo run's flag and limits (undo.lock_timeout, undo.statement_timeout).
type RunOptions struct {
	AllowTriggers    bool
	LockTimeout      time.Duration
	StatementTimeout time.Duration
}
