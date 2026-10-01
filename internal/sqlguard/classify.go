package sqlguard

import (
	"strings"
)

// Analyze classifies sql. It is pure and never panics: an internal failure is reported as
// ambiguous with the reason internal_error. Inputs over o.MaxBytes are not tokenized at all.
func Analyze(sql string, o Options) (f Facts) {
	o = o.withDefaults()
	defer func() {
		if r := recover(); r != nil {
			f = failed(len(sql), ReasonInternalError)
		}
	}()
	if len(sql) > o.MaxBytes {
		return failed(len(sql), ReasonTooLarge)
	}
	var lx lexer
	lx.run(sql)
	a := analyzer{opts: o, reasons: lx.reasons, maxDepth: lx.maxDepth}
	if lx.maxDepth > o.MaxDepth {
		a.reasons.add(ReasonTooDeep)
	}
	a.f = Facts{Present: true, Bytes: len(sql), TablesComplete: true, HasWhere: true}
	a.frames = make([]frame, lx.maxDepth+2)
	a.sig = make([]*Token, 0, len(lx.toks))
	n := 0
	for _, st := range Split(lx.toks) {
		a.sig = a.sig[:0]
		for i := range st.Tokens {
			if !st.Tokens[i].trivia() {
				a.sig = append(a.sig, &st.Tokens[i])
			}
		}
		if len(a.sig) == 0 {
			continue
		}
		n++
		if n > o.MaxStatements {
			a.reasons.add(ReasonTooManyStatements)
			continue
		}
		a.statement()
	}
	return a.finish(n)
}

// failed is the result for input that was not classified.
func failed(n int, reason string) Facts {
	return Facts{Present: true, Kind: "other", Category: CatUnknown, Kinds: []string{}, Writes: true,
		WriteKinds: []string{}, Tables: []string{}, Functions: []string{}, DangerousFunctions: []string{},
		WriteFunctions: []string{}, UnknownFunctions: []string{}, Ambiguous: true, Reasons: []string{reason}, Bytes: n}
}

type frame struct {
	call bool // the parenthesis was opened by a function call: FROM inside it is not a table
}

// site is one INSERT, UPDATE, DELETE or MERGE that will run.
type site struct {
	kind   string
	at     int  // index in sig of the keyword
	top    bool // the statement's own DML (or EXPLAIN ANALYZE's), not a nested one
	nested bool
}

type analyzer struct {
	opts     Options
	f        Facts
	reasons  reasonSet
	maxDepth int
	sig      []*Token
	frames   []frame
	consumed []bool
	cte      []string
	sites    []site
	topDML   int // top-level UPDATE/DELETE seen
	topWhere int // of those, with their own WHERE
	cat      int // severity of the most severe statement
	catName  string
	writeSet uint8 // insert, update, delete, copy
}

const (
	wInsert uint8 = 1 << iota
	wUpdate
	wDelete
	wCopy
)

func (a *analyzer) kw(i int) string {
	if i < 0 || i >= len(a.sig) || a.sig[i].Kind != Ident {
		return ""
	}
	return a.sig[i].Norm
}

func (a *analyzer) punct(i int, p string) bool {
	return i >= 0 && i < len(a.sig) && a.sig[i].Kind == Punct && a.sig[i].Text == p
}

func (a *analyzer) isName(i int) bool {
	return i >= 0 && i < len(a.sig) && (a.sig[i].Kind == Ident || a.sig[i].Kind == QuotedIdent || a.sig[i].Kind == UnicodeIdent)
}

// closeOf returns the index of the ")" that closes the "(" at i, or len(sig).
func (a *analyzer) closeOf(i int) int {
	d := a.sig[i].Depth
	for j := i + 1; j < len(a.sig); j++ {
		if a.sig[j].Depth == d && a.punct(j, ")") {
			return j
		}
	}
	return len(a.sig)
}

// chain reads a dotted name starting at i and returns its parts' end (exclusive) and part count.
func (a *analyzer) chain(i int) (end, parts int) {
	if !a.isName(i) {
		return i, 0
	}
	end, parts = i+1, 1
	for a.punct(end, ".") && a.isName(end+1) {
		end += 2
		parts++
	}
	return end, parts
}

