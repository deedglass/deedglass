package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	starters "github.com/deedglass/deedglass/examples/policies"
	"github.com/deedglass/deedglass/internal/cli/mark"
	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/postgres"
	pgsetup "github.com/deedglass/deedglass/internal/connector/postgres/setup"
	"github.com/deedglass/deedglass/internal/setup"
	"github.com/deedglass/deedglass/internal/vault"
	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// deedglass connect <postgres-url> (adoption-v1 item 2): your own Postgres behind Deedglass in one
// command. It tells Supabase, Neon and plain Postgres apart, warns about poolers, runs read-only
// checks (version, wal_level, replication slots, privileges, which tables lack REPLICA IDENTITY
// FULL) and prints one plan. With --apply it creates the connector, runs connector postgres init
// (roles, publication, slot; the new roles' DSNs go to the vault), pushes and enforces a starter
// policy scoped to the connector's tools, creates an agent and saves its key, sends one real read
// through the Gate, and prints the MCP line. The URL's password is never printed or stored: the
// connection string is read once, like DEEDGLASS_INIT_DSN.

type connectOpts struct {
	name, schema, tables, policy, agent, confirm, dsnHost, urlEnv string
	apply                                                         bool
}

var connectValues connectOpts

func connectFlags(fs *flag.FlagSet) {
	v := &connectValues
	*v = connectOpts{}
	fs.StringVar(&v.name, "name", "", "the connector's name (default: supabase, neon, or the database's name)")
	fs.StringVar(&v.schema, "schema", "public", "the schema the agent reads and writes")
	fs.StringVar(&v.tables, "tables", "", "the tables the agent may write, comma-separated (default: every table in --schema)")
	fs.StringVar(&v.policy, "policy", setup.PolicyName, "the starter to enforce, scoped to this connector: postgres-safe (writes wait for a person) or postgres-check (writes run, checked at Proven)")
	fs.StringVar(&v.agent, "agent", "", "the agent to create (default: <name>-agent)")
	fs.BoolVar(&v.apply, "apply", false, "apply the plan")
	fs.StringVar(&v.confirm, "confirm", "", "the database name, typed exactly: required to apply to a remote database")
	fs.StringVar(&v.dsnHost, "dsn-host", "", "host:port the Deedglass server reaches the database at, when it differs from the URL's")
	fs.StringVar(&v.urlEnv, "url-env", "DATABASE_URL", "the environment variable holding the URL when none is given")
}

// connectTable is one table in --schema as the checks saw it.
type connectTable struct {
	Name         string `json:"name"` // schema.table
	ReplicaFull  bool   `json:"replica_identity_full"`
	Unsupported  string `json:"unsupported,omitempty"` // why it can't be listed (a quoted name)
	ReplicaIdent string `json:"-"`
}

// connectFacts are the read-only checks' results.
type connectFacts struct {
	Version       string         `json:"server_version"`
	VersionNum    int            `json:"server_version_num"`
	WALLevel      string         `json:"wal_level"`
	SlotsMax      int            `json:"max_replication_slots"`
	SlotsUsed     int            `json:"replication_slots_used"`
	WALSenders    int            `json:"max_wal_senders"`
	User          string         `json:"user"`
	Database      string         `json:"database"`
	Superuser     bool           `json:"superuser"`
	CreateRole    bool           `json:"createrole"`
	Replication   bool           `json:"replication"`
	Tables        []connectTable `json:"tables"`
	LackingFull   []string       `json:"tables_without_replica_identity_full"`
	Provider      string         `json:"provider"`
	ProviderLabel string         `json:"provider_label"`
	Pooler        string         `json:"pooler,omitempty"`
}

// connectPlanJSON is the plan as --json prints it.
type connectPlanJSON struct {
	Object      string         `json:"object"` // "connect_plan" or "connect_result"
	Target      string         `json:"target"` // user@host:port/db, never the password
	Server      string         `json:"server"`
	Connector   string         `json:"connector"`
	Agent       string         `json:"agent"`
	Policy      string         `json:"policy"`
	Checks      connectFacts   `json:"checks"`
	Witness     bool           `json:"proven"`
	Changes     []string       `json:"changes"`
	Notes       []string       `json:"notes"`
	Blockers    []string       `json:"blockers"`
	Applied     bool           `json:"applied"`
	KeyFile     string         `json:"key_file,omitempty"`
	Read        map[string]any `json:"read,omitempty"`
	MCP         string         `json:"mcp,omitempty"`
	NextCommand string         `json:"next,omitempty"`
}

var reNameFix = regexp.MustCompile(`[^a-z0-9-]+`)

// connectName is the default connector name: the provider for Supabase and Neon, else the
// database's name, made into a connector name (never postgres, the quickstart's).
func connectName(prov pgsetup.Provider, db string) string {
	switch prov {
	case pgsetup.ProviderSupabase:
		return "supabase"
	case pgsetup.ProviderNeon:
		return "neon"
	}
	n := strings.Trim(reNameFix.ReplaceAllString(strings.ToLower(db), "-"), "-")
	if len(n) > 32 {
		n = strings.Trim(n[:32], "-")
	}
	if n == "" || n == setup.ConnectorName || !connector.ReConnector.MatchString(n) || connector.Reserved(n, true) {
		return "db"
	}
	return n
}

