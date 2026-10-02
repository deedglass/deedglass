package sqlguard

import (
	"strconv"
	"strings"
)

// Shape recognition reads the few statement forms whose effect can be described as "these rows of
// this table, keyed by these values": a single-table UPDATE or DELETE with a key predicate, and an
// INSERT … VALUES with a column list. The Witness (S19 onward) uses it to know which rows to look
// at before and after a statement runs. S19's spec calls the function sqlguard.Shape; it is named
// ShapeOf here because Shape is the result type.
//
// It is built on Tokenize and Split, reads significant tokens only (comments and whitespace between
// tokens are ignored), and fails closed: anything it does not recognise token by token makes the
// shape not Simple, with the first reason in Why. It never guesses at what a value means.

// ShapeOptions parameterise ShapeOf.
type ShapeOptions struct {
	DefaultSchema string // the schema unqualified table names resolve to; "" means "public"
	// AnyWhere (S21, L2's count-only footprints) accepts an UPDATE or DELETE whose WHERE is not a
	// key predicate, or that has no WHERE: the shape is then Simple with Where nil and WhereOther
	// true. Everything else about the statement (one table, no FROM, USING, JOIN or CTE, a SET
	// list without subqueries or function calls) must still be recognised.
	AnyWhere bool
}

// Shape is what ShapeOf reports about one SQL string.
//
// When Simple is false, only Kind, Why and (when it was read) Table are set; every other field is
// empty, so a caller can't act on a half-read statement by accident.
type Shape struct {
	Simple     bool      `json:"simple"`                // every token was recognised
	Why        string    `json:"why,omitempty"`         // the first reason it isn't simple (ShapeWhy*); "" when Simple
	Kind       string    `json:"kind"`                  // update, delete, insert or other
	Table      string    `json:"table,omitempty"`       // schema.table; unquoted names lowercased, quoted ones exact
	Set        []Assign  `json:"set,omitempty"`         // UPDATE only, in statement order
	Where      *KeyPred  `json:"where,omitempty"`       // UPDATE and DELETE: the key predicate
	WhereOther bool      `json:"where_other,omitempty"` // AnyWhere only: the WHERE isn't a key predicate (or is missing)
	InsertCols []string  `json:"insert_cols,omitempty"` // INSERT only
	InsertRows [][]Value `json:"insert_rows,omitempty"` // INSERT only: one []Value per VALUES row, in column order
	Returning  []string  `json:"returning,omitempty"`   // plain column names, or "*"
}

// Value is one value in a SET, WHERE or VALUES position.
//
// Kind is one of the ShapeValue* constants. Text is the decoded string for a string, the digits as
// written (minus included) for a number, "true" or "false" for a bool, "" for null, "$n" for a
// parameter, and the exact source text (trimmed) for an expr. Cast is the type of an explicit cast
// (lowercased, single spaces, e.g. "numeric(10,2)"), or "". Param is n for $n, else 0.
type Value struct {
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Cast  string `json:"cast,omitempty"`
	Param int    `json:"param,omitempty"`
}

// Assign is one "column = value" of an UPDATE's SET list.
type Assign struct {
	Column string `json:"column"`
	Value  Value  `json:"value"`
}

// KeyPred is a recognised WHERE: "a = v", "a IN (v, …)" or "a = v AND b = w [AND …]" with distinct
// columns. Each row of Rows has one value per column, in Columns order: "id IN (1, 2)" is
// Columns [id], Rows [[1] [2]]; "a = 1 AND b = 'x'" is Columns [a b], Rows [[1 'x']].
type KeyPred struct {
	Columns []string  `json:"columns"`
	Rows    [][]Value `json:"rows"`
}

// Value kinds.
const (
	ShapeValueString  = "string"
	ShapeValueInteger = "integer"
	ShapeValueNumeric = "numeric"
	ShapeValueBool    = "bool"
	ShapeValueNull    = "null"
	ShapeValueParam   = "param"
	ShapeValueExpr    = "expr"
)

// Shape kinds.
const (
	ShapeKindUpdate = "update"
	ShapeKindDelete = "delete"
	ShapeKindInsert = "insert"
	ShapeKindOther  = "other"
)

// Reasons a shape is not Simple (Shape.Why).
const (
	ShapeWhyJoin             = "join"              // a JOIN in the statement
	ShapeWhySubquery         = "subquery"          // a subquery in a SET value
	ShapeWhyCTE              = "cte"               // WITH … before the statement
	ShapeWhyFunctionCall     = "function_call"     // a function call in a SET value
	ShapeWhyOnConflict       = "on_conflict"       // INSERT … ON CONFLICT
	ShapeWhyMultiStatement   = "multi_statement"   // more than one non-empty statement
	ShapeWhyFromClause       = "from_clause"       // UPDATE … FROM
	ShapeWhyUsingClause      = "using_clause"      // DELETE … USING
	ShapeWhyReturningExpr    = "returning_expr"    // RETURNING something other than plain columns or *
	ShapeWhyNoWhere          = "no_where"          // UPDATE or DELETE without WHERE
	ShapeWhyWhereNotKey      = "where_not_key"     // a WHERE that is not a key predicate
	ShapeWhyValuesExpr       = "values_expr"       // a VALUES entry that is not a literal or parameter
	ShapeWhyDefaultValues    = "default_values"    // INSERT … DEFAULT VALUES
	ShapeWhyInsertSelect     = "insert_select"     // INSERT … SELECT
	ShapeWhyUnknownQualifier = "unknown_qualifier" // a column qualified by something other than the table or its alias
	ShapeWhyLexError         = "lex_error"         // Tokenize reported a problem
	ShapeWhyNotDML           = "not_dml"           // not an INSERT, UPDATE or DELETE
	ShapeWhyEmpty            = "empty"             // only whitespace and comments
	ShapeWhyTooLarge         = "too_large"         // over DefaultMaxBytes
	ShapeWhyOther            = "other"             // anything else this recogniser does not read
)

