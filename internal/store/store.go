package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/ids"
)

// Errors.
var (
	ErrNoOrg        = errors.New("store: org id is required") // a zero org id never reaches the database
	ErrNotFound     = errors.New("store: not found")
	ErrUnknownScope = errors.New("store: unknown system reason")
	ErrConflict     = errors.New("store: a live row with that name exists") // S16: connector names
)

// Options tunes the pool. The zero value is fine for the server.
type Options struct {
	MaxConns int32        // default: pgxpool's default (max(4, NumCPU))
	MinConns int32        // default 0
	Logger   *slog.Logger // default slog.Default()
	// Tracer observes every query (profiling and tests only; S13's overhead budget used it). It
	// sees SQL text and arguments, so it must never log arguments.
	Tracer pgx.QueryTracer
	// AsyncCommit sets synchronous_commit = off on every connection: a commit returns before its
	// WAL reaches disk (a crash may lose the last few commits, never corrupt them). Dev bulk tools
	// only (deedglass dev simulate on a throwaway database); the server never sets it.
	AsyncCommit bool
}

// Session settings sent in the startup packet of every pooled connection. Transaction-local
// deedglass.* settings are the only other state, and they end with the transaction.
var sessionParams = map[string]string{
	"application_name":                    "deedglass",
	"search_path":                         "pg_catalog, pg_temp",
	"TimeZone":                            "UTC",
	"statement_timeout":                   "15s",
	"idle_in_transaction_session_timeout": "30s",
	"lock_timeout":                        "5s",
}

