//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/witness"
)

// S19 §6: the Postgres row read-back, live (PG 18 in make integration; 16 and 17 in make matrix).

// typedSchema is a table with the column types the typed comparison must handle.
const typedSchema = `
DROP TABLE IF EXISTS public.typed;
DROP TYPE IF EXISTS public.ship_state;
CREATE TYPE public.ship_state AS ENUM ('pending', 'packed', 'shipped');
CREATE TABLE public.typed (id integer PRIMARY KEY, flag boolean NOT NULL DEFAULT false, amount numeric(10,2) NOT NULL DEFAULT 0,
  state public.ship_state NOT NULL DEFAULT 'pending', label text NOT NULL DEFAULT '', email text NOT NULL UNIQUE);
INSERT INTO public.typed (id, email) SELECT g, 'u' || g || '@example.test' FROM generate_series(1, 10) AS g;
GRANT SELECT, UPDATE, DELETE ON public.typed TO deedglass_agent;
`

// rbObserver adapts the connector's ReadBack to the Witness, the way the Gate does.
type rbObserver struct {
	c    *Connector
	in   *connector.Instance
	call connector.Call
	res  connector.Result
}

func (o rbObserver) ReadBack(ctx context.Context, _ witness.Subject, _ witness.ReadRequest, _ int) (witness.Observation, error) {
	obs, err := o.c.ReadBack(ctx, o.in, o.call, o.res)
	var skip *connector.ErrReadBackSkipped
	switch ce := connector.AsError(err); {
	case err == nil:
		var body any
		_ = json.Unmarshal(obs.Body, &body)
		return witness.Observation{Body: body}, nil
	case errors.As(err, &skip):
		return witness.Observation{}, &witness.SkipError{Why: skip.Why}
	case ce != nil && ce.Kind == connector.KindNotFound:
		return witness.Observation{}, witness.ErrNotFound
	case ce != nil:
		return witness.Observation{}, &witness.ReadError{Code: ce.Code, Err: err}
	}
	return witness.Observation{}, err
}

// writeAndReadBack runs sql through postgres.execute, then the Witness with the row read-back.
func writeAndReadBack(t *testing.T, c *Connector, in *connector.Instance, sql string, params ...any) (witness.Outcome, connector.Result) {
	t.Helper()
	ps, _ := json.Marshal(params)
	if params == nil {
		ps = []byte("[]")
	}
	args, _ := json.Marshal(map[string]any{"sql": sql, "params": json.RawMessage(ps)})
	var spec connector.ToolSpec
	tools, _ := c.Tools(context.Background(), in)
	for _, s := range tools {
		if s.Name == ToolExecute {
			spec = s
		}
	}
	call := connector.Call{ActionID: ids.New[ids.ActionKind](), OrgID: in.OrgID, AgentID: ids.New[ids.AgentKind](), Tool: spec,
		Qualified: "postgres.execute", Args: args, Deadline: time.Now().Add(30 * time.Second)}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := c.Execute(ctx, in, call)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	sub := witness.Subject{Tool: "postgres.execute", ConnectorType: "postgres", Mode: witness.ModeGateway, Args: args, Required: witness.L1,
		Result: witness.ResultView{Status: witness.StatusSucceeded, Output: res.Output, OutputBytes: len(res.Output), Evidence: res.Evidence},
		Spec:   witness.ToolWitnessSpec{Connector: "postgres", Tool: "execute", PostgresRow: true, DefaultSchema: "public"}}
	rctx, rcancel := witness.WithBudget(context.Background(), 5*time.Second)
	defer rcancel()
	out, _, err := witness.Run(rctx, witness.Input{Subject: sub, Observer: rbObserver{c: c, in: in, call: call, res: res}, Final: true})
	if err != nil {
		t.Fatal(err)
	}
	return out, res
}

func l1Codes(o witness.Outcome) []string {
	var out []string
	for _, r := range o.Decision.Reasons {
		if r.Level == witness.L1 {
			out = append(out, string(r.Code))
		}
	}
	return out
}

