package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/postgres/setup"
)

// DSN is a parsed connection string with what Deedglass decided about it. It never prints the
// password: String shows user@host:port/db.
type DSN struct {
	Config   *pgx.ConnConfig
	Host     string
	Port     uint16
	User     string
	DB       string
	SSLMode  string // as written, or "prefer" (libpq's default)
	RootCert string // sslrootcert, a path
	Socket   bool   // a Unix socket
	Pooler   bool   // a transaction pooler: port 6543 or pgbouncer=true
	Warning  string // "sslmode=require doesn't check who the server is…"
}

// String is the DSN's hint: user@host:port/db.
func (d *DSN) String() string {
	return fmt.Sprintf("%s@%s:%d/%s", d.User, d.Host, d.Port, d.DB)
}

// DSNError is a refused connection string: Message is for admins and never holds the password.
type DSNError struct{ Message string }

func (e *DSNError) Error() string { return e.Message }

// ParseDSN parses a libpq connection string (URL or key=value) and applies S17 §3.4's rules:
// every dial goes through env's egress policy (metadata and reserved addresses never; private and
// loopback only with the dev override or an allow list); in prod, a host that isn't local needs
// sslmode=verify-full or verify-ca (require is a warning; disable, allow and prefer are refused);
// Unix sockets are dev only. Unknown parameters are passed to Postgres as pgx does, except
// pgbouncer, which only marks a pooler.
func ParseDSN(raw string, env connector.Env) (*DSN, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, &DSNError{"the DSN is empty"}
	}
	cleaned, sslmode, rootCert, pooler, err := scanDSN(raw)
	if err != nil {
		return nil, err
	}
	cfg, err := pgx.ParseConfig(cleaned)
	if err != nil {
		return nil, &DSNError{"the DSN can't be read: " + scrubDSNError(err.Error(), raw)}
	}
	d := &DSN{Config: cfg, Host: cfg.Host, Port: cfg.Port, User: cfg.User, DB: cfg.Database, SSLMode: sslmode, RootCert: rootCert, Pooler: pooler || cfg.Port == 6543}
	d.Socket = strings.HasPrefix(cfg.Host, "/")
	if d.User == "" || d.DB == "" {
		return nil, &DSNError{"the DSN needs a user and a database name"}
	}
	if len(cfg.Fallbacks) > 0 && !d.Socket {
		// libpq's comma-separated hosts or sslmode=prefer's plain-text fallback: keep the first
		// configuration only, so a refused TLS handshake never silently downgrades.
		if sslmode != "prefer" && sslmode != "allow" {
			return nil, &DSNError{"the DSN names several hosts. Deedglass connects to one host per connector."}
		}
	}
	local := setup.IsLocalHost(d.Host)
	prod := !env.Dev()
	switch {
	case d.Socket && prod:
		return nil, &DSNError{"Unix sockets are for dev only. Connect over TCP with sslmode=verify-full."}
	case prod && !local:
		switch sslmode {
		case "verify-full", "verify-ca":
		case "require":
			d.Warning = "sslmode=require encrypts but doesn't check that the server is who it claims to be. Use verify-full with the server's CA file."
		default:
			return nil, &DSNError{fmt.Sprintf("sslmode=%s isn't allowed in prod for %s. Use sslmode=verify-full (or verify-ca) with the server's CA.", sslmode, d.Host)}
		}
	}
	// With sslmode=prefer, pgx tries TLS then plain text; keep that only for local or dev hosts.
	if prod && !local && len(cfg.Fallbacks) > 0 {
		cfg.Fallbacks = nil
	}
	pol := env.Egress
	if pol == nil {
		pol = &egress.Policy{Env: egress.EnvProd}
	}
	// pgx resolves the host itself unless LookupFunc says otherwise: hand egress the name, so it
	// resolves, checks every answer and re-checks the address at connect time.
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.DialFunc = pol.DialContext
	if cfg.ConnectTimeout == 0 || cfg.ConnectTimeout > 10*time.Second {
		cfg.ConnectTimeout = 5 * time.Second
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	if _, ok := cfg.RuntimeParams["application_name"]; !ok {
		cfg.RuntimeParams["application_name"] = "deedglass"
	}
	// Session-level settings a DSN could smuggle in (options=-c …) are reset by every transaction
	// recipe or by DISCARD ALL; search_path and role are refused outright.
	for _, k := range []string{"search_path", "role", "session_authorization", "session_replication_role", "default_transaction_read_only"} {
		if _, ok := cfg.RuntimeParams[k]; ok {
			return nil, &DSNError{fmt.Sprintf("the DSN sets %s. Remove it: Deedglass sets the session itself.", k)}
		}
	}
	if opts := cfg.RuntimeParams["options"]; opts != "" {
		return nil, &DSNError{"the DSN sets options. Remove them: Deedglass sets the session itself."}
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.StatementCacheCapacity = 0
	cfg.DescriptionCacheCapacity = 0
	return d, nil
}

// scanDSN finds sslmode and pgbouncer in a URL or key=value DSN and drops pgbouncer (Postgres
// would refuse it as an unknown setting).
func scanDSN(raw string) (cleaned, sslmode, rootCert string, pooler bool, err error) {
	sslmode = "prefer"
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		u, perr := url.Parse(raw)
		if perr != nil {
			return "", "", "", false, &DSNError{"the DSN isn't a valid URL"}
		}
		q := u.Query()
		if v := q.Get("sslmode"); v != "" {
			sslmode = v
		}
		rootCert = q.Get("sslrootcert")
		if q.Has("pgbouncer") {
			pooler = q.Get("pgbouncer") == "true"
			q.Del("pgbouncer")
			u.RawQuery = q.Encode()
		}
		return u.String(), sslmode, rootCert, pooler, nil
	}
	var kept []string
	for _, f := range strings.Fields(raw) {
		k, v, _ := strings.Cut(f, "=")
		switch strings.TrimSpace(k) {
		case "sslmode":
			sslmode = strings.Trim(v, "'")
		case "sslrootcert":
			rootCert = strings.Trim(v, "'")
		case "pgbouncer":
			pooler = strings.Trim(v, "'") == "true"
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " "), sslmode, rootCert, pooler, nil
}

// scrubDSNError removes the password from a pgx parse error, which can quote the input.
func scrubDSNError(msg, raw string) string {
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			msg = strings.ReplaceAll(msg, pw, "[redacted]")
		}
	}
	for _, f := range strings.Fields(raw) {
		if k, v, ok := strings.Cut(f, "="); ok && k == "password" && v != "" {
			msg = strings.ReplaceAll(msg, strings.Trim(v, "'"), "[redacted]")
		}
	}
	return msg
}

