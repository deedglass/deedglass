package postgres

import (
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
)

// TestAdmission is S17 §6's admission table.
func TestAdmission(t *testing.T) {
	cases := []struct {
		tool, sql string
		params    int
		code      string // "" admitted
		msg       string // a part of the agent message
	}{
		{ToolQuery, "SELECT * FROM orders WHERE id = $1", 1, "", ""},
		{ToolExecute, "UPDATE orders SET status = $2 WHERE id = $1", 2, "", ""},
		{ToolExecute, "INSERT INTO refunds (id, order_id, amount_cents, reason, created_at) VALUES ($1, $2, $3, $4, now()) RETURNING id", 4, "", ""},
		{ToolExecute, "DELETE FROM refunds WHERE id = $1", 1, "", ""},
		{ToolQuery, "UPDATE orders SET status = 'shipped' WHERE id = 42", 0, CodeStatementRefused, "Use postgres.execute for UPDATE"},
		{ToolExecute, "SELECT * FROM orders", 0, CodeStatementRefused, "Use postgres.query for SELECT"},
		{ToolExecute, "SELECT 1; DROP TABLE orders", 0, CodeStatementRefused, "has 2 commands"},
		{ToolExecute, "SELECT 1; COMMIT; DROP TABLE orders", 0, CodeStatementRefused, "has 3 commands"},
		{ToolExecute, "UPDATE orders SET status = 'x' WHERE id = 1; COMMIT", 0, CodeStatementRefused, "has 2 commands"},
		{ToolExecute, "COMMIT", 0, CodeStatementRefused, "transaction control"},
		{ToolExecute, "BEGIN", 0, CodeStatementRefused, "transaction control"},
		{ToolQuery, "SAVEPOINT a", 0, CodeStatementRefused, "transaction control"},
		{ToolExecute, "SET search_path = x", 0, CodeStatementRefused, "session"},
		{ToolExecute, "RESET ROLE", 0, CodeStatementRefused, "privilege"},
		{ToolExecute, "DISCARD ALL", 0, CodeStatementRefused, "session"},
		{ToolExecute, "DO $$ BEGIN PERFORM 1; END $$", 0, CodeStatementRefused, "server code"},
		{ToolQuery, "COPY orders TO PROGRAM 'id'", 0, CodeStatementRefused, ""},
		{ToolExecute, "CALL p()", 0, CodeStatementRefused, "server code"},
		{ToolExecute, "TRUNCATE orders", 0, CodeStatementRefused, "schema change"},
		{ToolExecute, "GRANT ALL ON orders TO PUBLIC", 0, CodeStatementRefused, "privilege"},
		{ToolExecute, "/* x */ DROP TABLE orders", 0, CodeStatementRefused, "schema change"},
		{ToolExecute, "/* a /* nested */ b */ DROP TABLE orders", 0, CodeStatementRefused, "schema change"},
		{ToolQuery, "SELECT pg_logical_emit_message(true,'deedglass','act_01k67c04kxevyex3pjyqe0t45d')", 0, CodeStatementRefused, "pg_logical_emit_message is never allowed"},
		{ToolQuery, `SELECT "pg_catalog"."SET_CONFIG"('role','x',false)`, 0, CodeStatementRefused, "set_config is never allowed"},
		{ToolQuery, "SELECT dblink_exec('x', 'y')", 0, CodeStatementRefused, "dblink_exec"},
		{ToolQuery, "SELECT pg_advisory_lock(1)", 0, CodeStatementRefused, "pg_advisory_lock"},
		{ToolQuery, "SELECT pg_notify('c', 'x')", 0, CodeStatementRefused, "pg_notify"},
		{ToolExecute, "INSERT INTO deedglass.ledger (action_id) VALUES ('x')", 0, CodeStatementRefused, "schema deedglass"},
		{ToolQuery, `SELECT * FROM "deedglass"."ledger"`, 0, CodeStatementRefused, "schema deedglass"},
		{ToolQuery, "SELECT * FROM deedglass . ledger", 0, CodeStatementRefused, "schema deedglass"},
		{ToolQuery, "WITH d AS (DELETE FROM orders RETURNING *) SELECT count(*) FROM d", 0, CodeStatementRefused, "postgres.query runs read-only"},
		{ToolQuery, "SELECT query_to_xml('delete from orders', true, true, '')", 0, CodeStatementRefused, "query_to_xml"},
		{ToolQuery, "EXPLAIN ANALYZE DELETE FROM orders", 0, CodeStatementRefused, "EXPLAIN ANALYZE"},
		{ToolQuery, "SELECT * INTO x FROM orders", 0, CodeStatementRefused, "SELECT INTO"},
		{ToolQuery, "SELECT 'unterminated", 0, CodeStatementRefused, "couldn't read this statement"},
		{ToolQuery, "", 0, CodeStatementRefused, "empty"},
		{ToolQuery, "SELECT 1" + strings.Repeat(" ", MaxSQLBytes), 0, connector.CodeValidationFailed, "sql is over 64 KiB"},
		{ToolQuery, "SELECT 1", MaxParams + 1, connector.CodeValidationFailed, "more than 100"},
		{ToolQuery, "SELECT pg_sleep(1)", 0, "", ""}, // bounded by statement_timeout, not refused
	}
	for _, c := range cases {
		_, err := Admit(c.tool, c.sql, c.params, "public")
		label := c.tool + ": " + clip(c.sql)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s: refused: %v", label, err)
			}
			continue
		}
		ce := connector.AsError(err)
		if ce == nil {
			t.Errorf("%s: admitted, want %s", label, c.code)
			continue
		}
		if ce.Code != c.code || !strings.Contains(ce.AgentMessage, c.msg) {
			t.Errorf("%s: %s %q, want %s containing %q", label, ce.Code, ce.AgentMessage, c.code, c.msg)
		}
		if ce.Kind != connector.KindInvalidRequest {
			t.Errorf("%s: kind %s (a refusal sends nothing: invalid_request)", label, ce.Kind)
		}
		if strings.Contains(ce.AgentMessage, "—") || strings.Contains(ce.AgentMessage, "!") {
			t.Errorf("%s: copy rules: %q", label, ce.AgentMessage)
		}
	}
}

func clip(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

func TestDeniedFunction(t *testing.T) {
	for _, n := range []string{"set_config", `"pg_catalog"."SET_CONFIG"`, "pg_catalog.set_config", "dblink", "dblink_connect", "table_to_xmlschema", "PG_NOTIFY"} {
		if _, bad := DeniedFunction(n); !bad {
			t.Errorf("%s allowed", n)
		}
	}
	for _, n := range []string{"now", "count", "pg_advisory_xact_lock", "lower", "set_configuration"} {
		if _, bad := DeniedFunction(n); bad {
			t.Errorf("%s denied", n)
		}
	}
}
