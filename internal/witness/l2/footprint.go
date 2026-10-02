package l2

import (
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/deedglass/deedglass/internal/sqlguard"
	"github.com/deedglass/deedglass/internal/witness"
)

// Derivations: how a footprint was predicted (S21 §3.3).
const (
	DerivationExactKeys = "exact_keys" // a WHERE on the primary key: these rows and no others
	DerivationCountOnly = "count_only" // this table, this operation, the reported count
	DerivationContract  = "contract"   // postgres.call: what the controlled function declares it writes
	DerivationExplicit  = "explicit"   // actions.witness_expect, written by the Gate (S23's undo)
	DerivationUnknown   = "unknown"    // nothing Deedglass can predict: a gap, never a guess
)

// Footprint is what a statement should touch.
type Footprint struct {
	Derivation string        `json:"derivation"`
	Why        string        `json:"why,omitempty"` // unknown: the sqlguard ShapeWhy, or another short code
	Tables     []TableExpect `json:"tables"`
}

// TableExpect is one table of a footprint.
type TableExpect struct {
	Table   string              `json:"table"`              // "public.orders"
	Ops     []string            `json:"ops"`                // allowed operations: insert, update, delete
	Rows    *int64              `json:"rows,omitempty"`     // the expected row count (rows_affected); nil = not asserted
	MaxRows *int64              `json:"max_rows,omitempty"` // witness.max_rows[table], or a contract's max_rows
	Keys    []map[string]string `json:"keys,omitempty"`     // exact primary-key values (exact_keys)
	Columns []string            `json:"columns,omitempty"`  // UPDATE: SET columns ∪ auto_columns; nil = any
	Values  map[string]Literal  `json:"values,omitempty"`   // SET column → a plain literal, for the value check
}

// Literal is a SET value the value check may compare: a string, an integer or a boolean literal,
// or a resolved $n parameter of one of those kinds.
type Literal struct {
	Kind string `json:"kind"` // string | integer | bool
	Text string `json:"text"`
}

// ContractWrite is one table a controlled function declares it writes (functions[].writes).
type ContractWrite struct {
	Table   string   `json:"table"`
	Ops     []string `json:"ops"`
	MaxRows *int64   `json:"max_rows,omitempty"`
	Columns []string `json:"columns,omitempty"`
}

// Config is the connector's witness.* settings L2 reads (S21 §3.12).
type Config struct {
	AutoColumns    map[string][]string // table → columns a trigger is known to maintain (updated_at)
	MaxRows        map[string]int64    // table → the most rows one action may change
	Watched        []string            // the watched tables; nil means "not checked"
	StrictTriggers bool                // triggers that may write unwatched tables are a gap
	DiffRows       int                 // rows the diff shows (default 20)
	DiffColumns    int                 // columns the diff shows (default 4)
}

// DefaultDiffRows and DefaultDiffColumns are witness.diff_rows and the column cap of S21 §3.7.
const (
	DefaultDiffRows    = 20
	DefaultDiffColumns = 4
)

// Tools L2 derives footprints for (the connector's unqualified tool names).
const (
	ToolExecute = "execute"
	ToolCall    = "call"
	ToolUndo    = "undo"
)

// DeriveInput is everything Derive reads.
type DeriveInput struct {
	Tool          string            // execute | call | undo
	SQL           string            // execute: the authorized statement
	Params        []json.RawMessage // execute: args.params
	DefaultSchema string            // the schema unqualified names resolve to
	Reported      *int64            // Result.Meta["rows_affected"]; nil when the result didn't say
	Returning     []map[string]any  // the result's RETURNING rows, when the statement had one
	Explicit      *Footprint        // actions.witness_expect (S23)
	Contract      []ContractWrite   // postgres.call: the function's declared writes; nil when none
	Config        Config
	PK            map[string][]string // table → primary-key columns, from the frame; a missing table is unknown
}

// whyWords says a ShapeWhy (or Derive's own codes) in words, for footprint_unknown's message.
var whyWords = map[string]string{
	sqlguard.ShapeWhyJoin:             "a JOIN",
	sqlguard.ShapeWhySubquery:         "a subquery in SET",
	sqlguard.ShapeWhyCTE:              "a WITH query",
	sqlguard.ShapeWhyFunctionCall:     "a function call",
	sqlguard.ShapeWhyOnConflict:       "INSERT … ON CONFLICT",
	sqlguard.ShapeWhyMultiStatement:   "more than one statement",
	sqlguard.ShapeWhyFromClause:       "UPDATE … FROM",
	sqlguard.ShapeWhyUsingClause:      "DELETE … USING",
	sqlguard.ShapeWhyReturningExpr:    "RETURNING an expression",
	sqlguard.ShapeWhyNoWhere:          "no WHERE",
	sqlguard.ShapeWhyWhereNotKey:      "a WHERE Deedglass doesn't read",
	sqlguard.ShapeWhyValuesExpr:       "a computed VALUES entry",
	sqlguard.ShapeWhyDefaultValues:    "DEFAULT VALUES",
	sqlguard.ShapeWhyInsertSelect:     "INSERT … SELECT",
	sqlguard.ShapeWhyUnknownQualifier: "a column of another table",
	sqlguard.ShapeWhyLexError:         "SQL that doesn't parse",
	sqlguard.ShapeWhyNotDML:           "not an INSERT, UPDATE or DELETE",
	sqlguard.ShapeWhyEmpty:            "an empty statement",
	sqlguard.ShapeWhyTooLarge:         "a statement over 256 KiB",
	sqlguard.ShapeWhyOther:            "a form Deedglass doesn't read",
	WhyMerge:                          "MERGE",
	WhyNoContract:                     "the function doesn't declare what it writes",
	WhyNoExplicit:                     "the undo has no recorded footprint",
	WhyUnknownTool:                    "a tool L2 doesn't read",
}