// poolerNote says when the URL goes through a connection pooler, and the direct host to use
// instead when it can be told from the URL. "" when the URL looks direct.
func poolerNote(d *postgres.DSN, prov pgsetup.Provider) (note, direct string) {
	h := strings.ToLower(d.Host)
	switch {
	case strings.HasSuffix(h, ".pooler.supabase.com"):
		// Supavisor: user postgres.<ref> on aws-0-<region>.pooler.supabase.com; direct is db.<ref>.supabase.co:5432 as postgres.
		if _, ref, ok := strings.Cut(d.User, "."); ok && ref != "" {
			direct = "postgres@db." + ref + ".supabase.co:5432/" + d.DB
		}
		return "This URL goes through Supabase's pooler (Supavisor). Proven checks read the change log over a direct connection, and setup creates roles, so use the direct connection string (Project Settings › Database › Connection string › Direct connection).", direct
	case prov == pgsetup.ProviderNeon && strings.Contains(h, "-pooler."):
		direct = d.User + "@" + strings.Replace(d.Host, "-pooler.", ".", 1) + ":" + strconv.Itoa(int(d.Port)) + "/" + d.DB
		return "This URL goes through Neon's pooler (PgBouncer). Logical replication needs the direct host: the same host without -pooler.", direct
	case d.Pooler:
		return "This URL goes through a transaction pooler (port 6543 or pgbouncer=true). Proven checks need a direct connection to the database's own port.", ""
	}
	return "", ""
}