func (a *analyzer) chainName(i, end int) string {
	if end == i+1 {
		return a.sig[i].Norm
	}
	var b strings.Builder
	for j := i; j < end; j++ {
		if a.sig[j].Kind == Punct {
			b.WriteByte('.')
		} else {
			b.WriteString(a.sig[j].Norm)
		}
	}
	return b.String()
}

func (a *analyzer) consume(i, end int) {
	for j := i; j < end; j++ {
		a.consumed[j] = true
	}
}

func (a *analyzer) setCategory(c string) {
	if s := Severity(c); s > a.cat || a.catName == "" {
		a.cat, a.catName = s, c
	}
}

// statement classifies one non-empty statement (a.sig).
func (a *analyzer) statement() {
	if cap(a.consumed) < len(a.sig) {
		a.consumed = make([]bool, len(a.sig))
	}
	a.consumed = a.consumed[:len(a.sig)]
	clear(a.consumed)
	a.cte = a.cte[:0]
	a.sites = a.sites[:0]

	main := a.lead(0)
	kind, cat := a.leading(main)
	exec := true // the statement runs what it contains
	analyze := false
	innerMain := -1
	switch kind {
	case "explain":
		var inner int
		analyze, inner = a.explainOptions(main)
		innerMain = a.lead(inner)
		innerKind, innerCat := a.leading(innerMain)
		if analyze {
			a.f.ExplainAnalyze = true
			cat = innerCat
			if isDML(innerKind) {
				a.sites = append(a.sites, site{kind: innerKind, at: innerMain, top: true})
			}
		} else {
			cat = CatRead
			exec = false
		}
	case "prepare":
		exec = false // PREPARE p AS DELETE … runs nothing until EXECUTE, which is ambiguous
	case "insert", "update", "delete", "merge":
		a.sites = append(a.sites, site{kind: kind, at: main, top: true})
	case "execute":
		a.reasons.add(ReasonExecutePrepared)
	case "other":
		if t := a.sig[main]; t.Kind != Invalid && isASCII(t.Text) {
			a.reasons.add(ReasonUnknownStatement)
		}
	case "lock":
		a.f.LocksRows = true
	}
	if cat == CatServerCode || kind == "do" || kind == "call" || kind == "load" {
		a.f.TablesComplete = false
	}

	ddl := cat == CatDDL || cat == CatPrivilege
	if ddl {
		a.ddlTargets(main, kind)
	}
	wf := a.scan(main, kind, ddl)

	selectInto := false
	if kind == "select" || (analyze && innerMain >= 0 && a.kw(innerMain) == "select") {
		selectInto = a.selectInto()
	}
	if kind == "copy" {
		cat = a.copyCategory(main)
	}
	if exec {
		for _, s := range a.sites {
			a.dmlSite(s)
			if s.nested {
				cat = maxCat(cat, CatWrite)
			}
		}
		if wf {
			cat = maxCat(cat, CatWrite)
		}
	}
	if selectInto {
		a.f.SelectInto = true
		cat = maxCat(cat, CatDDL)
	}
	a.f.Kinds = append(a.f.Kinds, kind)
	a.setCategory(cat)
	switch cat {
	case CatWrite, CatDDL, CatPrivilege, CatServerCode, CatUnknown:
		a.f.Writes = true
	}
}

func maxCat(a, b string) string {
	if Severity(b) > Severity(a) {
		return b
	}
	return a
}

func isDML(k string) bool { return k == "insert" || k == "update" || k == "delete" || k == "merge" }

// lead skips leading parentheses and a WITH list, and returns the index of the main keyword.
func (a *analyzer) lead(i int) int {
	for a.punct(i, "(") {
		i++
	}
	if a.kw(i) == "with" {
		i = a.withList(i)
	}
	for a.punct(i, "(") {
		i++
	}
	if i >= len(a.sig) {
		return len(a.sig) - 1
	}
	return i
}

