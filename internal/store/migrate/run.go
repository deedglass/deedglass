package migrate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/config"
)

// lockKey is the session advisory lock Up holds: ASCII "deedmig1".
const lockKey int64 = 0x646565646d696731

// appRole is the runtime role that must exist before the first migration (its policies and grants
// name it). A variable only so an integration test can point preflight at a missing role.
var appRole = "deedglass_app"

// Sentinel errors. Each typed error below wraps one, so callers use errors.Is.
var (
	ErrChecksumMismatch = errors.New("migrate: an applied migration changed")
	ErrDatabaseAhead    = errors.New("migrate: the database has migrations this build doesn't know")
	ErrLockTimeout      = errors.New("migrate: another migration holds the lock")
	ErrMissingRole      = errors.New("migrate: role deedglass_app doesn't exist")
	ErrNotControlPlane  = errors.New("migrate: this is a watched customer database")
	ErrServerTooOld     = errors.New("migrate: PostgreSQL 16 or newer is required")
	ErrSuperuser        = errors.New("migrate: prod refuses to migrate as a superuser")
)

// ChecksumError names the migration whose file no longer matches what was applied.
type ChecksumError struct {
	Version       int
	Name          string
	Applied, File string
}

func (e *ChecksumError) Error() string {
	return fmt.Sprintf("migration %04d %s changed after it was applied (applied %s, binary %s)", e.Version, e.Name, e.Applied, e.File)
}
func (e *ChecksumError) Unwrap() error { return ErrChecksumMismatch }

// AheadError names the newest applied version and the newest this build carries.
type AheadError struct{ Version, Known int }

func (e *AheadError) Error() string {
	return fmt.Sprintf("the database is at migration %04d, and this build knows up to %04d", e.Version, e.Known)
}
func (e *AheadError) Unwrap() error { return ErrDatabaseAhead }

// ServerTooOldError carries the server's version string ("15.8").
type ServerTooOldError struct{ Version string }

func (e *ServerTooOldError) Error() string {
	return "Deedglass needs PostgreSQL 16 or newer. This server is " + e.Version + "."
}
func (e *ServerTooOldError) Unwrap() error { return ErrServerTooOld }

// NotControlPlaneError says what marked the database as a customer database.
type NotControlPlaneError struct{ Found string } // "deedglass.ledger" or "the deedglass_pub publication"

func (e *NotControlPlaneError) Error() string {
	return "this database has " + e.Found + ", so it's a watched customer database"
}
func (e *NotControlPlaneError) Unwrap() error { return ErrNotControlPlane }

// Row states.
const (
	StateApplied = "applied"
	StatePending = "pending"
	StateChanged = "changed" // applied, and the file's checksum differs
	StateUnknown = "unknown" // applied, and this build has no such file
)

// Row is one line of migrate status.
type Row struct {
	Version         int
	Name            string
	State           string
	AppliedAt       time.Time // zero when pending
	Checksum        string    // the file's checksum; "" for unknown
	AppliedChecksum string    // "" when pending
}

// UpOptions controls Up.
type UpOptions struct {
	DryRun      bool
	Env         config.Env
	LockTimeout time.Duration // default 60 s
	AppVersion  string        // recorded in schema_migrations.app_version
	// OnStart is called once the lock is held, with the migrations about to run.
	OnStart func(pending []Migration)
	// OnApply is called after each migration commits.
	OnApply func(Applied)
}

// Applied is one migration Up ran.
type Applied struct {
	Migration
	Duration time.Duration
}

// Result is what Up did.
type Result struct {
	Applied  []Applied
	Pending  []Migration // what a dry run would apply
	Current  int         // the schema version after Up (for a dry run, the version before)
	Warnings []string
	Duration time.Duration
}

// Status compares the applied migrations with ms. It reads only; a database without
// deedglass.schema_migrations has everything pending.
func Status(ctx context.Context, c *pgx.Conn, ms []Migration) ([]Row, error) {
	applied, err := readApplied(ctx, c)
	if err != nil {
		return nil, err
	}
	return compare(ms, applied), nil
}

