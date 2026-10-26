package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/connector/postgres/setup"
	"github.com/deedglass/deedglass/internal/connectorapi"
	"github.com/deedglass/deedglass/internal/vault"
)

// deedglass connector postgres init|check (S17 §3.9, §3.10). init reads the connector's config
// (from the server, or offline from --config), connects with an admin DSN read once from the
// environment, plans, and applies only after review: immediately for a local or throwaway
// database, otherwise with --confirm <database>. Passwords are generated here and reach Postgres
// only as SCRAM verifiers; the DSNs go into the vault (or a 0600 file), never to the terminal.

type pgInitOpts struct {
	connector, adminEnv, config, confirm, planDir, dsnHost string
	apply, rotate, noStore                                 bool
}

var pgInitValues pgInitOpts

func pgInitFlags(fs *flag.FlagSet) {
	v := &pgInitValues
	*v = pgInitOpts{}
	fs.StringVar(&v.connector, "connector", "", "the postgres connector's name")
	fs.StringVar(&v.adminEnv, "admin-dsn-env", "DEEDGLASS_INIT_DSN", "the environment variable holding the admin DSN (read once, never stored)")
	fs.StringVar(&v.config, "config", "", "read the connector's config from this YAML or JSON file instead of the server")
	fs.BoolVar(&v.apply, "apply", false, "apply the plan in one transaction")
	fs.StringVar(&v.confirm, "confirm", "", "the database name, typed exactly: required to apply to a remote, non-throwaway database")
	fs.BoolVar(&v.rotate, "rotate-passwords", false, "give deedglass_agent and deedglass_witness new passwords and store new DSNs")
	fs.BoolVar(&v.noStore, "no-store", false, "write the DSNs to .deedglass/pg-<connector>.env instead of the vault")
	fs.StringVar(&v.planDir, "plan-dir", filepath.Join(".deedglass", "plans"), "where the reviewable plan is written")
	fs.StringVar(&v.dsnHost, "dsn-host", "", "host:port the Deedglass server reaches the database at, when it differs from the admin DSN's (a Docker service name, a private DNS name)")
}

// cliEgress is the CLI's door: a person at a terminal may reach private and loopback databases;
// cloud metadata and reserved addresses never.
func cliEgress() connector.Env {
	return connector.Env{Name: egress.EnvDev, Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
}

// pgTarget is the connector init and check work on.
type pgTarget struct {
	ID     string // con_… ("" offline)
	Name   string
	Config postgres.Config
}

// loadPGTarget reads the connector from --config, or from the server by name.
func loadPGTarget(ctx context.Context, e *Env, name, file string) (pgTarget, error) {
	if file != "" {
		f, err := readConnectorFile(file)
		if err != nil {
			return pgTarget{}, err
		}
		if f.Type != "" && f.Type != postgres.Type {
			return pgTarget{}, usageErr(fmt.Errorf("%s describes a %s connector", file, f.Type), "Pass a postgres connector's config, like examples/demo/connectors/postgres.yaml.")
		}
		raw, _ := json.Marshal(f.Config)
		cfg, errs := postgres.ParseConfig(raw)
		if len(errs) > 0 {
			return pgTarget{}, &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: fmt.Errorf("%s: %s", errs[0].Param, strings.TrimSuffix(errs[0].Message, "."))}
		}
		return pgTarget{Name: firstNonEmpty(name, f.Name, "postgres"), Config: cfg}, nil
	}
	if name == "" {
		return pgTarget{}, usageErr(errors.New("this command needs --connector or --config"), "deedglass connector postgres init --connector postgres")
	}
	c, err := apiClient(e)
	if err != nil {
		return pgTarget{}, err
	}
	x, err := getConnector(ctx, e, c, name)
	if err != nil {
		return pgTarget{}, err
	}
	if x.Type != postgres.Type {
		return pgTarget{}, &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: fmt.Errorf("%s is a %s connector, not postgres", x.Name, x.Type),
			Next: "Pass a postgres connector, or create one: deedglass connector create --config examples/demo/connectors/postgres.yaml"}
	}
	cfg, errs := postgres.ParseConfig(x.Config)
	if len(errs) > 0 {
		return pgTarget{}, &ExitError{Code: ExitFailure, ErrCode: "validation_failed", Err: fmt.Errorf("the stored config: %s", errs[0].Message)}
	}
	return pgTarget{ID: x.ID, Name: x.Name, Config: cfg}, nil
}

