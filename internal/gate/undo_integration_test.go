//go:build integration

package gate

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

// TestConsolePrincipal is S23 §6's "Auth and isolation" and "Races" rows at the Gate: a person's
// action through the console system agent runs while Stop all is on (Stop all skips it), only the
// console principal may name the action it undoes, at most one live undo per action holds the
// slot (a second one is ErrUndoTaken), an undo that ends without applying frees it, and stopping
// the console agent on its own denies its actions with agent_stopped.
func TestConsolePrincipal(t *testing.T) {
	w := newWorld(t, worldOpts{agents: 1})
	// The Gate's internal resolver: here the fake connector (the undo executor in production).
	w.p.o.Internal = connector.NewRegistry(w.fake)
	console := ids.New[ids.AgentKind]()
	if err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO deedglass.agents (id, org_id, name, description, labels, budgets, created_by, kind)
		    VALUES ($1, $2, 'console', 'Actions a person starts.', '{}', '{}', 'system', 'system')`, console, w.org)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	person := principal{Kind: httpx.KindConsole, OrgID: w.org, AgentID: console, UserID: w.owner, AgentName: "console", AgentStatus: "active"}
	orig := w.must(0, req("fake.echo", `{"message":"original"}`))
	undoOf := func(tool, args string) (*Action, error) {
		r := req(tool, args)
		r.UndoesActionID, r.OnBehalfOf = orig.ID, w.owner.String()
		return w.p.Submit(bg, person, r)
	}
	setStop := func(on bool) {
		q := `UPDATE deedglass.orgs SET stopped_at = NULL, stopped_by = NULL WHERE id = $1`
		if on {
			q = `UPDATE deedglass.orgs SET stopped_at = now(), stopped_by = 'operator' WHERE id = $1`
		}
		if err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, q, w.org); return err }); err != nil {
			t.Fatal(err)
		}
	}

	// An agent can't name an action it undoes.
	r := req("fake.echo", `{}`)
	r.UndoesActionID = orig.ID
	if _, err := w.p.Submit(bg, w.pr(0), r); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeInsufficientScope {
		t.Fatalf("an agent's undoes_action_id: %v", err)
	}

	// 1. Stop all is on: agents are refused, the console agent still acts.
	setStop(true)
	if a, _ := w.submit(0, req("fake.echo", `{}`)); a == nil || a.Status != "denied" {
		t.Fatalf("an agent during Stop all: %+v", a)
	}
	failed, err := undoOf("fake.fail", `{}`)
	if err != nil || failed.Status != "failed" {
		t.Fatalf("the console's action during Stop all: %+v %v", failed, err)
	}
	setStop(false)

	// 2. The failed one released its slot at the seal; the next one holds it.
	var released *string
	if err := w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT undo_released_at::text FROM deedglass.actions WHERE id = $1`, failed.ID).Scan(&released)
	}); err != nil || released == nil {
		t.Fatalf("a failed undo kept its slot: %v %v", released, err)
	}
	applied, err := undoOf("fake.echo", `{"message":"undo"}`)
	if err != nil || applied.Status != "executed" {
		t.Fatalf("the undo: %+v %v", applied, err)
	}
	if _, err := undoOf("fake.echo", `{"message":"again"}`); !errors.Is(err, ErrUndoTaken) {
		t.Fatalf("a second live undo: %v", err)
	}
	var undoes string
	if err := w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT undoes_action_id FROM deedglass.actions WHERE id = $1`, applied.ID).Scan(&undoes)
	}); err != nil || undoes != orig.ID {
		t.Fatalf("undoes_action_id = %q, %v", undoes, err)
	}

	// 3. Stopped on its own, the console agent is denied like any agent.
	if err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.agents SET status = 'stopped', status_changed_by = 'operator' WHERE id = $1`, console)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	other := w.must(0, req("fake.echo", `{"message":"another"}`))
	r = req("fake.echo", `{}`)
	r.UndoesActionID = other.ID
	a, _ := w.p.Submit(bg, person, r)
	if a == nil || a.Status != "denied" || a.Decision == nil || a.Decision.Code == nil || *a.Decision.Code != "agent_stopped" {
		t.Fatalf("a stopped console agent: %+v", a)
	}
}
