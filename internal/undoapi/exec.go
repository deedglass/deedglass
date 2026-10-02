package undoapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/witness/undo"
)

// Resolver is the Gate's internal resolver (gate.Options.Internal): it finds a connector's internal
// undo tool and wraps it in the undo Executor. Only the console principal's actions (a person's
// undo) and actions already admitted reach it; agents get tool_unknown from the normal resolver.
type Resolver struct {
	Reg     InternalResolver
	Planner *Planner
	DB      *store.DB
	Log     *slog.Logger
}

// Resolve implements gate.Resolver for "<connector>.undo".
func (r Resolver) Resolve(ctx context.Context, org ids.OrgID, qualified string) (connector.Executor, connector.ToolSpec, string, error) {
	if r.Reg == nil {
		return nil, connector.ToolSpec{}, "", connector.ErrToolUnknown
	}
	ex, spec, ctype, err := r.Reg.ResolveInternal(ctx, org, qualified)
	if err != nil {
		return nil, connector.ToolSpec{}, "", err
	}
	b, ok := ex.(connector.Bound)
	if !ok || spec.Name != undoTool {
		return nil, connector.ToolSpec{}, "", connector.ErrToolUnknown
	}
	run, ok := b.Impl().(Runner)
	if !ok {
		return nil, connector.ToolSpec{}, "", connector.ErrToolUnknown
	}
	log := r.Log
	if log == nil {
		log = slog.Default()
	}
	return &Executor{b: b, run: run, planner: r.Planner, db: r.DB, log: log}, spec, ctype, nil
}

// Executor runs "<connector>.undo" for the Gate (S23 §3.5). It recomputes the plan from the stored
// evidence and the catalog now, refuses a digest that differs from args.plan_digest
// (undo_plan_stale: a certain failure, nothing was sent), stores the plan in undo_plans and its
// exact footprint in actions.witness_expect (its own short transaction in the Deedglass
// database), then asks the connector to run it in one transaction.
type Executor struct {
	b       connector.Bound
	run     Runner
	planner *Planner
	db      *store.DB
	log     *slog.Logger
}

var (
	_ connector.Executor     = (*Executor)(nil)
	_ connector.L2Witnessing = (*Executor)(nil)
)

// Args are postgres.undo's arguments: ids, a digest, a flag and a reason. Never a value.
type Args struct {
	Undoes        string `json:"undoes"`
	PlanDigest    string `json:"plan_digest"`
	AllowTriggers bool   `json:"allow_triggers"`
	Reason        string `json:"reason"`
}

// CodeUndoPlanStale is the executor's failure code when the plan changed since the preview.
const CodeUndoPlanStale = "undo_plan_stale"

// Execute implements connector.Executor.
func (e *Executor) Execute(ctx context.Context, call connector.Call) (connector.Result, error) {
	if err := connector.ValidateArgs(call.Tool.InputSchema, call.Args); err != nil {
		return connector.Result{}, err
	}
	var a Args
	if err := json.Unmarshal(call.Args, &a); err != nil {
		return connector.Result{}, connector.ErrValidation("args", "can't be read")
	}
	if a.Undoes == "" || a.PlanDigest == "" {
		return connector.Result{}, connector.ErrValidation("args", "need undoes and plan_digest")
	}
	plan, _, _, err := e.planner.Plan(ctx, call.OrgID, a.Undoes)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return connector.Result{}, &connector.Error{Kind: connector.KindConfig, Code: "connector_unavailable",
			AgentMessage: "Deedglass couldn't read the database's catalog to check the plan, so nothing ran. Try again in a moment.", Err: err}
	}
	if plan == nil || plan.Digest != a.PlanDigest {
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: CodeUndoPlanStale, AgentMessage: undo.MsgStale}
	}
	// The plan and the exact footprint, stored before the connector runs (S21 reads witness_expect).
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return connector.Result{}, err
	}
	fp, err := json.Marshal(undo.ExactFootprint(plan))
	if err != nil {
		return connector.Result{}, err
	}
	err = e.db.OrgTx(ctx, call.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := store.InsertUndoPlan(ctx, tx, call.OrgID, store.UndoPlanRow{ActionID: call.ActionID.String(), UndoesActionID: a.Undoes,
			Plan: planJSON, PlanDigest: plan.Digest, RowCount: max(plan.Rows, 1), Tables: plan.TableNames()}); err != nil {
			return err
		}
		return store.SetWitnessExpect(ctx, tx, call.OrgID, call.ActionID, fp)
	})
	if err != nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindConfig, Code: "internal_error",
			AgentMessage: "Deedglass couldn't record the undo's plan, so nothing ran. Try again.", Err: err}
	}
	cfg := e.planner.Config()
	res, err := e.run.RunUndo(ctx, e.b.Instance(), call, plan, undo.RunOptions{AllowTriggers: a.AllowTriggers,
		LockTimeout: cfg.LockTimeout, StatementTimeout: cfg.StatementTimeout})
	if err != nil {
		var ce *connector.Error
		if errors.As(err, &ce) && ce.Code == "undo_conflict" {
			e.log.Info("undo: the undo rolled back", "org_id", call.OrgID.String(), "action_id", call.ActionID.String(), "undoes", a.Undoes)
		}
		return connector.Result{}, err
	}
	return res, nil
}

// L2Target implements connector.L2Witnessing through the bound connector.
func (e *Executor) L2Target(call connector.Call) (*connector.L2Target, error) {
	w, ok := e.b.(connector.L2Witnessing)
	if !ok {
		return nil, nil
	}
	return w.L2Target(call)
}

// L2Triggers implements connector.L2Witnessing through the bound connector.
func (e *Executor) L2Triggers(ctx context.Context, tables []string, fresh bool) (map[string][]string, error) {
	w, ok := e.b.(connector.L2Witnessing)
	if !ok {
		return nil, nil
	}
	return w.L2Triggers(ctx, tables, fresh)
}
