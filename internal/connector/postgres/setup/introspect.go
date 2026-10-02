package setup

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Querier is what introspection reads through: a *pgx.Conn, a pgx.Tx or a pool connection.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// reIdent is one identifier Deedglass will name in DDL: lowercase, unquoted-safe, ≤ 63 bytes.
var reIdent = regexp.MustCompile(`^[a-z_][a-z0-9_$]{0,62}$`)

// ValidIdent reports whether s is an identifier the planner accepts.
func ValidIdent(s string) bool { return reIdent.MatchString(s) }

// SplitQualified cuts "schema.table" (or "schema.table.column") into its parts, each a valid
// identifier; n is the number of parts required.
func SplitQualified(s string, n int) ([]string, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != n {
		return nil, false
	}
	for _, p := range parts {
		if !ValidIdent(p) {
			return nil, false
		}
	}
	return parts, true
}

// RoleState is one role as the catalogs show it.
type RoleState struct {
	Exists      bool              `json:"exists"`
	Super       bool              `json:"super,omitempty"`
	Inherit     bool              `json:"inherit,omitempty"`
	CreateRole  bool              `json:"create_role,omitempty"`
	CreateDB    bool              `json:"create_db,omitempty"`
	Login       bool              `json:"login,omitempty"`
	Replication bool              `json:"replication,omitempty"`
	BypassRLS   bool              `json:"bypass_rls,omitempty"`
	MemberOf    []string          `json:"member_of,omitempty"` // every role reached through pg_auth_members, sorted
	Settings    map[string]string `json:"settings,omitempty"`  // ALTER ROLE … SET (all databases)
}

// TableState is one relation the config names or that lives in an allowed schema.
type TableState struct {
	Name           string   `json:"name"` // schema.table
	Kind           string   `json:"kind"` // r, p, v, m, f
	ReplIdent      string   `json:"repl_ident,omitempty"`
	Persistence    string   `json:"persistence,omitempty"` // p, u, t
	HasPK          bool     `json:"has_pk,omitempty"`
	Triggers       []string `json:"triggers,omitempty"`
	AgentPrivs     []string `json:"agent_privs,omitempty"`     // of SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
	WitnessSelect  bool     `json:"witness_select,omitempty"`  //
	Sequences      []string `json:"sequences,omitempty"`       // owned (serial or identity) sequences, schema.name
	SeqAgentUsage  []string `json:"seq_agent_usage,omitempty"` // the owned sequences deedglass_agent may use
	PartitionChild bool     `json:"partition_child,omitempty"`
	// VirtualGenerated names the table's virtual generated columns (PG 18+). PG 18 refuses UPDATE
	// and DELETE on a published REPLICA IDENTITY FULL table that has one (42P10), so a watched
	// table must not (DL-037).
	VirtualGenerated []string `json:"virtual_generated,omitempty"`
}

// HasAgent reports whether deedglass_agent holds priv on the table.
func (t TableState) HasAgent(priv string) bool {
	for _, p := range t.AgentPrivs {
		if p == priv {
			return true
		}
	}
	return false
}

// FuncState is a function the config allow-lists (postgres.call).
type FuncState struct {
	Signature string `json:"signature"` // public.ship_order(bigint)
	Exists    bool   `json:"exists"`
	AgentExec bool   `json:"agent_exec,omitempty"`
	SecDef    bool   `json:"security_definer,omitempty"`
	Config    string `json:"config,omitempty"` // proconfig, comma-joined
}

// BeginActionState is deedglass.begin_action as the catalogs show it.
type BeginActionState struct {
	Exists     bool   `json:"exists"`
	Owner      string `json:"owner,omitempty"`
	SecDef     bool   `json:"security_definer,omitempty"`
	Config     string `json:"config,omitempty"` // proconfig, comma-joined
	BodySHA256 string `json:"body_sha256,omitempty"`
	AgentExec  bool   `json:"agent_exec,omitempty"`
	PublicExec bool   `json:"public_exec,omitempty"`
	WitnessExe bool   `json:"witness_exec,omitempty"`
}

