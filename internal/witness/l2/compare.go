package l2

import (
	"math/big"
	"slices"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/witness"
)

// Type OIDs the key and value checks compare (S21 §3.4: text, varchar, bpchar, int2/4/8, bool;
// uuid for keys). Anything else isn't compared, and that isn't a gap: the column was expected to
// change.
const (
	oidBool    = 16
	oidInt8    = 20
	oidInt2    = 21
	oidInt4    = 23
	oidText    = 25
	oidBpchar  = 1042
	oidVarchar = 1043
	oidUUID    = 2950
)

// canon writes a value of a type the checks compare in one form: integers as Postgres prints them,
// booleans as t or f, bpchar without its padding, uuid lowercase without separators. ok is false
// for a type (or a text) that isn't compared.
func canon(oid uint32, s string, key bool) (string, bool) {
	switch oid {
	case oidText, oidVarchar:
		return s, true
	case oidBpchar:
		return strings.TrimRight(s, " "), true
	case oidInt2, oidInt4, oidInt8:
		n, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
		if !ok {
			return "", false
		}
		return n.String(), true
	case oidBool:
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "t", "true", "yes", "on", "1", "y":
			return "t", true
		case "f", "false", "no", "off", "0", "n":
			return "f", true
		}
		return "", false
	case oidUUID:
		if !key {
			return "", false
		}
		r := strings.NewReplacer("-", "", "{", "", "}", "")
		return strings.ToLower(r.Replace(strings.TrimSpace(s))), true
	}
	return "", false
}

// rowsWord is "row" or "rows".
func rowsWord(n int64) string {
	if n == 1 {
		return "row"
	}
	return "rows"
}

// opPast and opOnly name operations in sentences.
var (
	opPast = map[string]string{OpInsert: "inserted", OpUpdate: "updated", OpDelete: "deleted", OpTruncate: "truncated"}
	opOnly = map[string]string{OpInsert: "inserts rows", OpUpdate: "updates rows", OpDelete: "deletes rows"}
)

// keySet holds a footprint table's keys in canonical form, for one table's column types.
type keySet struct {
	cols []string
	set  map[string]bool
	ok   bool // every key column's type is compared
}

func newKeySet(te TableExpect, ti TableInfo) keySet {
	ks := keySet{set: map[string]bool{}, ok: len(te.Keys) > 0}
	if !ks.ok {
		return ks
	}
	for c := range te.Keys[0] {
		ks.cols = append(ks.cols, c)
	}
	sort.Strings(ks.cols)
	for _, k := range te.Keys {
		id, ok := ks.id(ti, func(c string) (string, bool) { v, ok := k[c]; return v, ok })
		if !ok {
			ks.ok = false
			return ks
		}
		ks.set[id] = true
	}
	return ks
}

// id is the canonical key of one row, or false when a key column is missing or isn't compared.
func (ks keySet) id(ti TableInfo, get func(string) (string, bool)) (string, bool) {
	parts := make([]string, len(ks.cols))
	for i, c := range ks.cols {
		v, ok := get(c)
		if !ok {
			return "", false
		}
		cv, ok := canon(ti.TypeOf(c), v, true)
		if !ok {
			return "", false
		}
		parts[i] = cv
	}
	return strings.Join(parts, "\x00"), true
}

// changeKey reads a change's key for ks; known is false when the change has no key.
func (ks keySet) changeKey(ti TableInfo, ch Change) (id string, known bool) {
	if ch.Key == nil {
		return "", false
	}
	return ks.id(ti, func(c string) (string, bool) {
		v, ok := ch.Key[c]
		if !ok {
			return "", false
		}
		if v == nil {
			return "NULL", true
		}
		return *v, true
	})
}

