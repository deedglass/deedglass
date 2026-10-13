package undo

//deedglass:allow-sql-build the script is text a person reads and runs; identifiers come from the catalog by relid and are quoted, types match TypePattern, values are quoted literals

import (
	"fmt"
	"sort"
	"strings"
)

// The free undo path (S50 §3.5): the plan as SQL a person reviews and runs themselves. Deedglass
// never runs it and never connects with write rights to make it. It is the same plan the Pro
// one-step undo runs, with the same guards, written as text:
//
//   - one transaction (BEGIN … COMMIT), the decoder's pinned output settings set locally so a
//     column's output text (format('%s', col): its type's output function) compares byte for byte
//     with the stored image;
//   - each step in its own DO block: an UPDATE or DELETE names its row by primary key and, in its
//     WHERE clause, every value the session left there (the guard); if the row changed since,
//     nothing matches and the block raises, so Postgres rolls back everything;
//   - an INSERT puts back a deleted row; if a row with that key exists again, the key's unique
//     index refuses it and everything rolls back.

// ScriptOptions are the script's header facts.
type ScriptOptions struct {
	Subject   string   // "watched session obs_…" or "action act_…"
	Connector string   // the connector's name
	Generated string   // "2026-10-17 14:05 UTC"
	Excluded  []string // transactions a person left out (listed, not undone)
	Warnings  []string // failing or warning checks, one sentence each
}

// ScriptRefusal is why no SQL can be printed: the plan's findings, one sentence each.
type ScriptRefusal struct{ Reasons []string }

func (r *ScriptRefusal) Error() string { return "undo: no SQL: " + strings.Join(r.Reasons, " ") }

// Script writes the plan as SQL. A plan that holds a value it can't print as text (non-UTF-8
// output) is refused with the reason.
func Script(p *Plan, o ScriptOptions) (string, error) {
	if p == nil || len(p.Steps) == 0 {
		return "", &ScriptRefusal{Reasons: []string{"There is nothing to undo."}}
	}
	for _, t := range p.Tables {
		if err := checkTable(t); err != nil {
			return "", err
		}
	}
	guards := map[string]Guard{}
	for _, g := range p.Guards {
		t, _ := p.Table(g.Table)
		guards[fmt.Sprintf("%d|%s", g.Table, g.Key.ID(t.PK))] = g
	}
	var body strings.Builder
	for i, s := range p.Steps {
		t, ok := p.Table(s.Table)
		if !ok {
			return "", fmt.Errorf("undo: step %d names a table the plan doesn't", i+1)
		}
		stmt, err := scriptStep(t, s, guards[fmt.Sprintf("%d|%s", s.Table, s.Key.ID(t.PK))])
		if err != nil {
			return "", err
		}
		body.WriteString(stmt)
	}
	tag := dollarTag(body.String())
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("-- Deedglass undo SQL for %s (connector %s).\n", commentSafe(o.Subject), commentSafe(o.Connector))
	if o.Generated != "" {
		w("-- Written %s from the row images Postgres committed. Deedglass doesn't run it; you do.\n", o.Generated)
	}
	w("-- It puts back %s: %s, newest change first, in one transaction.\n", rowsText(p.Rows), countsText(p))
	w("-- Each step checks the row still holds what the session left. If any row changed since, the\n")
	w("-- step stops the script and Postgres rolls back everything: nothing is half undone.\n")
	w("-- Covered: the tables below, by primary key. Not covered: sequences (they don't move back),\n")
	w("-- unwatched tables, and work your triggers do when these statements run.\n")
	for _, t := range p.TableNames() {
		w("--   %s\n", commentSafe(t))
	}
	if len(o.Excluded) > 0 {
		w("-- Left out by a person (not undone): %s\n", commentSafe(strings.Join(o.Excluded, ", ")))
	}
	for _, x := range o.Warnings {
		w("-- Check: %s\n", oneLine(x))
	}
	w("-- Run it with: psql \"$DATABASE_URL\" -v ON_ERROR_STOP=1 -f undo.sql\n")
	w("-- Plan %s\n", p.Digest)
	w("BEGIN;\n")
	w("SET LOCAL TimeZone = 'UTC';\nSET LOCAL DateStyle = 'ISO, YMD';\nSET LOCAL IntervalStyle = 'postgres';\n")
	w("SET LOCAL bytea_output = 'hex';\nSET LOCAL extra_float_digits = 3;\nSET LOCAL standard_conforming_strings = on;\n")
	w("%s", strings.ReplaceAll(body.String(), "$TAG$", tag))
	w("COMMIT;\n")
	return b.String(), nil
}