func TestPostgresReadBack(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	admin := d.Admin(t)
	if _, err := admin.Exec(ctx, typedSchema); err != nil {
		t.Fatal(err)
	}
	c := New(nil)
	defer c.Close()
	in := inst(t, c, pgtest.ConfigJSON, d.AgentDSN, d.WitnessDSN, devEnv())

	t.Run("row written", func(t *testing.T) {
		var readOnly, user string
		readBackConnHook = func(ctx context.Context, conn *pgx.Conn) {
			_ = conn.QueryRow(ctx, "SELECT current_setting('transaction_read_only'), current_user").Scan(&readOnly, &user)
		}
		defer func() { readBackConnHook = nil }()
		out, _ := writeAndReadBack(t, c, in, "UPDATE public.orders SET status = 'shipped' WHERE id = 42")
		if out.Decision.Level != witness.L1 || out.Decision.Verdict != witness.Verified || out.Decision.Method != witness.MethodPostgresRow ||
			len(l1Codes(out)) != 1 || l1Codes(out)[0] != "row_written" {
			t.Fatalf("%+v", out.Decision)
		}
		if readOnly != "on" || user != "deedglass_agent" {
			t.Fatalf("the read-back ran with transaction_read_only=%s as %s", readOnly, user)
		}
	})
	t.Run("trigger rewrites the value", func(t *testing.T) {
		if _, err := admin.Exec(ctx, `CREATE FUNCTION public.it_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.status := 'cancelled'; RETURN NEW; END $$;
		    CREATE TRIGGER it_rewrite BEFORE UPDATE ON public.orders FOR EACH ROW EXECUTE FUNCTION public.it_rewrite();`); err != nil {
			t.Fatal(err)
		}
		defer admin.Exec(ctx, `DROP TRIGGER it_rewrite ON public.orders; DROP FUNCTION public.it_rewrite();`)
		out, _ := writeAndReadBack(t, c, in, "UPDATE public.orders SET status = 'shipped' WHERE id = 43")
		r := out.Decision.Reasons
		if out.Decision.Verdict != witness.Mismatch || out.Decision.Level != witness.L1 || l1Codes(out)[0] != "value_mismatch" {
			t.Fatalf("%+v", out.Decision)
		}
		for _, x := range r {
			if x.Code == witness.CodeValueMismatch && (x.Diff[0].Path != "status" || x.Diff[0].Expected != "shipped" || x.Diff[0].Observed != "cancelled") {
				t.Fatalf("diff %+v", x.Diff)
			}
		}
	})
	t.Run("another session wrote the row since", func(t *testing.T) {
		readBackHook = func(ctx context.Context) {
			if _, err := admin.Exec(ctx, "UPDATE public.orders SET total_cents = total_cents + 1 WHERE id = 44"); err != nil {
				t.Error(err)
			}
		}
		defer func() { readBackHook = nil }()
		out, _ := writeAndReadBack(t, c, in, "UPDATE public.orders SET status = 'shipped' WHERE id = 44")
		if out.Decision.Verdict == witness.Mismatch || l1Codes(out)[0] != "row_superseded" {
			t.Fatalf("a concurrent writer must be a gap, never a MISMATCH: %+v", out.Decision)
		}
	})
	t.Run("row deleted before the read-back", func(t *testing.T) {
		readBackHook = func(ctx context.Context) {
			if _, err := admin.Exec(ctx, "DELETE FROM public.typed WHERE id = 9"); err != nil {
				t.Error(err)
			}
		}
		defer func() { readBackHook = nil }()
		out, _ := writeAndReadBack(t, c, in, "UPDATE public.typed SET label = 'x' WHERE id = 9")
		if out.Decision.Verdict == witness.Mismatch || l1Codes(out)[0] != "row_missing" {
			t.Fatalf("%+v", out.Decision)
		}
	})
	t.Run("typed values: boolean, numeric, enum, params", func(t *testing.T) {
		out, _ := writeAndReadBack(t, c, in, "UPDATE public.typed SET flag = 't', amount = 10.50, state = 'shipped' WHERE id = 1")
		if out.Decision.Verdict != witness.Verified || l1Codes(out)[0] != "row_written" {
			t.Fatalf("%+v", out.Decision)
		}
		out, _ = writeAndReadBack(t, c, in, "UPDATE public.typed SET label = $2, amount = $3 WHERE id = $1", 2, "packed", "7.5")
		if out.Decision.Verdict != witness.Verified || l1Codes(out)[0] != "row_written" {
			t.Fatalf("params: %+v", out.Decision)
		}
		// A computed value isn't compared: the row is still confirmed by its xmin.
		out, _ = writeAndReadBack(t, c, in, "UPDATE public.typed SET amount = amount + 1 WHERE id = 3")
		if out.Decision.Verdict != witness.Verified {
			t.Fatalf("expr: %+v", out.Decision)
		}
	})
	t.Run("not the primary key", func(t *testing.T) {
		out, _ := writeAndReadBack(t, c, in, "UPDATE public.typed SET label = 'y' WHERE email = 'u4@example.test'")
		if out.Decision.Level != witness.L0 || l1Codes(out)[0] != "readback_skipped" {
			t.Fatalf("%+v", out.Decision)
		}
	})
	t.Run("not a single-row update", func(t *testing.T) {
		out, _ := writeAndReadBack(t, c, in, "UPDATE public.typed SET label = 'z' WHERE id IN (5, 6)")
		if out.Decision.Level != witness.L0 || l1Codes(out)[0] != "readback_skipped" {
			t.Fatalf("%+v", out.Decision)
		}
	})
}