// AllShapeWhys lists every ShapeWhy* value.
var AllShapeWhys = []string{
	ShapeWhyJoin, ShapeWhySubquery, ShapeWhyCTE, ShapeWhyFunctionCall, ShapeWhyOnConflict,
	ShapeWhyMultiStatement, ShapeWhyFromClause, ShapeWhyUsingClause, ShapeWhyReturningExpr, ShapeWhyNoWhere,
	ShapeWhyWhereNotKey, ShapeWhyValuesExpr, ShapeWhyDefaultValues, ShapeWhyInsertSelect,
	ShapeWhyUnknownQualifier, ShapeWhyLexError, ShapeWhyNotDML, ShapeWhyEmpty, ShapeWhyTooLarge, ShapeWhyOther,
}

// maxParam is the highest parameter number the Postgres wire protocol can bind.
const maxParam = 65535

// ShapeOf recognises the shape of sql (S19's sqlguard.Shape). It is pure, deterministic and never
// panics; an internal failure is reported as not Simple with Why "other".
//
// Recognised, and only these:
//
//	UPDATE [ONLY] t [[AS] a] SET c = v {, c = v} WHERE <key> [RETURNING cols]
//	DELETE FROM [ONLY] t [[AS] a] WHERE <key> [RETURNING cols]
//	INSERT INTO t (cols) VALUES (v, …) {, (v, …)} [RETURNING cols]
//
// where <key> is "c = v", "c IN (v, …)" or "c = v AND d = w …" (distinct columns, column on the
// left), optionally inside one or more pairs of parentheses. A SET value may be any expression
// except one with a subquery or a function call; WHERE and VALUES positions take literals,
// parameters and casts of them only. A shape is never Simple when Analyze reports the same SQL as
// Ambiguous.
func ShapeOf(sql string, o ShapeOptions) (s Shape) {
	defer func() {
		if r := recover(); r != nil {
			s = Shape{Kind: ShapeKindOther, Why: ShapeWhyOther}
		}
	}()
	if len(sql) > DefaultMaxBytes {
		return Shape{Kind: ShapeKindOther, Why: ShapeWhyTooLarge}
	}
	schema := o.DefaultSchema
	if schema == "" {
		schema = DefaultSchema
	}
	toks, err := Tokenize(sql)
	var stmts [][]Token
	for _, st := range Split(toks) {
		if sig := significant(st.Tokens); len(sig) > 0 {
			stmts = append(stmts, sig)
		}
	}
	if len(stmts) == 0 {
		if err != nil {
			return Shape{Kind: ShapeKindOther, Why: ShapeWhyLexError}
		}
		return Shape{Kind: ShapeKindOther, Why: ShapeWhyEmpty}
	}
	p := &shapeParser{src: sql, sig: stmts[0], schema: schema, anyWhere: o.AnyWhere}
	kind := p.kind()
	switch {
	case err != nil:
		return Shape{Kind: kind, Why: ShapeWhyLexError}
	case len(stmts) > 1:
		return Shape{Kind: kind, Why: ShapeWhyMultiStatement}
	}
	s = p.parse()
	if s.Simple && Analyze(sql, Options{DefaultSchema: schema}).Ambiguous {
		// The classifier saw something this recogniser did not (nesting too deep, for one): fail closed.
		s = Shape{Kind: s.Kind, Table: s.Table, Why: ShapeWhyOther}
	}
	return s
}

// significant returns the non-trivia tokens of a statement, with operators split the way the
// Postgres lexer splits them (so "=-5" reads as "=" then "-5").
func significant(toks []Token) []Token {
	out := make([]Token, 0, len(toks))
	for i := range toks {
		t := toks[i]
		if t.trivia() {
			continue
		}
		if t.Kind != Op {
			out = append(out, t)
			continue
		}
		rest, at := t.Text, t.Start
		for rest != "" {
			n := pgOpLen(rest)
			out = append(out, Token{Kind: Op, Text: rest[:n], Norm: rest[:n], Start: at, End: at + n, Depth: t.Depth})
			rest, at = rest[n:], at+n
		}
	}
	return out
}

