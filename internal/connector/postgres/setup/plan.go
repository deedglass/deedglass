package setup

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Desired is what a connector's config asks of the database.
type Desired struct {
	AllowedSchemas []string // the read scope (and search_path)
	WritableTables []string // schema.table: INSERT, UPDATE, DELETE for deedglass_agent
	WatchedTables  []string // schema.table: replica identity full, in the publication; includes every writable table
	Functions      []string // signatures for EXECUTE: public.ship_order(bigint)
	Provider       Provider // RDS and Aurora get rds_replication instead of the REPLICATION attribute
}

// Want is the introspection scope for d.
func (d Desired) Want() Want {
	return Want{Schemas: append([]string{}, d.AllowedSchemas...), Tables: sortedUnique(append(append([]string{}, d.WritableTables...), d.WatchedTables...)),
		Functions: append([]string{}, d.Functions...)}
}

// Change is one reviewable step of a plan: a summary line and the statements that make it.
type Change struct {
	Op      string   `json:"op"`      // "+" create, "~" change, "-" remove
	Summary string   `json:"summary"` // "role deedglass_agent · password generated at apply"
	SQL     []string `json:"sql"`
	Lock    bool     `json:"lock,omitempty"`     // takes a brief ACCESS EXCLUSIVE lock
	NewRole string   `json:"new_role,omitempty"` // a login role created here: Apply sets its password
}

// Note is a plan line that needs a person (○): something setup can't or won't do in its
// transaction, like wal_level.
type Note struct {
	Text   string `json:"text"`
	FixSQL string `json:"fix_sql,omitempty"`
}

// Plan is what init shows and applies.
type Plan struct {
	Changes  []Change `json:"changes"`
	Notes    []Note   `json:"notes"`
	Blockers []string `json:"blockers"` // the plan can't be applied until these are fixed
	State    *State   `json:"-"`
}

// Empty reports whether nothing needs to change.
func (p *Plan) Empty() bool { return len(p.Changes) == 0 }

// Role settings init pins (S17 §3.3; CLAUDE.md amendment #5 for the witness).
var (
	AgentSettings = [][2]string{{"statement_timeout", "5s"}, {"lock_timeout", "2s"}, {"idle_in_transaction_session_timeout", "10s"}}
	// WitnessSettings are the decode session's pinned settings; S20 asserts them on every poll.
	WitnessSettings = [][2]string{{"TimeZone", "UTC"}, {"DateStyle", "ISO, YMD"}, {"IntervalStyle", "postgres"}, {"bytea_output", "hex"}, {"extra_float_digits", "3"}}
)

// reType is a function argument type the planner names in DDL: bigint, text, numeric(10,2),
// timestamp with time zone, int4[] …
var reType = regexp.MustCompile(`^[a-z_][a-z0-9_ ]{0,62}(\([0-9, ]{1,16}\))?(\[\])?$`)

// ValidType reports whether s is an argument type the planner accepts.
func ValidType(s string) bool { return reType.MatchString(s) && !strings.Contains(s, "  ") }

// quoteIdent quotes schema-qualified names for DDL. Each part has passed ValidIdent.
//
//deedglass:allow-sql-build ddl-identifiers
func quoteIdent(qualified string) string {
	return pgx.Identifier(strings.Split(qualified, ".")).Sanitize()
}

// QuoteFunction renders "public.ship_order(bigint)" as `"public"."ship_order"(bigint)`.
func QuoteFunction(sig string) string {
	name, args, _ := strings.Cut(sig, "(")
	return quoteIdent(name) + "(" + args
}

// quoteLiteral is a SQL string literal (standard_conforming_strings on, as since 9.1).
func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Build diffs st against d. It is pure: the same state and config always give the same plan.
func Build(st *State, d Desired) *Plan {
	p := &Plan{State: st, Changes: []Change{}, Notes: []Note{}, Blockers: []string{}}
	b := planner{st: st, d: d, p: p}
	b.blockers()
	if len(p.Blockers) > 0 {
		return p
	}
	b.roles()
	b.schema()
	b.grants()
	b.revokes()
	b.replicaIdentity()
	b.publication()
	b.notes()
	return p
}

