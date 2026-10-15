package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/securitypack"
	"github.com/deedglass/deedglass/internal/securitypack/pgsource"
	"github.com/deedglass/deedglass/internal/securitypackapi"
	"github.com/deedglass/deedglass/internal/store"
)

// newSecurityPackAPI wires S51's routes: the facts come from this server's own database and
// Ledger, the outbound policy from the egress door serve built, and the Pro builder from
// licence.go (asked at start and at most daily).
func newSecurityPackAPI(ctx context.Context, e *Env, cfg *config.Config, db *store.DB, led *ledger.Ledger, cw *connectors, log *slog.Logger) *securitypackapi.API {
	var eg *securitypack.Egress
	if cw != nil && cw.egress != nil {
		eg = &securitypack.Egress{Env: cw.egress.Env, AllowPrivate: cw.egress.AllowPrivate, AllowCIDRs: len(cw.egress.AllowCIDRs)}
		if eg.Env == "" {
			eg.Env = "prod"
		}
	}
	bi := buildInfo()
	return &securitypackapi.API{
		Source:  &pgsource.Source{DB: db, Ledger: led},
		Ledger:  led,
		Builder: securityPackBuilder(ctx, e, log),
		Egress:  eg,
		Build:   securitypack.BuildFactsFor(bi.Version, bi.Commit),
		TempDir: os.TempDir(),
		Log:     log,
	}
}