// pgOpLen returns the length of the first operator in s, following scan.l: a multi-character
// operator ending in + or - loses its trailing + and - characters unless it contains one of
// ~ ! @ # ^ & | ` ? %.
func pgOpLen(s string) int {
	n := len(s)
	if n > 1 && (s[n-1] == '+' || s[n-1] == '-') {
		if !strings.ContainsAny(s[:n-1], "~!@#^&|`?%") {
			for n > 1 && (s[n-1] == '+' || s[n-1] == '-') {
				n--
			}
		}
	}
	return n
}

// reservedWord reports whether w is a Postgres reserved keyword (fully reserved, or reserved but
// allowed as a function or type name), so it can't be an unquoted table, column or alias name.
// https://www.postgresql.org/docs/18/sql-keywords-appendix.html
func reservedWord(w string) bool {
	switch w {
	case "all", "analyse", "analyze", "and", "any", "array", "as", "asc", "asymmetric", "both", "case", "cast",
		"check", "collate", "column", "constraint", "create", "current_catalog", "current_date", "current_role",
		"current_time", "current_timestamp", "current_user", "default", "deferrable", "desc", "distinct", "do",
		"else", "end", "except", "false", "fetch", "for", "foreign", "from", "grant", "group", "having", "in",
		"initially", "intersect", "into", "lateral", "leading", "limit", "localtime", "localtimestamp", "not",
		"null", "offset", "on", "only", "or", "order", "placing", "primary", "references", "returning", "select",
		"session_user", "some", "symmetric", "system_user", "table", "then", "to", "trailing", "true", "union",
		"unique", "user", "using", "variadic", "when", "where", "window", "with",
		// Reserved, but allowed as function or type names.
		"authorization", "binary", "collation", "concurrently", "cross", "current_schema", "freeze", "full",
		"ilike", "inner", "is", "isnull", "join", "left", "like", "natural", "notnull", "outer", "overlaps",
		"right", "similar", "tablesample", "verbose":
		return true
	}
	return false
}

// shapeFail stops parsing with a reason.
type shapeFail struct{ why string }

type shapeParser struct {
	src    string
	sig    []Token
	schema string
	i      int
	// The target table: its name as written (last part), its resolved schema, and its alias.
	tname, tschema, alias string
	anyWhere              bool
}

func (p *shapeParser) fail(why string) { panic(shapeFail{why}) }

func (p *shapeParser) kw(i int) string {
	if i < 0 || i >= len(p.sig) || p.sig[i].Kind != Ident {
		return ""
	}
	return p.sig[i].Norm
}

func (p *shapeParser) punct(i int, s string) bool {
	return i >= 0 && i < len(p.sig) && p.sig[i].Kind == Punct && p.sig[i].Text == s
}

func (p *shapeParser) op(i int, s string) bool {
	return i >= 0 && i < len(p.sig) && p.sig[i].Kind == Op && p.sig[i].Text == s
}

// name reports whether token i is a plain name: an unreserved unquoted identifier or a quoted one.
func (p *shapeParser) name(i int) bool {
	if i < 0 || i >= len(p.sig) {
		return false
	}
	t := &p.sig[i]
	return t.Kind == QuotedIdent || t.Kind == Ident && !reservedWord(t.Norm)
}

// closeOf returns the index of the ")" that closes the "(" at i, or len(sig).
func (p *shapeParser) closeOf(i int) int {
	d := p.sig[i].Depth
	for j := i + 1; j < len(p.sig); j++ {
		if p.sig[j].Depth == d && p.punct(j, ")") {
			return j
		}
	}
	return len(p.sig)
}

// kind is the statement's best-known kind, looking past a WITH list.
func (p *shapeParser) kind() string {
	k := p.kw(0)
	if k == "with" {
		for j := 1; j < len(p.sig); j++ {
			if p.sig[j].Depth != p.sig[0].Depth {
				continue
			}
			if w := p.kw(j); w == "update" || w == "delete" || w == "insert" || w == "select" || w == "merge" ||
				w == "values" || w == "table" {
				k = w
				break
			}
		}
	}
	switch k {
	case "update", "delete", "insert":
		return k
	}
	return ShapeKindOther
}

// parse reads the one statement in p.sig.
func (p *shapeParser) parse() (s Shape) {
	s.Kind = p.kind()
	defer func() {
		if r := recover(); r != nil {
			f, ok := r.(shapeFail)
			if !ok {
				panic(r)
			}
			s = Shape{Kind: s.Kind, Table: s.Table, Why: f.why}
		}
	}()
	switch p.kw(0) {
	case "with":
		p.fail(ShapeWhyCTE)
	case "update":
		p.update(&s)
	case "delete":
		p.delete(&s)
	case "insert":
		p.insert(&s)
	case "merge":
		p.fail(ShapeWhyOther)
	default:
		p.fail(ShapeWhyNotDML)
	}
	s.Simple = true
	return s
}

// depth0 returns the index of the first keyword w at the statement's own depth, from index from
// on, or -1.
func (p *shapeParser) depth0(from int, w string) int {
	for j := from; j < len(p.sig); j++ {
		if p.sig[j].Depth == 0 && p.kw(j) == w {
			return j
		}
	}
	return -1
}