// pgSetupJSON is init's --json output.
type pgSetupJSON struct {
	Object        string         `json:"object"` // "postgres_setup"
	Connector     string         `json:"connector"`
	Database      string         `json:"database"`
	Host          string         `json:"host"`
	ServerVersion string         `json:"server_version"`
	Provider      string         `json:"provider"`
	Changes       []setup.Change `json:"changes"`
	Notes         []setup.Note   `json:"notes"`
	Blockers      []string       `json:"blockers"`
	PlanSHA256    string         `json:"plan_sha256"`
	PlanFile      string         `json:"plan_file,omitempty"`
	Applied       bool           `json:"applied"`
	Credentials   string         `json:"credentials"` // stored | file | unchanged | none
	CredentialsAt string         `json:"credentials_file,omitempty"`
	Deed          string         `json:"deed,omitempty"`
}

func runPostgresInit(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass connector postgres init takes flags only (got %q)", args[0]), "deedglass connector postgres init --connector postgres")
	}
	v := pgInitValues
	t, err := loadPGTarget(ctx, e, v.connector, v.config)
	if err != nil {
		return err
	}
	adminDSN := e.Getenv(v.adminEnv)
	if strings.TrimSpace(adminDSN) == "" {
		return &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: errors.New(v.adminEnv + " is empty or unset"),
			Next: "Export the admin DSN in this shell (a superuser, or the RDS master user), then run the command again. Deedglass reads it once and never stores it."}
	}
	d, err := postgres.ParseDSN(adminDSN, cliEgress())
	if err != nil {
		return &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: fmt.Errorf("%s: %s", v.adminEnv, strings.TrimSuffix(err.Error(), "."))}
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(cctx, d.Config)
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "connector_unavailable", Err: fmt.Errorf("Can't connect to %s", d.String()),
			Detail: []string{vault.Scrub(lastPart(err))}, Next: "Check the admin DSN in " + v.adminEnv + " and that the database is running."}
	}
	defer conn.Close(context.WithoutCancel(ctx))
	want := t.Config.Desired(setup.ProviderLocal).Want()
	st, err := setup.Introspect(cctx, conn, want)
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "internal_error", Err: errors.New("Can't read the database's catalogs"), Detail: []string{lastPart(err)}}
	}
	prov := setup.Detect(d.Host, st)
	desired := t.Config.Desired(prov)
	plan := setup.Build(st, desired)
	out := pgSetupJSON{Object: "postgres_setup", Connector: t.Name, Database: st.Database, Host: d.Host, ServerVersion: st.Version, Provider: string(prov),
		Changes: plan.Changes, Notes: plan.Notes, Blockers: plan.Blockers, PlanSHA256: plan.SHA256(), Credentials: "none"}
	s := e.Style
	text := !e.Globals.JSON
	target := "--connector " + t.Name
	if v.config != "" {
		target = "--config " + v.config
	}
	if text {
		fmt.Fprintf(e.Stdout, "Deedglass %s Postgres setup plan %s %s on %s:%d %s PostgreSQL %s %s %s\n", ui.Sep, ui.Sep, st.Database, d.Host, d.Port, ui.Sep, st.Version, ui.Sep, prov.Label())
		for _, c := range plan.Changes {
			sym := s.Teal("+")
			switch c.Op {
			case "~":
				sym = s.Amber("~")
			case "-":
				sym = s.Red("-")
			}
			fmt.Fprintf(e.Stdout, "  %s %s\n", sym, c.Summary)
		}
		for _, n := range plan.Notes {
			fmt.Fprintf(e.Stdout, "  %s %s\n", s.Amber(ui.Unknown), n.Text)
		}
		for _, b := range plan.Blockers {
			fmt.Fprintf(e.Stdout, "  %s %s\n", s.Red(ui.Fail), b)
		}
	}
	if len(plan.Blockers) > 0 {
		if e.Globals.JSON {
			_ = writeJSON(e, out)
		}
		return &ExitError{Code: ExitFailure, ErrCode: "validation_failed", Err: errors.New("The plan can't be applied until the lines marked ✕ are fixed"), printed: e.Globals.JSON}
	}
	if !plan.Empty() {
		path, err := writePlanFile(v.planDir, t.Name, plan, st, d, prov)
		if err != nil {
			return &ExitError{Code: ExitFailure, ErrCode: "internal_error", Err: fmt.Errorf("Can't write the plan to %s", v.planDir), Detail: []string{lastPart(err)}}
		}
		out.PlanFile = path
		if text {
			n := len(plan.Changes)
			fmt.Fprintf(e.Stdout, "%d %s %s SQL for review: %s\n", n, plural(n, "change", "changes"), ui.Sep, path)
		}
	} else if text && !v.rotate {
		fmt.Fprintln(e.Stdout, "Nothing to change.")
	}
	if !v.apply {
		if e.Globals.JSON {
			return writeJSON(e, out)
		}
		if plan.Empty() {
			fmt.Fprintf(e.Stdout, "Next: deedglass connector postgres check %s\n", checkTarget(t, v.config))
		} else {
			fmt.Fprintf(e.Stdout, "Next: deedglass connector postgres init %s --apply\n", target)
		}
		return nil
	}
	if plan.Empty() && !v.rotate {
		if e.Globals.JSON {
			return writeJSON(e, out)
		}
		fmt.Fprintf(e.Stdout, "Next: deedglass connector postgres check %s\n", checkTarget(t, v.config))
		return nil
	}
	// The apply gate: local or throwaway now; otherwise the database's name, typed exactly.
	if !setup.IsLocalHost(d.Host) && !t.Config.Throwaway {
		switch {
		case v.confirm == st.Database:
		case v.confirm != "":
			return &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: fmt.Errorf("--confirm %q isn't this database's name", v.confirm),
				Next: fmt.Sprintf("The admin DSN points at %s on %s. Pass --confirm %s to apply there.", st.Database, d.Host, st.Database)}
		case e.IsTTY && !e.Globals.JSON:
			fmt.Fprintf(e.Stdout, "%s is a remote database. Type its name to apply the plan: ", st.Database)
			line, _ := bufio.NewReader(io.LimitReader(e.stdin(), 256)).ReadString('\n')
			if strings.TrimSpace(line) != st.Database {
				return &ExitError{Code: ExitFailure, ErrCode: "cancelled", Err: errors.New("Nothing applied: the name didn't match")}
			}
		default:
			return &ExitError{Code: ExitUsage, ErrCode: "confirmation_required", Err: fmt.Errorf("%s on %s is remote and not marked throwaway", st.Database, d.Host),
				Next: fmt.Sprintf("Review the plan, then run again with --apply --confirm %s.", st.Database)}
		}
	}
	// Passwords: for each login role created here, and for both with --rotate-passwords.
	passwords := map[string]string{}
	var created []string
	for _, c := range plan.Changes {
		if c.NewRole != "" {
			created = append(created, c.NewRole)
		}
	}
	for _, c := range plan.Changes {
		if strings.HasPrefix(c.Summary, "role deedglass_owner") && c.Op == "+" {
			created = append(created, setup.RoleOwner)
		}
	}
	roles := created
	if v.rotate {
		roles = []string{setup.RoleAgent, setup.RoleWitness}
	}
	for _, r := range roles {
		if r == setup.RoleOwner {
			continue
		}
		pw, err := setup.GeneratePassword(nil)
		if err != nil {
			return err
		}
		passwords[r] = pw
	}
	defer func() {
		for k := range passwords {
			passwords[k] = ""
		}
	}()
	if !plan.Empty() {
		if text {
			n := len(plan.Changes)
			fmt.Fprintf(e.Stdout, "%s Applying %d %s in one transaction %s lock_timeout 5s\n", s.Violet(ui.Pending), n, plural(n, "change", "changes"), ui.Sep)
		}
		start := time.Now()
		actx, acancel := context.WithTimeout(ctx, 2*time.Minute)
		defer acancel()
		if err := setup.Apply(actx, conn, plan, desired, passwords); err != nil {
			return applyErr(err)
		}
		out.Applied = true
		if text {
			n := len(plan.Changes)
			fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Applied %s %d %s %s %s", ui.Sep, n, plural(n, "change", "changes"), ui.Sep, durText(time.Since(start).Milliseconds()))))
		}
	}
	if v.rotate {
		if err := setup.SetPasswords(ctx, conn, passwords); err != nil {
			return &ExitError{Code: ExitFailure, ErrCode: "internal_error", Err: errors.New("Setting the new passwords failed; nothing was stored")}
		}
	}
	// Store the DSNs (or say why not).
	_, agentOK := passwords[setup.RoleAgent]
	_, witnessOK := passwords[setup.RoleWitness]
	switch {
	case !agentOK && !witnessOK:
		out.Credentials = "unchanged"
		if text {
			fmt.Fprintf(e.Stdout, "  Credentials unchanged %s the roles already existed. Pass --rotate-passwords to give them new passwords and store new DSNs.\n", ui.Sep)
		}
	default:
		dsns := map[string]string{}
		if agentOK {
			dsns["dsn"] = roleDSN(d, v.dsnHost, setup.RoleAgent, passwords[setup.RoleAgent])
		}
		if witnessOK {
			dsns["witness_dsn"] = roleDSN(d, v.dsnHost, setup.RoleWitness, passwords[setup.RoleWitness])
		}
		stored := false
		if t.ID != "" && !v.noStore {
			c, err := apiClient(e)
			if err == nil {
				var m vault.Meta
				_, err = c.Do(ctx, "PUT", "/v1/connectors/"+url.PathEscape(t.ID)+"/credentials", map[string]any{"kind": vault.KindDSN, "secret": dsns}, &m)
				stored = err == nil
				if err != nil && text {
					fmt.Fprintf(e.Stdout, "  %s The server didn't store the credentials (%s); writing them to a file instead.\n", s.Amber(ui.Unknown), vault.Scrub(lastPart(err)))
				}
			}
		}
		for k := range dsns {
			defer func(k string) { dsns[k] = "" }(k)
		}
		hints := []string{t.Name, setup.RoleAgent + "@" + hostPortDB(d, v.dsnHost)}
		if witnessOK {
			hints = append(hints, setup.RoleWitness+"@"+hostPortDB(d, v.dsnHost))
		}
		if stored {
			out.Credentials = "stored"
			if text {
				fmt.Fprintln(e.Stdout, s.OKLine("Credentials stored "+ui.Sep+" "+strings.Join(hints, " "+ui.Sep+" ")))
			}
		} else {
			path, err := writeDSNFile(t.Name, dsns)
			if err != nil {
				return &ExitError{Code: ExitFailure, ErrCode: "internal_error", Err: errors.New("Can't write the credentials file"), Detail: []string{lastPart(err)}}
			}
			out.Credentials, out.CredentialsAt = "file", path
			if text {
				fmt.Fprintln(e.Stdout, s.OKLine("Credentials written "+ui.Sep+" "+path+" (mode 0600)"))
				fmt.Fprintf(e.Stdout, "  Store them: set -a; . %s; set +a; deedglass connector credentials set %s --kind dsn --field dsn=env:DSN --field witness_dsn=env:WITNESS_DSN\n", path, t.Name)
			}
		}
		if text {
			fmt.Fprintln(e.Stdout, "  Passwords were generated here and sent to Postgres as SCRAM verifiers. They weren't printed or logged.")
		}
	}
	// Record: an admin deed with the plan's sha256 and the change count.
	if out.Applied && t.ID != "" {
		if c, err := apiClient(e); err == nil {
			var res connectorapi.Setup
			body := connectorapi.InitializedBody{PlanSHA256: plan.SHA256(), Changes: len(plan.Changes), Database: st.Database, ServerVersion: st.Version, Provider: string(prov), RolesCreated: created}
			if _, err := c.Do(ctx, "POST", "/v1/connectors/"+url.PathEscape(t.ID)+"/initialized", body, &res); err == nil {
				out.Deed = res.Deed
				if text {
					fmt.Fprintf(e.Stdout, "  %s\n", s.Dim("Sealed connector.postgres_initialized "+ui.Sep+" "+res.Deed))
				}
			} else if text {
				fmt.Fprintf(e.Stdout, "  %s The setup wasn't sealed as a record: %s\n", s.Amber(ui.Unknown), lastPart(err))
			}
		}
	}
	if e.Globals.JSON {
		return writeJSON(e, out)
	}
	fmt.Fprintf(e.Stdout, "Next: deedglass connector postgres check %s\n", checkTarget(t, v.config))
	return nil
}

