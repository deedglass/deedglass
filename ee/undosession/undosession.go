// Package undosession is Pro's one-step session undo (S50 §3.6, ADR-095): one countersign puts back
// every row a watched session changed, newest first, in one Postgres transaction, with S23's
// catalog re-check, locked guards compared byte for byte, and its own record (the undo action's
// deed, witnessed at L2 against the plan's exact footprint).
//
// It is the engine behind the Gate executor for "<connector>.undo_session" (undoapi.SessionUndo).
// It never reads a licence: serve asks cmd/deedglass/licence.go once at start and passes this
// engine, or nil. Everything a person needs without Pro stays free: the preview, per-action undo
// and the undo SQL they run themselves (deedglass observe show <id> --undo-sql).
//
// Licensed under ee/LICENSE (FSL-1.1-ALv2).
package undosession

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/undoapi"
	"github.com/deedglass/deedglass/internal/witness/undo"
)

// Engine runs session undos.
type Engine struct{}

// New returns the engine.
func New() *Engine { return &Engine{} }

var _ undoapi.SessionUndo = (*Engine)(nil)

// ErrIncomplete: the run is missing its plan, runner or footprint recorder (a wiring bug; nothing ran).
var ErrIncomplete = errors.New("undosession: the run is incomplete")

// Run checks the plan is the one the person approved, records its exact footprint on the undo
// action, then runs it through the connector in one transaction. A row that changed after the plan
// was made rolls everything back (undo_conflict): nothing is half undone.
func (*Engine) Run(ctx context.Context, in undoapi.SessionRun) (connector.Result, error) {
	if in.Plan == nil || in.Runner == nil || in.Record == nil {
		return connector.Result{}, ErrIncomplete
	}
	if in.Plan.Digest == "" || in.Plan.Digest != in.Digest || len(in.Plan.Steps) == 0 {
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: undoapi.CodeUndoPlanStale, AgentMessage: undo.MsgStale}
	}
	fp, err := json.Marshal(undo.ExactFootprint(in.Plan))
	if err != nil {
		return connector.Result{}, err
	}
	if err := in.Record(ctx, fp); err != nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindConfig, Code: "internal_error",
			AgentMessage: "Deedglass couldn't record the undo's plan, so nothing ran. Try again.", Err: err}
	}
	return in.Runner.RunUndo(ctx, in.Instance, in.Call, in.Plan, in.Options)
}