// withList reads WITH [RECURSIVE] name [(cols)] AS [NOT] [MATERIALIZED] (body) [, …] and returns
// the index after it. It records the CTE names, which are never tables.
func (a *analyzer) withList(i int) int {
	d := a.sig[i].Depth
	j := i + 1
	if a.kw(j) == "recursive" {
		j++
	}
	for j < len(a.sig) {
		if !a.isName(j) {
			return j
		}
		a.cte = append(a.cte, a.sig[j].Norm)
		a.consumed[j] = true
		j++
		if a.punct(j, "(") {
			j = a.closeOf(j) + 1
		}
		if a.kw(j) != "as" {
			return j
		}
		j++
		if a.kw(j) == "not" {
			j++
		}
		if a.kw(j) == "materialized" {
			j++
		}
		if !a.punct(j, "(") {
			return j
		}
		j = a.closeOf(j) + 1
		// SEARCH … SET col and CYCLE … SET … USING …: skip to the next "," or the main statement.
		for j < len(a.sig) && a.sig[j].Depth == d && !a.punct(j, ",") {
			switch a.kw(j) {
			case "select", "insert", "update", "delete", "merge", "values", "table", "with":
				return j
			}
			if a.punct(j, "(") {
				return j
			}
			j++
		}
		if !a.punct(j, ",") {
			return j
		}
		j++
	}
	return j
}

// leading maps a statement's leading keyword(s) to its kind and category (S11 §3.5 table).
func (a *analyzer) leading(i int) (kind, cat string) {
	k := a.kw(i)
	next := a.kw(i + 1)
	switch k {
	case "select", "values", "table":
		return "select", CatRead
	case "insert", "update", "delete", "merge":
		return k, CatWrite
	case "copy":
		return "copy", CatRead // refined by copyCategory
	case "create":
		for j := i + 1; j < len(a.sig) && j < i+6; j++ {
			switch a.kw(j) {
			case "role", "user", "group":
				return "create", CatPrivilege
			case "or", "replace", "temp", "temporary", "unlogged", "global", "local", "unique", "materialized",
				"recursive", "trusted", "procedural", "default", "constraint":
				continue
			}
			break
		}
		return "create", CatDDL
	case "alter":
		switch next {
		case "role", "user", "group", "system":
			return "alter", CatPrivilege
		case "default":
			if a.kw(i+2) == "privileges" {
				return "alter", CatPrivilege
			}
		}
		return "alter", CatDDL
	case "drop":
		switch next {
		case "role", "user", "group", "owned":
			return "drop", CatPrivilege
		}
		return "drop", CatDDL
	case "truncate", "comment":
		return k, CatDDL
	case "import":
		return "import", CatDDL
	case "security":
		if next == "label" {
			return "security_label", CatDDL
		}
	case "grant", "revoke", "reassign":
		return k, CatPrivilege
	case "do", "call", "load":
		return k, CatServerCode
	case "set":
		switch next {
		case "role":
			return "set", CatPrivilege
		case "session", "local":
			n2 := a.kw(i + 2)
			if n2 == "authorization" || n2 == "role" || (n2 == "session" && a.kw(i+3) == "authorization") {
				return "set", CatPrivilege
			}
		}
		return "set", CatSession
	case "reset":
		if next == "role" || next == "session" {
			return "reset", CatPrivilege
		}
		return "reset", CatSession
	case "prepare":
		if next == "transaction" {
			return "prepare", CatTransaction
		}
		return "prepare", CatSession
	case "discard", "listen", "unlisten", "notify", "deallocate", "declare", "fetch", "move", "close":
		return k, CatSession
	case "show":
		return "show", CatRead
	case "begin", "start", "commit", "end", "rollback", "abort", "savepoint", "release":
		return k, CatTransaction
	case "vacuum", "cluster", "reindex", "checkpoint", "refresh", "lock":
		return k, CatMaintenance
	case "analyze", "analyse":
		return "analyze", CatMaintenance
	case "explain":
		return "explain", CatRead
	case "execute":
		return "execute", CatUnknown
	}
	return "other", CatUnknown
}