// checkTarget is check's arguments after an init: the connector, or local mode for an offline init.
func checkTarget(t pgTarget, config string) string {
	if t.ID != "" || config == "" {
		return "--connector " + t.Name
	}
	return "--config " + config + " --dsn-env DSN --witness-dsn-env WITNESS_DSN (after: set -a; . .deedglass/pg-" + t.Name + ".env; set +a)"
}

func applyErr(err error) error {
	if errors.Is(err, setup.ErrPlanChanged) {
		return &ExitError{Code: ExitFailure, ErrCode: "conflict", Err: errors.New("The database changed since the plan was made. Nothing was applied"),
			Next: "Run init again to see the new plan."}
	}
	var ae *setup.ApplyError
	if errors.As(err, &ae) {
		return &ExitError{Code: ExitFailure, ErrCode: "internal_error", Err: fmt.Errorf("The change %q failed, so the whole transaction rolled back. Nothing was applied", ae.Change.Summary),
			Detail: []string{vault.Scrub(lastPart(ae.Err))}, Next: "Fix the cause (often a missing privilege or a lock held elsewhere), then run init again."}
	}
	return &ExitError{Code: ExitFailure, ErrCode: "internal_error", Err: errors.New("Applying the plan failed. Nothing was applied"), Detail: []string{vault.Scrub(lastPart(err))}}
}

