package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/migrate"
	"github.com/deedglass/deedglass/internal/store/migrations"
)

// --- flags ---

type migrateOpts struct {
	databaseURL string
	dryRun      bool
}

var migrateFlagValues migrateOpts

func migrateFlags(withDryRun bool) func(*flag.FlagSet) {
	return func(fs *flag.FlagSet) {
		migrateFlagValues = migrateOpts{}
		fs.StringVar(&migrateFlagValues.databaseURL, "database-url", "", "the migrator role's Postgres URL (default DEEDGLASS_MIGRATE_DATABASE_URL)")
		if withDryRun {
			fs.BoolVar(&migrateFlagValues.dryRun, "dry-run", false, "show what would be applied, and apply nothing")
		}
	}
}

// --- the database seam (tests replace openMigrator) ---

// Target is where migrate connects, for messages. It never holds the password.
type Target struct {
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
}

func (t Target) addr() string { return net.JoinHostPort(t.Host, fmt.Sprint(t.Port)) }

type migrator interface {
	Target() Target
	Status(ctx context.Context) ([]migrate.Row, error)
	Up(ctx context.Context, o migrate.UpOptions) (migrate.Result, error)
	Verify(ctx context.Context) error
	Close()
}

var openMigrator = func(ctx context.Context, url config.Secret) (migrator, error) {
	ms, err := migrate.Load(migrations.FS)
	if err != nil {
		return nil, err
	}
	cc, err := pgx.ParseConfig(url.Reveal())
	if err != nil {
		return nil, &connectError{reason: "the URL can't be parsed; use the form postgres://user:password@host:5432/db"}
	}
	if cc.ConnectTimeout == 0 {
		cc.ConnectTimeout = 10 * time.Second
	}
	cc.RuntimeParams["application_name"] = "deedglass migrate"
	t := Target{Host: cc.Host, Port: cc.Port, Database: cc.Database, User: cc.User}
	c, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		return nil, &connectError{target: t, reason: connectReason(err, cc.Password)}
	}
	return &pgMigrator{c: c, ms: ms, t: t}, nil
}

type pgMigrator struct {
	c  *pgx.Conn
	ms []migrate.Migration
	t  Target
}

func (m *pgMigrator) Target() Target { return m.t }
func (m *pgMigrator) Status(ctx context.Context) ([]migrate.Row, error) {
	return migrate.Status(ctx, m.c, m.ms)
}
func (m *pgMigrator) Up(ctx context.Context, o migrate.UpOptions) (migrate.Result, error) {
	return migrate.Up(ctx, m.c, m.ms, o)
}
func (m *pgMigrator) Verify(ctx context.Context) error { return migrate.Verify(ctx, m.c, m.ms) }
func (m *pgMigrator) Close()                           { _ = m.c.Close(context.Background()) }

type connectError struct {
	target Target
	reason string
}

func (e *connectError) Error() string {
	if e.target.Host == "" {
		return "Can't connect: " + e.reason
	}
	return fmt.Sprintf("Can't connect to %s as %s: %s", e.target.addr(), e.target.User, e.reason)
}

// connectReason keeps the last, most specific part of a connection error and never the password.
func connectReason(err error, password string) string {
	s := err.Error()
	if password != "" {
		s = strings.ReplaceAll(s, password, "****")
	}
	if i := strings.LastIndex(s, ": "); i >= 0 && i+2 < len(s) {
		s = s[i+2:]
	}
	return strings.TrimSuffix(s, ".")
}

// --- commands ---

