package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deedglass/deedglass/pkg/deedglass"
)

// The three smoke actions (S32 §3.9 step 11), as the quickstart agent: a read (L0 VERIFIED), a
// DROP TABLE (denied by block-destructive-sql before Postgres sees it) and an UPDATE of order 42
// (postgres-safe sends it to approval; init countersigns it as the local owner through the dev-only
// approve --as, then waits for L2 VERIFIED).
const (
	SmokeRead   = "SELECT count(*) AS orders FROM public.orders"
	SmokeDrop   = "DROP TABLE public.orders"
	SmokeUpdate = "UPDATE public.orders SET status = 'shipped' WHERE id = 42"
)

func sqlArgs(sql string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"sql": sql})
	return b
}

func witnessLine(a *deedglass.Action) string {
	if a == nil || a.Witness == nil || a.Witness.Verdict == nil {
		return "no verdict yet"
	}
	return a.Witness.Level + " · " + *a.Witness.Verdict
}

func deedOf(a *deedglass.Action) string {
	if a == nil || a.Deed == nil {
		return ""
	}
	d := *a.Deed
	if len(d) > 10 {
		d = d[:9] + "…"
	}
	return " · " + d
}

func (x *run) smoke(ctx context.Context) []StepResult {
	c := &deedglass.Client{BaseURL: x.stack.Server(), APIKey: x.agent, UserAgent: "deedglass-init"}
	wait := 10 * time.Second
	act := func(tool, sql string) (*deedglass.Action, error) {
		a, _, err := c.Act(ctx, deedglass.ActRequest{Tool: tool, Args: sqlArgs(sql), Wait: &wait, Client: "deedglass-init", Version: "0.1.0"})
		return a, err
	}
	var out []StepResult
	last := time.Now()
	stamp := func(r StepResult) StepResult {
		r.TookMS = time.Since(last).Milliseconds()
		last = time.Now()
		return r
	}
	fix := "Run deedglass doctor, then deedglass init --smoke."
	// 1 · a read
	a, err := act("postgres.query", SmokeRead)
	if err != nil || a.Status != "executed" {
		return append(out, stamp(fail("smoke_read", "Smoke · postgres.query", "the read didn't run: "+errText(err, a), fix)))
	}
	out = append(out, stamp(StepResult{ID: "smoke_read", Title: "Smoke · postgres.query", Status: Done, Detail: witnessLine(a) + deedOf(a)}))
	// 2 · DROP TABLE: denied
	_, err = act("postgres.execute", SmokeDrop)
	var de *deedglass.Error
	if !errors.As(err, &de) || de.Code != "policy_denied" {
		return append(out, stamp(fail("smoke_drop", "Smoke · DROP TABLE", "DROP TABLE wasn't denied: "+errText(err, nil), fix)))
	}
	out = append(out, stamp(StepResult{ID: "smoke_drop", Title: "Smoke · DROP TABLE", Status: Done, Symbol: "✕", Detail: de.Message,
		Lines: []string{"That ✕ is the point: Deedglass stopped it before Postgres saw it."}}))
	// 3 · UPDATE order 42: approval, countersigned as the local owner (dev only), then L2.
	a, err = act("postgres.execute", SmokeUpdate)
	if err != nil {
		return append(out, stamp(fail("smoke_update", "Smoke · UPDATE 1 row", "the UPDATE wasn't accepted: "+errText(err, nil), fix)))
	}
	if a.Status == "pending_approval" && a.Approval != nil {
		out = append(out, stamp(StepResult{ID: "smoke_approval", Title: "Smoke · UPDATE 1 row", Status: Done, Symbol: "◐",
			Detail: "waiting for approval · countersigning as " + x.stack.OwnerEmail + " (dev only)"}))
		if _, e2, err := x.r.Self(ctx, x.serverEnv(), "approve", a.Approval.ID, "--as", x.stack.OwnerEmail, "--reason", "deedglass init smoke test (dev only)"); err != nil {
			return append(out, stamp(fail("smoke_update", "Smoke · UPDATE 1 row", "the countersign failed: "+lastLine(e2), fix)))
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for a.State != "sealed" || a.Witness == nil || a.Witness.Verdict == nil {
		if time.Now().After(deadline) {
			return append(out, stamp(fail("smoke_update", "Smoke · UPDATE 1 row", "The smoke action didn't settle in 15 s.", "Run deedglass doctor.")))
		}
		if a, err = c.GetAction(ctx, a.ID, 5*time.Second); err != nil {
			return append(out, stamp(fail("smoke_update", "Smoke · UPDATE 1 row", "couldn't read the action: "+errText(err, nil), fix)))
		}
	}
	st := Done
	sym := ""
	if v := *a.Witness.Verdict; v != "VERIFIED" {
		sym = "○"
	}
	out = append(out, stamp(StepResult{ID: "smoke_update", Title: "Smoke · UPDATE 1 row", Status: st, Symbol: sym,
		Detail: witnessLine(a) + " · Postgres committed the row in public.orders" + deedOf(a)}))
	return out
}

func errText(err error, a *deedglass.Action) string {
	if err != nil {
		var de *deedglass.Error
		if errors.As(err, &de) {
			return strings.TrimSuffix(de.Message, ".")
		}
		return err.Error()
	}
	if a != nil {
		return fmt.Sprintf("status %s", a.Status)
	}
	return "no answer"
}
