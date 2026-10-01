package sqlguard

import "strings"

// Function lists (S11 §3.5). A function is looked up by its last name part, so
// pg_catalog.pg_read_file is pg_read_file. One line of reason per entry; docs/SQLGUARD.md
// prints the same reasons.

type fnClass uint8

const (
	fnSafe fnClass = iota
	fnDangerous
	fnWrite
	fnUnknown
)

// dangerousRule matches a dangerous function by exact name, prefix, or prefix plus a fragment.
type dangerousRule struct {
	exact, prefix, contains string
	Reason                  string
}

// Dangerous lists the functions the starter policies refuse, with why.
var Dangerous = []dangerousRule{
	{exact: "pg_read_file", Reason: "reads files on the database server"},
	{exact: "pg_read_binary_file", Reason: "reads files on the database server"},
	{exact: "pg_ls_dir", Reason: "lists directories on the database server"},
	{exact: "pg_stat_file", Reason: "reads file metadata on the database server"},
	{exact: "lo_import", Reason: "reads a server file into a large object"},
	{exact: "lo_export", Reason: "writes a large object to a server file"},
	{prefix: "dblink", Reason: "opens connections to other databases and runs SQL there"},
	{prefix: "query_to_xml", Reason: "runs the SQL text it is given (dynamic SQL)"},
	{exact: "cursor_to_xml", Reason: "reads from a cursor that can hide any query"},
	{exact: "pg_terminate_backend", Reason: "ends other sessions"},
	{exact: "pg_cancel_backend", Reason: "cancels other sessions' queries"},
	{exact: "pg_reload_conf", Reason: "reloads the server configuration"},
	{exact: "pg_rotate_logfile", Reason: "rotates the server log"},
	{exact: "pg_promote", Reason: "promotes a standby to primary"},
	{exact: "pg_switch_wal", Reason: "forces a WAL switch"},
	{exact: "set_config", Reason: "changes settings such as search_path or role for the session"},
	{prefix: "pg_sleep", Reason: "holds a connection open (denial of service)"},
	{prefix: "pg_advisory_", contains: "lock", Reason: "takes or releases advisory locks (denial of service)"},
	{exact: "pg_logical_emit_message", Reason: "writes logical messages, which can forge Deedglass's own markers"},
	{prefix: "pg_", contains: "replication_slot", Reason: "creates, advances or drops replication slots"},
	{prefix: "pg_replication_origin_", Reason: "changes replication origins"},
	{exact: "pg_file_write", Reason: "writes server files (adminpack)"},
	{exact: "pg_file_rename", Reason: "renames server files (adminpack)"},
	{exact: "pg_file_unlink", Reason: "deletes server files (adminpack)"},
}

// WriteFunctions change data even inside a SELECT.
var WriteFunctions = map[string]string{
	"nextval":     "advances a sequence",
	"setval":      "sets a sequence",
	"lo_create":   "creates a large object",
	"lo_unlink":   "deletes a large object",
	"lo_put":      "writes into a large object",
	"lo_truncate": "truncates a large object",
	"pg_notify":   "sends a notification to listeners",
}

// safeFunctions are common built-ins that read only. Anything not here, and not dangerous or a
// write function, is reported in unknown_functions.
var safeFunctions = func() map[string]bool {
	m := map[string]bool{}
	for _, f := range strings.Fields(`
		count sum avg min max array_agg string_agg json_agg jsonb_agg json_object_agg jsonb_object_agg
		bool_and bool_or every bit_and bit_or stddev stddev_pop stddev_samp variance var_pop var_samp
		percentile_cont percentile_disc mode corr covar_pop covar_samp regr_slope
		row_number rank dense_rank percent_rank cume_dist ntile lag lead first_value last_value nth_value
		lower upper length char_length character_length octet_length bit_length substring substr trim
		btrim ltrim rtrim concat concat_ws replace split_part left right position strpos lpad rpad
		repeat reverse initcap format quote_ident quote_literal quote_nullable translate starts_with
		regexp_replace regexp_matches regexp_match regexp_split_to_array regexp_split_to_table regexp_like
		regexp_count regexp_instr regexp_substr md5 sha224 sha256 sha384 sha512 encode decode to_hex ascii chr
		overlay normalize string_to_array array_to_string unistr
		abs round ceil ceiling floor trunc mod power sqrt cbrt exp ln log log10 sign div pi degrees radians
		sin cos tan asin acos atan atan2 greatest least coalesce nullif width_bucket gcd lcm factorial
		random scale
		now current_timestamp clock_timestamp statement_timestamp transaction_timestamp timeofday
		date_trunc date_part date_bin to_char to_date to_timestamp to_number age make_date make_time
		make_timestamp make_timestamptz make_interval justify_days justify_hours justify_interval isfinite
		jsonb_build_object json_build_object jsonb_build_array json_build_array jsonb_set jsonb_insert
		to_json to_jsonb row_to_json array_to_json jsonb_array_elements json_array_elements
		jsonb_array_elements_text json_array_elements_text jsonb_each json_each jsonb_each_text
		json_each_text jsonb_extract_path json_extract_path jsonb_extract_path_text json_extract_path_text
		jsonb_typeof json_typeof jsonb_array_length json_array_length jsonb_object_keys json_object_keys
		jsonb_strip_nulls json_strip_nulls jsonb_pretty jsonb_path_query jsonb_path_exists jsonb_path_match
		jsonb_path_query_array jsonb_path_query_first json_object jsonb_object
		array_length array_lower array_upper array_position array_positions array_remove array_replace
		array_append array_prepend array_cat array_dims array_ndims cardinality unnest generate_series
		generate_subscripts
		gen_random_uuid uuidv4 uuidv7 uuid_extract_timestamp uuid_extract_version merge_action
		int2 int4 int8 float4 float8 numeric text varchar bool date time timestamp timestamptz interval
		uuid jsonb json bytea inet cidr
		pg_typeof num_nonnulls num_nulls
	`) {
		m[f] = true
	}
	return m
}()