// LedgerState is deedglass.ledger as the catalogs show it.
type LedgerState struct {
	Exists        bool     `json:"exists"`
	Owner         string   `json:"owner,omitempty"`
	ReplIdent     string   `json:"repl_ident,omitempty"`
	RLS           bool     `json:"rls,omitempty"`
	ForceRLS      bool     `json:"force_rls,omitempty"`
	Columns       []string `json:"columns,omitempty"` // "name type", in order
	Policies      []string `json:"policies,omitempty"`
	AgentPrivs    []string `json:"agent_privs,omitempty"`
	WitnessSelect bool     `json:"witness_select,omitempty"`
	PublicPrivs   []string `json:"public_privs,omitempty"`
}

// PublicationState is deedglass_pub.
type PublicationState struct {
	Exists     bool     `json:"exists"`
	AllTables  bool     `json:"all_tables,omitempty"`
	Publish    []string `json:"publish,omitempty"` // insert, update, delete, truncate
	Tables     []string `json:"tables,omitempty"`  // explicit tables, sorted
	Schemas    []string `json:"schemas,omitempty"` // FOR TABLES IN SCHEMA
	RowFilters []string `json:"row_filters,omitempty"`
	ColLists   []string `json:"column_lists,omitempty"`
	ViaRoot    bool     `json:"via_root,omitempty"`
	// GenCols is publish_generated_columns on PG 18+ ("none" or "stored"); empty before 18. PG 18
	// refuses UPDATE and DELETE on a published REPLICA IDENTITY FULL table with a stored generated
	// column unless the publication publishes them (DL-037).
	GenCols string `json:"generated_columns,omitempty"`
}

// SlotState is one replication slot.
type SlotState struct {
	Name         string `json:"name"`
	Plugin       string `json:"plugin,omitempty"`
	Active       bool   `json:"active"`
	WALStatus    string `json:"wal_status,omitempty"`
	Invalidation string `json:"invalidation_reason,omitempty"`
	Database     string `json:"database,omitempty"`
}

// State is everything the planner and the checks read, from one connection.
type State struct {
	Database         string                `json:"database"`
	VersionNum       int                   `json:"version_num"`
	Version          string                `json:"version"` // "18.6"
	CurrentUser      string                `json:"current_user"`
	CurrentSuper     bool                  `json:"current_super,omitempty"`
	CurrentCreate    bool                  `json:"current_createrole,omitempty"`
	CurrentOwnerSet  bool                  `json:"current_owner_set,omitempty"` // may SET ROLE deedglass_owner
	CurrentRDSAdmin  bool                  `json:"current_rds_superuser,omitempty"`
	ControlPlane     bool                  `json:"control_plane,omitempty"` // deedglass.schema_migrations exists
	Roles            map[string]RoleState  `json:"roles"`
	SchemaExists     bool                  `json:"schema_exists"`
	SchemaOwner      string                `json:"schema_owner,omitempty"`
	SchemaUsage      map[string]bool       `json:"schema_usage,omitempty"` // "role/schema" → USAGE
	SchemasFound     map[string]bool       `json:"schemas_found,omitempty"`
	Ledger           LedgerState           `json:"ledger"`
	BeginAction      BeginActionState      `json:"begin_action"`
	Tables           map[string]TableState `json:"tables"`
	Functions        map[string]FuncState  `json:"functions,omitempty"` // by signature
	Publication      PublicationState      `json:"publication"`
	Slots            []SlotState           `json:"slots,omitempty"`
	SendersUsed      int                   `json:"senders_used,omitempty"`
	Settings         map[string]string     `json:"settings"`
	SettingUnits     map[string]string     `json:"setting_units,omitempty"`
	NeonSettings     bool                  `json:"neon_settings,omitempty"`
	AuroraVersion    bool                  `json:"aurora,omitempty"`
	ProviderRoles    []string              `json:"provider_roles,omitempty"` // rds_superuser, supabase_admin, … that exist
	EmitPublicExec   bool                  `json:"emit_public_exec"`
	AgentExtraReads  []string              `json:"agent_extra_reads,omitempty"`  // tables outside the allowed schemas deedglass_agent can SELECT
	AgentExtraWrites []string              `json:"agent_extra_writes,omitempty"` // tables outside the allowed schemas deedglass_agent can write
}