// table reads [ONLY] name[.name] at p.i and sets s.Table.
func (p *shapeParser) table(s *Shape, only bool) {
	if only && p.kw(p.i) == "only" {
		p.i++
	}
	if !p.name(p.i) {
		p.fail(ShapeWhyOther)
	}
	first := p.sig[p.i].Norm
	p.i++
	p.tschema, p.tname = p.schema, first
	if p.punct(p.i, ".") {
		if !p.name(p.i + 1) {
			p.fail(ShapeWhyOther)
		}
		p.tschema, p.tname = first, p.sig[p.i+1].Norm
		p.i += 2
		if p.punct(p.i, ".") {
			p.fail(ShapeWhyOther) // catalog.schema.table
		}
	}
	s.Table = p.tschema + "." + p.tname
}

// aliasOpt reads [[AS] alias] at p.i. A bare alias can't be one of the words that may follow.
func (p *shapeParser) aliasOpt(next ...string) {
	if p.kw(p.i) == "as" {
		if !p.name(p.i + 1) {
			p.fail(ShapeWhyOther)
		}
		p.alias = p.sig[p.i+1].Norm
		p.i += 2
		return
	}
	if !p.name(p.i) {
		return
	}
	if p.sig[p.i].Kind == Ident {
		for _, w := range next {
			if p.sig[p.i].Norm == w {
				return
			}
		}
	}
	p.alias = p.sig[p.i].Norm
	p.i++
}

// joinCheck fails with "join" when the statement has a JOIN at its own depth (after the FROM and
// USING checks, which name the usual cause).
func (p *shapeParser) joinCheck() {
	if p.depth0(0, "join") >= 0 {
		p.fail(ShapeWhyJoin)
	}
}

func (p *shapeParser) update(s *Shape) {
	for j := 1; j < len(p.sig); j++ {
		if p.sig[j].Depth == 0 && p.kw(j) == "from" && p.kw(j-1) != "distinct" {
			p.fail(ShapeWhyFromClause)
		}
	}
	p.joinCheck()
	p.i = 1
	p.table(s, true)
	p.aliasOpt("set")
	if p.kw(p.i) != "set" {
		p.fail(ShapeWhyOther)
	}
	p.i++
	seen := map[string]bool{}
	for {
		if !p.name(p.i) || !p.op(p.i+1, "=") {
			// (a, b) = (…), a.b = …, a[1] = …: not read.
			p.fail(ShapeWhyOther)
		}
		col := p.sig[p.i].Norm
		if seen[col] {
			p.fail(ShapeWhyOther) // Postgres rejects multiple assignments to the same column
		}
		seen[col] = true
		start := p.i + 2
		end := start
		brackets := 0 // the tokenizer's Depth counts parentheses only; ARRAY[1, 2] holds a ","
		for end < len(p.sig) {
			if p.sig[end].Depth == 0 {
				switch {
				case p.punct(end, "["):
					brackets++
				case p.punct(end, "]"):
					brackets--
				}
				if brackets == 0 {
					if p.punct(end, ",") {
						break
					}
					if w := p.kw(end); w == "where" || w == "returning" {
						break
					}
				}
			}
			end++
		}
		v := p.setValue(start, end)
		s.Set = append(s.Set, Assign{Column: col, Value: v})
		p.i = end
		if !p.punct(p.i, ",") {
			break
		}
		p.i++
	}
	p.whereAndReturning(s)
}

func (p *shapeParser) delete(s *Shape) {
	if p.depth0(1, "using") >= 0 {
		p.fail(ShapeWhyUsingClause)
	}
	p.joinCheck()
	if p.kw(1) != "from" {
		p.fail(ShapeWhyOther)
	}
	p.i = 2
	p.table(s, true)
	p.aliasOpt("where", "returning")
	p.whereAndReturning(s)
}

// whereAndReturning reads WHERE <key> [RETURNING cols] to the end of the statement.
func (p *shapeParser) whereAndReturning(s *Shape) {
	if p.kw(p.i) != "where" {
		if p.i >= len(p.sig) || p.kw(p.i) == "returning" {
			if p.anyWhere {
				s.WhereOther = true
				p.returning(s)
				return
			}
			p.fail(ShapeWhyNoWhere)
		}
		p.fail(ShapeWhyOther)
	}
	p.i++
	end := p.depth0(p.i, "returning")
	if end < 0 {
		end = len(p.sig)
	}
	if p.anyWhere {
		s.Where, s.WhereOther = p.keyPredOrOther(p.i, end)
	} else {
		s.Where = p.keyPred(p.i, end)
	}
	p.i = end
	p.returning(s)
}

// keyPredOrOther is keyPred, except that a WHERE that isn't a key predicate is reported as other
// (AnyWhere) instead of failing the shape. Any other failure still fails it.
func (p *shapeParser) keyPredOrOther(from, end int) (k *KeyPred, other bool) {
	defer func() {
		if r := recover(); r != nil {
			if f, ok := r.(shapeFail); ok && f.why == ShapeWhyWhereNotKey {
				k, other = nil, true
				return
			}
			panic(r)
		}
	}()
	if from >= end {
		p.fail(ShapeWhyOther)
	}
	return p.keyPred(from, end), false
}

