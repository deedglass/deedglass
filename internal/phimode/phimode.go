// Package phimode is PHI mode's serve job (HC-P09): in an org with settings.phi_mode on, once an
// action is sealed and no read-back or L2 job still needs it, its stored request, result, error
// message and witness detail are replaced by ids and digests (internal/phi), and its policy input
// is cleared. The deed is untouched: it already holds digests only, and the action's digest
// columns keep the digests of what was sent and seen.
package phimode

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/phi"
	"github.com/deedglass/deedglass/internal/store"
)

// Defaults.
const (
	DefaultInterval = 2 * time.Second
	DefaultBatch    = 100
	maxOrgsPerTick  = 100
)

// KeepFunc says which identifiers a tool keeps (the connector's PHIKeep), or nothing.
type KeepFunc func(ctx context.Context, org ids.OrgID, tool string) connector.PHIKeep

// Job replaces the payloads of sealed PHI-mode actions.
type Job struct {
	DB       *store.DB
	Keep     KeepFunc
	Interval time.Duration
	Batch    int
	Log      *slog.Logger
}

// KeepFrom resolves a tool through a connector registry; a tool that no longer resolves keeps
// nothing (every value becomes a digest).
func KeepFrom(reg interface {
	Resolve(ctx context.Context, org ids.OrgID, qualified string) (connector.Executor, connector.ToolSpec, string, error)
}) KeepFunc {
	return func(ctx context.Context, org ids.OrgID, tool string) connector.PHIKeep {
		ex, spec, _, err := reg.Resolve(ctx, org, tool)
		if err != nil {
			return connector.PHIKeep{}
		}
		if k, ok := ex.(connector.PHIKeeping); ok {
			return k.PHIKeep(spec)
		}
		return connector.PHIKeep{}
	}
}

// Run ticks until ctx ends.
func (j *Job) Run(ctx context.Context) error {
	every := j.Interval
	if every <= 0 {
		every = DefaultInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := j.Tick(ctx); err != nil && ctx.Err() == nil {
			j.log().WarnContext(ctx, "phi mode: minimizing payloads failed; retrying on the next tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Tick minimizes every org's pending actions and returns how many it changed.
func (j *Job) Tick(ctx context.Context) (int, error) {
	orgs, err := j.DB.ClaimPHIOrgs(ctx, maxOrgsPerTick)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, org := range orgs {
		n, err := j.MinimizeOrg(ctx, org)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// MinimizeOrg minimizes one org's pending actions, a batch per transaction, until none is left
// that can be done now.
func (j *Job) MinimizeOrg(ctx context.Context, org ids.OrgID) (int, error) {
	batch := j.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}
	total := 0
	for {
		n := 0
		err := j.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := store.PendingPHI(ctx, tx, org, batch)
			if err != nil {
				return err
			}
			for _, p := range rows {
				var keep connector.PHIKeep
				if p.Mode == "gateway" && j.Keep != nil {
					keep = j.Keep(ctx, org, p.Tool)
				}
				if err := store.MinimizePHI(ctx, tx, org, Project(p, keep)); err != nil {
					return err
				}
			}
			n = len(rows)
			return nil
		})
		total += n
		if err != nil || n < batch {
			if total > 0 {
				j.log().InfoContext(ctx, "phi mode: payloads replaced by ids and digests", "org_id", org.String(), "actions", total)
			}
			return total, err
		}
	}
}

// Project is the minimized form of one action's payloads: the request keeps its argument names and
// the connector's identifier arguments, the result keeps the connector's identifier paths, the
// error message becomes its digest, and the witness detail keeps verdicts, levels, methods, reason
// codes and compared paths (phi.WitnessDetail).
func Project(p store.PHIPending, keep connector.PHIKeep) store.PHIMinimized {
	m := store.PHIMinimized{ID: p.ID,
		Request:       phi.MinimizeArgs(p.Request, keep.Args),
		Result:        phi.Minimize(p.Result, phi.Paths(keep.Result)),
		WitnessDetail: phi.Minimize(p.WitnessDetail, phi.WitnessDetail)}
	if p.ErrorMessage != nil && *p.ErrorMessage != "" {
		d := phi.Text(*p.ErrorMessage)
		m.ErrorMessage = &d
	}
	if string(m.Request) == "null" {
		m.Request = nil
	}
	if string(m.Result) == "null" {
		m.Result = nil
	}
	if string(m.WitnessDetail) == "null" {
		m.WitnessDetail = json.RawMessage(nil)
	}
	return m
}

func (j *Job) log() *slog.Logger {
	if j.Log != nil {
		return j.Log
	}
	return slog.Default()
}
