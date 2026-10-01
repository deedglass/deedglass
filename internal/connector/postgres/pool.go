package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/postgres/setup"
)

// pools holds one pgxpool per connector (S17 §3.11), keyed by the config hash and a keyed MAC of
// the DSN: a credential replace or a config PATCH makes a new pool and drains the old one.
type pools struct {
	mu  sync.Mutex
	m   map[string]*poolEntry // by connector id
	mac [32]byte
	log *slog.Logger
}

type poolEntry struct {
	key    string
	pool   *pgxpool.Pool
	dsn    *DSN
	unsafe atomic.Pointer[unsafeRoleError] // dev: what the last probe found (prod refuses instead)
}

func newPools(log *slog.Logger) *pools {
	p := &pools{m: map[string]*poolEntry{}, log: log}
	_, _ = rand.Read(p.mac[:])
	return p
}

func (p *pools) keyFor(cfgHash, dsn string) string {
	m := hmac.New(sha256.New, p.mac[:])
	m.Write([]byte(cfgHash))
	m.Write([]byte{0})
	m.Write([]byte(dsn))
	return hex.EncodeToString(m.Sum(nil))
}

// probeSQL is the role safety probe every new connection runs (S17 §3.6): the session's role, its
// attributes, and every unsafe role it reaches through memberships.
const probeSQL = `SELECT CURRENT_USER::text, r.rolsuper, r.rolbypassrls, r.rolreplication, r.rolcreaterole, r.rolcreatedb,
	ARRAY(WITH RECURSIVE m(roleid) AS (
			SELECT a.roleid FROM pg_catalog.pg_auth_members AS a WHERE a.member = r.oid
			UNION
			SELECT a.roleid FROM pg_catalog.pg_auth_members AS a JOIN m ON a.member = m.roleid)
		SELECT x.rolname::text FROM m JOIN pg_catalog.pg_roles AS x ON x.oid = m.roleid WHERE x.rolname = ANY($1) ORDER BY 1)
	FROM pg_catalog.pg_roles AS r WHERE r.rolname = CURRENT_USER`

// probeRole runs the probe on one connection.
func probeRole(ctx context.Context, conn *pgx.Conn, connector string) (*unsafeRoleError, error) {
	var user string
	var super, bypass, repl, createRole, createDB bool
	var unsafe []string
	if err := conn.QueryRow(ctx, probeSQL, setup.UnsafeRoles).Scan(&user, &super, &bypass, &repl, &createRole, &createDB, &unsafe); err != nil {
		return nil, fmt.Errorf("postgres: the role safety probe failed: %w", err)
	}
	var reasons []string
	for _, x := range []struct {
		on   bool
		name string
	}{{super, "SUPERUSER"}, {bypass, "BYPASSRLS"}, {repl, "REPLICATION"}, {createRole, "CREATEROLE"}, {createDB, "CREATEDB"}} {
		if x.on {
			reasons = append(reasons, x.name)
		}
	}
	reasons = append(reasons, unsafe...)
	if len(reasons) == 0 && user == setup.RoleAgent {
		return nil, nil
	}
	return &unsafeRoleError{User: user, Connector: connector, Reasons: reasons, NotAgent: user != setup.RoleAgent}, nil
}

// get returns the instance's pool, building it when the config or the DSN changed.
func (p *pools) get(ctx context.Context, inst *connector.Instance, cfg Config, rawDSN string) (*poolEntry, error) {
	key := p.keyFor(inst.ConfigHash, rawDSN)
	id := inst.ID.String()
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.m[id]; e != nil && e.key == key {
		return e, nil
	}
	d, err := ParseDSN(rawDSN, inst.Env)
	if err != nil {
		return nil, err
	}
	pc, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, err
	}
	pc.ConnConfig = d.Config
	pc.MaxConns = int32(cfg.MaxConnections)
	pc.MinConns = 0
	pc.MaxConnIdleTime = 5 * time.Minute
	pc.MaxConnLifetime = 30 * time.Minute
	pc.HealthCheckPeriod = time.Minute
	e := &poolEntry{key: key, dsn: d}
	name, prod, log := inst.Name, !inst.Env.Dev(), p.log
	pc.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		ue, err := probeRole(ctx, conn, name)
		if err != nil {
			return err
		}
		if ue == nil {
			e.unsafe.Store(nil)
			return nil
		}
		if prod {
			log.Error("postgres: refused a connection whose role isn't least privilege (database_role_unsafe)",
				"connector", name, "role", ue.User, "reasons", ue.Reasons, "alert", CodeDatabaseRoleUnsafe)
			return ue
		}
		e.unsafe.Store(ue)
		log.Warn("postgres: DEV ONLY: this connector's role isn't least privilege; prod refuses it (database_role_unsafe)",
			"connector", name, "role", ue.User, "reasons", ue.Reasons)
		return nil
	}
	if !d.Pooler {
		// DISCARD ALL after every use, off the request path (pgxpool runs AfterRelease in its own
		// goroutine). A failure destroys the connection rather than reuse a dirty session.
		pc.AfterRelease = func(conn *pgx.Conn) bool {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := conn.Exec(ctx, "DISCARD ALL")
			return err == nil
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, connectErr(err, name)
	}
	e.pool = pool
	if old := p.m[id]; old != nil {
		go old.pool.Close() // waits for in-flight calls, then closes
	}
	p.m[id] = e
	return e, nil
}

// close closes every pool.
func (p *pools) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, e := range p.m {
		e.pool.Close()
		delete(p.m, id)
	}
}

// forget drops one connector's pool (deleted or disabled).
func (p *pools) forget(id string) {
	p.mu.Lock()
	e := p.m[id]
	delete(p.m, id)
	p.mu.Unlock()
	if e != nil {
		go e.pool.Close()
	}
}

// acquire takes a connection, mapping a refused or failed connect to the connector's error.
func (e *poolEntry) acquire(ctx context.Context, name string) (*pgxpool.Conn, error) {
	c, err := e.pool.Acquire(ctx)
	if err != nil {
		return nil, connectErr(err, name)
	}
	return c, nil
}