// classifyFunction looks a function up by its last name part.
func classifyFunction(name string) fnClass {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	for _, r := range Dangerous {
		switch {
		case r.exact != "":
			if name == r.exact {
				return fnDangerous
			}
		case r.contains != "":
			if strings.HasPrefix(name, r.prefix) && strings.Contains(name[len(r.prefix):], r.contains) {
				return fnDangerous
			}
		default:
			if strings.HasPrefix(name, r.prefix) {
				return fnDangerous
			}
		}
	}
	if _, ok := WriteFunctions[name]; ok {
		return fnWrite
	}
	if safeFunctions[name] {
		return fnSafe
	}
	return fnUnknown
}

// DangerousReason returns why a function is on the dangerous list, or "".
func DangerousReason(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	for _, r := range Dangerous {
		if (r.exact != "" && name == r.exact) ||
			(r.exact == "" && r.contains != "" && strings.HasPrefix(name, r.prefix) && strings.Contains(name[len(r.prefix):], r.contains)) ||
			(r.exact == "" && r.contains == "" && strings.HasPrefix(name, r.prefix)) {
			return r.Reason
		}
	}
	return ""
}

// SafeFunctionCount is the size of the safe list (for docs and tests).
func SafeFunctionCount() int { return len(safeFunctions) }

// notFunc lists words that are not function calls when "(" follows them: clause keywords, type
// names with a modifier (varchar(10), numeric(10,2)) and constructs such as EXISTS (…).
func notFunc(w string) bool {
	switch w {
	case "exists", "in", "any", "all", "some", "values", "as", "cast", "extract", "over", "filter", "within",
		"row", "array", "using", "on", "conflict", "set", "select", "from", "where", "and", "or", "not",
		"join", "lateral", "into", "table", "with", "recursive", "materialized", "union", "intersect", "except",
		"by", "partition", "order", "group", "case", "when", "then", "else", "returning", "explain", "copy",
		"insert", "update", "delete", "merge", "do", "check", "default", "key", "unique", "primary",
		"references", "constraint", "grouping", "sets", "cube", "rollup", "distinct", "only", "tablesample",
		"is", "like", "ilike", "similar", "between", "overlaps", "limit", "offset", "fetch", "window",
		"include", "range", "rows", "groups", "of", "collate", "escape", "analyze", "analyse", "verbose",
		"return", "returns", "language", "precision", "varying", "character", "char", "nchar", "national",
		"varchar", "numeric", "decimal", "dec", "bit", "varbit", "time", "timestamp", "timestamptz", "timetz",
		"interval", "float", "if", "to", "for", "having", "options", "nulls", "lock", "share", "nowait",
		"vacuum", "cluster", "reindex", "refresh", "comment", "grant", "revoke", "call", "execute", "prepare",
		"declare", "storage", "parallel", "tables":
		return true
	}
	return false
}

// keyword reports whether w is a keyword, so a name right after it is not an alias.
func keyword(w string) bool {
	if notFunc(w) {
		return true
	}
	switch w {
	case "coalesce", "nullif", "greatest", "least", "trim", "substring", "position", "overlay", "left",
		"right", "full", "inner", "outer", "cross", "natural", "end", "null", "true", "false", "asc", "desc",
		"leading", "trailing", "both", "call", "load", "begin", "commit", "rollback", "create", "alter",
		"drop", "truncate", "show", "discard", "listen", "notify", "user", "current_user", "session_user",
		"normalize", "xmlelement", "xmlforest", "xmlconcat", "xmlparse", "xmlpi", "xmlroot", "xmlserialize",
		"current_date", "current_time", "current_timestamp", "localtime", "localtimestamp", "else":
		return true
	}
	return false
}
