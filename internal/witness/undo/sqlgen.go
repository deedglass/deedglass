package undo

//deedglass:allow-sql-build identifiers come from the catalog by relid and are quoted; types match TypePattern; every value is a parameter

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Statement is one generated statement and its parameters: strings (sent as text) or nil (NULL).
type Statement struct {
	SQL  string
	Args []any
}

// QuoteIdent quotes one identifier as pgx.Identifier.Sanitize does: double quotes doubled, NUL
// bytes dropped, always quoted.
func QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, "\x00", ""), `"`, `""`) + `"`
}

// Qualified is "schema"."name".
func Qualified(t PlanTable) string { return QuoteIdent(t.Schema) + "." + QuoteIdent(t.Name) }

// cast is "$n::text::<type>": every value is sent as text and cast by the type's input function,
// which reads exactly what the decoder's text output printed.
func cast(n int, typ string) string { return "$" + strconv.Itoa(n) + "::text::" + typ }

// arg is a value as a parameter: its text, its bytes for non-UTF-8 text (hex in the plan), or nil.
func arg(v Value) (any, error) {
	switch {
	case v.N:
		return nil, nil
	case v.D != "":
		return nil, fmt.Errorf("undo: a sensitive value is never written")
	case v.U:
		return nil, fmt.Errorf("undo: an unchanged TOAST value is never written")
	case v.X != "":
		b, err := hex.DecodeString(v.X)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	case v.T != nil:
		return *v.T, nil
	}
	return nil, fmt.Errorf("undo: a value with nothing in it")
}

// checkTable refuses a plan table whose types can't go into a statement.
func checkTable(t PlanTable) error {
	if t.Schema == "" || t.Name == "" || len(t.PK) == 0 {
		return fmt.Errorf("undo: table %d has no name or no primary key", t.RelID)
	}
	for _, c := range t.Columns {
		if !ValidTypeName(c.Type) {
			return fmt.Errorf("undo: %s.%s has a type name that isn't safe in a cast", t.String(), c.Name)
		}
	}
	for _, k := range t.PK {
		if t.Type(k) == "" {
			return fmt.Errorf("undo: %s's key column %s has no type in the plan", t.String(), k)
		}
	}
	return nil
}

// GuardSelect reads the guard rows of one table: the primary key and cols, in text format (the
// caller asks for text results, so each value is the type's output, as pgoutput printed it), keys
// sorted, with FOR UPDATE when lock is set (preview omits it).
func GuardSelect(t PlanTable, cols []string, keys []Key, lock bool) (Statement, error) {
	if err := checkTable(t); err != nil {
		return Statement{}, err
	}
	sel := make([]string, 0, len(t.PK)+len(cols))
	for _, c := range append(append([]string{}, t.PK...), cols...) {
		sel = append(sel, QuoteIdent(c))
	}
	sorted := append([]Key{}, keys...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID(t.PK) < sorted[j].ID(t.PK) })
	args := make([]any, len(t.PK))
	for i, k := range t.PK {
		vals := make([]string, len(sorted))
		for j, key := range sorted {
			vals[j] = key[k]
		}
		args[i] = vals
	}
	var where string
	if len(t.PK) == 1 {
		where = fmt.Sprintf("%s = ANY($1::text[]::%s[])", QuoteIdent(t.PK[0]), t.Type(t.PK[0]))
	} else {
		cs, us, ps := make([]string, len(t.PK)), make([]string, len(t.PK)), make([]string, len(t.PK))
		for i, k := range t.PK {
			cs[i] = QuoteIdent(k)
			us[i] = fmt.Sprintf("u.c%d::%s", i+1, t.Type(k))
			ps[i] = fmt.Sprintf("$%d::text[]", i+1)
		}
		names := make([]string, len(t.PK))
		for i := range names {
			names[i] = fmt.Sprintf("c%d", i+1)
		}
		where = fmt.Sprintf("(%s) IN (SELECT %s FROM pg_catalog.unnest(%s) AS u(%s))", strings.Join(cs, ", "), strings.Join(us, ", "),
			strings.Join(ps, ", "), strings.Join(names, ", "))
	}
	order := make([]string, len(t.PK))
	for i, k := range t.PK {
		order[i] = QuoteIdent(k)
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s", strings.Join(sel, ", "), Qualified(t), where, strings.Join(order, ", "))
	if lock {
		q += " FOR UPDATE"
	}
	return Statement{SQL: q, Args: args}, nil
}