// Derive's own why codes.
const (
	WhyMerge       = "merge"
	WhyNoContract  = "no_contract"
	WhyNoExplicit  = "no_explicit_footprint"
	WhyUnknownTool = "unknown_tool"
)

// WhyWords is a why code in words ("UPDATE … FROM"); the code itself when it has none.
func WhyWords(why string) string {
	if w, ok := whyWords[why]; ok {
		return w
	}
	return why
}

func unknown(why string) Footprint {
	return Footprint{Derivation: DerivationUnknown, Why: why, Tables: []TableExpect{}}
}

// Derive predicts a statement's footprint (S21 §3.3). It never guesses: a statement it can't read
// is unknown, with footprint_unknown as an L2 gap. Its other reasons are unwatched_table for a
// footprint table the change log doesn't show.
func Derive(in DeriveInput) (Footprint, []witness.Reason) {
	var fp Footprint
	switch in.Tool {
	case ToolUndo:
		if in.Explicit == nil {
			fp = unknown(WhyNoExplicit)
		} else {
			fp = *in.Explicit
			fp.Derivation = DerivationExplicit
		}
	case ToolCall:
		fp = fromContract(in)
	case ToolExecute:
		fp = fromSQL(in)
	default:
		fp = unknown(WhyUnknownTool)
	}
	var rs []witness.Reason
	if fp.Derivation == DerivationUnknown {
		rs = append(rs, witness.R(witness.L2, witness.CodeFootprintUnknown, "footprint", map[string]any{"why": WhyWords(fp.Why)}).
			WithEvidence(map[string]any{"why": fp.Why}))
		return fp, rs
	}
	for i := range fp.Tables {
		t := &fp.Tables[i]
		if n, ok := in.Config.MaxRows[t.Table]; ok && t.MaxRows == nil {
			t.MaxRows = &n
		}
		if in.Config.Watched != nil && !slices.Contains(in.Config.Watched, t.Table) {
			rs = append(rs, witness.R(witness.L2, witness.CodeUnwatchedTable, "footprint", map[string]any{"table": t.Table}))
		}
	}
	return fp, rs
}

func fromContract(in DeriveInput) Footprint {
	if len(in.Contract) == 0 {
		return unknown(WhyNoContract)
	}
	fp := Footprint{Derivation: DerivationContract}
	for _, w := range in.Contract {
		te := TableExpect{Table: w.Table, Ops: sortedOps(w.Ops), MaxRows: w.MaxRows}
		if len(w.Columns) > 0 {
			te.Columns = withAuto(w.Columns, in.Config.AutoColumns[w.Table])
		}
		fp.Tables = append(fp.Tables, te)
	}
	return fp
}

func fromSQL(in DeriveInput) Footprint {
	if strings.EqualFold(firstWord(in.SQL), "merge") {
		return unknown(WhyMerge)
	}
	sh := sqlguard.ShapeOf(in.SQL, sqlguard.ShapeOptions{DefaultSchema: in.DefaultSchema, AnyWhere: true})
	if !sh.Simple {
		return unknown(sh.Why)
	}
	if sh.WhereOther {
		// A WHERE that isn't a key predicate is read only for its table and operation; a function
		// that may write (or that Deedglass doesn't know) makes it unknown.
		f := sqlguard.Analyze(in.SQL, sqlguard.Options{DefaultSchema: in.DefaultSchema})
		if f.Ambiguous || len(f.WriteFunctions)+len(f.UnknownFunctions)+len(f.DangerousFunctions) > 0 || f.CTEWrite {
			return unknown(sqlguard.ShapeWhyFunctionCall)
		}
	}
	te := TableExpect{Table: sh.Table, Rows: in.Reported}
	fp := Footprint{Derivation: DerivationCountOnly}
	switch sh.Kind {
	case sqlguard.ShapeKindUpdate:
		te.Ops = []string{OpUpdate}
		cols := make([]string, 0, len(sh.Set))
		for _, a := range sh.Set {
			cols = append(cols, a.Column)
			if lit, ok := literalOf(a.Value, in.Params); ok {
				if te.Values == nil {
					te.Values = map[string]Literal{}
				}
				te.Values[a.Column] = lit
			}
		}
		te.Columns = withAuto(cols, in.Config.AutoColumns[sh.Table])
		if keys, ok := exactKeys(sh, in); ok {
			fp.Derivation, te.Keys = DerivationExactKeys, keys
		}
	case sqlguard.ShapeKindDelete:
		te.Ops = []string{OpDelete}
		if keys, ok := exactKeys(sh, in); ok {
			fp.Derivation, te.Keys = DerivationExactKeys, keys
		}
	case sqlguard.ShapeKindInsert:
		te.Ops = []string{OpInsert}
		if te.Rows == nil {
			n := int64(len(sh.InsertRows))
			te.Rows = &n
		}
		if keys, ok := returnedKeys(sh, in); ok {
			fp.Derivation, te.Keys = DerivationExactKeys, keys
		}
	default:
		return unknown(sqlguard.ShapeWhyNotDML)
	}
	fp.Tables = []TableExpect{te}
	return fp
}