func runMigrateUp(ctx context.Context, e *Env, args []string) error {
	m, cfg, err := openForMigrate(ctx, e, args, "up")
	if err != nil {
		return err
	}
	defer m.Close()
	t := m.Target()
	s := e.Style
	o := migrate.UpOptions{Env: cfg.Env, DryRun: migrateFlagValues.dryRun, AppVersion: buildInfo().Version}
	var nameW int
	if !e.Globals.JSON {
		o.OnStart = func(p []migrate.Migration) {
			for _, x := range p {
				nameW = max(nameW, ui.Width(x.Name))
			}
			if len(p) > 0 {
				fmt.Fprintln(e.Stdout, s.Violet(ui.Pending)+" "+fmt.Sprintf("Migrating %s on %s as %s %s %d pending", t.Database, t.addr(), t.User, ui.Sep, len(p)))
			}
		}
		o.OnApply = func(a migrate.Applied) {
			fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("%04d %s  %5s", a.Version, ui.Pad(a.Name, nameW), ms(a.Duration))))
		}
	}
	res, err := m.Up(ctx, o)
	if err != nil {
		return migrateErr(err, t)
	}
	if e.Globals.JSON {
		return writeJSON(e, upJSON(res, t, o.DryRun))
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(e.Stderr, s.UnknownLine(w))
	}
	switch {
	case o.DryRun && len(res.Pending) == 0:
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Schema is current at %04d %s dry run, nothing to apply", res.Current, ui.Sep)))
	case o.DryRun:
		fmt.Fprintln(e.Stdout, s.Violet(ui.Pending)+" "+fmt.Sprintf("Dry run on %s as %s %s schema at %04d %s %d pending", t.addr(), t.User, ui.Sep, res.Current, ui.Sep, len(res.Pending)))
		for _, x := range res.Pending {
			nameW = max(nameW, ui.Width(x.Name))
		}
		for _, x := range res.Pending {
			fmt.Fprintf(e.Stdout, "  %04d %s  %s\n", x.Version, ui.Pad(x.Name, nameW), s.Dim(shortSum(x.Checksum)))
		}
		fmt.Fprintln(e.Stdout, s.UnknownLine("Nothing was applied. Run deedglass migrate up to apply "+them(len(res.Pending))+"."))
	case len(res.Applied) == 0:
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Schema is current at %04d", res.Current)))
	default:
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Schema at %04d %s %d applied in %.2f s", res.Current, ui.Sep, len(res.Applied), res.Duration.Seconds())))
	}
	return nil
}

func runMigrateStatus(ctx context.Context, e *Env, args []string) error {
	m, _, err := openForMigrate(ctx, e, args, "status")
	if err != nil {
		return err
	}
	defer m.Close()
	rows, err := m.Status(ctx)
	if err != nil {
		return migrateErr(err, m.Target())
	}
	var applied, pending, bad int
	for _, r := range rows {
		switch r.State {
		case migrate.StateApplied:
			applied++
		case migrate.StatePending:
			pending++
		default:
			bad++
		}
	}
	if e.Globals.JSON {
		data := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			data = append(data, rowJSON(r))
		}
		if err := writeJSON(e, map[string]any{"object": "list", "data": data, "has_more": false, "next_cursor": nil}); err != nil {
			return err
		}
		if bad > 0 {
			return &ExitError{Code: ExitFailure, Err: errors.New("migration problems"), printed: true}
		}
		return nil
	}
	s := e.Style
	head := []string{"VERSION", "NAME", "STATE", "APPLIED", "CHECKSUM"}
	table := [][]string{head}
	for _, r := range rows {
		when, sum := ui.None, shortSum(r.Checksum)
		if !r.AppliedAt.IsZero() {
			when = r.AppliedAt.UTC().Format("2006-01-02 15:04") + " UTC"
		}
		if r.State == migrate.StateUnknown {
			sum = shortSum(r.AppliedChecksum)
		}
		table = append(table, []string{fmt.Sprintf("%04d", r.Version), r.Name, r.State, when, sum})
	}
	w := make([]int, len(head))
	for _, row := range table {
		for i, c := range row {
			w[i] = max(w[i], ui.Width(c))
		}
	}
	for i, row := range table {
		line := make([]string, len(row))
		for j, c := range row {
			cell := c
			if j < len(row)-1 {
				cell = ui.Pad(c, w[j])
			}
			if i > 0 && j == 2 {
				switch c {
				case migrate.StateChanged, migrate.StateUnknown:
					cell = s.Red(cell)
				case migrate.StatePending:
					cell = s.Violet(cell)
				}
			}
			line[j] = cell
		}
		fmt.Fprintln(e.Stdout, strings.Join(line, "  "))
	}
	switch {
	case bad > 0:
		fmt.Fprintln(e.Stdout, s.FailLine(fmt.Sprintf("%d applied %s %d pending %s %d %s this build. Run deedglass migrate verify for details.",
			applied, ui.Sep, pending, ui.Sep, bad, plural(bad, "doesn't match", "don't match"))))
		return &ExitError{Code: ExitFailure, Err: errors.New("migration problems"), printed: true}
	case pending == 0:
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("%d applied %s schema is current.", applied, ui.Sep)))
	default:
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("%d applied %s %d pending. Run deedglass migrate up to apply %s.", applied, ui.Sep, pending, them(pending))))
	}
	return nil
}

