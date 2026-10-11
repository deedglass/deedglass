package postgres

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/postgres/setup"
	"github.com/deedglass/deedglass/internal/ids"
)

// CheckOptions is one readiness check: the server's Test (the stored credential) and the CLI's
// local mode (--dsn-env) run the same code.
type CheckOptions struct {
	Config      Config
	ConnectorID string // con_…; "" in local mode without a connector
	Name        string
	AgentDSN    string // the dsn credential field
	WitnessDSN  string // the witness_dsn field ("" when not stored)
	Env         connector.Env
	Probe       bool // run begin_action in a rolled-back transaction
}

// CheckReport is what RunChecks found.
type CheckReport struct {
	Checks   []connector.Check
	State    *setup.State // nil when the database couldn't be reached
	Provider setup.Provider
	DSN      *DSN
	Ready    bool
	Line     string // "Ready for Proven checks · 8 passed · 2 notes · 0 failed"
}

// RunChecks connects with the agent DSN (and the witness DSN), reads the catalogs and runs the
// readiness table. It changes nothing; --probe writes inside a transaction it rolls back.
func RunChecks(ctx context.Context, o CheckOptions) CheckReport {
	rep := CheckReport{}
	add := func(c connector.Check) { rep.Checks = append(rep.Checks, c) }
	finish := func() CheckReport {
		rep.Ready, rep.Line = setup.Readiness(rep.Checks, o.Config.Witness.Enabled)
		return rep
	}
	d, err := ParseDSN(o.AgentDSN, o.Env)
	if err != nil {
		add(connector.Check{ID: "connection", Title: "connection", Status: connector.CheckFail, Detail: "dsn: " + dsnMessage(err),
			Fix: "Store a working DSN: deedglass connector postgres init --connector " + o.Name + " --apply writes it."})
		return finish()
	}
	rep.DSN = d
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(cctx, d.Config)
	if err != nil {
		ce := connectErr(err, o.Name)
		detail := ce.AdminDetail
		if detail == "" {
			detail = ce.AgentMessage
		}
		add(connector.Check{ID: "connection", Title: "connection", Status: connector.CheckFail, Detail: d.String() + " · " + detail,
			Fix: "Check that the database is running and reachable from the Deedglass server, then run check again."})
		return finish()
	}
	defer conn.Close(context.WithoutCancel(ctx))
	ue, err := probeRole(cctx, conn, o.Name)
	switch {
	case err != nil:
		add(connector.Check{ID: "connection", Title: "connection", Status: connector.CheckFail, Detail: err.Error()})
		return finish()
	case ue != nil:
		msg := errRoleUnsafe(ue).AgentMessage
		add(connector.Check{ID: "connection", Title: "connection", Status: connector.CheckFail, Detail: d.String() + " · " + ue.Error(),
			Fix: msg, DocURL: "https://docs.deedglass.com/errors/database_role_unsafe"})
	default:
		add(connector.Check{ID: "connection", Title: "connection", Status: connector.CheckPass, Detail: d.String()})
	}
	_, tlsOn := conn.PgConn().Conn().(*tls.Conn)
	st, err := setup.Introspect(cctx, conn, o.Config.Desired(setup.ProviderLocal).Want())
	if err != nil {
		add(connector.Check{ID: "catalogs", Title: "catalogs", Status: connector.CheckFail, Detail: err.Error(),
			Fix: "Check that the connector's role can read pg_catalog, then run check again."})
		return finish()
	}
	rep.State = st
	rep.Provider = setup.Detect(d.Host, st)
	if rep.Checks[0].Status == connector.CheckPass {
		rep.Checks[0].Detail += " · " + rep.Provider.Label()
	}
	desired := o.Config.Desired(rep.Provider)
	in := setup.CheckInput{State: st, Desired: desired, Witness: o.Config.Witness.Enabled, ConnectorID: o.ConnectorID, Connector: o.Name,
		Prod: !o.Env.Dev(), Host: d.Host, TLS: tlsOn, Pooler: d.Pooler}
	if o.Config.Witness.Enabled {
		in.WitnessDSN = witnessInfo(ctx, o)
	}
	if o.Probe {
		in.Probe = probeLedger(cctx, conn)
	}
	rep.Checks = append(rep.Checks, setup.Checks(in)...)
	return finish()
}

func dsnMessage(err error) string {
	var de *DSNError
	if errors.As(err, &de) {
		return de.Message
	}
	return "can't be read"
}