func hostPortDB(d *postgres.DSN, override string) string {
	if override != "" {
		return override + "/" + d.DB
	}
	return fmt.Sprintf("%s:%d/%s", d.Host, d.Port, d.DB)
}

// roleDSN is a role's DSN on the admin DSN's host, port, database and TLS settings (host:port
// replaced by --dsn-host when the server reaches the database another way).
func roleDSN(d *postgres.DSN, hostOverride, role, password string) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(role, password), Host: fmt.Sprintf("%s:%d", d.Host, d.Port), Path: "/" + d.DB}
	if hostOverride != "" {
		u.Host = hostOverride
	} else if d.Socket {
		u.Host = ""
	}
	q := url.Values{}
	q.Set("sslmode", d.SSLMode)
	if d.Socket && hostOverride == "" {
		q.Set("host", d.Host)
		q.Set("port", fmt.Sprint(d.Port))
	}
	if d.RootCert != "" {
		// a path: the server needs the same file to verify the database
		q.Set("sslrootcert", d.RootCert)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// writePlanFile writes the reviewable plan (mode 0600, never a password).
func writePlanFile(dir, name string, p *setup.Plan, st *setup.State, d *postgres.DSN, prov setup.Provider) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.sql", name, now.Format("20060102T1504Z")))
	header := []string{
		"Deedglass · Postgres setup plan · connector " + name,
		fmt.Sprintf("%s on %s:%d · PostgreSQL %s · %s · %s", st.Database, d.Host, d.Port, st.Version, prov.Label(), now.Format(time.RFC3339)),
		"Review it, then apply it with deedglass connector postgres init --connector " + name + " --apply.",
		"It runs as one transaction. Credentials for roles it creates are generated at apply and never written here.",
	}
	return path, os.WriteFile(path, []byte(p.File(header)), 0o600)
}