// insert needs no JOIN check: a JOIN at the statement's depth can only follow INSERT … SELECT,
// which fails first.
func (p *shapeParser) insert(s *Shape) {
	for j := 1; j+1 < len(p.sig); j++ {
		if p.sig[j].Depth == 0 && p.kw(j) == "on" && p.kw(j+1) == "conflict" {
			p.fail(ShapeWhyOnConflict)
		}
	}
	if p.kw(1) != "into" {
		p.fail(ShapeWhyOther)
	}
	p.i = 2
	p.table(s, false)
	switch p.kw(p.i) {
	case "default":
		p.fail(ShapeWhyDefaultValues)
	case "select", "with", "table":
		p.fail(ShapeWhyInsertSelect)
	}
	if !p.punct(p.i, "(") {
		p.fail(ShapeWhyOther) // no column list (or an alias, OVERRIDING, …)
	}
	if w := p.kw(p.i + 1); w == "select" || w == "with" || w == "values" || w == "table" {
		p.fail(ShapeWhyInsertSelect) // INSERT INTO t (SELECT …)
	}
	closeAt := p.closeOf(p.i)
	seen := map[string]bool{}
	for j := p.i + 1; ; j += 2 {
		if !p.name(j) {
			p.fail(ShapeWhyOther)
		}
		col := p.sig[j].Norm
		if seen[col] {
			p.fail(ShapeWhyOther)
		}
		seen[col] = true
		s.InsertCols = append(s.InsertCols, col)
		if j+1 == closeAt {
			break
		}
		if !p.punct(j+1, ",") {
			p.fail(ShapeWhyOther)
		}
	}
	p.i = closeAt + 1
	switch p.kw(p.i) {
	case "default":
		p.fail(ShapeWhyDefaultValues)
	case "select", "with", "table":
		p.fail(ShapeWhyInsertSelect)
	case "values":
	default:
		if p.punct(p.i, "(") {
			p.fail(ShapeWhyInsertSelect) // INSERT INTO t (a) (SELECT …)
		}
		p.fail(ShapeWhyOther)
	}
	p.i++
	for {
		if !p.punct(p.i, "(") {
			p.fail(ShapeWhyOther)
		}
		closeRow := p.closeOf(p.i)
		if closeRow >= len(p.sig) {
			p.fail(ShapeWhyOther)
		}
		vals := p.list(p.i+1, closeRow)
		if len(vals) != len(s.InsertCols) {
			p.fail(ShapeWhyOther) // Postgres rejects a row whose width differs from the column list
		}
		row := make([]Value, 0, len(vals))
		for _, r := range vals {
			v := p.value(r[0], r[1])
			if v.Kind == ShapeValueExpr {
				p.fail(ShapeWhyValuesExpr)
			}
			row = append(row, v)
		}
		s.InsertRows = append(s.InsertRows, row)
		p.i = closeRow + 1
		if !p.punct(p.i, ",") {
			break
		}
		p.i++
	}
	p.returning(s)
}

// list splits [from, end) on "," at the depth of sig[from] and returns each item's [start, end).
// An empty item fails the parse.
func (p *shapeParser) list(from, end int) [][2]int {
	if from >= end {
		p.fail(ShapeWhyOther)
	}
	d := p.sig[from].Depth
	var out [][2]int
	start := from
	brackets := 0
	for j := from; j <= end; j++ {
		if j < end && p.sig[j].Depth == d {
			switch {
			case p.punct(j, "["):
				brackets++
			case p.punct(j, "]"):
				brackets--
			}
		}
		if j == end || p.sig[j].Depth == d && brackets == 0 && p.punct(j, ",") {
			if j == start {
				p.fail(ShapeWhyOther)
			}
			out = append(out, [2]int{start, j})
			start = j + 1
		}
	}
	return out
}

// returning reads [RETURNING col {, col}] to the end of the statement.
func (p *shapeParser) returning(s *Shape) {
	if p.i >= len(p.sig) {
		return
	}
	if p.kw(p.i) != "returning" {
		p.fail(ShapeWhyOther)
	}
	if p.i+1 >= len(p.sig) {
		p.fail(ShapeWhyOther)
	}
	for _, r := range p.list(p.i+1, len(p.sig)) {
		if r[1]-r[0] != 1 {
			p.fail(ShapeWhyReturningExpr)
		}
		t := &p.sig[r[0]]
		switch {
		case t.Kind == Op && t.Text == "*":
			s.Returning = append(s.Returning, "*")
		case p.name(r[0]):
			// The table's own name or alias, and PG 18's old and new, are whole-row references.
			if n := t.Norm; n == p.tname || n == p.alias || n == "old" || n == "new" {
				p.fail(ShapeWhyReturningExpr)
			}
			s.Returning = append(s.Returning, t.Norm)
		default:
			p.fail(ShapeWhyReturningExpr)
		}
	}
	p.i = len(p.sig)
}