// Verify checks every applied migration against ms. Pending migrations are not an error.
func Verify(ctx context.Context, c *pgx.Conn, ms []Migration) error {
	rows, err := Status(ctx, c, ms)
	if err != nil {
		return err
	}
	return problem(rows, ms)
}

// Up applies every pending migration, each in its own transaction, under the advisory lock.
func Up(ctx context.Context, c *pgx.Conn, ms []Migration, o UpOptions) (Result, error) {
	start := time.Now()
	var res Result
	warn, err := preflight(ctx, c, o.Env)
	if err != nil {
		return res, err
	}
	res.Warnings = warn
	if o.DryRun {
		rows, err := Status(ctx, c, ms)
		if err != nil {
			return res, err
		}
		if err := problem(rows, ms); err != nil {
			return res, err
		}
		for _, r := range rows {
			if r.State == StatePending {
				res.Pending = append(res.Pending, ms[r.Version-1])
			} else {
				res.Current = r.Version
			}
		}
		res.Duration = time.Since(start)
		return res, nil
	}
	if err := lock(ctx, c, o.LockTimeout); err != nil {
		return res, err
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = c.Exec(uctx, "SELECT pg_catalog.pg_advisory_unlock($1)", lockKey)
	}()
	if err := bootstrap(ctx, c); err != nil {
		return res, err
	}
	rows, err := Status(ctx, c, ms)
	if err != nil {
		return res, err
	}
	if err := problem(rows, ms); err != nil {
		return res, err
	}
	var pending []Migration
	for _, r := range rows {
		if r.State == StatePending {
			pending = append(pending, ms[r.Version-1])
		} else {
			res.Current = r.Version
		}
	}
	if o.OnStart != nil {
		o.OnStart(pending)
	}
	for _, m := range pending {
		d, err := apply(ctx, c, m, o.AppVersion)
		if err != nil {
			return res, fmt.Errorf("migration %s failed: %w", m.File(), err)
		}
		a := Applied{Migration: m, Duration: d}
		res.Applied = append(res.Applied, a)
		res.Current = m.Version
		if o.OnApply != nil {
			o.OnApply(a)
		}
	}
	res.Duration = time.Since(start)
	return res, nil
}

// preflight checks the server before anything is written.
func preflight(ctx context.Context, c *pgx.Conn, env config.Env) (warnings []string, err error) {
	var num, ver string
	if err := c.QueryRow(ctx, "SELECT pg_catalog.current_setting('server_version_num'), pg_catalog.current_setting('server_version')").Scan(&num, &ver); err != nil {
		return nil, err
	}
	if n, _ := strconv.Atoi(num); n < 160000 {
		return nil, &ServerTooOldError{Version: ver}
	}
	var ledger, pub, role, super bool
	// Catalog lookups only: the guard must work on a database whose deedglass schema this role
	// can't use.
	err = c.QueryRow(ctx, `SELECT
	    EXISTS (SELECT 1 FROM pg_catalog.pg_class AS c JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
	            WHERE n.nspname = 'deedglass' AND c.relname = 'ledger'),
	    EXISTS (SELECT 1 FROM pg_catalog.pg_publication WHERE pubname = 'deedglass_pub'),
	    EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = $1),
	    (SELECT rolsuper FROM pg_catalog.pg_roles WHERE rolname = current_user)`, appRole).Scan(&ledger, &pub, &role, &super)
	if err != nil {
		return nil, err
	}
	switch {
	case ledger:
		return nil, &NotControlPlaneError{Found: "deedglass.ledger"}
	case pub:
		return nil, &NotControlPlaneError{Found: "the deedglass_pub publication"}
	case !role:
		return nil, ErrMissingRole
	}
	if super {
		if env == config.Prod {
			return nil, ErrSuperuser
		}
		warnings = append(warnings, "Migrating as a superuser. Dev allows it; prod refuses. Run migrations as deedglass_migrator.")
	}
	return warnings, nil
}