// keysText writes a footprint's keys the way the statement named them: "id = 42",
// "id IN (17, 18)", "(a, b) IN ((1, 'x'))".
func keysText(te TableExpect, ti TableInfo) string {
	if len(te.Keys) == 0 {
		return ""
	}
	var cols []string
	for c := range te.Keys[0] {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	lit := func(c, v string) string {
		switch oid := ti.TypeOf(c); {
		case oid == oidInt2 || oid == oidInt4 || oid == oidInt8:
			return v
		case oid == 0 && isInteger(v):
			return v // no type at hand (the CLI's render): digits read as a number
		}
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	if len(cols) == 1 {
		c := cols[0]
		if len(te.Keys) == 1 {
			return c + " = " + lit(c, te.Keys[0][c])
		}
		vs := make([]string, 0, len(te.Keys))
		for _, k := range te.Keys {
			vs = append(vs, lit(c, k[c]))
		}
		return c + " IN (" + strings.Join(vs, ", ") + ")"
	}
	if len(te.Keys) == 1 {
		parts := make([]string, len(cols))
		for i, c := range cols {
			parts[i] = c + " = " + lit(c, te.Keys[0][c])
		}
		return strings.Join(parts, " AND ")
	}
	rows := make([]string, 0, len(te.Keys))
	for _, k := range te.Keys {
		vs := make([]string, len(cols))
		for i, c := range cols {
			vs[i] = lit(c, k[c])
		}
		rows = append(rows, "("+strings.Join(vs, ", ")+")")
	}
	return "(" + strings.Join(cols, ", ") + ") IN (" + strings.Join(rows, ", ") + ")"
}

// identityWord is a replica identity as ALTER TABLE spells it.
func identityWord(id string) string {
	switch id {
	case "f":
		return "FULL"
	case "n":
		return "NOTHING"
	case "i":
		return "USING INDEX"
	}
	return "DEFAULT"
}

// Compare runs every rule of S21 §3.4 over one bound frame and returns every finding. The
// footprint must not be unknown (Derive already gave footprint_unknown). A clean comparison adds
// footprint_matched per footprint table; anything else stays as found.
func Compare(fp Footprint, f *Frame, cfg Config) []witness.Reason {
	var rs []witness.Reason
	add := func(code witness.Code, check string, vars map[string]any) *witness.Reason {
		rs = append(rs, witness.R(witness.L2, code, check, vars))
		return &rs[len(rs)-1]
	}
	byTable := map[string]TableExpect{}
	for _, te := range fp.Tables {
		byTable[te.Table] = te
	}
	observed := observedTables(f)
	for _, t := range observed {
		c := f.Counts[t]
		te, in := byTable[t]
		if !in {
			n := int64(c.Rows())
			if n == 0 {
				n = int64(c.Truncate)
			}
			add(witness.CodeUnexpectedTable, "table", map[string]any{"n": n, "rows": rowsWord(n), "table": t})
			continue
		}
		for _, op := range []string{OpInsert, OpUpdate, OpDelete} {
			if n := int64(c.Of(op)); n > 0 && !slices.Contains(te.Ops, op) {
				add(witness.CodeUnexpectedOperation, "operation", map[string]any{"op_past": opPast[op], "n": n, "rows": rowsWord(n),
					"table": t, "expected_op": expectedOps(te.Ops)})
			}
		}
		if c.Truncate > 0 {
			add(witness.CodeUnexpectedOperation, "operation", map[string]any{"op_past": opPast[OpTruncate], "n": "all", "rows": "rows",
				"table": t, "expected_op": expectedOps(te.Ops)})
		}
	}
	for _, te := range fp.Tables {
		rs = append(rs, compareTable(te, f, cfg)...)
	}
	if len(fp.Exact) > 0 {
		rs = append(rs, exactChecks(fp, f)...)
	}
	rs = append(rs, rowLimits(byTable, observed, f, cfg)...)
	clean := true
	for _, r := range rs {
		if r.Class == witness.Violation || r.Class == witness.Gap {
			clean = false
		}
	}
	if clean && len(fp.Exact) > 0 {
		for _, t := range exactTables(fp) {
			n := int64(f.Counts[t].Rows())
			add(witness.CodeUndoRestored, "footprint", map[string]any{"observed": n, "rows": rowsWord(n), "table": t})
		}
		return rs
	}
	if clean {
		for _, te := range fp.Tables {
			n := int64(0)
			for _, op := range te.Ops {
				n += int64(f.Counts[te.Table].Of(op))
			}
			add(witness.CodeFootprintMatched, "footprint", map[string]any{"observed": n, "rows": rowsWord(n), "table": te.Table})
		}
	}
	return rs
}

// observedTables are the tables a frame changed (the ledger aside), sorted.
func observedTables(f *Frame) []string {
	var out []string
	for t, c := range f.Counts {
		if t != LedgerTable && (c.Rows() > 0 || c.Truncate > 0) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// rowLimits is row_limit_exceeded: witness.max_rows (or a contract's max_rows) applies to every
// table the frame changed, whatever the footprint says.
func rowLimits(byTable map[string]TableExpect, observed []string, f *Frame, cfg Config) []witness.Reason {
	var rs []witness.Reason
	for _, t := range observed {
		max, ok := cfg.MaxRows[t]
		if te, in := byTable[t]; in && te.MaxRows != nil {
			max, ok = *te.MaxRows, true
		}
		if n := int64(f.Counts[t].Rows()); ok && n > max {
			rs = append(rs, witness.R(witness.L2, witness.CodeRowLimitExceeded, "max_rows", map[string]any{"observed": n, "table": t, "max": max}))
		}
	}
	return rs
}

// RowLimits is rowLimits for a frame whose footprint is unknown.
func RowLimits(f *Frame, cfg Config) []witness.Reason {
	return rowLimits(nil, observedTables(f), f, cfg)
}

func expectedOps(ops []string) string {
	if len(ops) == 1 {
		return opOnly[ops[0]]
	}
	parts := make([]string, len(ops))
	for i, op := range ops {
		parts[i] = strings.TrimSuffix(opOnly[op], " rows")
	}
	return strings.Join(parts, " and ") + " rows"
}

// compareTable is the count, key, column and value checks for one footprint table.
func compareTable(te TableExpect, f *Frame, cfg Config) []witness.Reason {
	var rs []witness.Reason
	t := te.Table
	ti, present := f.Tables[t]
	c := f.Counts[t]
	obs := int64(0)
	for _, op := range te.Ops {
		obs += int64(c.Of(op))
	}
	if te.Rows != nil {
		switch exp := *te.Rows; {
		case obs > exp:
			rs = append(rs, witness.R(witness.L2, witness.CodeRowCountExceeded, "rows", map[string]any{"observed": obs, "table": t, "expected": exp}).
				WithEvidence(map[string]any{"observed": obs, "expected": exp}))
		case obs < exp:
			rs = append(rs, witness.R(witness.L2, witness.CodeMissingChange, "rows", map[string]any{"expected": exp, "rows": rowsWord(exp), "table": t,
				"observed": obs}).WithEvidence(map[string]any{"observed": obs, "expected": exp}))
		}
	}
	if f.HasFlag("relation_changed:" + t) {
		rs = append(rs, witness.R(witness.L2, witness.CodeRelationChanged, "relation", map[string]any{"table": t}))
	}
	if !present {
		return rs // the transaction didn't touch the table: no rows to match or check
	}
	// Which changes the statement is answerable for: its operations on its table, and with exact
	// keys only the named rows.
	ks := newKeySet(te, ti)
	exact := len(te.Keys) > 0
	var mine []Change
	var outside, unknownKeys int64
	found := map[string]bool{}
	for _, ch := range f.Changes {
		if ch.Table != t || !slices.Contains(te.Ops, opName(ch.Op)) {
			continue
		}
		if !exact || !ks.ok {
			mine = append(mine, ch)
			continue
		}
		id, known := ks.changeKey(ti, ch)
		switch {
		case !known:
			unknownKeys++
		case ks.set[id]:
			found[id] = true
			mine = append(mine, ch)
		default:
			outside++
		}
	}
	if exact && ks.ok {
		imaged := f.imaged(t)
		if unknownKeys > 0 {
			why := "the table has no primary key"
			if len(ti.PK) > 0 {
				why = "some changed rows carry no key"
			}
			rs = append(rs, witness.R(witness.L2, witness.CodeKeysUnknown, "keys", map[string]any{"table": t, "why": why}))
		}
		if !imaged && len(found) < len(ks.set) {
			rs = append(rs, witness.R(witness.L2, witness.CodeKeysUnknown, "keys", map[string]any{"table": t,
				"why": "the rows are past the image cap"}))
		}
		if outside > 0 {
			are := "are"
			if outside == 1 {
				are = "is"
			}
			rs = append(rs, witness.R(witness.L2, witness.CodeUnexpectedKey, "keys", map[string]any{"n": outside, "rows": rowsWord(outside), "table": t,
				"are": are, "keys": keysText(te, ti)}).WithEvidence(map[string]any{"outside": outside, "imaged": imaged}))
		}
	} else if exact && !ks.ok && len(ti.PK) == 0 {
		rs = append(rs, witness.R(witness.L2, witness.CodeKeysUnknown, "keys", map[string]any{"table": t, "why": "the table has no primary key"}))
	}
	// Columns and values: only the rows the statement is answerable for, only for an UPDATE whose
	// SET list is known.
	if te.Columns != nil && slices.Contains(te.Ops, OpUpdate) {
		rs = append(rs, columnChecks(te, ti, mine, cfg)...)
	}
	return rs
}

// columnChecks are unexpected_column (needs a full old image) and value_mismatch over the rows the
// statement is answerable for.
func columnChecks(te TableExpect, ti TableInfo, mine []Change, cfg Config) []witness.Reason {
	var rs []witness.Reason
	t := te.Table
	extra := map[string]int64{}
	var extraOrder []string
	missingOld := false
	values := map[string]*mismatch{}
	var valueOrder []string
	for _, ch := range mine {
		if ch.Op != "U" {
			continue
		}
		if ch.Changed == nil && (ti.Identity != "f" || len(ch.Old) == 0) {
			missingOld = true
		} else {
			for _, col := range ch.Changed {
				if !slices.Contains(te.Columns, col) {
					if extra[col] == 0 {
						extraOrder = append(extraOrder, col)
					}
					extra[col]++
				}
			}
		}
		for _, col := range sortedKeys(te.Values) {
			lit := te.Values[col]
			nv, ok := ch.New[col]
			if !ok || nv.D != "" || nv.U || nv.X != "" {
				continue // not sent, or sensitive: not compared
			}
			oid := ti.TypeOf(col)
			if nv.N {
				if _, cmp := canon(oid, lit.Text, false); cmp {
					note(values, &valueOrder, col, keyText(ch, ti), "NULL", lit.Text)
				}
				continue
			}
			if nv.T == nil {
				continue
			}
			want, ok1 := canon(oid, lit.Text, false)
			got, ok2 := canon(oid, *nv.T, false)
			if !ok1 || !ok2 {
				continue
			}
			if want != got {
				note(values, &valueOrder, col, keyText(ch, ti), *nv.T, lit.Text)
			}
		}
	}
	if missingOld {
		rs = append(rs, witness.R(witness.L2, witness.CodeOldImageMissing, "columns", map[string]any{"table": t, "identity": identityWord(ti.Identity)}))
	}
	for _, col := range extraOrder {
		n := extra[col]
		rs = append(rs, witness.R(witness.L2, witness.CodeUnexpectedColumn, "columns", map[string]any{"column": col, "n": n, "rows": rowsWord(n),
			"table": t, "columns": strings.Join(te.Columns, ", ")}))
	}
	for _, col := range valueOrder {
		m := values[col]
		r := witness.R(witness.L2, witness.CodeValueMismatch, "value", map[string]any{"column": col, "observed": m.observed, "key": m.key,
			"table": t, "expected": m.exp}).WithDiff([]witness.FieldDiff{{Path: col, Expected: m.exp, Observed: m.observed}})
		if m.n > 1 {
			r = r.WithEvidence(map[string]any{"rows": m.n})
		}
		rs = append(rs, r)
	}
	return rs
}

// mismatch is one SET column whose committed value differs: the first row's key and values, and
// how many rows differ.
type mismatch struct {
	key           string
	observed, exp string
	n             int64
}

func note(values map[string]*mismatch, order *[]string, col, key, observed, exp string) {
	if x, ok := values[col]; ok {
		x.n++
		return
	}
	values[col] = &mismatch{key: key, observed: observed, exp: exp, n: 1}
	*order = append(*order, col)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// keyText is a change's key as "id = 42" (the table's primary-key order).
func keyText(ch Change, ti TableInfo) string {
	cols := ti.PK
	if len(cols) == 0 {
		cols = sortedKeys(ch.Key)
	}
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		v, ok := ch.Key[c]
		switch {
		case !ok:
			continue
		case v == nil:
			parts = append(parts, c+" = NULL")
		default:
			parts = append(parts, c+" = "+*v)
		}
	}
	if len(parts) == 0 {
		return "with no key"
	}
	return strings.Join(parts, " AND ")
}

func isInteger(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" || len(s) > 19 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