func runMigrateVerify(ctx context.Context, e *Env, args []string) error {
	m, _, err := openForMigrate(ctx, e, args, "verify")
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Verify(ctx); err != nil {
		return migrateErr(err, m.Target())
	}
	rows, err := m.Status(ctx)
	if err != nil {
		return migrateErr(err, m.Target())
	}
	applied := 0
	for _, r := range rows {
		if r.State == migrate.StateApplied {
			applied++
		}
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "migration_check", "ok": true, "applied": applied, "pending": len(rows) - applied})
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("%d applied %s match this build %s %d pending",
		applied, plural(applied, "migration", "migrations"), ui.Sep, len(rows)-applied)))
	return nil
}

// openForMigrate loads the config, picks the URL and connects.
func openForMigrate(ctx context.Context, e *Env, args []string, sub string) (migrator, *config.Config, error) {
	if len(args) > 0 {
		return nil, nil, usageErr(fmt.Errorf("deedglass migrate %s takes no arguments (got %q)", sub, args[0]), "Run deedglass help migrate "+sub+".")
	}
	cfg, warnings, err := config.Load(cliLoadOptions(e))
	var ps *config.Problems
	if errors.As(err, &ps) {
		return nil, nil, configProblems(e, ps, warnings)
	}
	if err != nil {
		return nil, nil, err
	}
	var flagURL config.Secret
	if migrateFlagValues.databaseURL != "" {
		flagURL = config.NewSecret(migrateFlagValues.databaseURL)
	}
	url, fallback, err := cfg.MigrateURL(flagURL)
	if errors.As(err, &ps) {
		return nil, nil, configProblems(e, ps, warnings)
	}
	if !e.Globals.JSON {
		printWarnings(e, warnings)
		if fallback {
			fmt.Fprintln(e.Stderr, e.Style.UnknownLine("Using DEEDGLASS_DATABASE_URL for migrations (dev only)."))
		}
	}
	m, err := openMigrator(ctx, url)
	if err != nil {
		var ce *connectError
		if errors.As(err, &ce) {
			return nil, nil, &ExitError{Code: ExitUsage, ErrCode: "database_unreachable", Err: ce,
				Next: "Check that Postgres is running and that the URL, user and password are right."}
		}
		return nil, nil, err
	}
	return m, cfg, nil
}