// witnessInfo connects with the witness DSN, reads who it is, and disconnects.
func witnessInfo(ctx context.Context, o CheckOptions) *setup.WitnessInfo {
	w := &setup.WitnessInfo{}
	if o.WitnessDSN == "" {
		return w
	}
	w.Configured = true
	d, err := ParseDSN(o.WitnessDSN, o.Env)
	if err != nil {
		w.Err = dsnMessage(err)
		return w
	}
	w.Pooler = d.Pooler
	w.User = d.User
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(cctx, d.Config)
	if err != nil {
		ce := connectErr(err, o.Name)
		w.Err = firstNonEmpty(ce.AdminDetail, ce.AgentMessage)
		return w
	}
	defer conn.Close(context.WithoutCancel(ctx))
	if err := conn.QueryRow(cctx, "SELECT CURRENT_USER::text").Scan(&w.User); err != nil {
		w.Err = "the witness role can't run a query"
		return w
	}
	w.Connected = true
	return w
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// probeLedger is check --probe (S17 §3.10): begin_action with throwaway ids, then the agent's own
// INSERT into the ledger (which must get 42501), all rolled back.
func probeLedger(ctx context.Context, conn *pgx.Conn) *setup.ProbeResult {
	p := &setup.ProbeResult{}
	tx, err := conn.Begin(ctx)
	if err != nil {
		p.Detail = "couldn't begin a transaction"
		return p
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	act, org, agt := ids.New[ids.ActionKind]().String(), ids.New[ids.OrgKind]().String(), ids.New[ids.AgentKind]().String()
	digest := "sha256:" + strings.Repeat("0", 64)
	var xid, lsn string
	if err := tx.QueryRow(ctx, `SELECT b.xid8::text, b.marker_lsn::text FROM deedglass.begin_action($1, $2, $3, NULL, $4) AS b`, act, org, agt, digest).Scan(&xid, &lsn); err != nil {
		p.Detail = fmt.Sprintf("begin_action failed (SQLSTATE %s)", firstNonEmpty(sqlState(err), "none"))
		return p
	}
	p.BeginAction = xid != "" && lsn != ""
	if _, err := tx.Exec(ctx, "SAVEPOINT deedglass_probe"); err != nil {
		p.Detail = "couldn't set a savepoint"
		return p
	}
	_, err = tx.Exec(ctx, `INSERT INTO deedglass.ledger (action_id, org_id, agent_id, sql_digest, xid8, created_at) VALUES ($1, $2, $3, $4, pg_catalog.pg_current_xact_id(), pg_catalog.now())`,
		ids.New[ids.ActionKind]().String(), org, agt, digest)
	switch code := sqlState(err); {
	case code == "42501":
		p.DirectRefused = true
	case err == nil:
		p.Detail = "deedglass_agent could insert into the ledger directly"
	default:
		p.Detail = "the direct ledger insert failed with SQLSTATE " + firstNonEmpty(code, "none") + ", not 42501"
	}
	_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT deedglass_probe")
	return p
}

// Test implements connector.Connector: the readiness checks with the stored dsn and witness_dsn.
func (c *Connector) Test(ctx context.Context, inst *connector.Instance) (connector.TestReport, error) {
	start := time.Now()
	rep := connector.TestReport{}
	defer func() { rep.Duration = time.Since(start) }()
	st, err := parse(inst)
	if err != nil {
		rep.Checks = []connector.Check{{ID: "config", Title: "config", Status: connector.CheckFail, Detail: "the stored config can't be read",
			Fix: "Update the connector with a config that passes deedglass connector create's checks."}}
		rep.Settle()
		return rep, nil
	}
	sec, err := inst.Secret(ctx)
	if err != nil {
		ce := connector.AsError(err)
		ch := connector.Check{ID: "credentials", Title: "credentials", Status: connector.CheckFail, Detail: "not set",
			Fix: "Run deedglass connector postgres init --connector " + inst.Name + " --apply, which stores dsn and witness_dsn."}
		if ce != nil && ce.Kind != connector.KindCredentialsMissing {
			ch.Detail, ch.Fix = firstNonEmpty(ce.AdminDetail, "can't be opened"), ""
			ch.DocURL = "https://docs.deedglass.com/errors/vault_unavailable"
		}
		rep.Checks = []connector.Check{ch}
		rep.Settle()
		return rep, nil
	}
	agent, witness := sec.Field("dsn"), sec.Field("witness_dsn")
	sec.Destroy()
	r := RunChecks(ctx, CheckOptions{Config: st.cfg, ConnectorID: inst.ID.String(), Name: inst.Name, AgentDSN: agent, WitnessDSN: witness, Env: inst.Env})
	rep.Checks = r.Checks
	rep.Settle()
	return rep, nil
}