// connectErr maps a failed connection to the connector's error, never naming the password.
func connectErr(err error, name string) *connector.Error {
	var be *egress.BlockedError
	if errors.As(err, &be) {
		return connector.ErrDestinationBlocked(be)
	}
	if ue := (*unsafeRoleError)(nil); errors.As(err, &ue) {
		return errRoleUnsafe(ue)
	}
	code := sqlState(err)
	switch code {
	case "28P01", "28000":
		return &connector.Error{Kind: connector.KindAuth, Code: connector.CodeUpstreamAuthFailed,
			AgentMessage: "The database refused the connector's credentials. Nothing ran. An admin can store new ones in Console › Connectors.",
			AdminDetail:  fmt.Sprintf("Postgres refused the login (SQLSTATE %s). Run deedglass connector postgres init --connector %s --rotate-passwords --apply.", code, name), Err: err}
	case "3D000":
		return &connector.Error{Kind: connector.KindConfig, Code: connector.CodeConnectorUnavailable,
			AgentMessage: "The connector's database doesn't exist. Nothing ran.", AdminDetail: "Postgres has no database by the DSN's name (3D000).", Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &connector.Error{Kind: connector.KindConfig, Code: connector.CodeConnectorUnavailable,
			AgentMessage: "Deedglass couldn't reach the database in time, so nothing ran. Try again later; an admin can run deedglass connector test.", Err: err}
	}
	return connector.ErrUnreachable(err)
}