// DB is the control-plane database as the runtime role.
type DB struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// Open connects to url (DEEDGLASS_DATABASE_URL) and checks the connection. It never logs the URL.
func Open(ctx context.Context, url config.Secret, o Options) (*DB, error) {
	if !url.IsSet() {
		return nil, errors.New("store: DEEDGLASS_DATABASE_URL isn't set")
	}
	pc, err := pgxpool.ParseConfig(url.Reveal())
	if err != nil {
		// pgx's parse errors can quote the URL; never pass them on.
		return nil, errors.New("store: the database URL can't be parsed; check its form (postgres://user:password@host:5432/db)")
	}
	for k, v := range sessionParams {
		pc.ConnConfig.RuntimeParams[k] = v
	}
	if o.AsyncCommit {
		pc.ConnConfig.RuntimeParams["synchronous_commit"] = "off"
	}
	if pc.ConnConfig.ConnectTimeout == 0 {
		pc.ConnConfig.ConnectTimeout = 5 * time.Second
	}
	if o.MaxConns > 0 {
		pc.MaxConns = o.MaxConns
	}
	pc.MinConns = o.MinConns
	if o.Tracer != nil {
		pc.ConnConfig.Tracer = o.Tracer
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: can't reach the database: %w", err)
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &DB{pool: pool, log: log}, nil
}

// Close closes the pool.
func (db *DB) Close() { db.pool.Close() }

// Ping checks that a connection can be used.
func (db *DB) Ping(ctx context.Context) error { return db.pool.Ping(ctx) }

// TxFunc is the body of a transaction. Returning an error rolls back.
type TxFunc func(ctx context.Context, tx pgx.Tx) error

// OrgTx runs fn in a read-write transaction scoped to org: row-level security shows fn only that
// org's rows, and refuses writes into any other org.
func (db *DB) OrgTx(ctx context.Context, org ids.OrgID, fn TxFunc) error {
	return db.orgTx(ctx, org, pgx.TxOptions{}, fn)
}

// OrgReadTx is OrgTx in a READ ONLY transaction.
func (db *DB) OrgReadTx(ctx context.Context, org ids.OrgID, fn TxFunc) error {
	return db.orgTx(ctx, org, pgx.TxOptions{AccessMode: pgx.ReadOnly}, fn)
}

// OrgSnapshotTx is OrgTx in a REPEATABLE READ READ ONLY transaction: every statement sees the same
// snapshot, and writers are never blocked. Exports use it so their two passes agree (S06).
func (db *DB) OrgSnapshotTx(ctx context.Context, org ids.OrgID, fn TxFunc) error {
	return db.orgTx(ctx, org, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, fn)
}

// ErrLocked: another process holds the advisory lock WithLock asked for.
var ErrLocked = errors.New("store: another run holds this lock")

// WithLock runs fn while one dedicated connection holds the session-level advisory lock named key
// (pg_try_advisory_lock(hashtextextended(key, 0))). It never waits: when another session holds the
// lock it returns ErrLocked. fn runs its own transactions on other pooled connections; the lock
// lasts for all of them and is released at the end, or when the connection drops.
func (db *DB) WithLock(ctx context.Context, key string, fn func(ctx context.Context) error) error {
	c, err := db.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer c.Release()
	var got bool
	if err := c.QueryRow(ctx, `SELECT pg_catalog.pg_try_advisory_lock(pg_catalog.hashtextextended($1::text, 0))`, key).Scan(&got); err != nil {
		return err
	}
	if !got {
		return ErrLocked
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := c.Exec(uctx, `SELECT pg_catalog.pg_advisory_unlock(pg_catalog.hashtextextended($1::text, 0))`, key); err != nil {
			// The lock ends with the session: drop the connection rather than return it locked.
			_ = c.Conn().Close(uctx)
		}
	}()
	return fn(ctx)
}

func (db *DB) orgTx(ctx context.Context, org ids.OrgID, opts pgx.TxOptions, fn TxFunc) error {
	if org.IsZero() {
		return ErrNoOrg
	}
	return pgx.BeginTxFunc(ctx, db.pool, opts, func(tx pgx.Tx) error {
		// Transaction-local (third argument true): the setting ends with the transaction, so a
		// pooled connection never carries an org into the next one.
		if _, err := tx.Exec(ctx, "SELECT pg_catalog.set_config('deedglass.org_id', $1, true)", org.String()); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// SystemReason names why a transaction needs to see across orgs. Every SystemTx call site is listed
// by a test, so a new one shows up in review.
type SystemReason string

const (
	ReasonOrgCreate   SystemReason = "org_create"  // a new org, its first owner and default policy (S07, S12)
	ReasonMaintenance SystemReason = "maintenance" // operator commands, pruning jobs and tests
	ReasonHealth      SystemReason = "health"      // migration state and counts (S03)
	// Pre-auth lookups (S07–S09) and worker claims (S13, S20, S22) don't use SystemTx: they call
	// narrow SECURITY DEFINER functions (preauth_*, claim_*, maint_*). See docs/DATA_MODEL.md.
)

func (r SystemReason) valid() bool {
	switch r {
	case ReasonOrgCreate, ReasonMaintenance, ReasonHealth:
		return true
	}
	return false
}

// SystemTx runs fn with deedglass.scope = 'system': tables with a system_scope policy show every
// org's rows; the others show none. Use it only for the reasons above.
func (db *DB) SystemTx(ctx context.Context, r SystemReason, fn TxFunc) error {
	if !r.valid() {
		return fmt.Errorf("%w %q", ErrUnknownScope, string(r))
	}
	db.log.DebugContext(ctx, "store: system transaction", "reason", string(r))
	return pgx.BeginTxFunc(ctx, db.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_catalog.set_config('deedglass.scope', 'system', true)"); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// RoleReport describes the role the server connects as.
type RoleReport struct {
	User             string
	Superuser        bool
	BypassRLS        bool
	OwnsSchema       bool // owns schema deedglass, or is a member of its owner
	ServerVersionNum int
}

// Clean reports whether row-level security applies to this role and it can't change the schema.
func (r RoleReport) Clean() bool { return !r.Superuser && !r.BypassRLS && !r.OwnsSchema }

// Problems lists why the role isn't clean, one sentence each.
func (r RoleReport) Problems() []string {
	var out []string
	if r.Superuser {
		out = append(out, fmt.Sprintf("Role %s is a superuser, so row-level security doesn't apply to it.", r.User))
	}
	if r.BypassRLS {
		out = append(out, fmt.Sprintf("Role %s has BYPASSRLS, so row-level security doesn't apply to it.", r.User))
	}
	if r.OwnsSchema {
		out = append(out, fmt.Sprintf("Role %s owns schema deedglass (or is a member of its owner), so it could alter tables and triggers.", r.User))
	}
	return out
}

// CheckRuntimeRole reports on the role this pool connects as. The server refuses to start in prod
// when the report isn't clean, and warns in dev (S03). Run the server as deedglass_app.
func (db *DB) CheckRuntimeRole(ctx context.Context) (RoleReport, error) {
	var r RoleReport
	err := db.pool.QueryRow(ctx, `SELECT r.rolname, r.rolsuper, r.rolbypassrls,
	    COALESCE((SELECT pg_catalog.pg_has_role(r.oid, n.nspowner, 'MEMBER')
	              FROM pg_catalog.pg_namespace AS n WHERE n.nspname = 'deedglass'), false),
	    pg_catalog.current_setting('server_version_num')::integer
	  FROM pg_catalog.pg_roles AS r WHERE r.rolname = CURRENT_USER`).
		Scan(&r.User, &r.Superuser, &r.BypassRLS, &r.OwnsSchema, &r.ServerVersionNum)
	return r, err
}

// SQLState returns the Postgres error code of err ("23505", "42501", "DG004"), or "".
func SQLState(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// Transient reports whether err is a refusal that a new transaction may not meet: the session's
// lock_timeout (55P03) or statement_timeout (57014) under contention, a serialization failure
// (40001) or a deadlock victim (40P01). Nothing was committed; the caller may run the whole
// transaction again. A context cancelled by the caller also surfaces as 57014, so a caller that
// retries checks its own context first.
func Transient(err error) bool {
	switch SQLState(err) {
	case CodeLockNotAvailable, CodeQueryCanceled, CodeSerialization, CodeDeadlock:
		return true
	}
	return false
}

// Postgres error codes the store and its callers act on.
const (
	CodeUniqueViolation   = "23505"
	CodeCheckViolation    = "23514"
	CodeForeignKey        = "23503"
	CodeInsufficientPriv  = "42501"
	CodeLockNotAvailable  = "55P03"
	CodeQueryCanceled     = "57014"
	CodeSerialization     = "40001"
	CodeDeadlock          = "40P01"
	CodeAppendOnly        = "DG001"
	CodeNoPredecessor     = "DG002"
	CodeChainBroken       = "DG003"
	CodeIllegalTransition = "DG004"
	CodeLastOwner         = "DG005"
	CodeApprovalVote      = "DG006" // reserved for S22's approval-vote guard
)
