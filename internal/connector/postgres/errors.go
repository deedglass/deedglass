package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/deedglass/deedglass/internal/connector"
)

// The Postgres connector's codes (contract §8; each has a section in docs/api/errors.md).
const (
	CodeStatementRefused   = "statement_refused"
	CodeRowLimitExceeded   = "row_limit_exceeded"
	CodeDatabaseRoleUnsafe = "database_role_unsafe"
	CodeDatabaseNotReady   = "database_not_ready"
)

// The connector's copy (S17 §5). Agent messages never name a credential or a host.
const (
	msgMulti        = "Refused by the Postgres connector: the statement has %d commands. Deedglass runs one statement per action."
	msgQueryKind    = "postgres.query runs read-only statements. Use postgres.execute for %s."
	msgExecuteKind  = "postgres.execute runs one INSERT, UPDATE, DELETE or MERGE. Use postgres.query for %s."
	msgSchema       = "Statements can't reference schema deedglass."
	msgFunction     = "Refused by the Postgres connector: %s is never allowed in agent SQL."
	msgClass        = "Refused by the Postgres connector: %s statements never run through agents."
	msgUnclassified = "Refused by the Postgres connector: Deedglass couldn't read this statement with confidence. Send one plain SELECT, INSERT, UPDATE, DELETE or MERGE."
	msgNotReady     = "Deedglass isn't set up in this database yet. Run deedglass connector postgres init --connector %s."
	msgRoleUnsafe   = "The connector's database role is a superuser. Deedglass refuses to run agent SQL with it. Run deedglass connector postgres init to create deedglass_agent."
	msgRoleNotAgent = "The connector connects as %s, not deedglass_agent. Deedglass runs agent SQL only as deedglass_agent. Run deedglass connector postgres init --connector %s."
	msgRows         = "The statement changed %s rows, over this connector's limit of %s. Nothing was committed. Narrow the WHERE clause."
	msgTimeout      = "The statement ran past this connector's %s limit, so Postgres stopped it. Nothing was committed."
	msgLock         = "The statement waited more than %s for a row lock, so Postgres stopped it. Nothing was committed. Try again shortly."
	msgCommitLost   = "The connection to the database was lost while it committed. The change may have been applied."
	msgReadOnly     = "postgres.query runs read-only. This statement tried to change data, so Postgres refused it."
	msgInternal     = "The Postgres connector couldn't start its transaction safely, so nothing ran. An admin has been alerted."
)

func refused(format string, a ...any) *connector.Error {
	return &connector.Error{Kind: connector.KindInvalidRequest, Code: CodeStatementRefused, AgentMessage: fmt.Sprintf(format, a...)}
}

// unsafeRoleError is the AfterConnect probe's refusal.
type unsafeRoleError struct {
	User      string
	Connector string
	Reasons   []string // SUPERUSER, BYPASSRLS, pg_write_all_data, …
	NotAgent  bool
}

func (e *unsafeRoleError) Error() string {
	if len(e.Reasons) == 0 {
		return fmt.Sprintf("postgres: the connector connects as %s, not deedglass_agent", e.User)
	}
	return fmt.Sprintf("postgres: role %s is superuser-class (%s)", e.User, strings.Join(e.Reasons, ", "))
}

func errRoleUnsafe(e *unsafeRoleError) *connector.Error {
	msg := msgRoleUnsafe
	if e.NotAgent && len(e.Reasons) == 0 {
		msg = fmt.Sprintf(msgRoleNotAgent, e.User, e.Connector)
	}
	return &connector.Error{Kind: connector.KindConfig, Code: CodeDatabaseRoleUnsafe, AgentMessage: msg, AdminDetail: e.Error(), Err: e}
}