// writeDSNFile writes the DSNs for --no-store (mode 0600) and returns its path.
func writeDSNFile(name string, dsns map[string]string) (string, error) {
	if err := os.MkdirAll(".deedglass", 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(".deedglass", "pg-"+name+".env")
	var sb strings.Builder
	sb.WriteString("# Written by deedglass connector postgres init. Secrets: never commit or share this file.\n")
	for _, k := range sortedKeys(dsns) {
		sb.WriteString(strings.ToUpper(k) + "=" + dsns[k] + "\n")
	}
	return path, os.WriteFile(path, []byte(sb.String()), 0o600)
}

// --- check ---

type pgCheckOpts struct {
	connector, dsnEnv, witnessEnv, config string
	probe                                 bool
}

var pgCheckValues pgCheckOpts

func pgCheckFlags(fs *flag.FlagSet) {
	v := &pgCheckValues
	*v = pgCheckOpts{}
	fs.StringVar(&v.connector, "connector", "", "the postgres connector's name")
	fs.StringVar(&v.dsnEnv, "dsn-env", "", "local mode: check with the DSN in this environment variable instead of the server")
	fs.StringVar(&v.witnessEnv, "witness-dsn-env", "", "local mode: the witness DSN's environment variable")
	fs.StringVar(&v.config, "config", "", "local mode: the connector's config file (else read from the server)")
	fs.BoolVar(&v.probe, "probe", false, "local mode: run begin_action in a transaction that is rolled back")
}

func runPostgresCheck(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass connector postgres check takes flags only (got %q)", args[0]), "deedglass connector postgres check --connector postgres")
	}
	v := pgCheckValues
	if v.dsnEnv == "" {
		if v.probe {
			return usageErr(errors.New("--probe runs in local mode only"), "Pass --dsn-env with the agent DSN's variable, like --dsn-env DEEDGLASS_PG_DSN --probe.")
		}
		if v.connector == "" {
			return usageErr(errors.New("deedglass connector postgres check needs --connector (or --dsn-env)"), "deedglass connector postgres check --connector postgres")
		}
		c, err := apiClient(e)
		if err != nil {
			return err
		}
		var res connectorapi.TestResult
		if _, err := c.Do(ctx, "POST", "/v1/connectors/"+url.PathEscape(v.connector)+"/test", struct{}{}, &res); err != nil {
			return apiErr(e, c, err)
		}
		witness := true
		for _, ch := range res.Checks {
			if ch.ID == "witness_role" && ch.Status == connector.CheckSkip {
				witness = false
			}
		}
		_, line := setup.Readiness(res.Checks, witness)
		return renderPGCheck(e, res.Checks, line, res.Status)
	}
	t, err := loadPGTarget(ctx, e, v.connector, v.config)
	if err != nil {
		return err
	}
	agent := e.Getenv(v.dsnEnv)
	if strings.TrimSpace(agent) == "" {
		return &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: errors.New(v.dsnEnv + " is empty or unset"),
			Next: "Export the agent DSN (deedglass_agent) in this shell, then run the command again."}
	}
	witness := ""
	if v.witnessEnv != "" {
		witness = e.Getenv(v.witnessEnv)
	}
	r := postgres.RunChecks(ctx, postgres.CheckOptions{Config: t.Config, ConnectorID: t.ID, Name: t.Name, AgentDSN: agent, WitnessDSN: witness, Env: cliEgress(), Probe: v.probe})
	st := connector.TestReport{Checks: r.Checks}
	st.Settle()
	return renderPGCheck(e, r.Checks, r.Line, string(st.Status))
}

