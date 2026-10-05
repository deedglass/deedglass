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
	"github.com/deedglass/deedglass/internal/connector/github"
	gmailconn "github.com/deedglass/deedglass/internal/connector/gmail"
	notionconn "github.com/deedglass/deedglass/internal/connector/notion"
	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/connector/rest"
	slackconn "github.com/deedglass/deedglass/internal/connector/slack"
	stripeconn "github.com/deedglass/deedglass/internal/connector/stripe"
	"github.com/deedglass/deedglass/internal/connectorapi"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mcp/upstream"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

// connectors is S16's wiring: the egress policy, the vault and the connector registry.
type connectors struct {
	egress  *egress.Policy
	vault   *vault.Store // nil when DEEDGLASS_VAULT_KEY isn't set
	ring    *vault.Ring  // the key ring itself (S22: webhook signing secrets); nil without the key
	reg     *connector.Registry
	decoder *decoder.Supervisor   // S20; nil when decoder.enabled is false
	breaker *connectorapi.Breaker // S30 trips; S34 seals connector.tool_hidden
}

// egressPolicy builds the process's one door from its config. self is Deedglass's own database,
// resolved once at start (S16 §3.2 class 4).
func egressPolicy(ctx context.Context, cfg *config.Config, log *slog.Logger) *egress.Policy {
	env := egress.EnvProd
	if cfg.Env == config.Dev {
		env = egress.EnvDev
	}
	p := &egress.Policy{Env: env, AllowPrivate: cfg.AllowPrivateNetworks, AllowCIDRs: cfg.EgressAllowCIDRs,
		Self: selfAddrs(ctx, cfg.DatabaseURL.Reveal()), Log: log}
	if egressResolverHook != nil {
		p.Resolver = egressResolverHook(cfg, log)
	}
	return p
}

// egressResolverHook is set only in a build with -tags attackhooks (attackhooks.go): the attack
// harness's host answers, dev only. A release build has no hook (TestNoAttackHooks).
var egressResolverHook func(cfg *config.Config, log *slog.Logger) egress.Resolver

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
	o := connector.Options{Builtins: natives, Types: []connector.Connector{rest.New(), postgres.New(log), github.New(), slackconn.New(), stripeconn.New(), gmailconn.New(), notionconn.New()},
		Loader: connector.DBLoader{DB: db},
		Env:    connector.Env{Name: string(cfg.Env), Egress: c.egress}, Logger: log}
	if cfg.VaultKey.IsSet() {
		ring, err := vault.NewRing(cfg.VaultKey.Reveal())
		if err != nil {
			return nil, err
		}
		c.ring = ring
		c.vault = vault.NewStore(db, ring, vault.StoreOptions{Ledger: led, Logger: log, Prod: cfg.Env == config.Prod})
		o.Opener = c.vault
	}
	br := &connectorapi.Breaker{DB: db, Ledger: led, Log: log}
	o.Env.Breaker = br
	c.breaker = br
	c.reg = connector.New(o)
	br.Registry = c.reg
	return c, nil
}

// connectorRoutes are S16's routes. With no wiring (route tests) they mount over empty deps.
func connectorRoutes(db *store.DB, led *ledger.Ledger, cw *connectors, log *slog.Logger) []httpx.Route {
	a := &connectorapi.API{DB: db, Ledger: led, Log: log}
	if cw == nil {
		p, _ := upstream.New(upstream.Options{})
		a.Registry = connector.New(connector.Options{Types: []connector.Connector{rest.New(), postgres.New(nil), upstream.NewType(p),
			github.New(), slackconn.New(), stripeconn.New(), gmailconn.New(), notionconn.New()}})
	} else {
		a.Registry = cw.reg
		if cw.vault != nil {
			a.Vault = cw.vault
		}
		if cw.decoder != nil {
			a.Decoder = cw.decoder
		}
	}
	return connectorapi.Routes(a)
}
