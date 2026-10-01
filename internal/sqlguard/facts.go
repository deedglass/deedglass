package sqlguard

// Version names the classifier's output. Bump it whenever classification output changes for any
// input; it is recorded in every policy decision and trace, so a replay can say which classifier
// it used (S27).
const Version = "1"

// Options bound and parameterise Analyze.
type Options struct {
	DefaultSchema string // the schema unqualified table names resolve to; "" means "public"
	MaxBytes      int    // larger inputs are ambiguous (too_large); 0 means 262,144
	MaxStatements int    // more statements are ambiguous (too_many_statements); 0 means 1,000
	MaxDepth      int    // deeper parentheses are ambiguous (too_deep); 0 means 256
}

// Defaults of Options.
const (
	DefaultSchema        = "public"
	DefaultMaxBytes      = 262_144
	DefaultMaxStatements = 1_000
	DefaultMaxDepth      = 256
)

func (o Options) withDefaults() Options {
	if o.DefaultSchema == "" {
		o.DefaultSchema = DefaultSchema
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.MaxStatements <= 0 {
		o.MaxStatements = DefaultMaxStatements
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	return o
}

// Facts is what Analyze reports about one SQL string. It mirrors policy.SQLFacts field by field
// (docs/POLICY_LANGUAGE.md §7.1); policy converts it. Lists are never nil. Kinds is in statement
// order; every other list is sorted and unique.
type Facts struct {
	Present            bool     `json:"present"`             // always true from Analyze
	Kind               string   `json:"kind"`                // the first statement's kind; "empty" for only comments
	Category           string   `json:"category"`            // the most severe category across statements
	Kinds              []string `json:"kinds"`               // every non-empty statement's kind
	Statements         int      `json:"statements"`          // non-empty statements
	Multi              bool     `json:"multi"`               // Statements > 1
	Writes             bool     `json:"writes"`              // anything may change data or schema
	WriteKinds         []string `json:"write_kinds"`         // insert, update, delete, copy (row-level writes)
	HasWhere           bool     `json:"has_where"`           // every top-level UPDATE/DELETE has its own WHERE
	UnboundedWrite     bool     `json:"unbounded_write"`     // any UPDATE/DELETE, at any depth, lacks a WHERE or has a trivial one
	WhereTrivial       bool     `json:"where_trivial"`       // any UPDATE/DELETE WHERE is trivially true
	HasReturning       bool     `json:"has_returning"`       // a RETURNING clause anywhere
	CTEWrite           bool     `json:"cte_write"`           // a data-modifying WITH query
	SelectInto         bool     `json:"select_into"`         // SELECT … INTO (creates a table)
	CopyProgram        bool     `json:"copy_program"`        // COPY … PROGRAM
	ExplainAnalyze     bool     `json:"explain_analyze"`     // EXPLAIN with ANALYZE (it runs the statement)
	LocksRows          bool     `json:"locks_rows"`          // FOR UPDATE/SHARE, or LOCK
	Tables             []string `json:"tables"`              // schema.table at every depth; CTE names excluded
	TablesComplete     bool     `json:"tables_complete"`     // false when the extractor met something it can't follow
	Functions          []string `json:"functions"`           // every function called, as written (lowercased unless quoted)
	DangerousFunctions []string `json:"dangerous_functions"` // on the dangerous list (functions.go)
	WriteFunctions     []string `json:"write_functions"`     // on the write list
	UnknownFunctions   []string `json:"unknown_functions"`   // on neither list nor the safe list
	Ambiguous          bool     `json:"ambiguous"`           // the classifier isn't sure; fail closed
	Reasons            []string `json:"reasons"`             // why it is ambiguous (Reason* constants)
	Bytes              int      `json:"bytes"`               // len(sql)
}

// Categories, from most to least severe. The most severe across all statements wins.
const (
	CatUnknown     = "unknown"
	CatServerCode  = "server_code"
	CatPrivilege   = "privilege"
	CatDDL         = "ddl"
	CatWrite       = "write"
	CatTransaction = "transaction"
	CatSession     = "session"
	CatMaintenance = "maintenance"
	CatRead        = "read"
	CatNone        = "none"
)

// Severity returns a category's rank: higher is more severe. Unknown names rank as CatUnknown.
func Severity(category string) int {
	switch category {
	case CatNone:
		return 0
	case CatRead:
		return 1
	case CatMaintenance:
		return 2
	case CatSession:
		return 3
	case CatTransaction:
		return 4
	case CatWrite:
		return 5
	case CatDDL:
		return 6
	case CatPrivilege:
		return 7
	case CatServerCode:
		return 8
	}
	return 9
}

// Reasons for Ambiguous. docs/SQLGUARD.md explains each in plain English.
const (
	ReasonUnterminatedString     = "unterminated_string"
	ReasonUnterminatedComment    = "unterminated_comment"
	ReasonUnterminatedDollar     = "unterminated_dollar_quote"
	ReasonUnterminatedIdentifier = "unterminated_identifier"
	ReasonNonASCII               = "non_ascii_outside_literal"
	ReasonNULByte                = "nul_byte"
	ReasonBackslashCommand       = "backslash_command"
	ReasonBackslashInString      = "backslash_in_string"
	ReasonExecutePrepared        = "execute_prepared"
	ReasonUnknownStatement       = "unknown_statement"
	ReasonTooLarge               = "too_large"
	ReasonTooManyStatements      = "too_many_statements"
	ReasonTooDeep                = "too_deep"
	ReasonInternalError          = "internal_error"
)

// AllReasons lists every reason, in documentation order.
var AllReasons = []string{
	ReasonUnterminatedString, ReasonUnterminatedComment, ReasonUnterminatedDollar, ReasonUnterminatedIdentifier,
	ReasonNonASCII, ReasonNULByte, ReasonBackslashCommand, ReasonBackslashInString,
	ReasonExecutePrepared, ReasonUnknownStatement, ReasonTooLarge, ReasonTooManyStatements, ReasonTooDeep,
	ReasonInternalError,
}
