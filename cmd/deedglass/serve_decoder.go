package main

import (
	"context"
	"log/slog"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/decoder"
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
// connector whose lease this instance holds. The witness DSN comes through the registry's
// instance and the Postgres connector (Instance.Secret), never the vault directly.
func newDecoder(cfg *config.Config, db *store.DB, led *ledger.Ledger, cw *connectors, log *slog.Logger) *decoder.Supervisor {
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
		t, err := postgres.WitnessTargetOf(ctx, inst)
		return t, row.Name, err
	}
	return decoder.NewSupervisor(decoder.Deps{DB: db, Ledger: led, Target: target, Log: log.With("component", "decoder"),
		Worker: instanceName() + "/decoder"}, decoderConfig(cfg))
}

// decoderHealth is /v1/health's decoder check on this instance.
func decoderHealth(s *decoder.Supervisor) httpx.PolicyCheck {
	if s == nil {
		return nil
	}
	return s.Health
}
