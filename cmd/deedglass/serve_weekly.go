package main

import (
	"context"
	"log/slog"

	"github.com/deedglass/deedglass/ee/agency"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mailer"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/weekly"
	"github.com/deedglass/deedglass/internal/weekly/pgsource"
	"github.com/deedglass/deedglass/internal/weeklyapi"
)

// weeklyWiring is S52's: the This week card and email setting, the Monday job, and the Clients view.
type weeklyWiring struct {
	api    *weeklyapi.API
	job    *weekly.Job
	agency *agency.API
}

// newWeekly wires S52. The Monday email goes through the mail queue sign-in already uses; a
// production server without Resend has no mailer, so the job sends nothing and the Console card
// carries the summary. undoPro is S50's undo_session check (the caught block's Pro line shows only
// without it); the agency check comes from licence.go, at start and daily.
func newWeekly(ctx context.Context, e *Env, cfg *config.Config, db *store.DB, led *ledger.Ledger, q *mailer.Queue, people *identity.Service,
	undoPro *proFeature, log *slog.Logger) (*weeklyWiring, error) {
	pepper, err := cfg.Pepper()
	if err != nil {
		return nil, err
	}
	src := &pgsource.Source{DB: db, Ledger: led}
	key := weekly.OffKeyFrom(pepper)
	mail := cfg.ResendAPIKey.IsSet() || cfg.Env != config.Prod
	proLine := func() bool { return !undoPro.Allowed() }
	w := &weeklyWiring{api: &weeklyapi.API{Source: src, Mailer: mail, OffKey: key, ProUndoLine: proLine, Log: log}}
	j := &weekly.Job{Store: src, BaseURL: cfg.BaseURL.String(), OffKey: key, ProUndoLine: proLine, Log: log}
	if mail && q != nil {
		j.Mail = q
	}
	w.job = j
	ag := agencyPro(ctx, e, log)
	w.agency = &agency.API{Reader: agency.PG{DB: db, Ledger: led}, Allowed: ag.Allowed,
		Memberships: func(ctx context.Context, org ids.OrgID, user ids.UserID) ([]agency.Membership, error) {
			ms, err := people.Memberships(ctx, org, user)
			if err != nil {
				return nil, err
			}
			out := make([]agency.Membership, 0, len(ms))
			for _, m := range ms {
				out = append(out, agency.Membership{OrgID: m.OrgID, Role: m.Role, Status: m.Status, OrgStatus: m.OrgStatus})
			}
			return out, nil
		}}
	return w, nil
}
