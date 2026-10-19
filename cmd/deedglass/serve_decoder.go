package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/fhir"
	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// decoderConfig maps the decoder.* settings (and DEEDGLASS_SLOT_LAG_ALERT_MB) to the decoder's.
func decoderConfig(cfg *config.Config) decoder.Config {
	d := decoder.DefaultConfig()
	s := cfg.Decoder
	d.PollActive, d.PollIdle, d.BatchChanges, d.MaxImages = s.PollActive, s.PollIdle, s.BatchChanges, s.MaxImagesPerTx
	d.HeartbeatInterval, d.HeartbeatDeadline, d.Lease = s.HeartbeatInterval, s.HeartbeatDeadline, s.Lease
	d.ReconcileInterval, d.Retention = s.ReconcileInterval, s.Retention
	d.LagAlertBytes = int64(cfg.SlotLagAlertMB) << 20
	if cfg.DecoderPause && cfg.Env == config.Dev {
		d.Paused = func() bool { return true }
	}
	return d
}

// newDecoder is S20's wiring: one supervisor per server, reading every witnessed Postgres
// connector (and, ADR-108, every witnessed FHIR connector's server database) whose lease this
// instance holds. The witness DSN comes through the registry's
// instance and the Postgres connector (Instance.Secret), never the vault directly.
func newDecoder(cfg *config.Config, db *store.DB, led *ledger.Ledger, cw *connectors, log *slog.Logger, hooks decoder.Hooks) *decoder.Supervisor {
	if !cfg.Decoder.Enabled || db == nil || cw == nil {
		return nil
	}
	loader := connector.DBLoader{DB: db}
	target := func(ctx context.Context, org ids.OrgID, id ids.ConnectorID) (*postgres.WitnessTarget, string, error) {
		row, err := loader.Load(ctx, org, id.String())
		if err != nil {
			return nil, "", err
		}
		inst, err := cw.reg.Instance(org, row)
		if err != nil {
			return nil, row.Name, err
		}
		if row.Type == fhir.Type {
			// ADR-108: a FHIR server's own database, bound by the version the server reported.
			t, err := fhir.WitnessTargetOf(ctx, inst)
			return t, row.Name, err
		}
		t, err := postgres.WitnessTargetOf(ctx, inst)
		return t, row.Name, err
	}
	return decoder.NewSupervisor(decoder.Deps{DB: db, Ledger: led, Target: target, Log: log.With("component", "decoder"),
		Worker: instanceName() + "/decoder", Hooks: hooks}, decoderConfig(cfg))
}

// l2Hooks are S21's consumers of the decoder's hooks: a frame naming an action finalizes its L2
// wait (or amends its deed), reconcile's missing frame and a lost slot end waits with their gaps.
// The hooks run in the runner's goroutine, so the work goes to its own goroutine, bounded.
func l2Hooks(gp *gate.Pipeline) decoder.Hooks {
	sem := make(chan struct{}, 16)
	async := func(f func(ctx context.Context)) {
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			f(ctx)
		}()
	}
	return decoder.Hooks{
		OnBound: func(_ context.Context, org ids.OrgID, _ ids.ConnectorID, actionID string, _ decoder.Class, _ decoder.LSN) {
			async(func(ctx context.Context) { gp.L2Evidence(ctx, org, actionID) })
		},
		OnMissing: func(_ context.Context, org ids.OrgID, _ ids.ConnectorID, actionID string) {
			async(func(ctx context.Context) { gp.L2Missing(ctx, org, actionID) })
		},
		SlotLost: func(_ context.Context, org ids.OrgID, con ids.ConnectorID, reason string) {
			async(func(ctx context.Context) { gp.L2SlotLost(ctx, org, con, reason) })
		},
	}
}

// decoderHealth is /v1/health's decoder check on this instance.
func decoderHealth(s *decoder.Supervisor) httpx.PolicyCheck {
	if s == nil {
		return nil
	}
	return s.Health
}