func firstWord(sql string) string {
	f := strings.Fields(sql)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func withAuto(cols, auto []string) []string {
	out := append([]string{}, cols...)
	for _, c := range auto {
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

func sortedOps(ops []string) []string {
	out := append([]string{}, ops...)
	sort.Strings(out)
	return slices.Compact(out)
}

// exactKeys returns the WHERE's key values when its columns are exactly the table's primary key
// and every value is a plain literal or a resolved parameter.
func exactKeys(sh sqlguard.Shape, in DeriveInput) ([]map[string]string, bool) {
	if sh.Where == nil {
		return nil, false
	}
	pk, ok := in.PK[sh.Table]
	if !ok || len(pk) == 0 || len(pk) != len(sh.Where.Columns) {
		return nil, false
	}
	for _, c := range sh.Where.Columns {
		if !slices.Contains(pk, c) {
			return nil, false
		}
	}
	var out []map[string]string
	for _, row := range sh.Where.Rows {
		k := map[string]string{}
		for i, c := range sh.Where.Columns {
			lit, ok := literalOf(row[i], in.Params)
			if !ok {
				return nil, false
			}
			k[c] = lit.Text
		}
		out = append(out, k)
	}
	return out, true
}

// returnedKeys reads an INSERT's keys from its RETURNING rows, when they hold every primary-key
// column and there is one per VALUES row.
func returnedKeys(sh sqlguard.Shape, in DeriveInput) ([]map[string]string, bool) {
	pk, ok := in.PK[sh.Table]
	if !ok || len(pk) == 0 || len(in.Returning) == 0 || len(in.Returning) != len(sh.InsertRows) {
		return nil, false
	}
	if !slices.Contains(sh.Returning, "*") {
		for _, c := range pk {
			if !slices.Contains(sh.Returning, c) {
				return nil, false
			}
		}
	}
	var out []map[string]string
	for _, r := range in.Returning {
		k := map[string]string{}
		for _, c := range pk {
			v, ok := r[c]
			if !ok || v == nil {
				return nil, false
			}
			s, ok := scalarText(v)
			if !ok {
				return nil, false
			}
			k[c] = s
		}
		out = append(out, k)
	}
	return out, true
}

// literalOf reads a value the checks can compare: a string, integer or boolean literal (with or
// without a cast), or a $n parameter whose JSON value is one of those. Anything else (an
// expression, a numeric, NULL) isn't compared.
func literalOf(v sqlguard.Value, params []json.RawMessage) (Literal, bool) {
	switch v.Kind {
	case sqlguard.ShapeValueString:
		return Literal{Kind: "string", Text: v.Text}, true
	case sqlguard.ShapeValueInteger:
		return Literal{Kind: "integer", Text: v.Text}, true
	case sqlguard.ShapeValueBool:
		return Literal{Kind: "bool", Text: v.Text}, true
	case sqlguard.ShapeValueParam:
		if v.Param < 1 || v.Param > len(params) {
			return Literal{}, false
		}
		var x any
		dec := json.NewDecoder(strings.NewReader(string(params[v.Param-1])))
		dec.UseNumber()
		if dec.Decode(&x) != nil {
			return Literal{}, false
		}
		switch t := x.(type) {
		case string:
			return Literal{Kind: "string", Text: t}, true
		case json.Number:
			if _, err := strconv.ParseInt(t.String(), 10, 64); err == nil {
				return Literal{Kind: "integer", Text: t.String()}, true
			}
		case bool:
			return Literal{Kind: "bool", Text: strconv.FormatBool(t)}, true
		}
	}
	return Literal{}, false
}

// scalarText is a RETURNING value as text (strings as they are, numbers as written).
func scalarText(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10), true
		}
	case int64:
		return strconv.FormatInt(t, 10), true
	case int:
		return strconv.Itoa(t), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
}
