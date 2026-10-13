package undosession

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/undoapi"
	"github.com/deedglass/deedglass/internal/witness/undo"
)

type fakeRunner struct {
	calls []string
	err   error
}

func (f *fakeRunner) UndoCatalog(context.Context, *connector.Instance, []uint32) (*undo.Catalog, error) {
	return nil, nil
}
func (f *fakeRunner) UndoRows(context.Context, *connector.Instance, *undo.Plan) (undo.Current, error) {
	return nil, nil
}
func (f *fakeRunner) RunUndo(_ context.Context, _ *connector.Instance, _ connector.Call, p *undo.Plan, o undo.RunOptions) (connector.Result, error) {
	f.calls = append(f.calls, "run:"+p.Digest)
	if f.err != nil {
		return connector.Result{}, f.err
	}
	return connector.Result{}, nil
}

func plan() *undo.Plan {
	return &undo.Plan{V: 1, Undoes: "obs_01m3x71p6wesj7xewec6bwbwxm", Digest: "sha256:aa",
		Tables: []undo.PlanTable{{RelID: 100, Schema: "public", Name: "orders", PK: []string{"id"}, Columns: []undo.PlanColumn{{Name: "id", Type: "bigint"}, {Name: "status", Type: "text"}}}},
		Steps:  []undo.Step{{Op: undo.OpUpdate, Table: 100, Key: undo.Key{"id": "1"}, Set: map[string]undo.Value{"status": {T: ptr("paid")}}}},
		Guards: []undo.Guard{{Table: 100, Key: undo.Key{"id": "1"}, Expect: map[string]undo.Value{"status": {T: ptr("shipped")}}}}, Rows: 1}
}

func ptr(s string) *string { return &s }

func TestRun(t *testing.T) {
	ctx := context.Background()
	e := New()
	var recorded []byte
	var order []string
	r := &fakeRunner{}
	in := undoapi.SessionRun{Plan: plan(), Digest: plan().Digest, Runner: r,
		Record: func(_ context.Context, fp []byte) error { order = append(order, "record"); recorded = fp; return nil }}
	if _, err := e.Run(ctx, in); err != nil {
		t.Fatal(err)
	}
	order = append(order, r.calls...)
	if len(order) != 2 || order[0] != "record" || order[1] != "run:"+plan().Digest {
		t.Fatalf("the footprint is recorded before anything runs: %v", order)
	}
	var fp map[string]any
	if json.Unmarshal(recorded, &fp) != nil || len(recorded) == 0 {
		t.Fatalf("footprint %s", recorded)
	}

	// A plan that differs from what was approved: stale, nothing recorded or run.
	r2 := &fakeRunner{}
	stale := in
	stale.Runner, stale.Digest = r2, "sha256:bb"
	stale.Record = func(context.Context, []byte) error { t.Fatal("recorded a stale plan"); return nil }
	_, err := e.Run(ctx, stale)
	var ce *connector.Error
	if !errors.As(err, &ce) || ce.Code != undoapi.CodeUndoPlanStale || len(r2.calls) != 0 {
		t.Fatalf("stale: %v %v", err, r2.calls)
	}
	// An empty plan is stale too.
	empty := in
	empty.Plan = &undo.Plan{Digest: in.Digest}
	if _, err := e.Run(ctx, empty); !errors.As(err, &ce) {
		t.Fatal("an empty plan never runs")
	}
	// Incomplete wiring.
	for _, bad := range []undoapi.SessionRun{{}, {Plan: plan()}, {Plan: plan(), Runner: r}} {
		if _, err := e.Run(ctx, bad); !errors.Is(err, ErrIncomplete) {
			t.Fatalf("incomplete: %v", err)
		}
	}
	// The record fails: nothing runs.
	r3 := &fakeRunner{}
	rec := in
	rec.Runner, rec.Record = r3, func(context.Context, []byte) error { return errors.New("db down") }
	if _, err := e.Run(ctx, rec); !errors.As(err, &ce) || ce.Code != "internal_error" || len(r3.calls) != 0 {
		t.Fatalf("record failure: %v", err)
	}
	// A conflict from Postgres passes through unchanged (nothing was changed).
	conflict := &connector.Error{Kind: connector.KindInvalidRequest, Code: "undo_conflict", AgentMessage: "rolled back"}
	r4 := &fakeRunner{err: conflict}
	c := in
	c.Runner = r4
	if _, err := e.Run(ctx, c); !errors.Is(err, conflict) {
		t.Fatalf("conflict: %v", err)
	}
}