// renderPGCheck prints S17 §5's readiness report.
func renderPGCheck(e *Env, checks []connector.Check, line, status string) error {
	fail := status == string(connector.CheckFail)
	if e.Globals.JSON {
		ready := !fail
		if err := writeJSON(e, map[string]any{"object": "postgres_check", "status": status, "ready_for_l2": ready && strings.HasPrefix(line, "Ready for Proven checks"),
			"summary": line, "checks": checks}); err != nil {
			return err
		}
		if fail {
			return &ExitError{Code: ExitFailure, ErrCode: "connector_test_failed", Err: errors.New("not ready for Proven checks"), printed: true}
		}
		return nil
	}
	s := e.Style
	var version, conn string
	for _, c := range checks {
		switch c.ID {
		case "version":
			version = c.Detail
		case "connection":
			conn = c.Detail
		}
	}
	if conn != "" {
		// deedglass_agent@127.0.0.1:5432/demo · local → PostgreSQL 18.6 · demo · 127.0.0.1 · local
		hp, prov, _ := strings.Cut(conn, " "+ui.Sep+" ")
		_, hostdb, _ := strings.Cut(hp, "@")
		host, db, _ := strings.Cut(hostdb, "/")
		if i := strings.LastIndexByte(host, ':'); i > 0 {
			host = host[:i]
		}
		parts := []string{}
		for _, p := range []string{version, db, host, prov} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		fmt.Fprintln(e.Stdout, strings.Join(parts, " "+ui.Sep+" "))
	}
	for _, c := range checks {
		if c.ID == "version" && c.Status == connector.CheckPass {
			continue
		}
		sym := s.Teal(ui.OK)
		switch c.Status {
		case connector.CheckFail:
			sym = s.Red(ui.Fail)
		case connector.CheckWarn:
			sym = s.Amber(ui.Unknown)
		case connector.CheckSkip:
			sym = s.Dim(ui.Unknown)
		}
		fmt.Fprintf(e.Stdout, "%s %s %s\n", sym, ui.Pad(c.Title, 18), c.Detail)
		if c.Status == connector.CheckFail || c.Status == connector.CheckWarn {
			if c.Fix != "" {
				fmt.Fprintf(e.Stdout, "  %s\n", c.Fix)
			}
			if c.FixSQL != "" && c.Status == connector.CheckFail {
				for _, l := range strings.Split(c.FixSQL, "\n") {
					fmt.Fprintf(e.Stdout, "    %s\n", s.Dim(l))
				}
			}
			if c.DocURL != "" && c.Status == connector.CheckFail {
				fmt.Fprintf(e.Stdout, "  %s\n", c.DocURL)
			}
		}
	}
	fmt.Fprintln(e.Stdout, line)
	if fail {
		return &ExitError{Code: ExitFailure, ErrCode: "connector_test_failed", Err: errors.New("not ready for Proven checks"), printed: true}
	}
	return nil
}