// StepStatement is one step as a statement. Each must affect exactly one row.
func StepStatement(t PlanTable, s Step) (Statement, error) {
	if err := checkTable(t); err != nil {
		return Statement{}, err
	}
	var args []any
	next := func(v any) int { args = append(args, v); return len(args) }
	whereKey := func() (string, error) {
		parts := make([]string, len(t.PK))
		for i, k := range t.PK {
			v, ok := s.Key[k]
			if !ok {
				return "", fmt.Errorf("undo: a step on %s has no value for key column %s", t.String(), k)
			}
			parts[i] = fmt.Sprintf("%s = %s", QuoteIdent(k), cast(next(v), t.Type(k)))
		}
		return strings.Join(parts, " AND "), nil
	}
	cols := make([]string, 0, len(s.Set))
	for c := range s.Set {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	for _, c := range cols {
		if t.Type(c) == "" {
			return Statement{}, fmt.Errorf("undo: %s.%s has no type in the plan", t.String(), c)
		}
	}
	switch s.Op {
	case OpDelete:
		w, err := whereKey()
		if err != nil {
			return Statement{}, err
		}
		return Statement{SQL: fmt.Sprintf("DELETE FROM %s WHERE %s", Qualified(t), w), Args: args}, nil
	case OpUpdate:
		if len(cols) == 0 {
			return Statement{}, fmt.Errorf("undo: an update step on %s sets nothing", t.String())
		}
		sets := make([]string, len(cols))
		for i, c := range cols {
			a, err := arg(s.Set[c])
			if err != nil {
				return Statement{}, err
			}
			sets[i] = fmt.Sprintf("%s = %s", QuoteIdent(c), cast(next(a), t.Type(c)))
		}
		w, err := whereKey()
		if err != nil {
			return Statement{}, err
		}
		return Statement{SQL: fmt.Sprintf("UPDATE %s SET %s WHERE %s", Qualified(t), strings.Join(sets, ", "), w), Args: args}, nil
	case OpInsert:
		if len(cols) == 0 {
			return Statement{}, fmt.Errorf("undo: an insert step on %s has no columns", t.String())
		}
		names, vals := make([]string, len(cols)), make([]string, len(cols))
		for i, c := range cols {
			a, err := arg(s.Set[c])
			if err != nil {
				return Statement{}, err
			}
			names[i] = QuoteIdent(c)
			vals[i] = cast(next(a), t.Type(c))
		}
		ov := ""
		if s.Overriding {
			ov = " OVERRIDING SYSTEM VALUE"
		}
		return Statement{SQL: fmt.Sprintf("INSERT INTO %s (%s)%s VALUES (%s)", Qualified(t), strings.Join(names, ", "), ov, strings.Join(vals, ", ")),
			Args: args}, nil
	}
	return Statement{}, fmt.Errorf("undo: unknown step %q", s.Op)
}

// GuardColumns is every column the guards of one table compare, sorted, the key aside.
func GuardColumns(p *Plan, relid uint32) []string {
	t, _ := p.Table(relid)
	set := map[string]bool{}
	for _, g := range p.Guards {
		if g.Table != relid {
			continue
		}
		for c := range g.Expect {
			set[c] = true
		}
	}
	var out []string
	for c := range set {
		isKey := false
		for _, k := range t.PK {
			isKey = isKey || k == c
		}
		if !isKey {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// GuardKeys is every key the guards of one table name.
func GuardKeys(p *Plan, relid uint32) []Key {
	var out []Key
	for _, g := range p.Guards {
		if g.Table == relid {
			out = append(out, g.Key)
		}
	}
	return out
}