// scriptStep is one step in its own DO block (body tagged $TAG$, replaced once every value is known).
func scriptStep(t PlanTable, s Step, g Guard) (string, error) {
	var where []string
	for _, k := range t.PK {
		v, ok := s.Key[k]
		if !ok {
			return "", fmt.Errorf("undo: a step on %s has no value for key column %s", t.String(), k)
		}
		where = append(where, fmt.Sprintf("%s = %s::%s", QuoteIdent(k), quoteLiteral(v), t.Type(k)))
	}
	if s.Op != OpInsert {
		cols := make([]string, 0, len(g.Expect))
		for c := range g.Expect {
			if !isKey(t, c) {
				cols = append(cols, c)
			}
		}
		sort.Strings(cols)
		for _, c := range cols {
			pred, err := guardPredicate(c, g.Expect[c])
			if err != nil {
				return "", err
			}
			if pred != "" {
				where = append(where, pred)
			}
		}
	}
	keyText := KeyText(s.Key, t.PK)
	var stmt string
	switch s.Op {
	case OpDelete:
		stmt = fmt.Sprintf("DELETE FROM %s WHERE %s", Qualified(t), strings.Join(where, " AND "))
	case OpUpdate:
		cols := sortedSet(s.Set)
		if len(cols) == 0 {
			return "", fmt.Errorf("undo: an update step on %s sets nothing", t.String())
		}
		sets := make([]string, len(cols))
		for i, c := range cols {
			lit, err := literal(s.Set[c])
			if err != nil {
				return "", err
			}
			sets[i] = fmt.Sprintf("%s = %s::%s", QuoteIdent(c), lit, t.Type(c))
		}
		stmt = fmt.Sprintf("UPDATE %s SET %s WHERE %s", Qualified(t), strings.Join(sets, ", "), strings.Join(where, " AND "))
	case OpInsert:
		cols := sortedSet(s.Set)
		if len(cols) == 0 {
			return "", fmt.Errorf("undo: an insert step on %s has no columns", t.String())
		}
		names, vals := make([]string, len(cols)), make([]string, len(cols))
		for i, c := range cols {
			lit, err := literal(s.Set[c])
			if err != nil {
				return "", err
			}
			names[i], vals[i] = QuoteIdent(c), lit+"::"+t.Type(c)
		}
		ov := ""
		if s.Overriding {
			ov = " OVERRIDING SYSTEM VALUE"
		}
		return fmt.Sprintf("-- put back %s in %s (it was deleted)\nINSERT INTO %s (%s)%s VALUES (%s);\n", commentSafe(keyText), commentSafe(t.String()),
			Qualified(t), strings.Join(names, ", "), ov, strings.Join(vals, ", ")), nil
	default:
		return "", fmt.Errorf("undo: unknown step %q", s.Op)
	}
	verb := map[Op]string{OpDelete: "remove", OpUpdate: "restore"}[s.Op]
	msg := fmt.Sprintf("Deedglass undo stopped: %s in %s changed after the session. Nothing was undone.", keyText, t.String())
	return fmt.Sprintf("-- %s %s in %s\nDO $TAG$ BEGIN\n  %s;\n  IF NOT FOUND THEN RAISE EXCEPTION USING MESSAGE = %s; END IF;\nEND $TAG$;\n",
		verb, commentSafe(keyText), commentSafe(t.String()), stmt, quoteLiteral(msg)), nil
}

// guardPredicate compares one column's current text with the stored image.
func guardPredicate(col string, v Value) (string, error) {
	c := QuoteIdent(col)
	switch {
	case v.N:
		return c + " IS NULL", nil
	case v.U:
		return "", nil // unchanged TOAST: the image didn't carry it
	case v.D != "":
		hex := strings.TrimPrefix(v.D, "sha256:")
		return fmt.Sprintf("%s IS NOT NULL AND pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(pg_catalog.format('%%s', %s), 'UTF8')), 'hex') = %s", c, c, quoteLiteral(hex)), nil
	case v.X != "":
		return "", &ScriptRefusal{Reasons: []string{"A value in " + col + " isn't UTF-8 text, so it can't be printed as SQL. Undo it by hand."}}
	case v.T != nil:
		// format('%s', …) prints the value with its type's output function, as the decoder stored
		// it (a cast to text differs for some types: boolean prints true, not t).
		return c + " IS NOT NULL AND pg_catalog.format('%s', " + c + ") = " + quoteLiteral(*v.T), nil
	}
	return "", fmt.Errorf("undo: a guard value with nothing in it")
}

// literal is a value as a quoted SQL literal (cast by the caller), or NULL.
func literal(v Value) (string, error) {
	switch {
	case v.N:
		return "NULL", nil
	case v.X != "":
		return "", &ScriptRefusal{Reasons: []string{"A value isn't UTF-8 text, so it can't be printed as SQL. Undo it by hand."}}
	}
	a, err := arg(v)
	if err != nil {
		return "", err
	}
	s, _ := a.(string)
	return quoteLiteral(s), nil
}

// quoteLiteral is a standard-conforming string literal: single quotes doubled (the script sets
// standard_conforming_strings on, so backslashes are plain characters). NUL never occurs in text.
func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// dollarTag picks a DO-block tag that occurs nowhere in the body.
func dollarTag(body string) string {
	for i := 0; ; i++ {
		tag := "$dg$"
		if i > 0 {
			tag = fmt.Sprintf("$dg%d$", i)
		}
		if !strings.Contains(body, tag) {
			return tag
		}
	}
}

func isKey(t PlanTable, c string) bool {
	for _, k := range t.PK {
		if k == c {
			return true
		}
	}
	return false
}

func sortedSet(m map[string]Value) []string {
	out := make([]string, 0, len(m))
	for c := range m {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func countsText(p *Plan) string {
	var parts []string
	for _, op := range []Op{OpUpdate, OpInsert, OpDelete} {
		if n := p.Counts.Of(op); n > 0 {
			parts = append(parts, thousands(n)+" "+opPlural(op, n))
		}
	}
	return strings.Join(parts, ", ")
}

func oneLine(s string) string { return commentSafe(strings.Join(strings.Fields(s), " ")) }

// commentSafe keeps text inside one SQL comment line: control characters (a newline would end
// the comment and turn the rest into SQL) become "?".
func commentSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '\u2028' || r == '\u2029' {
			return '?'
		}
		return r
	}, s)
}