// Want is what introspection needs to know from the config: the schemas, tables and functions to
// look at.
type Want struct {
	Schemas   []string // allowed schemas
	Tables    []string // writable + watched, schema.table
	Functions []string // signatures: public.ship_order(bigint)
}

// settingsRead are the pg_settings rows check and the planner read.
var settingsRead = []string{"wal_level", "max_replication_slots", "max_wal_senders", "max_slot_wal_keep_size",
	"idle_replication_slot_timeout", "rds.logical_replication", "server_version", "server_version_num",
	"cloudsql.logical_decoding", "ssl", "password_encryption"}

// UnsafeRoles are role memberships that make a role superuser-class (S17 §3.6).
var UnsafeRoles = []string{"pg_write_all_data", "pg_read_server_files", "pg_write_server_files", "pg_execute_server_program",
	"rds_superuser", "azure_pg_admin", "cloudsqlsuperuser", "neon_superuser", "supabase_admin"}

// providerRoles are roles whose existence tells a provider apart.
var providerRoles = []string{"rds_superuser", "rds_replication", "supabase_admin", "neon_superuser", "cloudsqlsuperuser", "azure_pg_admin"}

// Introspect reads State through q. Every query is parameterized; nothing is written. It runs as
// any role: an installer, deedglass_agent or deedglass_witness see the same catalogs.
func Introspect(ctx context.Context, q Querier, w Want) (*State, error) {
	if w.Schemas == nil {
		w.Schemas = []string{}
	}
	if w.Tables == nil {
		w.Tables = []string{}
	}
	st := &State{Roles: map[string]RoleState{}, Tables: map[string]TableState{}, Functions: map[string]FuncState{},
		Settings: map[string]string{}, SettingUnits: map[string]string{}, SchemaUsage: map[string]bool{}, SchemasFound: map[string]bool{}}
	if err := q.QueryRow(ctx, `SELECT pg_catalog.current_database(), pg_catalog.current_setting('server_version_num')::int,
		pg_catalog.current_setting('server_version'), CURRENT_USER::text,
		(SELECT r.rolsuper FROM pg_catalog.pg_roles AS r WHERE r.rolname = CURRENT_USER),
		(SELECT r.rolcreaterole FROM pg_catalog.pg_roles AS r WHERE r.rolname = CURRENT_USER),
		pg_catalog.to_regclass($1) IS NOT NULL`, ControlPlaneMark).
		Scan(&st.Database, &st.VersionNum, &st.Version, &st.CurrentUser, &st.CurrentSuper, &st.CurrentCreate, &st.ControlPlane); err != nil {
		return nil, fmt.Errorf("setup: reading the server: %w", err)
	}
	if i := strings.IndexByte(st.Version, ' '); i > 0 {
		st.Version = st.Version[:i]
	}
	// settings
	rows, err := q.Query(ctx, `SELECT s.name, s.setting, COALESCE(s.unit, '') FROM pg_catalog.pg_settings AS s WHERE s.name = ANY($1)`, settingsRead)
	if err != nil {
		return nil, fmt.Errorf("setup: reading settings: %w", err)
	}
	for rows.Next() {
		var n, v, u string
		if err := rows.Scan(&n, &v, &u); err != nil {
			rows.Close()
			return nil, err
		}
		st.Settings[n], st.SettingUnits[n] = v, u
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_settings AS s WHERE s.name LIKE 'neon.%'),
		EXISTS (SELECT 1 FROM pg_catalog.pg_proc AS p WHERE p.proname = 'aurora_version')`).Scan(&st.NeonSettings, &st.AuroraVersion); err != nil {
		return nil, fmt.Errorf("setup: reading provider markers: %w", err)
	}
	if err := scanStrings(ctx, q, &st.ProviderRoles, `SELECT r.rolname::text FROM pg_catalog.pg_roles AS r WHERE r.rolname = ANY($1) ORDER BY 1`, providerRoles); err != nil {
		return nil, err
	}
	// roles
	for _, name := range []string{RoleOwner, RoleAgent, RoleWitness} {
		rs, err := readRole(ctx, q, name)
		if err != nil {
			return nil, err
		}
		st.Roles[name] = rs
	}
	if st.Roles[RoleOwner].Exists {
		priv := "MEMBER"
		if st.VersionNum >= 160000 {
			priv = "SET"
		}
		if err := q.QueryRow(ctx, `SELECT pg_catalog.pg_has_role(CURRENT_USER, $1, $2)`, RoleOwner, priv).Scan(&st.CurrentOwnerSet); err != nil {
			return nil, fmt.Errorf("setup: reading the installer's membership: %w", err)
		}
	}
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_roles AS r WHERE r.rolname = 'rds_superuser')
		AND pg_catalog.pg_has_role(CURRENT_USER, 'rds_superuser', 'MEMBER')`).Scan(&st.CurrentRDSAdmin); err != nil {
		// pg_has_role errors when the role is missing on some versions; the EXISTS guard short-circuits
		// in practice, but a refusal here only loses a hint.
		st.CurrentRDSAdmin = false
	}
	agentOK, witnessOK := st.Roles[RoleAgent].Exists, st.Roles[RoleWitness].Exists
	// schema deedglass and the allowed schemas
	if err := q.QueryRow(ctx, `SELECT COALESCE((SELECT pg_catalog.pg_get_userbyid(n.nspowner)::text FROM pg_catalog.pg_namespace AS n WHERE n.nspname = $1), '')`, Schema).
		Scan(&st.SchemaOwner); err != nil {
		return nil, fmt.Errorf("setup: reading schema deedglass: %w", err)
	}
	st.SchemaExists = st.SchemaOwner != ""
	schemas := append([]string{Schema}, w.Schemas...)
	for _, t := range w.Tables {
		if p, ok := SplitQualified(t, 2); ok {
			schemas = append(schemas, p[0])
		}
	}
	schemas = sortedUnique(schemas)
	for _, s := range schemas {
		var found bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace AS n WHERE n.nspname = $1)`, s).Scan(&found); err != nil {
			return nil, err
		}
		st.SchemasFound[s] = found
		if !found {
			continue
		}
		for _, role := range []string{RoleAgent, RoleWitness} {
			if !st.Roles[role].Exists {
				continue
			}
			var ok bool
			if err := q.QueryRow(ctx, `SELECT pg_catalog.has_schema_privilege($1, $2, 'USAGE')`, role, s).Scan(&ok); err != nil {
				return nil, fmt.Errorf("setup: reading schema privileges: %w", err)
			}
			st.SchemaUsage[role+"/"+s] = ok
		}
	}
	if err := readLedger(ctx, q, st, agentOK, witnessOK); err != nil {
		return nil, err
	}
	if err := readBeginAction(ctx, q, st, agentOK, witnessOK); err != nil {
		return nil, err
	}
	if err := readTables(ctx, q, st, w, agentOK, witnessOK); err != nil {
		return nil, err
	}
	for _, sig := range w.Functions {
		fs := FuncState{Signature: sig}
		var oid *uint32
		if err := q.QueryRow(ctx, `SELECT pg_catalog.to_regprocedure($1)::oid`, sig).Scan(&oid); err != nil {
			return nil, fmt.Errorf("setup: reading function %s: %w", sig, err)
		}
		if oid != nil {
			fs.Exists = true
			if err := q.QueryRow(ctx, `SELECT p.prosecdef, COALESCE(pg_catalog.array_to_string(p.proconfig, ', '), '')
				FROM pg_catalog.pg_proc AS p WHERE p.oid = $1`, *oid).Scan(&fs.SecDef, &fs.Config); err != nil {
				return nil, err
			}
			if agentOK {
				if err := q.QueryRow(ctx, `SELECT pg_catalog.has_function_privilege($1, $2::oid, 'EXECUTE')`, RoleAgent, *oid).Scan(&fs.AgentExec); err != nil {
					return nil, err
				}
			}
		}
		st.Functions[sig] = fs
	}
	if err := readPublication(ctx, q, st); err != nil {
		return nil, err
	}
	if err := readSlots(ctx, q, st); err != nil {
		return nil, err
	}
	// pg_logical_emit_message has a text and a bytea form; PG 17 added a fourth argument (flush)
	// with a default, so the signatures differ by version.
	if err := q.QueryRow(ctx, `SELECT COALESCE(pg_catalog.bool_or(pg_catalog.has_function_privilege('public', p.oid, 'EXECUTE')), false)
		FROM pg_catalog.pg_proc AS p JOIN pg_catalog.pg_namespace AS n ON n.oid = p.pronamespace
		WHERE n.nspname = 'pg_catalog' AND p.proname = 'pg_logical_emit_message'`).Scan(&st.EmitPublicExec); err != nil {
		return nil, fmt.Errorf("setup: reading pg_logical_emit_message's privileges: %w", err)
	}
	if agentOK {
		if err := readAgentExtra(ctx, q, st, w); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func scanStrings(ctx context.Context, q Querier, out *[]string, sql string, args ...any) error {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		*out = append(*out, s)
	}
	return rows.Err()
}

func readRole(ctx context.Context, q Querier, name string) (RoleState, error) {
	var rs RoleState
	err := q.QueryRow(ctx, `SELECT r.rolsuper, r.rolinherit, r.rolcreaterole, r.rolcreatedb, r.rolcanlogin, r.rolreplication, r.rolbypassrls
		FROM pg_catalog.pg_roles AS r WHERE r.rolname = $1`, name).
		Scan(&rs.Super, &rs.Inherit, &rs.CreateRole, &rs.CreateDB, &rs.Login, &rs.Replication, &rs.BypassRLS)
	if errors.Is(err, pgx.ErrNoRows) {
		return RoleState{}, nil
	}
	if err != nil {
		return rs, fmt.Errorf("setup: reading role %s: %w", name, err)
	}
	rs.Exists = true
	if err := scanStrings(ctx, q, &rs.MemberOf, `WITH RECURSIVE m(roleid) AS (
			SELECT a.roleid FROM pg_catalog.pg_auth_members AS a JOIN pg_catalog.pg_roles AS r ON r.oid = a.member WHERE r.rolname = $1
			UNION
			SELECT a.roleid FROM pg_catalog.pg_auth_members AS a JOIN m ON a.member = m.roleid)
		SELECT r.rolname::text FROM m JOIN pg_catalog.pg_roles AS r ON r.oid = m.roleid ORDER BY 1`, name); err != nil {
		return rs, err
	}
	var settings []string
	if err := scanStrings(ctx, q, &settings, `SELECT pg_catalog.unnest(s.setconfig) FROM pg_catalog.pg_db_role_setting AS s
		JOIN pg_catalog.pg_roles AS r ON r.oid = s.setrole WHERE r.rolname = $1 AND s.setdatabase = 0`, name); err != nil {
		return rs, err
	}
	if len(settings) > 0 {
		rs.Settings = map[string]string{}
		for _, kv := range settings {
			k, v, _ := strings.Cut(kv, "=")
			rs.Settings[strings.ToLower(k)] = v
		}
	}
	return rs, nil
}

var tablePrivs = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}

func privsOf(ctx context.Context, q Querier, role string, oid uint32) ([]string, error) {
	var out []string
	for _, p := range tablePrivs {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT pg_catalog.has_table_privilege($1, $2::oid, $3)`, role, oid, p).Scan(&ok); err != nil {
			return nil, fmt.Errorf("setup: reading table privileges: %w", err)
		}
		if ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func readLedger(ctx context.Context, q Querier, st *State, agentOK, witnessOK bool) error {
	if !st.SchemaExists {
		return nil
	}
	var oid *uint32
	if err := q.QueryRow(ctx, `SELECT pg_catalog.to_regclass($1)::oid`, Ledger).Scan(&oid); err != nil {
		return fmt.Errorf("setup: reading the ledger: %w", err)
	}
	if oid == nil {
		return nil
	}
	l := &st.Ledger
	l.Exists = true
	if err := q.QueryRow(ctx, `SELECT pg_catalog.pg_get_userbyid(c.relowner)::text, c.relreplident::text, c.relrowsecurity, c.relforcerowsecurity
		FROM pg_catalog.pg_class AS c WHERE c.oid = $1`, *oid).Scan(&l.Owner, &l.ReplIdent, &l.RLS, &l.ForceRLS); err != nil {
		return err
	}
	if err := scanStrings(ctx, q, &l.Columns, `SELECT a.attname::text || ' ' || pg_catalog.format_type(a.atttypid, a.atttypmod)
		|| CASE WHEN a.attnotnull THEN ' not null' ELSE '' END
		FROM pg_catalog.pg_attribute AS a WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, *oid); err != nil {
		return err
	}
	if err := scanStrings(ctx, q, &l.Policies, `SELECT p.polname::text FROM pg_catalog.pg_policy AS p WHERE p.polrelid = $1 ORDER BY 1`, *oid); err != nil {
		return err
	}
	var err error
	if agentOK {
		if l.AgentPrivs, err = privsOf(ctx, q, RoleAgent, *oid); err != nil {
			return err
		}
	}
	if witnessOK {
		if err := q.QueryRow(ctx, `SELECT pg_catalog.has_table_privilege($1, $2::oid, 'SELECT')`, RoleWitness, *oid).Scan(&l.WitnessSelect); err != nil {
			return err
		}
	}
	return scanStrings(ctx, q, &l.PublicPrivs, `SELECT a.privilege_type::text FROM pg_catalog.pg_class AS c,
		LATERAL pg_catalog.aclexplode(COALESCE(c.relacl, pg_catalog.acldefault('r', c.relowner))) AS a
		WHERE c.oid = $1 AND a.grantee = 0 ORDER BY 1`, *oid)
}

func readBeginAction(ctx context.Context, q Querier, st *State, agentOK, witnessOK bool) error {
	if !st.SchemaExists {
		return nil
	}
	var oid *uint32
	if err := q.QueryRow(ctx, `SELECT pg_catalog.to_regprocedure($1)::oid`, BeginActionSig).Scan(&oid); err != nil {
		return fmt.Errorf("setup: reading begin_action: %w", err)
	}
	if oid == nil {
		return nil
	}
	b := &st.BeginAction
	b.Exists = true
	var src string
	if err := q.QueryRow(ctx, `SELECT pg_catalog.pg_get_userbyid(p.proowner)::text, p.prosecdef, COALESCE(pg_catalog.array_to_string(p.proconfig, ', '), ''), p.prosrc,
		EXISTS (SELECT 1 FROM pg_catalog.aclexplode(COALESCE(p.proacl, pg_catalog.acldefault('f', p.proowner))) AS a WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE')
		FROM pg_catalog.pg_proc AS p WHERE p.oid = $1`, *oid).Scan(&b.Owner, &b.SecDef, &b.Config, &src, &b.PublicExec); err != nil {
		return err
	}
	b.BodySHA256 = SHA256(src)
	if agentOK {
		if err := q.QueryRow(ctx, `SELECT pg_catalog.has_function_privilege($1, $2::oid, 'EXECUTE')`, RoleAgent, *oid).Scan(&b.AgentExec); err != nil {
			return err
		}
	}
	if witnessOK {
		if err := q.QueryRow(ctx, `SELECT pg_catalog.has_function_privilege($1, $2::oid, 'EXECUTE')`, RoleWitness, *oid).Scan(&b.WitnessExe); err != nil {
			return err
		}
	}
	return nil
}

func readTables(ctx context.Context, q Querier, st *State, w Want, agentOK, witnessOK bool) error {
	rows, err := q.Query(ctx, `SELECT c.oid, n.nspname::text || '.' || c.relname::text, c.relkind::text, c.relreplident::text, c.relpersistence::text,
			EXISTS (SELECT 1 FROM pg_catalog.pg_index AS i WHERE i.indrelid = c.oid AND i.indisprimary), c.relispartition
		FROM pg_catalog.pg_class AS c JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND (n.nspname = ANY($1) OR n.nspname::text || '.' || c.relname::text = ANY($2))
		ORDER BY 2`, w.Schemas, w.Tables)
	if err != nil {
		return fmt.Errorf("setup: reading tables: %w", err)
	}
	type rel struct {
		oid uint32
		t   TableState
	}
	var rels []rel
	for rows.Next() {
		var r rel
		if err := rows.Scan(&r.oid, &r.t.Name, &r.t.Kind, &r.t.ReplIdent, &r.t.Persistence, &r.t.HasPK, &r.t.PartitionChild); err != nil {
			rows.Close()
			return err
		}
		rels = append(rels, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	named := map[string]bool{}
	for _, t := range w.Tables {
		named[t] = true
	}
	for _, r := range rels {
		t := r.t
		if err := scanStrings(ctx, q, &t.Triggers, `SELECT t.tgname::text FROM pg_catalog.pg_trigger AS t WHERE t.tgrelid = $1 AND NOT t.tgisinternal ORDER BY 1`, r.oid); err != nil {
			return err
		}
		if st.VersionNum >= 180000 {
			if err := scanStrings(ctx, q, &t.VirtualGenerated, `SELECT a.attname::text FROM pg_catalog.pg_attribute AS a
				WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped AND a.attgenerated = 'v' ORDER BY a.attnum`, r.oid); err != nil {
				return err
			}
		}
		if agentOK {
			if t.AgentPrivs, err = privsOf(ctx, q, RoleAgent, r.oid); err != nil {
				return err
			}
		}
		if witnessOK {
			if err := q.QueryRow(ctx, `SELECT pg_catalog.has_table_privilege($1, $2::oid, 'SELECT')`, RoleWitness, r.oid).Scan(&t.WitnessSelect); err != nil {
				return err
			}
		}
		if named[t.Name] {
			srows, err := q.Query(ctx, `SELECT n.nspname::text || '.' || s.relname::text, s.oid FROM pg_catalog.pg_depend AS d
				JOIN pg_catalog.pg_class AS s ON s.oid = d.objid AND s.relkind = 'S'
				JOIN pg_catalog.pg_namespace AS n ON n.oid = s.relnamespace
				WHERE d.refobjid = $1 AND d.classid = 'pg_catalog.pg_class'::pg_catalog.regclass AND d.deptype IN ('a', 'i') ORDER BY 1`, r.oid)
			if err != nil {
				return err
			}
			type seq struct {
				name string
				oid  uint32
			}
			var seqs []seq
			for srows.Next() {
				var s seq
				if err := srows.Scan(&s.name, &s.oid); err != nil {
					srows.Close()
					return err
				}
				seqs = append(seqs, s)
			}
			srows.Close()
			for _, s := range seqs {
				t.Sequences = append(t.Sequences, s.name)
				if agentOK {
					var ok bool
					if err := q.QueryRow(ctx, `SELECT pg_catalog.has_sequence_privilege($1, $2::oid, 'USAGE')`, RoleAgent, s.oid).Scan(&ok); err != nil {
						return err
					}
					if ok {
						t.SeqAgentUsage = append(t.SeqAgentUsage, s.name)
					}
				}
			}
		}
		st.Tables[t.Name] = t
	}
	return nil
}

func readPublication(ctx context.Context, q Querier, st *State) error {
	p := &st.Publication
	var oid uint32
	var ins, upd, del, trunc bool
	err := q.QueryRow(ctx, `SELECT p.oid, p.puballtables, p.pubinsert, p.pubupdate, p.pubdelete, p.pubtruncate, p.pubviaroot
		FROM pg_catalog.pg_publication AS p WHERE p.pubname = $1`, Publication).Scan(&oid, &p.AllTables, &ins, &upd, &del, &trunc, &p.ViaRoot)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("setup: reading the publication: %w", err)
	}
	p.Exists = true
	if st.VersionNum >= 180000 {
		var gc string
		if err := q.QueryRow(ctx, `SELECT p.pubgencols::text FROM pg_catalog.pg_publication AS p WHERE p.oid = $1`, oid).Scan(&gc); err != nil {
			return fmt.Errorf("setup: reading the publication's generated columns: %w", err)
		}
		p.GenCols = map[string]string{"n": GenColsNone, "s": GenColsStored}[gc]
		if p.GenCols == "" {
			p.GenCols = gc
		}
	}
	for _, x := range []struct {
		on   bool
		name string
	}{{ins, "insert"}, {upd, "update"}, {del, "delete"}, {trunc, "truncate"}} {
		if x.on {
			p.Publish = append(p.Publish, x.name)
		}
	}
	cols := "false, false"
	if st.VersionNum >= 150000 {
		cols = "pr.prqual IS NOT NULL, pr.prattrs IS NOT NULL"
	}
	rows, err := q.Query(ctx, `SELECT n.nspname::text || '.' || c.relname::text, `+cols+`
		FROM pg_catalog.pg_publication_rel AS pr JOIN pg_catalog.pg_class AS c ON c.oid = pr.prrelid
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace WHERE pr.prpubid = $1 ORDER BY 1`, oid)
	if err != nil {
		return fmt.Errorf("setup: reading the publication's tables: %w", err)
	}
	for rows.Next() {
		var name string
		var filter, list bool
		if err := rows.Scan(&name, &filter, &list); err != nil {
			rows.Close()
			return err
		}
		p.Tables = append(p.Tables, name)
		if filter {
			p.RowFilters = append(p.RowFilters, name)
		}
		if list {
			p.ColLists = append(p.ColLists, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if st.VersionNum >= 150000 {
		return scanStrings(ctx, q, &p.Schemas, `SELECT n.nspname::text FROM pg_catalog.pg_publication_namespace AS pn
			JOIN pg_catalog.pg_namespace AS n ON n.oid = pn.pnnspid WHERE pn.pnpubid = $1 ORDER BY 1`, oid)
	}
	return nil
}

func readSlots(ctx context.Context, q Querier, st *State) error {
	inv := "''"
	if st.VersionNum >= 170000 {
		inv = "COALESCE(s.invalidation_reason, '')"
	}
	rows, err := q.Query(ctx, `SELECT s.slot_name::text, COALESCE(s.plugin::text, ''), s.active, COALESCE(s.wal_status, ''), `+inv+`, COALESCE(s.database::text, '')
		FROM pg_catalog.pg_replication_slots AS s ORDER BY 1`)
	if err != nil {
		return fmt.Errorf("setup: reading replication slots: %w", err)
	}
	for rows.Next() {
		var s SlotState
		if err := rows.Scan(&s.Name, &s.Plugin, &s.Active, &s.WALStatus, &s.Invalidation, &s.Database); err != nil {
			rows.Close()
			return err
		}
		st.Slots = append(st.Slots, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return q.QueryRow(ctx, `SELECT pg_catalog.count(*)::int FROM pg_catalog.pg_stat_replication`).Scan(&st.SendersUsed)
}

// readAgentExtra lists tables outside the allowed schemas (and outside pg_catalog,
// information_schema and deedglass) that deedglass_agent can read or write, at most 50 each.
func readAgentExtra(ctx context.Context, q Querier, st *State, w Want) error {
	const base = `SELECT n.nspname::text || '.' || c.relname::text FROM pg_catalog.pg_class AS c
		JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f') AND n.nspname <> ALL ($1)
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'deedglass') AND n.nspname NOT LIKE 'pg\_toast%' `
	if err := scanStrings(ctx, q, &st.AgentExtraReads, base+`AND pg_catalog.has_table_privilege($2, c.oid, 'SELECT') ORDER BY 1 LIMIT 50`, w.Schemas, RoleAgent); err != nil {
		return err
	}
	return scanStrings(ctx, q, &st.AgentExtraWrites, base+`AND (pg_catalog.has_table_privilege($2, c.oid, 'INSERT') OR pg_catalog.has_table_privilege($2, c.oid, 'UPDATE')
		OR pg_catalog.has_table_privilege($2, c.oid, 'DELETE') OR pg_catalog.has_table_privilege($2, c.oid, 'TRUNCATE')) ORDER BY 1 LIMIT 50`, w.Schemas, RoleAgent)
}

func sortedUnique(s []string) []string {
	m := map[string]bool{}
	out := []string{}
	for _, x := range s {
		if !m[x] {
			m[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// SettingBytes reads a size setting (max_slot_wal_keep_size, in MB units) as bytes; -1 is
// unlimited.
func (st *State) SettingBytes(name string) (int64, bool) {
	v, ok := st.Settings[name]
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	if n < 0 {
		return -1, true
	}
	switch st.SettingUnits[name] {
	case "MB":
		return n << 20, true
	case "kB":
		return n << 10, true
	case "8kB":
		return n * 8 << 10, true
	}
	return n, true
}

// SettingInt reads an integer setting.
func (st *State) SettingInt(name string) (int, bool) {
	v, ok := st.Settings[name]
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}