func runConnect(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass connect", connectFlags, args)
	if err != nil {
		return err
	}
	v := connectValues
	var raw string
	switch len(pos) {
	case 0:
		raw = e.Getenv(v.urlEnv)
		if strings.TrimSpace(raw) == "" {
			return usageErr(fmt.Errorf("deedglass connect needs a Postgres URL, or %s set", v.urlEnv), `deedglass connect "$DATABASE_URL"`)
		}
	case 1:
		raw = pos[0]
	default:
		return usageErr(errors.New("deedglass connect takes one Postgres URL"), `deedglass connect "$DATABASE_URL" (quote it: it holds a password)`)
	}
	if !slices.Contains(setup.Policies, v.policy) {
		return usageErr(fmt.Errorf("--policy takes %s (got %q)", strings.Join(setup.Policies, " or "), v.policy), "deedglass connect \"$DATABASE_URL\" --policy postgres-check")
	}
	if !pgsetup.ValidIdent(v.schema) {
		return usageErr(fmt.Errorf("--schema %q isn't a lowercase schema name", v.schema), "For example --schema public.")
	}
	d, err := postgres.ParseDSN(raw, cliEgress())
	raw = ""
	if err != nil {
		return &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: fmt.Errorf("that Postgres URL can't be used: %s", strings.TrimSuffix(vault.Scrub(err.Error()), ".")),
			Next: `Pass a URL like postgres://user:password@host:5432/dbname, quoted: deedglass connect "$DATABASE_URL"`}
	}
	prov := pgsetup.Detect(d.Host, nil)
	pooler, direct := poolerNote(d, prov)
	name := v.name
	if name == "" {
		name = connectName(prov, d.DB)
	}
	if !connector.ReConnector.MatchString(name) || connector.Reserved(name, true) {
		return usageErr(fmt.Errorf("--name %q isn't a connector name", name), "Use 2 to 32 lowercase letters, digits and hyphens, starting with a letter, like orders-db.")
	}
	agentName := firstNonEmpty(v.agent, name+"-agent")
	t := resolveServer(e)
	if t.From == fromDefault {
		// From any project: the stack the last deedglass init set up (~/.deedglass/current.json).
		if c, err := setup.ReadCurrent(e.Getenv("HOME")); err == nil && c != nil {
			if st, err := setup.ReadStack(c.Dir); err == nil && st != nil && sameServer(st.Server(), c.Server) {
				t = target{Server: c.Server, From: fromStack, StackDir: c.Dir, Stack: st}
			}
		}
	}
	if err := t.conflictErr(); err != nil {
		return err
	}
	admin := t.keyFor(e, setup.AdminKeyFile)
	text := !e.Globals.JSON
	s := e.Style
	inv := invocation(e)

	// 1 · read-only checks, on a session that can't write.
	cfg := d.Config.Copy()
	if pooler != "" {
		cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(cctx, cfg)
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "connector_unavailable", Err: fmt.Errorf("Can't connect to %s", d.String()),
			Detail: []string{vault.Scrub(lastPart(err))}, Next: "Check the URL, and that this machine can reach the database."}
	}
	defer conn.Close(context.WithoutCancel(ctx))
	if _, err := conn.Exec(cctx, "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY"); err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "internal_error", Err: errors.New("Can't make the checking session read-only, so nothing was checked"), Detail: []string{vault.Scrub(lastPart(err))}}
	}
	facts, err := connectChecks(cctx, conn, v.schema)
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "internal_error", Err: errors.New("Can't read the database's catalogs"), Detail: []string{vault.Scrub(lastPart(err))},
			Next: "Connect as the database's owner or admin user (postgres on Supabase, the owner role on Neon)."}
	}
	facts.Pooler = pooler

	// 2 · the tables and the connector's config.
	var writable []string
	if v.tables != "" {
		for _, x := range strings.Split(v.tables, ",") {
			x = strings.TrimSpace(x)
			if x == "" {
				continue
			}
			if !strings.Contains(x, ".") {
				x = v.schema + "." + x
			}
			if _, ok := pgsetup.SplitQualified(x, 2); !ok {
				return usageErr(fmt.Errorf("--tables: %q isn't schema.table in lowercase", x), "For example --tables public.orders,public.refunds.")
			}
			writable = append(writable, x)
		}
	} else {
		for _, tb := range facts.Tables {
			if tb.Unsupported == "" {
				writable = append(writable, tb.Name)
			}
		}
	}
	if len(writable) > 256 {
		return usageErr(fmt.Errorf("%s has %d tables; a connector lists at most 256", v.schema, len(writable)), "Pick the ones the agent needs: --tables public.orders,public.refunds.")
	}
	witnessOn := facts.WALLevel == "logical"
	conf := map[string]any{
		"mode":            "sql",
		"allowed_schemas": []string{v.schema},
		"writable_tables": writable,
		"witness":         map[string]any{"enabled": witnessOn, "watched_tables": writable},
	}
	rawConf, _ := json.Marshal(conf)
	pc, errs := postgres.ParseConfig(rawConf)
	if len(errs) > 0 {
		return &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: fmt.Errorf("the connector's config: %s %s", errs[0].Param, strings.TrimSuffix(errs[0].Message, "."))}
	}
	want := pc.Desired(pgsetup.ProviderLocal).Want()
	st, err := pgsetup.Introspect(cctx, conn, want)
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "internal_error", Err: errors.New("Can't read the database's catalogs"), Detail: []string{vault.Scrub(lastPart(err))}}
	}
	prov = pgsetup.Detect(d.Host, st)
	facts.Provider, facts.ProviderLabel = string(prov), prov.Label()
	if v.name == "" {
		name = connectName(prov, d.DB)
		agentName = firstNonEmpty(v.agent, name+"-agent")
	}
	plan := pgsetup.Build(st, pc.Desired(prov))
	out := connectPlanJSON{Object: "connect_plan", Target: d.String(), Server: t.Server, Connector: name, Agent: agentName, Policy: v.policy + "-" + name,
		Checks: facts, Witness: witnessOn, Changes: []string{}, Notes: []string{}, Blockers: append([]string{}, plan.Blockers...)}
	for _, c := range plan.Changes {
		out.Changes = append(out.Changes, c.Op+" "+c.Summary)
	}
	for _, n := range plan.Notes {
		out.Notes = append(out.Notes, n.Text)
	}
	if pooler != "" {
		b := "The URL goes through a pooler; pass the direct connection string to apply."
		if direct != "" {
			b = "The URL goes through a pooler; apply with the direct connection instead (" + direct + ")."
		}
		out.Blockers = append(out.Blockers, b)
	}
	if len(writable) == 0 {
		out.Blockers = append(out.Blockers, "There are no tables in "+v.schema+" to put behind Deedglass. Create one, or pass --schema or --tables.")
	}
	remote := !pgsetup.IsLocalHost(d.Host)

	// 3 · the server: reachable, the admin key works, what is there already.
	srv := connectServerState(ctx, t.Server, admin, name, agentName)
	if text {
		printConnectPlan(e, s, d, facts, out, srv, t, writable, witnessOn, prov)
	}
	if srv.problem != "" {
		out.Blockers = append(out.Blockers, srv.problem)
	}
	next := inv + ` connect "$` + v.urlEnv + `" --apply`
	if len(pos) == 1 {
		next = inv + ` connect "<the same URL>" --apply`
	}
	if v.name != "" {
		next += " --name " + name
	}
	if v.policy != setup.PolicyName {
		next += " --policy " + v.policy
	}
	if v.tables != "" {
		next += " --tables " + strings.Join(writable, ",")
	}
	if remote {
		next += " --confirm " + facts.Database
	}
	out.NextCommand = next
	if len(out.Blockers) > 0 {
		if !text {
			_ = writeJSON(e, out)
		}
		return &ExitError{Code: ExitFailure, ErrCode: "validation_failed", printed: !text, Err: errors.New("The plan can't be applied until the lines marked ✕ are fixed")}
	}
	if !v.apply {
		if !text {
			return writeJSON(e, out)
		}
		fmt.Fprintln(e.Stdout, "Nothing was changed. Next: "+next)
		return nil
	}
	if remote && v.confirm != facts.Database {
		switch {
		case v.confirm != "":
			return &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: fmt.Errorf("--confirm %q isn't this database's name", v.confirm),
				Next: fmt.Sprintf("The URL points at %s on %s. Pass --confirm %s to apply there.", facts.Database, d.Host, facts.Database)}
		case e.IsTTY && text:
			fmt.Fprintf(e.Stdout, "%s is a remote database. Type its name to apply the plan: ", facts.Database)
			line, _ := bufio.NewReader(io.LimitReader(e.stdin(), 256)).ReadString('\n')
			if strings.TrimSpace(line) != facts.Database {
				return &ExitError{Code: ExitFailure, ErrCode: "cancelled", Err: errors.New("Nothing applied: the name didn't match")}
			}
			v.confirm = facts.Database
		default:
			return &ExitError{Code: ExitUsage, ErrCode: "confirmation_required", Err: fmt.Errorf("%s on %s is remote", facts.Database, d.Host),
				Next: fmt.Sprintf("Review the plan, then run again with --apply --confirm %s.", facts.Database)}
		}
	}
	conn.Close(context.WithoutCancel(ctx))

	// 4 · apply, one step at a time, through the same commands a person would run.
	cx := &connectRun{e: e, server: t.Server, admin: admin, dsn: d, dsnRaw: pos, urlEnv: v.urlEnv, text: text}
	base := connectBaseDir(e, t)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "internal_error", Err: fmt.Errorf("can't create %s", base), Detail: []string{lastPart(err)}}
	}
	res, err := cx.apply(ctx, base, name, agentName, v, rawConf, srv, writable)
	if err != nil {
		return err
	}
	out.Object, out.Applied, out.KeyFile, out.Read, out.MCP = "connect_result", true, res.keyFile, res.read, res.mcp
	out.NextCommand = ""
	if !text {
		return writeJSON(e, out)
	}
	return nil
}

