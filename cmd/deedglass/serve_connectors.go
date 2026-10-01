package main

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/connector/rest"
	"github.com/deedglass/deedglass/internal/connectorapi"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

// connectors is S16's wiring: the egress policy, the vault and the connector registry.
type connectors struct {
	egress *egress.Policy
	vault  *vault.Store // nil when DEEDGLASS_VAULT_KEY isn't set
	reg    *connector.Registry
}

// egressPolicy builds the process's one door from its config. self is Deedglass's own database,
// resolved once at start (S16 §3.2 class 4).
func egressPolicy(ctx context.Context, cfg *config.Config, log *slog.Logger) *egress.Policy {
	env := egress.EnvProd
	if cfg.Env == config.Dev {
		env = egress.EnvDev
	}
	return &egress.Policy{Env: env, AllowPrivate: cfg.AllowPrivateNetworks, AllowCIDRs: cfg.EgressAllowCIDRs,
		Self: selfAddrs(ctx, cfg.DatabaseURL.Reveal()), Log: log}
}

// selfAddrs resolves the database URL's host:port (Unix sockets and unparsable URLs give none).
func selfAddrs(ctx context.Context, dsn string) []netip.AddrPort {
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return nil
	}
	host, portS := u.Hostname(), u.Port()
	if portS == "" {
		portS = "5432"
	}
	port, err := strconv.ParseUint(portS, 10, 16)
	if err != nil {
		return nil
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.AddrPort{netip.AddrPortFrom(a.Unmap(), uint16(port))}
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	as, err := net.DefaultResolver.LookupNetIP(rctx, "ip", host)
	if err != nil {
		return nil
	}
	out := make([]netip.AddrPort, 0, len(as))
	for _, a := range as {
		out = append(out, netip.AddrPortFrom(a.Unmap(), uint16(port)))
	}
	return out
}

// newConnectors builds the vault (when a key ring is set) and the registry: fake in dev only, the
// REST and Postgres types everywhere.
func newConnectors(ctx context.Context, cfg *config.Config, db *store.DB, led *ledger.Ledger, natives []connector.Connector, log *slog.Logger) (*connectors, error) {
	c := &connectors{egress: egressPolicy(ctx, cfg, log)}
	o := connector.Options{Builtins: natives, Types: []connector.Connector{rest.New(), postgres.New(log)}, Loader: connector.DBLoader{DB: db},
		Env: connector.Env{Name: string(cfg.Env), Egress: c.egress}, Logger: log}
	if cfg.VaultKey.IsSet() {
		ring, err := vault.NewRing(cfg.VaultKey.Reveal())
		if err != nil {
			return nil, err
		}
		c.vault = vault.NewStore(db, ring, vault.StoreOptions{Ledger: led, Logger: log, Prod: cfg.Env == config.Prod})
		o.Opener = c.vault
	}
	c.reg = connector.New(o)
	return c, nil
}

// connectorRoutes are S16's routes. With no wiring (route tests) they mount over empty deps.
func connectorRoutes(db *store.DB, led *ledger.Ledger, cw *connectors, log *slog.Logger) []httpx.Route {
	a := &connectorapi.API{DB: db, Ledger: led, Log: log}
	if cw == nil {
		a.Registry = connector.New(connector.Options{Types: []connector.Connector{rest.New(), postgres.New(nil)}})
	} else {
		a.Registry = cw.reg
		if cw.vault != nil {
			a.Vault = cw.vault
		}
	}
	return connectorapi.Routes(a)
}