// column reads a column reference filling [from, end) exactly: c, q.c or s.t.c, where the
// qualifier is the alias (when there is one) or the table's name. It returns "" when the tokens are
// not a column reference.
func (p *shapeParser) column(from, end int) string {
	var parts []string
	j := from
	for {
		if j >= end || !p.name(j) {
			return ""
		}
		parts = append(parts, p.sig[j].Norm)
		j++
		if j == end {
			break
		}
		if !p.punct(j, ".") {
			return ""
		}
		j++
	}
	q := parts[:len(parts)-1]
	switch {
	case len(q) == 0:
	case p.alias != "":
		if len(q) != 1 || q[0] != p.alias {
			p.fail(ShapeWhyUnknownQualifier)
		}
	case len(q) == 1 && q[0] == p.tname, len(q) == 2 && q[0] == p.tschema && q[1] == p.tname:
	default:
		p.fail(ShapeWhyUnknownQualifier)
	}
	return parts[len(parts)-1]
}

// keyPred reads a key predicate filling [from, end); anything else fails with where_not_key.
func (p *shapeParser) keyPred(from, end int) *KeyPred {
	if p.kw(from) == "current" && p.kw(from+1) == "of" {
		p.fail(ShapeWhyWhereNotKey)
	}
	// Strip parentheses that wrap the whole predicate.
	for end-from >= 2 && p.punct(from, "(") && p.closeOf(from) == end-1 {
		from, end = from+1, end-1
	}
	if from >= end {
		p.fail(ShapeWhyWhereNotKey)
	}
	d := p.sig[from].Depth
	var parts [][2]int
	start := from
	for j := from; j <= end; j++ {
		if j == end || p.sig[j].Depth == d && p.kw(j) == "and" {
			if j == start {
				p.fail(ShapeWhyWhereNotKey)
			}
			parts = append(parts, [2]int{start, j})
			start = j + 1
			continue
		}
		if p.sig[j].Depth == d {
			switch p.kw(j) {
			case "or", "not", "between", "is", "like", "ilike", "similar", "isnull", "notnull":
				p.fail(ShapeWhyWhereNotKey)
			}
		}
	}
	k := &KeyPred{}
	row := make([]Value, 0, len(parts))
	for _, part := range parts {
		// The column ends at the first "=" or IN at the part's depth.
		at := -1
		for j := part[0]; j < part[1]; j++ {
			if p.sig[j].Depth == d && (p.op(j, "=") || p.kw(j) == "in") {
				at = j
				break
			}
		}
		if at <= part[0] {
			p.fail(ShapeWhyWhereNotKey)
		}
		col := p.column(part[0], at)
		if col == "" {
			p.fail(ShapeWhyWhereNotKey)
		}
		for _, c := range k.Columns {
			if c == col {
				p.fail(ShapeWhyWhereNotKey)
			}
		}
		k.Columns = append(k.Columns, col)
		if p.kw(at) == "in" {
			if len(parts) != 1 || !p.punct(at+1, "(") || p.closeOf(at+1) != part[1]-1 {
				p.fail(ShapeWhyWhereNotKey)
			}
			if at+2 >= part[1]-1 {
				p.fail(ShapeWhyWhereNotKey) // IN ()
			}
			for _, r := range p.list(at+2, part[1]-1) {
				k.Rows = append(k.Rows, []Value{p.keyValue(r[0], r[1])})
			}
			return k
		}
		if at+1 >= part[1] {
			p.fail(ShapeWhyWhereNotKey)
		}
		row = append(row, p.keyValue(at+1, part[1]))
	}
	k.Rows = [][]Value{row}
	return k
}

// keyValue is a WHERE value: a literal, parameter or cast of one. NULL never equals anything, so
// "c = NULL" is not a key either.
func (p *shapeParser) keyValue(from, end int) Value {
	v := p.value(from, end)
	if v.Kind == ShapeValueExpr || v.Kind == ShapeValueNull {
		p.fail(ShapeWhyWhereNotKey)
	}
	return v
}

// setValue is a SET value: anything value reads, or an expr without a subquery or function call.
// DEFAULT may run a function (nextval, now), so it is not read either.
func (p *shapeParser) setValue(from, end int) Value {
	if from >= end {
		p.fail(ShapeWhyOther)
	}
	v := p.value(from, end)
	if v.Kind != ShapeValueExpr {
		return v
	}
	if end-from == 1 && p.kw(from) == "default" {
		p.fail(ShapeWhyOther)
	}
	for j := from; j < end; j++ {
		switch p.kw(j) {
		case "select", "with", "values", "table", "insert", "update", "delete", "merge", "lateral":
			p.fail(ShapeWhySubquery)
		}
	}
	for j := from; j < end; j++ {
		if noParenFunction(p.kw(j)) {
			p.fail(ShapeWhyFunctionCall) // CURRENT_TIMESTAMP is now()
		}
		if !p.punct(j, "(") || j == from {
			continue
		}
		prev := &p.sig[j-1]
		if prev.Kind == QuotedIdent || prev.Kind == UnicodeIdent || prev.Kind == Ident && !notFunc(prev.Norm) {
			p.fail(ShapeWhyFunctionCall)
		}
		if prev.Kind == Ident && (prev.Norm == "row" || prev.Norm == "exists" || prev.Norm == "any" ||
			prev.Norm == "all" || prev.Norm == "some" || prev.Norm == "extract") {
			// Constructs that read like keywords but run their own code (EXTRACT is date_part).
			p.fail(ShapeWhyFunctionCall)
		}
	}
	return v
}