// connectChecks are the read-only checks, on a READ ONLY session.
func connectChecks(ctx context.Context, conn *pgx.Conn, schema string) (connectFacts, error) {
	var f connectFacts
	err := conn.QueryRow(ctx, `SELECT pg_catalog.current_setting('server_version'), pg_catalog.current_setting('server_version_num')::int,
	    pg_catalog.current_setting('wal_level'), pg_catalog.current_setting('max_replication_slots')::int,
	    (SELECT count(*) FROM pg_catalog.pg_replication_slots)::int, pg_catalog.current_setting('max_wal_senders')::int,
	    r.rolsuper, r.rolcreaterole, r.rolreplication, current_user::text, pg_catalog.current_database()::text
	  FROM pg_catalog.pg_roles AS r WHERE r.rolname = current_user`).
		Scan(&f.Version, &f.VersionNum, &f.WALLevel, &f.SlotsMax, &f.SlotsUsed, &f.WALSenders, &f.Superuser, &f.CreateRole, &f.Replication, &f.User, &f.Database)
	if err != nil {
		return f, err
	}
	if i := strings.IndexByte(f.Version, ' '); i > 0 {
		f.Version = f.Version[:i]
	}
	rows, err := conn.Query(ctx, `SELECT c.relname::text, c.relreplident::text
	  FROM pg_catalog.pg_class AS c JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
	  WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition AND n.nspname = $1
	  ORDER BY c.relname`, schema)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	for rows.Next() {
		var rel, ident string
		if err := rows.Scan(&rel, &ident); err != nil {
			return f, err
		}
		tb := connectTable{Name: schema + "." + rel, ReplicaFull: ident == "f", ReplicaIdent: ident}
		if !pgsetup.ValidIdent(rel) {
			tb.Unsupported = "its name needs quotes; rename it, or leave it out"
		}
		f.Tables = append(f.Tables, tb)
		if !tb.ReplicaFull && tb.Unsupported == "" {
			f.LackingFull = append(f.LackingFull, tb.Name)
		}
	}
	return f, rows.Err()
}

// connectServer is what the Deedglass server already has.
type connectServer struct {
	reachable, authed bool
	problem           string
	org               string
	connectorExists   bool
	agentExists       bool
	activeEnforce     string
	quickstart        bool // the demo's postgres connector is there
}

func connectServerState(ctx context.Context, server, admin, name, agent string) connectServer {
	var cs connectServer
	if admin == "" {
		cs.problem = "No Deedglass server key found. Run deedglass init first (it starts a local Deedglass and saves its admin key), or set DEEDGLASS_SERVER and DEEDGLASS_API_KEY for your own server."
		return cs
	}
	c := &deedglass.Client{BaseURL: server, APIKey: admin, UserAgent: "deedglass-cli/" + buildInfo().Version}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var who struct {
		Org struct {
			Name string `json:"name"`
		} `json:"org"`
	}
	if _, err := c.Do(ctx, "GET", "/v1/whoami", nil, &who); err != nil {
		var de *deedglass.Error
		if errors.As(err, &de) {
			cs.reachable = true
			cs.problem = "The Deedglass server at " + server + " refused the admin key (" + strings.TrimSuffix(de.Message, ".") + ")."
			return cs
		}
		cs.problem = "Can't reach the Deedglass server at " + server + ". Start it (deedglass init in its directory), or set DEEDGLASS_SERVER."
		return cs
	}
	cs.reachable, cs.authed, cs.org = true, true, who.Org.Name
	var x struct {
		Type string `json:"type"`
	}
	if _, err := c.Do(ctx, "GET", "/v1/connectors/"+name, nil, &x); err == nil {
		cs.connectorExists = true
		if x.Type != postgres.Type {
			cs.problem = "A " + x.Type + " connector named " + name + " already exists. Pass --name with another name."
		}
	}
	if _, err := c.Do(ctx, "GET", "/v1/connectors/"+setup.ConnectorName, nil, &x); err == nil && x.Type == postgres.Type && name != setup.ConnectorName {
		cs.quickstart = true
	}
	var l struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if _, err := c.Do(ctx, "GET", "/v1/agents?limit=100", nil, &l); err == nil {
		for _, a := range l.Data {
			cs.agentExists = cs.agentExists || a.Name == agent
		}
	}
	var active struct {
		Enforce *struct {
			Name string `json:"name"`
		} `json:"enforce"`
	}
	if _, err := c.Do(ctx, "GET", "/v1/policies/active", nil, &active); err == nil && active.Enforce != nil {
		cs.activeEnforce = active.Enforce.Name
	}
	return cs
}