func lock(ctx context.Context, c *pgx.Conn, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		var ok bool
		if err := c.QueryRow(ctx, "SELECT pg_catalog.pg_try_advisory_lock($1)", lockKey).Scan(&ok); err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrLockTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

const bootstrapSQL = `SET LOCAL search_path = pg_catalog, pg_temp;
CREATE SCHEMA IF NOT EXISTS deedglass;
CREATE TABLE IF NOT EXISTS deedglass.schema_migrations (
  version integer PRIMARY KEY CHECK (version >= 1),
  name text NOT NULL,
  checksum text NOT NULL CHECK (checksum ~ '^sha256:[0-9a-f]{64}$'),
  applied_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  applied_by text NOT NULL DEFAULT CURRENT_USER,
  execution_ms integer NOT NULL CHECK (execution_ms >= 0),
  app_version text NOT NULL
);
REVOKE ALL ON SCHEMA deedglass FROM PUBLIC;
REVOKE ALL ON deedglass.schema_migrations FROM PUBLIC;
GRANT USAGE ON SCHEMA deedglass TO deedglass_app;
GRANT SELECT ON deedglass.schema_migrations TO deedglass_app;`

func bootstrap(ctx context.Context, c *pgx.Conn) error {
	return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, bootstrapSQL)
		return err
	})
}

func apply(ctx context.Context, c *pgx.Conn, m Migration, appVersion string) (time.Duration, error) {
	start := time.Now()
	err := pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path = pg_catalog, pg_temp; SET LOCAL lock_timeout = '5s'; SET LOCAL statement_timeout = '120s'"); err != nil {
			return err
		}
		// No arguments: pgx uses the simple protocol, so a file may hold many statements.
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return err
		}
		ms := time.Since(start).Milliseconds()
		_, err := tx.Exec(ctx, `INSERT INTO deedglass.schema_migrations (version, name, checksum, execution_ms, app_version)
		    VALUES ($1, $2, $3, $4, $5)`, m.Version, m.Name, m.Checksum, ms, appVersion)
		return err
	})
	return time.Since(start), err
}

type appliedRow struct {
	version   int
	name      string
	checksum  string
	appliedAt time.Time
}

func readApplied(ctx context.Context, c *pgx.Conn) ([]appliedRow, error) {
	var exists bool
	// A catalog lookup, not to_regclass: it must work without USAGE on the schema.
	if err := c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_class AS c JOIN pg_catalog.pg_namespace AS n
	    ON n.oid = c.relnamespace WHERE n.nspname = 'deedglass' AND c.relname = 'schema_migrations')`).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	rows, err := c.Query(ctx, "SELECT version, name, checksum, applied_at FROM deedglass.schema_migrations ORDER BY version")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (appliedRow, error) {
		var a appliedRow
		err := r.Scan(&a.version, &a.name, &a.checksum, &a.appliedAt)
		return a, err
	})
}

// compare merges files and applied rows, in version order.
func compare(ms []Migration, applied []appliedRow) []Row {
	byVersion := map[int]appliedRow{}
	for _, a := range applied {
		byVersion[a.version] = a
	}
	var out []Row
	for _, m := range ms {
		r := Row{Version: m.Version, Name: m.Name, State: StatePending, Checksum: m.Checksum}
		if a, ok := byVersion[m.Version]; ok {
			r.AppliedAt, r.AppliedChecksum = a.appliedAt, a.checksum
			r.State = StateApplied
			if a.checksum != m.Checksum {
				r.State = StateChanged
			}
			delete(byVersion, m.Version)
		}
		out = append(out, r)
	}
	for _, a := range applied {
		if _, left := byVersion[a.version]; left {
			out = append(out, Row{Version: a.version, Name: a.name, State: StateUnknown, AppliedAt: a.appliedAt, AppliedChecksum: a.checksum})
		}
	}
	return out
}

// problem returns the first changed or unknown row as an error.
func problem(rows []Row, ms []Migration) error {
	for _, r := range rows {
		switch r.State {
		case StateChanged:
			return &ChecksumError{Version: r.Version, Name: r.Name, Applied: r.AppliedChecksum, File: r.Checksum}
		case StateUnknown:
			last := r.Version
			for _, x := range rows {
				if x.State == StateUnknown && x.Version > last {
					last = x.Version
				}
			}
			return &AheadError{Version: last, Known: len(ms)}
		}
	}
	return nil
}