// noParenFunction reports whether w is one of the SQL functions written without parentheses.
func noParenFunction(w string) bool {
	switch w {
	case "current_catalog", "current_date", "current_role", "current_schema", "current_time",
		"current_timestamp", "current_user", "localtime", "localtimestamp", "session_user", "system_user", "user":
		return true
	}
	return false
}

// value classifies the tokens [from, end) as one value. It returns Kind expr for anything it does
// not read as a literal, a parameter, or a cast of one, and fails the parse for a malformed number
// or parameter.
func (p *shapeParser) value(from, end int) Value {
	n := end - from
	switch {
	case n <= 0:
		p.fail(ShapeWhyOther)
	case n >= 3 && p.op(from+1, "::") || n >= 4 && p.op(from+2, "::") && p.op(from, "-"):
		at := from + 1
		if !p.op(at, "::") {
			at = from + 2
		}
		typ := p.typeName(at+1, end)
		v := p.literal(from, at)
		if typ == "" || v.Kind == ShapeValueExpr || p.op(from, "-") {
			// -5::int is -(5::int): the cast applies before the minus, so it is not read.
			break
		}
		v.Cast = typ
		return v
	case n >= 6 && p.kw(from) == "cast" && p.punct(from+1, "(") && p.closeOf(from+1) == end-1:
		as := -1
		for j := from + 2; j < end-1; j++ {
			if p.sig[j].Depth == p.sig[from].Depth+1 && p.kw(j) == "as" {
				as = j
				break
			}
		}
		if as < 0 {
			break
		}
		typ := p.typeName(as+1, end-1)
		v := p.literal(from+2, as)
		if typ == "" || v.Kind == ShapeValueExpr {
			break
		}
		v.Cast = typ
		return v
	default:
		if v := p.literal(from, end); v.Kind != ShapeValueExpr {
			return v
		}
	}
	return p.expr(from, end)
}

// expr is the exact source text of [from, end). Its numbers and parameters must still be ones
// Postgres accepts, and its square brackets must balance.
func (p *shapeParser) expr(from, end int) Value {
	brackets := 0
	for j := from; j < end; j++ {
		switch p.sig[j].Kind {
		case Number:
			p.number(&p.sig[j])
		case Param:
			p.literal(j, j+1)
		case Punct:
			switch p.sig[j].Text {
			case "[":
				brackets++
			case "]":
				brackets--
			}
		}
		if brackets < 0 {
			p.fail(ShapeWhyOther)
		}
	}
	if brackets != 0 {
		p.fail(ShapeWhyOther)
	}
	return Value{Kind: ShapeValueExpr, Text: strings.TrimSpace(p.src[p.sig[from].Start:p.sig[end-1].End])}
}

// literal reads [from, end) as one literal: a string, number (with an optional leading minus),
// true, false, NULL or $n. Anything else is expr.
func (p *shapeParser) literal(from, end int) Value {
	expr := Value{Kind: ShapeValueExpr}
	if end-from == 2 && p.op(from, "-") && p.sig[from+1].Kind == Number {
		v := p.number(&p.sig[from+1])
		if v.Kind == ShapeValueExpr {
			return expr
		}
		v.Text = "-" + v.Text
		return v
	}
	if end-from != 1 {
		return expr
	}
	t := &p.sig[from]
	switch t.Kind {
	case String:
		body := t.Text
		if body[0] == 'n' || body[0] == 'N' {
			body = body[1:]
		}
		return Value{Kind: ShapeValueString, Text: strings.ReplaceAll(body[1:len(body)-1], "''", "'")}
	case EString:
		if s, ok := decodeE(t.Text[2 : len(t.Text)-1]); ok {
			return Value{Kind: ShapeValueString, Text: s}
		}
	case Number:
		return p.number(t)
	case Param:
		n, err := strconv.Atoi(t.Text[1:])
		if err != nil || n < 1 || n > maxParam {
			p.fail(ShapeWhyOther) // Postgres rejects $0 and out-of-range numbers
		}
		return Value{Kind: ShapeValueParam, Text: t.Text, Param: n}
	case Ident:
		switch t.Norm {
		case "true", "false":
			return Value{Kind: ShapeValueBool, Text: t.Norm}
		case "null":
			return Value{Kind: ShapeValueNull}
		}
	}
	return expr
}