type planner struct {
	st *State
	d  Desired
	p  *Plan
}

func (b *planner) add(c Change) { b.p.Changes = append(b.p.Changes, c) }

func (b *planner) blockers() {
	st, d := b.st, b.d
	if st.ControlPlane {
		b.p.Blockers = append(b.p.Blockers, fmt.Sprintf("%s has %s, so it's Deedglass's own control-plane database. Point the admin DSN at the customer database instead.", st.Database, ControlPlaneMark))
		return
	}
	for _, s := range d.AllowedSchemas {
		if !st.SchemasFound[s] {
			b.p.Blockers = append(b.p.Blockers, fmt.Sprintf("Schema %s doesn't exist in %s. Fix allowed_schemas, or create the schema first.", s, st.Database))
		}
	}
	for _, t := range sortedUnique(append(append([]string{}, d.WritableTables...), d.WatchedTables...)) {
		ts, ok := st.Tables[t]
		switch {
		case !ok:
			b.p.Blockers = append(b.p.Blockers, fmt.Sprintf("Table %s doesn't exist in %s. Fix the config, or create the table first.", t, st.Database))
		case ts.Kind != "r" && ts.Kind != "p":
			b.p.Blockers = append(b.p.Blockers, fmt.Sprintf("%s is a %s, not a table. Writable and watched tables must be tables.", t, relKind(ts.Kind)))
		}
	}
	for _, f := range d.Functions {
		if !st.Functions[f].Exists {
			b.p.Blockers = append(b.p.Blockers, fmt.Sprintf("Function %s doesn't exist in %s. Fix functions in the config, or create it first.", f, st.Database))
		}
	}
	missing := !st.Roles[RoleOwner].Exists || !st.Roles[RoleAgent].Exists || !st.Roles[RoleWitness].Exists
	if missing && !st.CurrentSuper && !st.CurrentCreate {
		b.p.Blockers = append(b.p.Blockers, fmt.Sprintf("%s can't create roles. Run init as a superuser, or as a role with CREATEROLE (on RDS, the master user).", st.CurrentUser))
	}
}

func relKind(k string) string {
	switch k {
	case "v":
		return "view"
	case "m":
		return "materialized view"
	case "f":
		return "foreign table"
	}
	return "relation"
}

// attrs renders a role's attributes; want is the full desired set.
func attrSQL(name string, want map[string]bool) string {
	order := []string{"LOGIN", "SUPERUSER", "CREATEDB", "CREATEROLE", "REPLICATION", "BYPASSRLS", "INHERIT"}
	var parts []string
	for _, a := range order {
		v, ok := want[a]
		if !ok {
			continue
		}
		if v {
			parts = append(parts, a)
		} else {
			parts = append(parts, "NO"+a)
		}
	}
	return strings.Join(parts, " ")
}

func roleDiff(rs RoleState, want map[string]bool) map[string]bool {
	have := map[string]bool{"LOGIN": rs.Login, "SUPERUSER": rs.Super, "CREATEDB": rs.CreateDB, "CREATEROLE": rs.CreateRole,
		"REPLICATION": rs.Replication, "BYPASSRLS": rs.BypassRLS, "INHERIT": rs.Inherit}
	out := map[string]bool{}
	for k, v := range want {
		if have[k] != v {
			out[k] = v
		}
	}
	return out
}