func printConnectPlan(e *Env, s ui.Style, d *postgres.DSN, f connectFacts, out connectPlanJSON, srv connectServer, t target, writable []string, witnessOn bool, prov pgsetup.Provider) {
	w := e.Stdout
	fmt.Fprintf(w, "deedglass connect %s %s %s %s:%d/%s %s PostgreSQL %s\n", ui.Sep, prov.Label(), ui.Sep, d.Host, d.Port, f.Database, ui.Sep, f.Version)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Checks (read-only)")
	line := func(sym, k, v string) { fmt.Fprintf(w, "  %s %s %s\n", sym, ui.Pad(k, 17), v) }
	ok, warn, bad := s.Teal(ui.OK), s.Amber(ui.Unknown), s.Red(ui.Fail)
	if f.VersionNum >= 140000 {
		line(ok, "version", "PostgreSQL "+f.Version)
	} else {
		line(warn, "version", "PostgreSQL "+f.Version+" · Proven checks are tested on 16, 17 and 18")
	}
	if f.WALLevel == "logical" {
		line(ok, "wal_level", "logical · Proven checks can read the change log")
	} else {
		fix, _ := pgsetup.WALLevelFix(prov)
		line(warn, "wal_level", f.WALLevel+" · Proven checks are off until it is logical. "+fix)
	}
	free := f.SlotsMax - f.SlotsUsed
	switch {
	case !witnessOn:
		line(ok, "replication slots", fmt.Sprintf("%d of %d in use", f.SlotsUsed, f.SlotsMax))
	case free >= 1:
		line(ok, "replication slots", fmt.Sprintf("%d of %d in use · Deedglass needs 1", f.SlotsUsed, f.SlotsMax))
	default:
		line(bad, "replication slots", fmt.Sprintf("%d of %d in use · none free for Deedglass; raise max_replication_slots or drop an unused slot", f.SlotsUsed, f.SlotsMax))
	}
	switch {
	case f.Superuser:
		line(ok, "privileges", f.User+" is a superuser")
	case f.CreateRole:
		line(ok, "privileges", f.User+" can create roles")
	default:
		line(bad, "privileges", f.User+" can't create roles · connect as the database's owner or admin user")
	}
	if n := len(f.LackingFull); n > 0 {
		names := f.LackingFull
		more := ""
		if len(names) > 6 {
			names, more = names[:6], fmt.Sprintf(" and %d more", n-6)
		}
		line(warn, "replica identity", fmt.Sprintf("%d of %d tables lack REPLICA IDENTITY FULL: %s%s (the plan sets it)", n, len(f.Tables), strings.Join(names, ", "), more))
	} else if len(f.Tables) > 0 {
		line(ok, "replica identity", fmt.Sprintf("FULL on all %d tables in %s", len(f.Tables), strings.TrimSuffix(strings.SplitN(writableSchema(writable, f), ".", 2)[0], ".")))
	}
	for _, tb := range f.Tables {
		if tb.Unsupported != "" {
			line(warn, "table", tb.Name+" · "+tb.Unsupported)
		}
	}
	if f.Pooler != "" {
		line(bad, "pooler", f.Pooler)
	}
	switch {
	case srv.authed:
		line(ok, "Deedglass", t.Server+" · org "+srv.org+serverFrom(t))
	case srv.problem != "":
		line(bad, "Deedglass", srv.problem)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Plan")
	n := 0
	step := func(text string) {
		n++
		fmt.Fprintf(w, "  %d %s\n", n, text)
	}
	tables := strings.Join(writable, ", ")
	if len(writable) > 6 {
		tables = strings.Join(writable[:6], ", ") + fmt.Sprintf(" and %d more", len(writable)-6)
	}
	proven := "Proven checks on"
	if !witnessOn {
		proven = "Proven checks off (wal_level)"
	}
	if srv.connectorExists {
		step("connector " + out.Connector + " (postgres) · already there")
	} else {
		step(fmt.Sprintf("connector %s (postgres) · %d %s the agent may read and write: %s · %s", out.Connector, len(writable), plural(len(writable), "table", "tables"), tables, proven))
	}
	step(fmt.Sprintf("database setup (connector postgres init) · %d %s:", len(out.Changes), plural(len(out.Changes), "change", "changes")))
	for i, c := range out.Changes {
		if i == 12 {
			fmt.Fprintf(w, "      … %d more (the full SQL is written for review when it applies)\n", len(out.Changes)-12)
			break
		}
		fmt.Fprintln(w, "      "+c)
	}
	for _, nt := range out.Notes {
		fmt.Fprintf(w, "      %s %s\n", warn, nt)
	}
	for _, b := range out.Blockers {
		fmt.Fprintf(w, "      %s %s\n", bad, b)
	}
	step("credentials · new passwords for deedglass_agent and deedglass_witness, sent to Postgres as SCRAM verifiers and stored in the Deedglass vault, never printed")
	pol := fmt.Sprintf("policy %s · %s scoped to %s's tools · enforce", out.Policy, policyBase(out.Policy, out.Connector), out.Connector)
	if srv.quickstart {
		pol += " · keeps the demo connector postgres working"
	}
	if srv.activeEnforce != "" && srv.activeEnforce != out.Policy {
		pol += " · replaces " + srv.activeEnforce
	}
	step(pol)
	if srv.agentExists {
		step("agent " + out.Agent + " · already there; its key file is reused if it exists")
	} else {
		step("agent " + out.Agent + " · a key saved to a file (0600), never printed")
	}
	step("one read through the Gate, then the MCP line for your client")
	fmt.Fprintln(w)
}

func writableSchema(writable []string, f connectFacts) string {
	if len(writable) > 0 {
		return writable[0]
	}
	if len(f.Tables) > 0 {
		return f.Tables[0].Name
	}
	return "public."
}

func serverFrom(t target) string {
	switch t.From {
	case fromStack:
		return " (from .deedglass/stack.json)"
	case fromFlag:
		return " (--server)"
	case fromEnv:
		return " (DEEDGLASS_SERVER)"
	}
	return ""
}

func policyBase(policyName, conn string) string { return strings.TrimSuffix(policyName, "-"+conn) }

// connectBaseDir is where connect keeps what it writes (the connector's config, the scoped policy,
// the agent key, the setup SQL): the init directory's .deedglass/ when a local stack named the
// server, else ~/.deedglass/.
func connectBaseDir(e *Env, t target) string {
	if t.From == fromStack && t.StackDir != "" {
		return filepath.Join(t.StackDir, setup.DirName)
	}
	if home := e.Getenv("HOME"); home != "" {
		return filepath.Join(home, setup.DirName)
	}
	return filepath.Join(workDir(e), setup.DirName)
}

// scopedPolicy is a starter rewritten for one connector (connectors name their tools
// <connector>.query, …): every rule that names a postgres.* tool names this connector's instead
// (and the demo connector's too, when keepDemo), under the name <starter>-<connector>.
func scopedPolicy(starter, conn string, keepDemo bool) ([]byte, string, error) {
	src, err := starters.Source(starter)
	if err != nil {
		return nil, "", err
	}
	name := starter + "-" + conn
	reTool := regexp.MustCompile(`tool: postgres\.([a-z_]+)`)
	repl := "tool: " + conn + ".$1"
	if keepDemo {
		repl = "tool: [" + setup.ConnectorName + ".$1, " + conn + ".$1]"
	}
	out := reTool.ReplaceAll(src, []byte(repl))
	out = regexp.MustCompile(`(?m)^name: `+regexp.QuoteMeta(starter)+`$`).ReplaceAll(out, []byte("name: "+name))
	head := "# " + name + " · written by deedglass connect from the " + starter + " starter, scoped to the connector " + conn + ".\n" +
		"# Edit it, then check it: deedglass policy lint " + name + ".yaml\n"
	return append([]byte(head), out...), name, nil
}

// connectRun applies a plan through the same commands a person would run, in this process.
type connectRun struct {
	e      *Env
	server string
	admin  string
	dsn    *postgres.DSN
	dsnRaw []string
	urlEnv string
	text   bool
}

type connectResult struct {
	keyFile string
	read    map[string]any
	mcp     string
}

// subDispatch is dispatch, set in init: the command table refers to runConnect, which runs commands.
var subDispatch func(context.Context, *Env, []string) error

func init() { subDispatch = dispatch }

// sub runs one deedglass command in this process with --json, against the resolved server and
// admin key, with env added to the environment. It returns the command's stdout.
func (cx *connectRun) sub(ctx context.Context, env map[string]string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	se := *cx.e
	se.Stdout, se.Stderr, se.IsTTY = &out, &errb, false
	se.Stdin = strings.NewReader("")
	se.Globals = Globals{JSON: true, NoColor: true, Server: cx.server, APIKey: cx.admin, Org: cx.e.Globals.Org}
	se.Style = ui.Style{}
	se.Mark = mark.Options{Quiet: true}
	parent := cx.e.Getenv
	se.Getenv = func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		if k == "DEEDGLASS_API_KEY" || k == "DEEDGLASS_SERVER" || k == "DEEDGLASS_INIT_DSN" {
			return ""
		}
		return parent(k)
	}
	err := subDispatch(ctx, &se, args)
	if err == nil {
		return out.String(), nil
	}
	report(&se, err)
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(errb.String())
	if json.Unmarshal(errb.Bytes(), &body) == nil && body.Error.Message != "" {
		msg = body.Error.Message
	}
	code := ExitFailure
	var x *ExitError
	if errors.As(err, &x) {
		code = x.Code
		if msg == "" {
			msg = x.Err.Error()
		}
	}
	return out.String(), &ExitError{Code: code, ErrCode: firstNonEmpty(body.Error.Code, "connect_failed"), Err: errors.New(strings.TrimSuffix(vault.Scrub(msg), "."))}
}