// migrateErr is the message catalog for migrate: what happened, then the next step.
func migrateErr(err error, t Target) error {
	var (
		ce *migrate.ChecksumError
		ae *migrate.AheadError
		ne *migrate.NotControlPlaneError
		se *migrate.ServerTooOldError
	)
	switch {
	case errors.As(err, &ce):
		return &ExitError{Code: ExitFailure, ErrCode: "migration_changed",
			Err:    fmt.Errorf("%04d %s changed after it was applied", ce.Version, ce.Name),
			Detail: []string{"applied  " + shortSum(ce.Applied), "binary   " + shortSum(ce.File)},
			Next:   "An applied migration must never change. Restore the original file, and put the change in a new migration.",
			Extra:  map[string]any{"version": ce.Version, "applied_checksum": ce.Applied, "binary_checksum": ce.File}}
	case errors.As(err, &ae):
		return &ExitError{Code: ExitFailure, ErrCode: "database_ahead",
			Err:  fmt.Errorf("The database is at migration %04d, and this build knows up to %04d", ae.Version, ae.Known),
			Next: "Run a deedglass build that has these migrations, or check that the URL points at the right database."}
	case errors.As(err, &ne):
		return &ExitError{Code: ExitFailure, ErrCode: "not_control_plane",
			Err:  fmt.Errorf("This database has %s, so it's a watched customer database", ne.Found),
			Next: "Deedglass keeps its own state in a separate database. Point DEEDGLASS_MIGRATE_DATABASE_URL at that one."}
	case errors.Is(err, migrate.ErrMissingRole):
		return &ExitError{Code: ExitFailure, ErrCode: "role_missing",
			Err:  errors.New("Role deedglass_app doesn't exist yet"),
			Next: "Create the roles first: psql -f deploy/postgres/control-plane-roles.sql (see docs/DATABASE_SETUP.md)."}
	case errors.As(err, &se):
		return &ExitError{Code: ExitFailure, ErrCode: "server_too_old", Err: errors.New(strings.TrimSuffix(se.Error(), ".")),
			Next: "Upgrade Postgres, or point at a PostgreSQL 16, 17 or 18 server."}
	case errors.Is(err, migrate.ErrLockTimeout):
		return &ExitError{Code: ExitFailure, ErrCode: "migrate_locked",
			Err:  errors.New("Another deedglass is migrating this database"),
			Next: "Wait for it to finish, or look for the lock holder in pg_locks."}
	case store.SQLState(err) == store.CodeInsufficientPriv:
		return &ExitError{Code: ExitFailure, ErrCode: "migrate_permission_denied",
			Err:  fmt.Errorf("Role %s can't change the schema in database %s", t.User, t.Database),
			Next: "Run migrations as deedglass_migrator: set DEEDGLASS_MIGRATE_DATABASE_URL, or pass --database-url."}
	case errors.Is(err, migrate.ErrSuperuser):
		return &ExitError{Code: ExitFailure, ErrCode: "superuser_refused",
			Err:  fmt.Errorf("Prod refuses to migrate as a superuser (%s)", t.User),
			Next: "Run migrations as deedglass_migrator."}
	}
	msg := err.Error()
	if msg != "" {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
	}
	return &ExitError{Code: ExitFailure, ErrCode: "migrate_failed", Err: errors.New(msg),
		Next: "Nothing after the failed migration was applied. Fix the cause and run deedglass migrate up again."}
}

// --- output helpers ---

func ms(d time.Duration) string { return fmt.Sprintf("%d ms", d.Milliseconds()) }

// shortSum shows a checksum as sha256:3f9a…c21e (brand §12).
func shortSum(s string) string {
	h, ok := strings.CutPrefix(s, "sha256:")
	if !ok || len(h) < 8 {
		return s
	}
	return "sha256:" + h[:4] + "…" + h[len(h)-4:]
}

func them(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func writeJSON(e *Env, v any) error {
	enc := json.NewEncoder(e.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func migrationJSON(m migrate.Migration) map[string]any {
	return map[string]any{"object": "migration", "version": m.Version, "name": m.Name, "checksum": m.Checksum}
}

func upJSON(res migrate.Result, t Target, dry bool) map[string]any {
	applied := make([]map[string]any, 0, len(res.Applied))
	for _, a := range res.Applied {
		j := migrationJSON(a.Migration)
		j["duration_ms"] = a.Duration.Milliseconds()
		applied = append(applied, j)
	}
	pending := make([]map[string]any, 0, len(res.Pending))
	for _, p := range res.Pending {
		pending = append(pending, migrationJSON(p))
	}
	warnings := res.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return map[string]any{"object": "migration_run", "dry_run": dry, "target": t, "current": res.Current,
		"applied": applied, "pending": pending, "warnings": warnings, "duration_ms": res.Duration.Milliseconds()}
}

func rowJSON(r migrate.Row) map[string]any {
	j := map[string]any{"object": "migration", "version": r.Version, "name": r.Name, "state": r.State,
		"checksum": nilIfEmpty(r.Checksum), "applied_checksum": nilIfEmpty(r.AppliedChecksum), "applied_at": nil}
	if !r.AppliedAt.IsZero() {
		j["applied_at"] = r.AppliedAt.UTC().Format(time.RFC3339)
	}
	return j
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
