package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/store"
)

// The demo data `deedglass dev seed` loads. Running it again adds only what is missing.
var seedOrg = store.NewOrg{Name: "Acme Robotics", Slug: "acme", OwnerEmail: "founder@example.test", OwnerName: "Founder"}

var seedAgents = []store.NewAgent{
	{Name: "support-bot", Description: "Answers support tickets and issues refunds under $50.", Labels: map[string]string{"team": "support"}},
	{Name: "billing-agent", Description: "Reconciles invoices and updates billing records.", Labels: map[string]string{"team": "finance"}},
}

func runDevSeed(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass dev seed takes no arguments (got %q)", args[0]), "Run deedglass help dev seed.")
	}
	cfg, warnings, err := config.Load(e.LoadOptions())
	var ps *config.Problems
	if errors.As(err, &ps) {
		return configProblems(e, ps, warnings)
	}
	if err != nil {
		return err
	}
	if cfg.Env == config.Prod {
		return &ExitError{Code: ExitFailure, ErrCode: "dev_only", Err: errors.New("deedglass dev seed is for dev databases only"),
			Next: "Unset DEEDGLASS_ENV=prod, and point DEEDGLASS_DATABASE_URL at a local database."}
	}
	if err := cfg.Require(config.NeedDatabase); err != nil {
		errors.As(err, &ps)
		return configProblems(e, ps, warnings)
	}
	db, err := store.Open(ctx, cfg.DatabaseURL, store.Options{MaxConns: 2})
	if err != nil {
		return &ExitError{Code: ExitUsage, ErrCode: "database_unreachable", Err: errors.New("Can't reach the database in DEEDGLASS_DATABASE_URL"),
			Next: "Check that Postgres is running and that migrations ran (deedglass migrate up)."}
	}
	defer db.Close()

	var org store.Org
	createdOrg, createdAgents := false, 0
	err = db.SystemTx(ctx, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		org, err = store.GetOrgBySlug(ctx, tx, seedOrg.Slug)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		org, _, _, err = db.CreateOrg(ctx, seedOrg)
		createdOrg = err == nil
	}
	if err != nil {
		return seedErr(err)
	}
	err = db.OrgTx(ctx, org.ID, func(ctx context.Context, tx pgx.Tx) error {
		for _, a := range seedAgents {
			if _, err := store.GetAgentByName(ctx, tx, org.ID, a.Name); err == nil {
				continue
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			a.CreatedBy = "system"
			if _, err := store.CreateAgent(ctx, tx, org.ID, a); err != nil {
				return err
			}
			createdAgents++
		}
		return nil
	})
	if err != nil {
		return seedErr(err)
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "seed", "org": org.ID, "slug": org.Slug,
			"created": map[string]int{"orgs": b2i(createdOrg), "owners": b2i(createdOrg), "agents": createdAgents}})
	}
	summary := "Seeded 1 org, 1 owner, 2 agents"
	switch {
	case !createdOrg && createdAgents == 0:
		summary = "Already seeded: 1 org, 1 owner, 2 agents"
	case !createdOrg:
		summary = fmt.Sprintf("Seeded %d missing %s into the existing org", createdAgents, plural(createdAgents, "agent", "agents"))
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(summary))
	fmt.Fprintf(e.Stdout, "  org    %s  %s\n  owner  %s\n", org.Slug, e.Style.Dim(org.ID.String()), seedOrg.OwnerEmail)
	for _, a := range seedAgents {
		fmt.Fprintf(e.Stdout, "  agent  %s\n", a.Name)
	}
	return nil
}

func seedErr(err error) error {
	next := "Run deedglass migrate up first, then try again."
	if store.SQLState(err) == store.CodeInsufficientPriv {
		next = "DEEDGLASS_DATABASE_URL must use the runtime role deedglass_app (see docs/DATABASE_SETUP.md)."
	}
	return &ExitError{Code: ExitFailure, ErrCode: "seed_failed", Err: fmt.Errorf("Seeding failed: %v", err), Next: next}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
