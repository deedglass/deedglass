package postgres

import (
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/sqlguard"
)

// Statement admission (S17 §3.7): hard refusals that hold even when policy allows the call.
// Policy decides what is allowed; the connector refuses what must never run through it. The
// denylist is defence in depth: the real boundary is deedglass_agent, which owns nothing, belongs
// to nothing and can't write the ledger, plus the extended protocol (one statement per execution)
// and DISCARD ALL between actions.

// deniedFunctions are matched on the function's last name part, unquoted and case folded, so
// "pg_catalog"."SET_CONFIG" is set_config. A trailing * is a prefix.
var deniedFunctions = []string{
	"pg_logical_emit_message", "set_config", "pg_read_file", "pg_read_binary_file", "pg_ls_dir", "pg_stat_file",
	"lo_import", "lo_export", "dblink*", "pg_terminate_backend", "pg_cancel_backend", "pg_reload_conf",
	"pg_advisory_lock", "pg_advisory_lock_shared", "query_to_xml*", "table_to_xml*", "cursor_to_xml", "pg_notify",
}

// DeniedFunction reports whether a function name (as sqlguard reports it) is on the denylist, and
// its normalized form.
func DeniedFunction(name string) (string, bool) {
	n := strings.ToLower(strings.ReplaceAll(name, `"`, ""))
	if i := strings.LastIndexByte(n, '.'); i >= 0 {
		n = n[i+1:]
	}
	for _, d := range deniedFunctions {
		if p, ok := strings.CutSuffix(d, "*"); ok {
			if strings.HasPrefix(n, p) {
				return n, true
			}
		} else if n == d {
			return n, true
		}
	}
	return n, false
}

// kindWord is a kind as the messages print it.
func kindWord(k string) string {
	switch k {
	case "select":
		return "SELECT"
	case "", "other", "empty":
		return "this statement"
	}
	return strings.ToUpper(k)
}

// Admit checks one SQL string for a tool (query or execute) and returns the classifier's facts.
// A refusal is a *connector.Error with code statement_refused (or validation_failed for size),
// and nothing reaches the database.
func Admit(tool, sql string, nparams int, defaultSchema string) (sqlguard.Facts, error) {
	if len(sql) > MaxSQLBytes {
		return sqlguard.Facts{}, connector.ErrValidation("sql", "is over 64 KiB")
	}
	if nparams > MaxParams {
		return sqlguard.Facts{}, connector.ErrValidation("params", "has more than 100 values")
	}
	f := sqlguard.Analyze(sql, sqlguard.Options{DefaultSchema: defaultSchema, MaxBytes: MaxSQLBytes})
	switch {
	case f.Statements > 1:
		return f, refused(msgMulti, f.Statements)
	case f.Ambiguous:
		return f, refused(msgUnclassified)
	case f.Statements == 0:
		return f, refused("Refused by the Postgres connector: the statement is empty. Send one SQL statement.")
	}
	if referencesDeedglass(sql, f) {
		return f, refused(msgSchema)
	}
	for _, fn := range f.Functions {
		if n, bad := DeniedFunction(fn); bad {
			return f, refused(msgFunction, n)
		}
	}
	if f.ExplainAnalyze {
		return f, refused(msgClass, "EXPLAIN ANALYZE")
	}
	switch {
	case tool == ToolQuery && f.Kind == "select":
		if f.SelectInto {
			return f, refused(msgClass, "SELECT INTO")
		}
		if f.CTEWrite || f.Writes {
			return f, refused(msgQueryKind, "statements that change data")
		}
		return f, nil
	case tool == ToolExecute && isDMLKind(f.Kind):
		return f, nil
	case tool == ToolQuery && isDMLKind(f.Kind):
		return f, refused(msgQueryKind, kindWord(f.Kind))
	case tool == ToolExecute && f.Kind == "select":
		return f, refused(msgExecuteKind, kindWord(f.Kind))
	case f.Category == sqlguard.CatUnknown:
		return f, refused(msgUnclassified)
	}
	return f, refused(msgClass, categoryWord(f.Category, f.Kind))
}

func isDMLKind(k string) bool { return k == "insert" || k == "update" || k == "delete" || k == "merge" }

func categoryWord(cat, kind string) string {
	switch cat {
	case sqlguard.CatTransaction:
		return "transaction control (" + kindWord(kind) + ")"
	case sqlguard.CatSession:
		return "session (" + kindWord(kind) + ")"
	case sqlguard.CatDDL:
		return "schema change (" + kindWord(kind) + ")"
	case sqlguard.CatPrivilege:
		return "privilege (" + kindWord(kind) + ")"
	case sqlguard.CatServerCode:
		return "server code (" + kindWord(kind) + ")"
	case sqlguard.CatMaintenance:
		return "maintenance (" + kindWord(kind) + ")"
	}
	return "unclassified"
}

// referencesDeedglass reports any use of schema deedglass: a table, a function, or the name
// followed by a dot anywhere in the statement.
func referencesDeedglass(sql string, f sqlguard.Facts) bool {
	for _, t := range f.Tables {
		if strings.HasPrefix(t, "deedglass.") {
			return true
		}
	}
	for _, fn := range f.Functions {
		if strings.HasPrefix(strings.ToLower(strings.ReplaceAll(fn, `"`, "")), "deedglass.") {
			return true
		}
	}
	toks, err := sqlguard.Tokenize(sql)
	if err != nil {
		return false // Analyze already refused unreadable SQL as ambiguous
	}
	for i := range toks {
		t := toks[i]
		if (t.Kind != sqlguard.Ident && t.Kind != sqlguard.QuotedIdent) || strings.ToLower(t.Norm) != "deedglass" {
			continue
		}
		for j := i + 1; j < len(toks); j++ {
			n := toks[j]
			if n.Kind == sqlguard.Space || n.Kind == sqlguard.LineComment || n.Kind == sqlguard.BlockComment {
				continue
			}
			if n.Kind == sqlguard.Punct && n.Text == "." {
				return true
			}
			break
		}
	}
	return false
}