func sqlState(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// statementError maps a failure of the agent's statement, inside a transaction that is then rolled
// back: nothing was committed, so every mapping is a failed kind, never unknown (S13 §3.5).
func statementError(err error, c Config) *connector.Error {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		if ce := connector.AsError(err); ce != nil {
			return ce
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return &connector.Error{Kind: connector.KindToolError, Code: connector.CodeTimeout,
				AgentMessage: "The statement didn't finish before the action's deadline, so Deedglass stopped it. Nothing was committed.", Err: err}
		}
		return &connector.Error{Kind: connector.KindToolError, Code: connector.CodeUpstreamError,
			AgentMessage: "The statement failed before Postgres answered. Nothing was committed.", Err: err}
	}
	e := &connector.Error{Kind: connector.KindToolError, Code: connector.CodeUpstreamError, Err: err, HTTPStatus: 0}
	switch {
	case pe.Code == "57014":
		e.Code, e.AgentMessage = connector.CodeTimeout, fmt.Sprintf(msgTimeout, msText(c.StatementTimeoutMS))
	case pe.Code == "55P03":
		e.Code, e.AgentMessage = connector.CodeTimeout, fmt.Sprintf(msgLock, msText(c.LockTimeoutMS))
	case pe.Code == "25006":
		e.Code, e.AgentMessage = CodeStatementRefused, msgReadOnly
	case pe.Code == "42501":
		e.AgentMessage = fmt.Sprintf("Postgres refused it: %s (SQLSTATE 42501). Nothing was committed.", clipMsg(pe.Message))
	case strings.HasPrefix(pe.Code, "23"):
		what := "the row breaks a constraint"
		if pe.ConstraintName != "" {
			what = "the row breaks constraint " + pe.ConstraintName
		}
		e.AgentMessage = fmt.Sprintf("Postgres refused it: %s (SQLSTATE %s). Nothing was committed.", what, pe.Code)
		// DETAIL can hold row values: admins only.
		e.AdminDetail = clipMsg(pe.Detail)
	case pe.Code == "40001" || pe.Code == "40P01":
		e.AgentMessage = fmt.Sprintf("Postgres stopped the transaction to resolve a conflict (SQLSTATE %s). Nothing was committed. Send the call again.", pe.Code)
		e.Retryable = true
	case pe.Code == "42601" && strings.Contains(pe.Message, "multiple commands"):
		e.Code, e.AgentMessage = CodeStatementRefused, fmt.Sprintf(msgMulti, 2)
	default:
		e.AgentMessage = fmt.Sprintf("Postgres refused it: %s (SQLSTATE %s). Nothing was committed.", clipMsg(pe.Message), pe.Code)
	}
	return e
}

// setupError maps a failure of the batched setup (set_config and begin_action).
func setupError(err error, name string, c Config) *connector.Error {
	switch code := sqlState(err); {
	case code == "42883" || code == "3F000" || code == "42P01":
		return &connector.Error{Kind: connector.KindConfig, Code: CodeDatabaseNotReady, AgentMessage: fmt.Sprintf(msgNotReady, name),
			AdminDetail: "deedglass.begin_action is missing (SQLSTATE " + code + "). Run deedglass connector postgres init.", Err: err}
	case code == "42501":
		return &connector.Error{Kind: connector.KindConfig, Code: CodeDatabaseNotReady, AgentMessage: fmt.Sprintf(msgNotReady, name),
			AdminDetail: "deedglass_agent can't execute deedglass.begin_action (42501). Run deedglass connector postgres check.", Err: err}
	case code == "DG101" || code == "DG102" || code == "DG103":
		return &connector.Error{Kind: connector.KindConfig, Code: "internal_error", AgentMessage: msgInternal,
			AdminDetail: "begin_action refused the transaction (" + code + "): the connector's recipe is broken. This is a Deedglass bug.", Err: err}
	case code == "57014" || code == "55P03":
		return statementError(err, c)
	}
	return statementError(err, c)
}

func clipMsg(s string) string {
	if len(s) > 300 {
		return s[:297] + "…"
	}
	return s
}

func msText(ms int) string {
	if ms%1000 == 0 {
		return fmt.Sprintf("%d s", ms/1000)
	}
	return fmt.Sprintf("%d ms", ms)
}

// thousands renders 1204 as "1,204".
func thousands(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