// decodeE decodes the body of an E'…' string when it uses only \\ \' \n \t \r and ”.
func decodeE(body string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '\\' && i+1 < len(body):
			i++
			switch body[i] {
			case '\\':
				b.WriteByte('\\')
			case '\'':
				b.WriteByte('\'')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				return "", false
			}
		case c == '\\':
			return "", false
		case c == '\'' && i+1 < len(body) && body[i+1] == '\'':
			b.WriteByte('\'')
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

// number classifies a Number token: integer, numeric (a point or an exponent), or expr for the
// hexadecimal, octal, binary and underscore forms. Anything else is not a number Postgres accepts.
func (p *shapeParser) number(t *Token) Value {
	s := t.Text
	if s == "" {
		p.fail(ShapeWhyOther)
	}
	if allDigits(s) {
		return Value{Kind: ShapeValueInteger, Text: s}
	}
	if decimal(s) {
		return Value{Kind: ShapeValueNumeric, Text: s}
	}
	if otherNumber(s) {
		return Value{Kind: ShapeValueExpr, Text: s}
	}
	p.fail(ShapeWhyOther)
	return Value{}
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// decimal matches digits.[digits] or .digits, each with an optional exponent, or digits with an
// exponent.
func decimal(s string) bool {
	mant, exp := s, ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant, exp = s[:i], s[i+1:]
		if exp != "" && (exp[0] == '+' || exp[0] == '-') {
			exp = exp[1:]
		}
		if !allDigits(exp) {
			return false
		}
	}
	intPart, frac, dot := strings.Cut(mant, ".")
	if !dot {
		return exp != "" && allDigits(intPart)
	}
	if intPart == "" && frac == "" {
		return false
	}
	return (intPart == "" || allDigits(intPart)) && (frac == "" || allDigits(frac))
}

// otherNumber matches 0x…, 0o…, 0b… and integers with underscores between digits (PG 16+).
func otherNumber(s string) bool {
	digits := func(body string, ok func(byte) bool) bool {
		if body == "" || body[0] == '_' || body[len(body)-1] == '_' || strings.Contains(body, "__") {
			return false
		}
		for i := 0; i < len(body); i++ {
			if body[i] != '_' && !ok(body[i]) {
				return false
			}
		}
		return true
	}
	if len(s) > 2 && s[0] == '0' {
		body := s[2:]
		switch s[1] {
		case 'x', 'X':
			return digits(strings.TrimPrefix(body, "_"), func(c byte) bool {
				return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
			})
		case 'o', 'O':
			return digits(strings.TrimPrefix(body, "_"), func(c byte) bool { return c >= '0' && c <= '7' })
		case 'b', 'B':
			return digits(strings.TrimPrefix(body, "_"), func(c byte) bool { return c == '0' || c == '1' })
		}
	}
	return strings.Contains(s, "_") && digits(s, isDigit)
}

// typeName normalises the type of a cast filling [from, end): lowercased names, single spaces
// between words, no spaces around punctuation ("numeric(10,2)", "timestamp with time zone",
// "int[]", "pg_catalog.int4"). It reads words, dots between words, one modifier list of integers
// after a word, and array brackets with an optional integer; anything else returns "".
func (p *shapeParser) typeName(from, end int) string {
	if from >= end || p.sig[from].Kind != Ident || reservedWord(p.sig[from].Norm) {
		return ""
	}
	integer := func(j int) bool { return j < end && p.sig[j].Kind == Number && allDigits(p.sig[j].Text) }
	var b strings.Builder
	j := from
	word := false // the last thing written was a word or a closing bracket
	for j < end {
		t := &p.sig[j]
		switch {
		case t.Kind == Ident:
			if word {
				b.WriteByte(' ')
			}
			b.WriteString(t.Norm)
			word = true
			j++
		case p.punct(j, ".") && j > from && p.sig[j-1].Kind == Ident && j+1 < end && p.sig[j+1].Kind == Ident:
			b.WriteByte('.')
			word = false
			j++
		case p.punct(j, "(") && j > from && p.sig[j-1].Kind == Ident:
			b.WriteByte('(')
			j++
			for {
				if !integer(j) {
					return ""
				}
				b.WriteString(p.sig[j].Text)
				j++
				if p.punct(j, ",") {
					b.WriteByte(',')
					j++
					continue
				}
				if !p.punct(j, ")") || j >= end {
					return ""
				}
				b.WriteByte(')')
				j++
				break
			}
			word = true
		case p.punct(j, "["):
			b.WriteByte('[')
			j++
			if integer(j) {
				b.WriteString(p.sig[j].Text)
				j++
			}
			if !p.punct(j, "]") || j >= end {
				return ""
			}
			b.WriteByte(']')
			j++
			word = true
		default:
			return ""
		}
	}
	out := b.String()
	// Several words must be one of the SQL-standard multi-word type names; otherwise the words
	// after the type belong to a larger expression ('x'::timestamp AT TIME ZONE z).
	if words := typeWords(out); strings.Contains(words, " ") && !multiWordType[words] {
		return ""
	}
	return out
}

// typeWords drops the modifiers and array brackets from a normalised type name.
func typeWords(t string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(t); i++ {
		switch c := t[i]; {
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case depth == 0:
			b.WriteByte(c)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// multiWordType lists the type names Postgres spells with more than one word.
var multiWordType = map[string]bool{
	"double precision": true, "character varying": true, "char varying": true, "national character": true,
	"national char": true, "national character varying": true, "national char varying": true,
	"nchar varying": true, "bit varying": true,
	"timestamp with time zone": true, "timestamp without time zone": true,
	"time with time zone": true, "time without time zone": true,
	"interval year": true, "interval month": true, "interval day": true, "interval hour": true,
	"interval minute": true, "interval second": true, "interval year to month": true,
	"interval day to hour": true, "interval day to minute": true, "interval day to second": true,
	"interval hour to minute": true, "interval hour to second": true, "interval minute to second": true,
}