func (cx *connectRun) ok(msg string) {
	if cx.text {
		fmt.Fprintln(cx.e.Stdout, cx.e.Style.OKLine(msg))
	}
}

func (cx *connectRun) fail(step string, err error) error {
	var x *ExitError
	if errors.As(err, &x) {
		x.Err = fmt.Errorf("%s: %s", step, strings.TrimSuffix(x.Err.Error(), "."))
		if x.Next == "" {
			x.Next = "Fix it, then run the same deedglass connect --apply again: every step that already ran is kept and skipped."
		}
		return x
	}
	return &ExitError{Code: ExitFailure, ErrCode: "connect_failed", Err: fmt.Errorf("%s: %w", step, err),
		Next: "Fix it, then run the same deedglass connect --apply again: every step that already ran is kept and skipped."}
}

func (cx *connectRun) apply(ctx context.Context, base, name, agentName string, v connectOpts, rawConf []byte, srv connectServer, writable []string) (*connectResult, error) {
	res := &connectResult{}
	e := cx.e
	// a · the connector
	if !srv.connectorExists {
		var cf map[string]any
		_ = json.Unmarshal(rawConf, &cf)
		doc := map[string]any{"type": postgres.Type, "name": name, "description": "Added by deedglass connect (" + cx.dsn.Host + "/" + cx.dsn.DB + ").", "config": cf}
		yb, _ := yaml.Marshal(doc)
		p := filepath.Join(base, "connector-"+name+".yaml")
		if err := os.WriteFile(p, yb, 0o600); err != nil {
			return nil, cx.fail("the connector's config", err)
		}
		if _, err := cx.sub(ctx, nil, "connector", "create", "--config", p); err != nil {
			return nil, cx.fail("creating the connector", err)
		}
		cx.ok(fmt.Sprintf("Connector created %s %s %s %d %s", ui.Sep, name, ui.Sep, len(writable), plural(len(writable), "table", "tables")))
	} else {
		cx.ok("Connector " + ui.Sep + " " + name + " " + ui.Sep + " already there")
	}
	// b · the database: roles, publication, slot, replica identity; the DSNs to the vault.
	args := []string{"connector", "postgres", "init", "--connector", name, "--apply", "--plan-dir", filepath.Join(base, "plans")}
	if v.confirm != "" {
		args = append(args, "--confirm", v.confirm)
	}
	if v.dsnHost != "" {
		args = append(args, "--dsn-host", v.dsnHost)
	}
	dsn := cx.dsnString()
	out, err := cx.sub(ctx, map[string]string{"DEEDGLASS_INIT_DSN": dsn}, args...)
	dsn = ""
	if err != nil {
		return nil, cx.fail("setting up the database", err)
	}
	var ps pgSetupJSON
	_ = json.Unmarshal([]byte(out), &ps)
	detail := "nothing to change"
	if ps.Applied {
		detail = fmt.Sprintf("%d %s applied in one transaction", len(ps.Changes), plural(len(ps.Changes), "change", "changes"))
	}
	switch ps.Credentials {
	case "stored":
		detail += " " + ui.Sep + " credentials stored in the vault"
	case "file":
		detail += " " + ui.Sep + " credentials written to " + ps.CredentialsAt + " (the server didn't take them)"
	case "unchanged":
		detail += " " + ui.Sep + " credentials unchanged"
	}
	if ps.PlanFile != "" {
		detail += " " + ui.Sep + " SQL in " + ps.PlanFile
	}
	cx.ok("Database set up " + ui.Sep + " " + detail)
	_, _ = cx.sub(ctx, nil, "connector", "test", name)

	// c · the policy, scoped to this connector's tools, enforced.
	src, pname, err := scopedPolicy(v.policy, name, srv.quickstart)
	if err != nil {
		return nil, cx.fail("the policy", err)
	}
	pfile := filepath.Join(base, pname+".yaml")
	if err := os.WriteFile(pfile, src, 0o600); err != nil {
		return nil, cx.fail("the policy", err)
	}
	pout, err := cx.sub(ctx, nil, "policy", "push", pfile)
	if err != nil {
		return nil, cx.fail("pushing the policy", err)
	}
	var pushed struct {
		Created bool `json:"created"`
		Version struct {
			Version int `json:"version"`
		} `json:"version"`
	}
	_ = json.Unmarshal([]byte(pout), &pushed)
	if pushed.Version.Version == 0 {
		return nil, cx.fail("pushing the policy", errors.New("the server didn't say which version it saved"))
	}
	if srv.activeEnforce != pname || pushed.Created {
		if _, err := cx.sub(ctx, nil, "policy", "activate", pname, "--version", strconv.Itoa(pushed.Version.Version), "--mode", "enforce",
			"--reason", "deedglass connect: the starter policy for "+name, "--yes"); err != nil {
			return nil, cx.fail("enforcing the policy", err)
		}
	}
	cx.ok(fmt.Sprintf("Policy %s %s v%d %s enforce %s %s", ui.Sep, pname, pushed.Version.Version, ui.Sep, ui.Sep, pfile))

	// d · the agent and its key.
	if !srv.agentExists {
		if _, err := cx.sub(ctx, nil, "agent", "create", agentName, "--description", "Added by deedglass connect for the connector "+name+"."); err != nil {
			return nil, cx.fail("creating the agent", err)
		}
	}
	keyFile := filepath.Join(base, "agent-"+agentName+".key")
	key := setup.ReadKeyFile(keyFile)
	if key == "" {
		kout, err := cx.sub(ctx, nil, "agent", "keys", "create", agentName, "--name", "connect")
		if err != nil {
			return nil, cx.fail("creating the agent's key", err)
		}
		var k struct {
			Secret string `json:"secret"`
		}
		if json.Unmarshal([]byte(kout), &k) != nil || k.Secret == "" {
			return nil, cx.fail("creating the agent's key", errors.New("the server didn't return a key"))
		}
		if err := writeKeyFile(keyFile, k.Secret); err != nil {
			return nil, cx.fail("saving the agent's key", err)
		}
		key = k.Secret
		cx.ok("Agent " + ui.Sep + " " + agentName + " " + ui.Sep + " key saved to " + keyFile + " (0600)")
	} else {
		cx.ok("Agent " + ui.Sep + " " + agentName + " " + ui.Sep + " key in " + keyFile)
	}
	res.keyFile = keyFile

	// e · one real read through the Gate, as the agent.
	c := &deedglass.Client{BaseURL: cx.server, APIKey: key, UserAgent: "deedglass-connect/" + buildInfo().Version}
	wait := 10 * time.Second
	sql := "SELECT count(*) AS rows FROM " + writable[0]
	argsJSON, _ := json.Marshal(map[string]string{"sql": sql})
	a, _, err := c.Act(ctx, deedglass.ActRequest{Tool: name + ".query", Args: argsJSON, Wait: &wait, Client: "deedglass-connect", Version: buildInfo().Version})
	key = ""
	if err != nil {
		var de *deedglass.Error
		msg := err.Error()
		if errors.As(err, &de) {
			msg = strings.TrimSuffix(de.Message, ".")
		}
		return nil, cx.fail("the read through the Gate ("+sql+")", errors.New(vault.Scrub(msg)))
	}
	check := "no check yet"
	if a.Witness != nil && a.Witness.Verdict != nil {
		check = witness.PlainLevel(a.Witness.Level) + " " + ui.Sep + " " + witness.PlainVerdict(*a.Witness.Verdict)
	}
	res.read = map[string]any{"action": a.ID, "tool": name + ".query", "sql": sql, "status": a.Status, "check": check}
	if a.Status != "executed" {
		return nil, cx.fail("the read through the Gate ("+sql+")", fmt.Errorf("the action ended %s", a.Status))
	}
	deed := ""
	if a.Deed != nil {
		deed = " " + ui.Sep + " " + shortID(*a.Deed)
	}
	cx.ok(fmt.Sprintf("Read through the Gate %s %s.query %s %s %s %s%s", ui.Sep, name, ui.Sep, sql, ui.Sep, check, deed))

	// f · the MCP line: the key stays in its file.
	res.mcp = "claude mcp add --scope user deedglass-" + name + " -e DEEDGLASS_SERVER=" + cx.server + " -e DEEDGLASS_API_KEY_FILE=" + keyFile + " -- npx -y deedglass mcp serve"
	if cx.text {
		fmt.Fprintln(e.Stdout)
		fmt.Fprintln(e.Stdout, "Next")
		fmt.Fprintln(e.Stdout, "  "+res.mcp)
		fmt.Fprintln(e.Stdout, "  "+invocation(e)+" actions list --limit 5")
		if v.policy == setup.PolicyName {
			fmt.Fprintln(e.Stdout, "  Writes wait for a person under "+v.policy+"-"+name+": "+invocation(e)+" approvals list shows them, and approvals open <apr_…> prints the link.")
		}
	}
	return res, nil
}

// dsnString is the URL again for connector postgres init (read once from the argument or the
// environment variable, never stored).
func (cx *connectRun) dsnString() string {
	if len(cx.dsnRaw) == 1 {
		return cx.dsnRaw[0]
	}
	return cx.e.Getenv(cx.urlEnv)
}

// writeKeyFile writes a key 0600, refusing a symlink or an existing file.
func writeKeyFile(p, key string) error {
	if fi, err := os.Lstat(p); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink", p)
		}
		return fmt.Errorf("%s already exists", p)
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(key + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