func (b *planner) roles() {
	st := b.st
	rds := b.d.Provider == ProviderRDS || b.d.Provider == ProviderAurora
	type spec struct {
		name     string
		want     map[string]bool
		create   string
		summary  string
		login    bool
		settings [][2]string
	}
	witnessWant := map[string]bool{"LOGIN": true, "SUPERUSER": false, "CREATEDB": false, "CREATEROLE": false, "BYPASSRLS": false, "INHERIT": false, "REPLICATION": true}
	witnessCreate := "CREATE ROLE deedglass_witness LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOINHERIT REPLICATION;"
	witnessSummary := "role deedglass_witness · REPLICATION · password generated at apply"
	if rds {
		delete(witnessWant, "REPLICATION")
		witnessCreate = "CREATE ROLE deedglass_witness LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOINHERIT;"
		witnessSummary = "role deedglass_witness · rds_replication · password generated at apply"
	}
	specs := []spec{
		{RoleOwner, map[string]bool{"LOGIN": false, "SUPERUSER": false, "CREATEDB": false, "CREATEROLE": false, "REPLICATION": false, "BYPASSRLS": false},
			"CREATE ROLE deedglass_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;", "role deedglass_owner · NOLOGIN", false, nil},
		{RoleAgent, map[string]bool{"LOGIN": true, "SUPERUSER": false, "CREATEDB": false, "CREATEROLE": false, "REPLICATION": false, "BYPASSRLS": false, "INHERIT": false},
			"CREATE ROLE deedglass_agent LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;", "role deedglass_agent · password generated at apply", true, AgentSettings},
		{RoleWitness, witnessWant, witnessCreate, witnessSummary, true, WitnessSettings},
	}
	for _, s := range specs {
		rs := st.Roles[s.name]
		var sql []string
		if !rs.Exists {
			sql = append(sql, s.create)
			if s.name == RoleWitness && rds {
				sql = append(sql, "GRANT rds_replication TO deedglass_witness;")
			}
			for _, kv := range s.settings {
				sql = append(sql, fmt.Sprintf("ALTER ROLE %s SET %s = %s;", s.name, kv[0], quoteLiteral(kv[1])))
			}
			c := Change{Op: "+", Summary: s.summary, SQL: sql}
			if s.login {
				c.NewRole = s.name
			}
			b.add(c)
			continue
		}
		var what []string
		if diff := roleDiff(rs, s.want); len(diff) > 0 {
			sql = append(sql, fmt.Sprintf("ALTER ROLE %s %s;", s.name, attrSQL(s.name, diff)))
			what = append(what, "attributes "+strings.ToLower(attrSQL(s.name, diff)))
		}
		if s.name == RoleWitness && rds && !contains(rs.MemberOf, "rds_replication") {
			sql = append(sql, "GRANT rds_replication TO deedglass_witness;")
			what = append(what, "rds_replication")
		}
		if s.name == RoleAgent {
			for _, m := range rs.MemberOf {
				sql = append(sql, fmt.Sprintf("REVOKE %s FROM deedglass_agent;", quoteIdent(m)))
				what = append(what, "leaves "+m)
			}
		}
		var changed []string
		for _, kv := range s.settings {
			if rs.Settings[strings.ToLower(kv[0])] != kv[1] {
				sql = append(sql, fmt.Sprintf("ALTER ROLE %s SET %s = %s;", s.name, kv[0], quoteLiteral(kv[1])))
				changed = append(changed, kv[0])
			}
		}
		if len(changed) > 0 {
			what = append(what, "settings "+strings.Join(changed, ", "))
		}
		if len(sql) > 0 {
			b.add(Change{Op: "~", Summary: "role " + s.name + " · " + strings.Join(what, " · "), SQL: sql})
		}
	}
	// The installer must be able to SET ROLE deedglass_owner (PG 16+ membership rules).
	if !st.CurrentSuper && !st.CurrentOwnerSet {
		b.add(Change{Op: "+", Summary: "membership · " + st.CurrentUser + " may act as deedglass_owner during setup", SQL: []string{"GRANT deedglass_owner TO CURRENT_USER;"}})
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func (b *planner) schema() {
	st := b.st
	short := ShortHash(BeginActionBodySHA256)
	if !st.SchemaExists {
		b.add(Change{Op: "+", Summary: "schema deedglass · ledger · begin_action (" + short + ")",
			SQL: []string{Section("schema"), Section("ledger"), Section("begin_action")}})
		return
	}
	var sql []string
	var what []string
	if st.SchemaOwner != RoleOwner {
		sql = append(sql, "ALTER SCHEMA deedglass OWNER TO deedglass_owner;")
		what = append(what, "owner")
	}
	for _, role := range []string{RoleAgent, RoleWitness} {
		if !st.SchemaUsage[role+"/"+Schema] {
			sql = append(sql, "GRANT USAGE ON SCHEMA deedglass TO "+role+";")
			what = append(what, role+" usage")
		}
	}
	if len(sql) > 0 {
		b.add(Change{Op: "~", Summary: "schema deedglass · " + strings.Join(what, " · "), SQL: sql})
	}
	l := st.Ledger
	if !l.Exists {
		b.add(Change{Op: "+", Summary: "ledger deedglass.ledger", SQL: []string{Section("ledger")}})
	} else {
		sql, what = nil, nil
		if l.Owner != RoleOwner {
			sql = append(sql, "ALTER TABLE deedglass.ledger OWNER TO deedglass_owner;")
			what = append(what, "owner")
		}
		if l.ReplIdent != "f" {
			sql = append(sql, "ALTER TABLE deedglass.ledger REPLICA IDENTITY FULL;")
			what = append(what, "replica identity full")
		}
		if !l.RLS {
			sql = append(sql, "ALTER TABLE deedglass.ledger ENABLE ROW LEVEL SECURITY;")
			what = append(what, "row level security")
		}
		if !l.ForceRLS {
			sql = append(sql, "ALTER TABLE deedglass.ledger FORCE ROW LEVEL SECURITY;")
			what = append(what, "forced")
		}
		var pol []string
		if !contains(l.Policies, "owner_all") {
			pol = append(pol, "CREATE POLICY owner_all ON deedglass.ledger TO deedglass_owner USING (true) WITH CHECK (true);")
		}
		if !contains(l.Policies, "witness_read") {
			pol = append(pol, "CREATE POLICY witness_read ON deedglass.ledger FOR SELECT TO deedglass_witness USING (true);")
		}
		if len(pol) > 0 {
			sql = append(sql, "SET LOCAL ROLE deedglass_owner;")
			sql = append(sql, pol...)
			sql = append(sql, "RESET ROLE;")
			what = append(what, "policies")
		}
		if len(l.PublicPrivs) > 0 || len(l.AgentPrivs) > 0 {
			sql = append(sql, "REVOKE ALL ON deedglass.ledger FROM PUBLIC, deedglass_agent;")
			what = append(what, "no direct access for deedglass_agent")
		}
		if !l.WitnessSelect {
			sql = append(sql, "GRANT SELECT ON deedglass.ledger TO deedglass_witness;")
			what = append(what, "witness reads")
		}
		if len(sql) > 0 {
			b.add(Change{Op: "~", Summary: "ledger · " + strings.Join(what, " · "), SQL: sql})
		}
		if !ledgerShapeOK(l.Columns) {
			b.p.Notes = append(b.p.Notes, Note{Text: "deedglass.ledger has a different shape than this version expects. Rename it (ALTER TABLE deedglass.ledger RENAME TO ledger_old), then run init again."})
		}
	}
	ba := st.BeginAction
	if !ba.Exists {
		b.add(Change{Op: "+", Summary: "begin_action (" + short + ")", SQL: []string{Section("begin_action")}})
		return
	}
	var why []string
	switch {
	case ba.BodySHA256 != BeginActionBodySHA256:
		why = append(why, "body changed: expected "+short+", found "+ShortHash(ba.BodySHA256))
	case !ba.SecDef:
		why = append(why, "not security definer")
	case ba.Config != "search_path=pg_catalog, pg_temp, TimeZone=UTC":
		why = append(why, "settings changed")
	}
	if ba.Owner != RoleOwner {
		why = append(why, "owner "+ba.Owner)
	}
	if !ba.AgentExec || ba.PublicExec || ba.WitnessExe {
		why = append(why, "grants")
	}
	if len(why) == 0 {
		return
	}
	sql = nil
	if ba.Owner != RoleOwner {
		sql = append(sql, "ALTER FUNCTION "+BeginActionSig+" OWNER TO deedglass_owner;")
	}
	sql = append(sql, Section("begin_action"))
	if ba.WitnessExe {
		sql = append(sql, "REVOKE EXECUTE ON FUNCTION "+BeginActionSig+" FROM deedglass_witness;")
	}
	b.add(Change{Op: "~", Summary: "begin_action · replace (" + strings.Join(why, " · ") + ")", SQL: sql})
}

// LedgerColumns is the ledger's expected shape as introspection renders it.
var LedgerColumns = []string{"action_id text not null", "org_id text not null", "agent_id text not null", "intent_hash text",
	"sql_digest text not null", "xid8 xid8 not null", "created_at timestamp with time zone not null"}

func ledgerShapeOK(cols []string) bool {
	if len(cols) != len(LedgerColumns) {
		return false
	}
	for i := range cols {
		if cols[i] != LedgerColumns[i] {
			return false
		}
	}
	return true
}

func schemaOf(t string) string { s, _, _ := strings.Cut(t, "."); return s }

func (b *planner) grants() {
	st, d := b.st, b.d
	var sql []string
	readTables := 0
	for _, s := range d.AllowedSchemas {
		if !st.SchemaUsage[RoleAgent+"/"+s] {
			sql = append(sql, fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO deedglass_agent;", quoteIdent(s)))
		}
		missing := false
		for _, name := range sortedKeys(st.Tables) {
			t := st.Tables[name]
			if schemaOf(name) != s {
				continue
			}
			readTables++
			if !t.HasAgent("SELECT") {
				missing = true
			}
		}
		if missing {
			sql = append(sql, fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA %s TO deedglass_agent;", quoteIdent(s)))
		}
	}
	var wrote []string
	for _, t := range d.WritableTables {
		ts := st.Tables[t]
		if !ts.HasAgent("INSERT") || !ts.HasAgent("UPDATE") || !ts.HasAgent("DELETE") {
			sql = append(sql, fmt.Sprintf("GRANT INSERT, UPDATE, DELETE ON TABLE %s TO deedglass_agent;", quoteIdent(t)))
			wrote = append(wrote, t)
		}
		for _, seq := range ts.Sequences {
			if !contains(ts.SeqAgentUsage, seq) {
				sql = append(sql, fmt.Sprintf("GRANT USAGE ON SEQUENCE %s TO deedglass_agent;", quoteIdent(seq)))
			}
		}
	}
	for _, f := range d.Functions {
		if !st.Functions[f].AgentExec {
			sql = append(sql, fmt.Sprintf("GRANT EXECUTE ON FUNCTION %s TO deedglass_agent;", QuoteFunction(f)))
		}
	}
	// the witness reads what it watches (S21's read-backs)
	var wschemas []string
	for _, t := range d.WatchedTables {
		wschemas = append(wschemas, schemaOf(t))
	}
	for _, s := range sortedUnique(wschemas) {
		if !st.SchemaUsage[RoleWitness+"/"+s] {
			sql = append(sql, fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO deedglass_witness;", quoteIdent(s)))
		}
	}
	for _, t := range d.WatchedTables {
		if !st.Tables[t].WitnessSelect {
			sql = append(sql, fmt.Sprintf("GRANT SELECT ON TABLE %s TO deedglass_witness;", quoteIdent(t)))
		}
	}
	if len(sql) == 0 {
		return
	}
	parts := []string{"grants"}
	parts = append(parts, fmt.Sprintf("read %s (%d %s)", strings.Join(d.AllowedSchemas, ", "), readTables, plural(readTables, "table", "tables")))
	if len(wrote) > 0 {
		parts = append(parts, "write "+strings.Join(wrote, ", "))
	}
	b.add(Change{Op: "+", Summary: strings.Join(parts, " · "), SQL: sql})
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (b *planner) revokes() {
	st, d := b.st, b.d
	writable := map[string]bool{}
	for _, t := range d.WritableTables {
		writable[t] = true
	}
	var sql, what []string
	for _, name := range sortedKeys(st.Tables) {
		t := st.Tables[name]
		var drop []string
		for _, p := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
			if !t.HasAgent(p) {
				continue
			}
			if writable[name] && (p == "INSERT" || p == "UPDATE" || p == "DELETE") {
				continue
			}
			drop = append(drop, p)
		}
		if len(drop) > 0 {
			sql = append(sql, fmt.Sprintf("REVOKE %s ON TABLE %s FROM deedglass_agent;", strings.Join(drop, ", "), quoteIdent(name)))
			what = append(what, name)
		}
	}
	for _, name := range st.AgentExtraWrites {
		if _, ok := SplitQualified(name, 2); !ok {
			continue
		}
		sql = append(sql, fmt.Sprintf("REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON TABLE %s FROM deedglass_agent;", quoteIdent(name)))
		what = append(what, name)
	}
	if len(sql) > 0 {
		b.add(Change{Op: "-", Summary: "grants · write " + strings.Join(what, ", "), SQL: sql})
	}
}

func (b *planner) replicaIdentity() {
	var sql []string
	for _, t := range b.d.WatchedTables {
		if b.st.Tables[t].ReplIdent != "f" {
			sql = append(sql, fmt.Sprintf("ALTER TABLE %s REPLICA IDENTITY FULL;", quoteIdent(t)))
		}
	}
	if len(sql) == 0 {
		return
	}
	n := len(sql)
	b.add(Change{Op: "+", Summary: fmt.Sprintf("replica identity full · %d watched %s · brief lock on each", n, plural(n, "table", "tables")), SQL: sql, Lock: true})
}

// PublicationTables is the publication's desired table list: the ledger, then the watched tables.
func PublicationTables(d Desired) []string {
	return append([]string{Ledger}, sortedUnique(d.WatchedTables)...)
}

func (b *planner) publication() {
	st, d := b.st, b.d
	want := PublicationTables(d)
	quoted := make([]string, len(want))
	for i, t := range want {
		if t == Ledger {
			quoted[i] = Ledger
		} else {
			quoted[i] = quoteIdent(t)
		}
	}
	with := "publish = 'insert, update, delete, truncate'"
	if st.VersionNum >= 180000 {
		// PG 18 refuses UPDATE and DELETE on a published REPLICA IDENTITY FULL table with a stored
		// generated column unless the publication publishes them (DL-037).
		with += ", publish_generated_columns = " + GenColsStored
	}
	create := fmt.Sprintf("CREATE PUBLICATION deedglass_pub FOR TABLE %s WITH (%s);", strings.Join(quoted, ", "), with)
	pub := st.Publication
	n := len(want)
	switch {
	case !pub.Exists:
		b.add(Change{Op: "+", Summary: fmt.Sprintf("publication deedglass_pub · %d %s", n, plural(n, "table", "tables")), SQL: []string{create}})
		return
	case pub.AllTables || len(pub.Schemas) > 0:
		b.add(Change{Op: "~", Summary: fmt.Sprintf("publication deedglass_pub · recreate with %d tables (it publishes whole schemas)", n),
			SQL: []string{"DROP PUBLICATION deedglass_pub;", create}})
		return
	}
	var sql, what []string
	have := sortedUnique(pub.Tables)
	sortedWant := sortedUnique(want)
	if strings.Join(have, ",") != strings.Join(sortedWant, ",") || len(pub.RowFilters) > 0 || len(pub.ColLists) > 0 {
		sql = append(sql, fmt.Sprintf("ALTER PUBLICATION deedglass_pub SET TABLE %s;", strings.Join(quoted, ", ")))
		what = append(what, fmt.Sprintf("set %d %s", n, plural(n, "table", "tables")))
		for _, t := range have {
			if !contains(sortedWant, t) {
				b.p.Notes = append(b.p.Notes, Note{Text: fmt.Sprintf("%s leaves the publication and keeps replica identity full. Set it back with ALTER TABLE %s REPLICA IDENTITY DEFAULT if nothing else needs it.", t, quoteIdent(t))})
			}
		}
	}
	if strings.Join(pub.Publish, ", ") != "insert, update, delete, truncate" {
		sql = append(sql, "ALTER PUBLICATION deedglass_pub SET (publish = 'insert, update, delete, truncate');")
		what = append(what, "insert, update, delete, truncate")
	}
	if st.VersionNum >= 180000 && pub.GenCols != GenColsStored {
		sql = append(sql, "ALTER PUBLICATION deedglass_pub SET (publish_generated_columns = "+GenColsStored+");")
		what = append(what, "stored generated columns")
	}
	if len(sql) > 0 {
		b.add(Change{Op: "~", Summary: "publication deedglass_pub · " + strings.Join(what, " · "), SQL: sql})
	}
}

// publish_generated_columns values (PG 18+).
const (
	GenColsNone   = "none"
	GenColsStored = "stored"
)

func (b *planner) notes() {
	if b.st.Settings["wal_level"] != "logical" {
		fix, fixSQL := WALLevelFix(b.d.Provider)
		b.p.Notes = append(b.p.Notes, Note{Text: fmt.Sprintf("wal_level is %s. L2 needs logical. %s", b.st.Settings["wal_level"], fix), FixSQL: fixSQL})
	}
}

// Body is the plan's SQL: one transaction, as Apply runs it (without passwords).
func (p *Plan) Body() string {
	var sb strings.Builder
	sb.WriteString("BEGIN;\nSET LOCAL search_path = pg_catalog, pg_temp;\nSET LOCAL lock_timeout = '5s';\n")
	for _, c := range p.Changes {
		sb.WriteString("\n-- " + c.Op + " " + c.Summary + "\n")
		for _, s := range c.SQL {
			sb.WriteString(strings.TrimRight(s, "\n") + "\n")
		}
		if c.NewRole != "" {
			sb.WriteString("-- Credentials for " + c.NewRole + " are generated at apply and never written to this file.\n")
		}
	}
	sb.WriteString("\nCOMMIT;\n")
	return sb.String()
}

// SHA256 is the plan's identity: the digest of Body. init records it in the admin deed.
func (p *Plan) SHA256() string { return SHA256(p.Body()) }

// File is the reviewable plan file: a header, then Body.
func (p *Plan) File(header []string) string {
	var sb strings.Builder
	for _, h := range header {
		sb.WriteString("-- " + h + "\n")
	}
	sb.WriteString("-- plan " + p.SHA256() + "\n\n")
	sb.WriteString(p.Body())
	return sb.String()
}

// ErrPlanChanged: the database changed between the plan and the apply.
var ErrPlanChanged = errors.New("setup: the database changed since the plan was made")

// ApplyError names the change that failed.
type ApplyError struct {
	Change Change
	Err    error
}

func (e *ApplyError) Error() string {
	return fmt.Sprintf("%s %s: %v", e.Change.Op, e.Change.Summary, e.Err)
}
func (e *ApplyError) Unwrap() error { return e.Err }

// Apply runs the plan in one transaction on conn: it re-reads the catalogs inside the transaction
// and refuses when the fresh plan differs from p (ErrPlanChanged), then runs every change, adding
// ALTER ROLE … PASSWORD with a client-computed SCRAM verifier after each role it creates.
// passwords maps a new role to its password; a role without one keeps no password.
func Apply(ctx context.Context, conn *pgx.Conn, p *Plan, d Desired, passwords map[string]string) error {
	if len(p.Blockers) > 0 {
		return errors.New("setup: the plan has blockers")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, "SET LOCAL search_path = pg_catalog, pg_temp"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
		return err
	}
	fresh, err := Introspect(ctx, tx, d.Want())
	if err != nil {
		return err
	}
	if Build(fresh, d).SHA256() != p.SHA256() {
		return ErrPlanChanged
	}
	for _, c := range p.Changes {
		for _, s := range c.SQL {
			if _, err := tx.Exec(ctx, s); err != nil {
				return &ApplyError{Change: c, Err: err}
			}
		}
		if pw, ok := passwords[c.NewRole]; ok && c.NewRole != "" {
			v, err := SCRAMVerifier(pw, nil)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "ALTER ROLE "+c.NewRole+" PASSWORD "+quoteLiteral(v)); err != nil {
				return &ApplyError{Change: c, Err: errors.New("setting the generated password failed")}
			}
		}
	}
	return tx.Commit(ctx)
}

// SetPasswords rotates the passwords of existing login roles (init --rotate-passwords) in one
// transaction, with client-computed verifiers.
func SetPasswords(ctx context.Context, conn *pgx.Conn, passwords map[string]string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, role := range sortedKeys(passwords) {
		if role != RoleAgent && role != RoleWitness {
			return fmt.Errorf("setup: %s isn't a Deedglass login role", role)
		}
		v, err := SCRAMVerifier(passwords[role], nil)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "ALTER ROLE "+role+" PASSWORD "+quoteLiteral(v)); err != nil {
			return errors.New("setup: setting the password of " + role + " failed")
		}
	}
	return tx.Commit(ctx)
}
