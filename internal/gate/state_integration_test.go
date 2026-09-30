//go:build integration

package gate_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// path returns the shortest list of states from Initial to s (excluding Initial).
func path(to gate.State) []gate.State {
	prev := map[gate.State]gate.State{}
	seen := map[gate.State]bool{gate.Initial: true}
	queue := []gate.State{gate.Initial}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for _, n := range s.Next() {
			if !seen[n] {
				seen[n], prev[n] = true, s
				queue = append(queue, n)
			}
		}
	}
	var out []gate.State
	for s := to; s != gate.Initial; s = prev[s] {
		out = append([]gate.State{s}, out...)
	}
	return out
}

// move sets the state and that state's timestamp column, as S13's Transition will.
func move(ctx context.Context, tx pgx.Tx, id ids.ActionID, to gate.State) error {
	q := `UPDATE deedglass.actions SET state = $2 WHERE id = $1`
	if to != gate.Received {
		q = fmt.Sprintf(`UPDATE deedglass.actions SET state = $2, %s = pg_catalog.clock_timestamp() WHERE id = $1`,
			pgx.Identifier{string(to) + "_at"}.Sanitize())
	}
	_, err := tx.Exec(ctx, q, id, string(to))
	return err
}

// TestTriggerMatchesStateGo checks all 15×15 pairs: the trigger accepts exactly gate.CanTransition,
// plus an UPDATE that keeps the state; every other pair is DG004.
func TestTriggerMatchesStateGo(t *testing.T) {
	ctx := context.Background()
	e := storetest.New(t, storetest.Options{})
	o, _, _, err := e.DB.CreateOrg(ctx, store.NewOrg{Name: "States", Slug: "states", OwnerEmail: "s@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	pairs, allowed := 0, 0
	err = e.DB.OrgTx(ctx, o.ID, func(ctx context.Context, tx pgx.Tx) error {
		ag, err := store.CreateAgent(ctx, tx, o.ID, store.NewAgent{Name: "walker"})
		if err != nil {
			return err
		}
		for _, from := range gate.States() {
			for _, to := range gate.States() {
				sp, err := tx.Begin(ctx)
				if err != nil {
					return err
				}
				id, err := storetest.NewAction(ctx, sp, o.ID, ag.ID)
				if err != nil {
					return err
				}
				for _, s := range path(from) {
					if err := move(ctx, sp, id, s); err != nil {
						return fmt.Errorf("walking to %s at %s: %w", from, s, err)
					}
				}
				err = move(ctx, sp, id, to)
				want := gate.CanTransition(from, to) || from == to
				switch {
				case want && err != nil:
					t.Errorf("%s → %s: refused (%v), but state.go allows it", from, to, err)
				case !want && store.SQLState(err) != store.CodeIllegalTransition:
					t.Errorf("%s → %s: got %v, want DG004", from, to, err)
				}
				if gate.CanTransition(from, to) {
					allowed++
				}
				pairs++
				if err := sp.Rollback(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pairs != 225 || allowed != 22 {
		t.Fatalf("%d pairs, %d allowed", pairs, allowed)
	}
	t.Logf("%d pairs checked · %d allowed", pairs, allowed)
}

func TestTriggerRefusals(t *testing.T) {
	ctx := context.Background()
	e := storetest.New(t, storetest.Options{})
	o, _, _, err := e.DB.CreateOrg(ctx, store.NewOrg{Name: "States", Slug: "states", OwnerEmail: "s@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.DB.OrgTx(ctx, o.ID, func(ctx context.Context, tx pgx.Tx) error {
		ag, err := store.CreateAgent(ctx, tx, o.ID, store.NewAgent{Name: "walker"})
		if err != nil {
			t.Fatal(err)
		}
		// sealed without sealed_at: the edge is legal, the row isn't.
		id, _ := storetest.NewAction(ctx, tx, o.ID, ag.ID)
		for _, s := range path(gate.Verified) {
			if err := move(ctx, tx, id, s); err != nil {
				t.Fatal(err)
			}
		}
		sp, _ := tx.Begin(ctx)
		_, err = sp.Exec(ctx, `UPDATE deedglass.actions SET state = 'sealed' WHERE id = $1`, id)
		if store.SQLState(err) != store.CodeCheckViolation {
			t.Errorf("sealed without sealed_at: %v, want 23514", err)
		}
		_ = sp.Rollback(ctx)
		// A new action can't start anywhere but received.
		sp, _ = tx.Begin(ctx)
		_, err = sp.Exec(ctx, `INSERT INTO deedglass.actions (id, org_id, agent_id, mode, tool, request_digest, state, sealed_at)
		    VALUES ($1, $2, $3, 'gateway', 't', $4, 'sealed', pg_catalog.now())`, ids.New[ids.ActionKind](), o.ID, ag.ID, storetest.Digest([]byte("x")))
		if store.SQLState(err) != store.CodeIllegalTransition {
			t.Errorf("insert in sealed: %v, want DG004", err)
		}
		_ = sp.Rollback(ctx)
		// The exact message for sealed → executing.
		if err := move(ctx, tx, id, gate.Sealed); err != nil {
			t.Fatal(err)
		}
		sp, _ = tx.Begin(ctx)
		err = move(ctx, sp, id, gate.Executing)
		if want := fmt.Sprintf("deedglass: action %s cannot move from sealed to executing", id); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("got %v, want %q", err, want)
		}
		_ = sp.Rollback(ctx)
		return nil
	})
}