// explainOptions reads EXPLAIN [ANALYZE] [VERBOSE] or EXPLAIN (option [value], …) and returns
// whether ANALYZE is on and where the explained statement starts.
func (a *analyzer) explainOptions(e int) (analyze bool, inner int) {
	j := e + 1
	if a.punct(j, "(") {
		end := a.closeOf(j)
		d := a.sig[j].Depth + 1
		for k := j + 1; k < end; k++ {
			if a.sig[k].Depth != d {
				continue
			}
			if w := a.kw(k); w == "analyze" || w == "analyse" {
				analyze = true
				if k+1 < end {
					switch strings.ToLower(a.sig[k+1].Norm) {
					case "false", "off", "0":
						analyze = false
					}
				}
			}
		}
		return analyze, end + 1
	}
	for {
		switch a.kw(j) {
		case "analyze", "analyse":
			analyze = true
			j++
			continue
		case "verbose":
			j++
			continue
		}
		return analyze, j
	}
}

// copyCategory: COPY … PROGRAM is server code; COPY … FROM writes; COPY … TO reads, unless the
// query it copies from writes.
func (a *analyzer) copyCategory(main int) string {
	cat := CatRead
	from, program := false, false
	for j := main + 1; j < len(a.sig); j++ {
		if a.sig[j].Depth != a.sig[main].Depth {
			continue
		}
		switch a.kw(j) {
		case "program":
			program = true
		case "from":
			from = true
		}
	}
	if from {
		a.writeSet |= wCopy
		cat = CatWrite
	}
	if program {
		a.f.CopyProgram = true
		cat = CatServerCode
	}
	return cat
}

// selectInto finds SELECT … INTO at the main query's own depth (not INSERT INTO).
func (a *analyzer) selectInto() bool {
	for j := range a.sig {
		if a.kw(j) != "into" || a.sig[j].Depth != 0 && !a.leadParenDepth(j) {
			continue
		}
		switch a.kw(j - 1) {
		case "insert", "merge":
			continue
		}
		return true
	}
	return false
}

// leadParenDepth reports whether token j sits at the depth of a statement written as "(SELECT …)".
func (a *analyzer) leadParenDepth(j int) bool {
	d := 0
	for a.punct(d, "(") {
		d++
	}
	return d > 0 && a.sig[j].Depth == d && a.kw(d) == "select"
}

// dmlSite records the write facts of one INSERT, UPDATE, DELETE or MERGE that will run.
func (a *analyzer) dmlSite(s site) {
	if s.nested {
		if p := a.kw(s.at - 2); a.punct(s.at-1, "(") && (p == "as" || p == "materialized") {
			a.f.CTEWrite = true
		}
	}
	d := a.sig[s.at].Depth
	end := len(a.sig)
	for j := s.at + 1; j < len(a.sig); j++ {
		if a.sig[j].Depth < d {
			end = j
			break
		}
	}
	switch s.kind {
	case "insert":
		a.writeSet |= wInsert
		for j := s.at + 1; j < end; j++ {
			if a.sig[j].Depth == d && a.kw(j) == "do" && a.kw(j+1) == "update" {
				a.writeSet |= wUpdate
			}
		}
	case "merge":
		for j := s.at + 1; j < end; j++ {
			if a.sig[j].Depth == d && a.kw(j) == "then" {
				switch a.kw(j + 1) {
				case "update":
					a.writeSet |= wUpdate
				case "insert":
					a.writeSet |= wInsert
				case "delete":
					a.writeSet |= wDelete
				}
			}
		}
	case "update", "delete":
		if s.kind == "update" {
			a.writeSet |= wUpdate
		} else {
			a.writeSet |= wDelete
		}
		where := -1
		for j := s.at + 1; j < end; j++ {
			if a.sig[j].Depth == d && a.kw(j) == "where" {
				where = j
				break
			}
		}
		trivial := where >= 0 && a.trivialWhere(where+1, end, d)
		if trivial {
			a.f.WhereTrivial = true
		}
		if where < 0 || trivial {
			a.f.UnboundedWrite = true
		}
		if s.top {
			a.topDML++
			if where >= 0 {
				a.topWhere++
			}
		}
	}
}

