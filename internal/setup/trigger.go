package setup

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	demosql "github.com/deedglass/deedglass/examples/demo/sql"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// Trigger is `deedglass init --trigger`: the quickstart's last step, on the stack init made. It puts
// the demo database's order statuses back to the seed's, turns on the Acme demo's legacy trigger
// (examples/demo/sql/10_trigger_on.sql: when one order ships, it ships the next 300 open ones too),
// sends the same one-row UPDATE of order 42 as the quickstart agent (postgres-safe asks a person;
// in dev the local owner countersigns), and waits for the Proven check, which should say Doesn't match:
// Postgres changed 301 rows, the agent reported 1. Then it turns the trigger off and shows the undo
// preview built from those 301 rows. Nothing is undone: the undo waits for a person, and Next says how.
//
// Only init's own demo container is touched (stack.json names it); the trigger is dropped again even
// when a step fails.
func Trigger(ctx context.Context, o Options) (*Result, error) {
	o.defaults()
	t0 := o.Now()
	x := &run{o: o, r: o.Runner}
	res := &Result{Object: "init_trigger_result", Dir: o.Dir}
	finish := func(err error) (*Result, error) {
		res.Steps, res.ElapsedMS = x.steps, o.Now().Sub(t0).Milliseconds()
		return res, err
	}
	step := func(s StepResult) bool {
		return x.add(s).Status != Failed
	}
	stack, err := ReadStack(o.Dir)
	if err != nil || stack == nil {
		step(fail("trigger_stack", "Stack", "there's no local stack in this directory", "Run deedglass init first, in the same directory."))
		return finish(ErrStep)
	}
	x.stack = stack
	x.admin, _ = ReadSecretFile(o.Dir, AdminKeyFile)
	x.agent, _ = ReadSecretFile(o.Dir, AgentKeyFile)
	if x.admin == "" || x.agent == "" || !x.r.Healthy(ctx, stack.Server()) {
		step(fail("trigger_stack", "Stack", "the local stack isn't running", "Run deedglass init, then deedglass init --trigger."))
		return finish(ErrStep)
	}
	dc := stack.DemoContainer
	sqlFile := func(name string) error {
		b, err := demosql.FS.ReadFile(name)
		if err != nil {
			return err
		}
		_, err = x.psqlIn(ctx, dc, "demo", string(b))
		return err
	}

	// 1 · order statuses back to the seed's (order 42 paid), so the trigger has 300 open orders to ship.
	if err := sqlFile("12_order_statuses.sql"); err != nil {
		step(fail("trigger_reset", "Orders reset", "the demo orders couldn't be reset: "+firstLine(err), "Run deedglass doctor."))
		return finish(ErrStep)
	}
	open, _ := x.psqlIn(ctx, dc, "demo", "SELECT count(*) FROM public.orders WHERE id <> 42 AND status <> 'shipped'")
	step(StepResult{ID: "trigger_reset", Title: "Orders reset", Status: Done, Detail: "order 42 paid · " + strings.TrimSpace(open) + " open orders besides 42"})

	// 2 · the legacy trigger on; off again on every way out.
	if err := sqlFile("10_trigger_on.sql"); err != nil {
		step(fail("trigger_on", "Trigger on", "the trigger couldn't be created: "+firstLine(err), "Run deedglass doctor."))
		return finish(ErrStep)
	}
	off := false
	triggerOff := func() StepResult {
		off = true
		if err := sqlFile("11_trigger_off.sql"); err != nil {
			return fail("trigger_off", "Trigger off", "the trigger couldn't be dropped: "+firstLine(err),
				"Run docker exec -i "+dc+" psql -U postgres -d demo -c 'DROP TRIGGER orders_ship_open_batch ON public.orders'.")
		}
		return StepResult{ID: "trigger_off", Title: "Trigger off", Status: Done, Detail: "orders_ship_open_batch dropped"}
	}
	defer func() {
		if !off {
			_ = triggerOff()
		}
	}()
	step(StepResult{ID: "trigger_on", Title: "Trigger on", Status: Done,
		Detail: "orders_ship_open_batch · when one order ships, it ships the next 300 open ones"})

	// 3 · the same one-row UPDATE as the smoke test, as the quickstart agent.
	c := &deedglass.Client{BaseURL: stack.Server(), APIKey: x.agent, UserAgent: "deedglass-init"}
	wait := 10 * time.Second
	const title = "UPDATE order 42"
	fix := "Run deedglass doctor, then deedglass init --trigger."
	a, _, err := c.Act(ctx, deedglass.ActRequest{Tool: "postgres.execute", Args: sqlArgs(SmokeUpdate), Wait: &wait, Client: "deedglass-init", Version: "0.1.0"})
	if err != nil {
		step(fail("trigger_update", title, "the UPDATE wasn't accepted: "+errText(err, nil), fix))
		return finish(ErrStep)
	}
	if a.Status == "pending_approval" && a.Approval != nil {
		step(StepResult{ID: "trigger_approval", Title: title, Status: Done, Symbol: "◐",
			Detail: "waiting for approval · countersigning as " + stack.OwnerEmail + " (dev only)"})
		if _, e2, err := x.r.Self(ctx, x.serverEnv(), "approve", a.Approval.ID, "--as", stack.OwnerEmail, "--reason", "deedglass init --trigger (dev only)"); err != nil {
			step(fail("trigger_update", title, "the countersign failed: "+lastLine(e2), fix))
			return finish(ErrStep)
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for a.State != "sealed" || a.Witness == nil || a.Witness.Verdict == nil {
		if time.Now().After(deadline) {
			step(fail("trigger_update", title, "The action didn't settle in 20 s.", "Run deedglass doctor."))
			return finish(ErrStep)
		}
		if a, err = c.GetAction(ctx, a.ID, 5*time.Second); err != nil {
			step(fail("trigger_update", title, "couldn't read the action: "+errText(err, nil), fix))
			return finish(ErrStep)
		}
	}
	cenv := x.clientEnv()
	show, _, _ := x.r.Self(ctx, cenv, "actions", "show", a.ID, "--diff")
	check, diff := splitShow(show)
	if check == "" {
		check = witnessLine(a)
	}
	if *a.Witness.Verdict != "MISMATCH" {
		step(fail("trigger_update", title, "expected Doesn't match, got "+check, "Run deedglass decoder status --connector "+ConnectorName+", then deedglass init --trigger."))
		return finish(ErrStep)
	}
	step(StepResult{ID: "trigger_update", Title: title, Status: Done, Symbol: "✕", Detail: check, Lines: diff})

	// 4 · trigger off (the undo checks that no trigger would fire again), then the undo preview.
	if s := triggerOff(); !step(s) {
		return finish(ErrStep)
	}
	out, e2, err := x.r.Self(ctx, cenv, "undo", "preview", a.ID)
	if err != nil {
		step(fail("trigger_undo", "Undo preview", "the undo preview failed: "+firstNonEmpty(lastLine(e2), lastLine(out)), "Run deedglass undo preview "+a.ID+"."))
		return finish(ErrStep)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "Next:") { // Next, below, says the same with the full plan
			lines = append(lines, l)
		}
	}
	head := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[0]), "✓"))
	step(StepResult{ID: "trigger_undo", Title: "Undo preview", Status: Done, Detail: head, Lines: trimLines(lines[1:])})
	plan := ""
	if js, _, err := x.r.Self(ctx, cenv, "--json", "undo", "preview", a.ID); err == nil {
		var pv struct {
			PlanDigest *string `json:"plan_digest"`
		}
		if json.Unmarshal([]byte(js), &pv) == nil && pv.PlanDigest != nil {
			plan = *pv.PlanDigest
		}
	}
	res.Changed = true
	run := "deedglass undo run " + a.ID
	if plan != "" {
		run += " --plan " + plan
	}
	res.Next = []string{
		"export DEEDGLASS_API_KEY=\"$(cat .deedglass/admin.key)\"",
		run + " --reason \"Put the 300 extra orders back\"",
		"It waits for a person: countersign it at " + stack.Server() + "/app/approvals as " + stack.OwnerEmail + " (the sign-in link is in .deedglass/serve.log)",
	}
	return finish(nil)
}

var reCheck = regexp.MustCompile(`^\s*check\s+(.*)$`)

// splitShow takes `deedglass actions show --diff` text apart: the check line's text, and the lines
// after it (expected, observed and the row table), without the record lines.
func splitShow(out string) (string, []string) {
	var check string
	var rest []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if check == "" {
			if m := reCheck.FindStringSubmatch(l); m != nil {
				check = strings.TrimSpace(m[1])
			}
			continue
		}
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "record ") || strings.HasPrefix(t, "Proven by Postgres") {
			continue
		}
		rest = append(rest, strings.TrimPrefix(l, "  "))
	}
	return check, trimLines(rest)
}

// trimLines drops leading and trailing blank lines.
func trimLines(ls []string) []string {
	for len(ls) > 0 && strings.TrimSpace(ls[0]) == "" {
		ls = ls[1:]
	}
	for len(ls) > 0 && strings.TrimSpace(ls[len(ls)-1]) == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}