// trivialWhere matches WHERE true, 1, NOT false, and x = x for one literal or column (S11 §3.5).
// It is best effort; the docs say so.
func (a *analyzer) trivialWhere(from, end, d int) bool {
	w := make([]Token, 0, 8)
	for j := from; j < end; j++ {
		if a.sig[j].Depth == d && a.kw(j) == "returning" {
			break
		}
		w = append(w, *a.sig[j])
	}
	for len(w) >= 2 && w[0].Kind == Punct && w[0].Text == "(" && w[len(w)-1].Kind == Punct && w[len(w)-1].Text == ")" &&
		w[len(w)-1].Depth == w[0].Depth {
		// Strip one pair only when it wraps the whole condition.
		inner := w[1 : len(w)-1]
		ok := true
		for _, t := range inner {
			if t.Depth <= w[0].Depth {
				ok = false
				break
			}
		}
		if !ok {
			break
		}
		w = inner
	}
	switch len(w) {
	case 1:
		return w[0].Kind == Ident && w[0].Norm == "true" || w[0].Kind == Number && w[0].Text == "1"
	case 2:
		return w[0].Kind == Ident && w[0].Norm == "not" && w[1].Kind == Ident && w[1].Norm == "false"
	}
	eq := -1
	for i, t := range w {
		if t.Kind == Op && t.Text == "=" && t.Depth == w[0].Depth {
			if eq >= 0 {
				return false
			}
			eq = i
		}
	}
	if eq <= 0 || eq == len(w)-1 || len(w)-1-eq != eq {
		return false
	}
	for i := 0; i < eq; i++ {
		l, r := w[i], w[eq+1+i]
		if l.Kind != r.Kind || l.Norm != r.Norm {
			return false
		}
		switch l.Kind {
		case Ident, QuotedIdent, Number, String, Punct:
			if l.Kind == Punct && l.Text != "." {
				return false
			}
			if l.Kind == Ident && l.Norm != "" && notFunc(l.Norm) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (a *analyzer) finish(n int) Facts {
	f := a.f
	f.Statements = min(n, a.opts.MaxStatements)
	f.Multi = f.Statements > 1
	if f.Kinds == nil {
		f.Kinds = []string{}
	}
	if n == 0 {
		f.Kind, f.Category = "empty", CatNone
	} else {
		f.Kind = f.Kinds[0]
		f.Category = a.catName
		if f.Multi && Severity(f.Category) < Severity(CatTransaction) {
			// Postgres runs a multi-statement string as one implicit transaction block, so it is
			// never just a read (S11 §7: several statements never classify as read).
			f.Category = CatTransaction
		}
	}
	f.HasWhere = a.topDML > 0 && a.topWhere == a.topDML
	f.WriteKinds = make([]string, 0, 2)
	for _, w := range []struct {
		bit  uint8
		name string
	}{{wCopy, "copy"}, {wDelete, "delete"}, {wInsert, "insert"}, {wUpdate, "update"}} {
		if a.writeSet&w.bit != 0 {
			f.WriteKinds = append(f.WriteKinds, w.name)
		}
	}
	f.Tables = sortedUnique(f.Tables)
	f.Functions = sortedUnique(f.Functions)
	f.DangerousFunctions = make([]string, 0)
	f.WriteFunctions = make([]string, 0)
	f.UnknownFunctions = make([]string, 0)
	for _, fn := range f.Functions {
		switch classifyFunction(fn) {
		case fnDangerous:
			f.DangerousFunctions = append(f.DangerousFunctions, fn)
		case fnWrite:
			f.WriteFunctions = append(f.WriteFunctions, fn)
		case fnUnknown:
			f.UnknownFunctions = append(f.UnknownFunctions, fn)
		}
	}
	if len(f.WriteFunctions) > 0 {
		f.Writes = true
	}
	f.Reasons = a.reasons.list()
	f.Ambiguous = len(f.Reasons) > 0
	if f.Ambiguous {
		f.Category = CatUnknown
		f.Writes = true
	}
	return f
}

func sortedUnique(s []string) []string {
	if len(s) == 0 {
		return []string{}
	}
	sortStrings(s)
	out := s[:1]
	for _, v := range s[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
