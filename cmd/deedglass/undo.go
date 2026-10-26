package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/deedglass/deedglass/internal/witness"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/undoapi"
	"github.com/deedglass/deedglass/internal/witness/undo"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// deedglass undo preview|run (S23 §5.1): an org admin key, through the API. Exit codes: 0 available
// (preview) or executed and VERIFIED (run); 1 unavailable, failed or MISMATCH; 2 pending or a
// usage error; 3 not authenticated.

// --- undo preview ---

var undoPreviewValues struct {
	allowTriggers bool
	steps         int
}

func undoPreviewFlags(fs *flag.FlagSet) {
	undoPreviewValues = struct {
		allowTriggers bool
		steps         int
	}{}
	fs.BoolVar(&undoPreviewValues.allowTriggers, "allow-triggers", false, "let triggers on the tables fire again (the check becomes a warning)")
	fs.IntVar(&undoPreviewValues.steps, "steps", 0, "list this many steps (1 to 1000; default 2 in text, 20 in --json)")
}

func runUndoPreview(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass undo preview", undoPreviewFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr(errors.New("deedglass undo preview needs one action id"), "deedglass undo preview act_…")
	}
	id := strings.ToLower(strings.TrimSpace(pos[0]))
	if undoPreviewValues.steps < 0 || undoPreviewValues.steps > undoapi.MaxStepsLimit {
		return usageErr(errors.New("--steps is a number from 1 to 1000"), "deedglass undo preview "+clean(id)+" --steps 20")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	path := "/v1/actions/" + url.PathEscape(id) + "/undo/preview"
	if n := undoPreviewValues.steps; n > 0 {
		path += "?steps_limit=" + strconv.Itoa(n)
	}
	var raw json.RawMessage
	if _, err := c.Do(ctx, "POST", path, map[string]any{"allow_triggers": undoPreviewValues.allowTriggers}, &raw); err != nil {
		return apiErr(e, c, err)
	}
	var pv undoapi.PreviewJSON
	if err := json.Unmarshal(raw, &pv); err != nil {
		return err
	}
	if e.Globals.JSON {
		if _, err := e.Stdout.Write(append(raw, '\n')); err != nil {
			return err
		}
	} else {
		shown := undoPreviewValues.steps
		if shown == 0 {
			shown = 2
		}
		printUndoPreview(e.Stdout, e.Style, &pv, shown, invocation(e))
	}
	if !pv.Available {
		return &ExitError{Code: ExitFailure, ErrCode: "undo_unavailable", printed: true, Err: errors.New("undo isn't available")}
	}
	return nil
}

// printUndoPreview writes the preview the way S23 §5.1 shows it: a calm checklist, one fix per line.
func printUndoPreview(w io.Writer, st ui.Style, pv *undoapi.PreviewJSON, shown int, cmd string) {
	short := undo.ShortID(pv.Action)
	if !pv.Available {
		head := "Undo not available " + ui.Sep + " " + clean(short)
		if pv.Tool != "" {
			head += " " + ui.Sep + " " + clean(pv.Tool)
		}
		fmt.Fprintln(w, st.FailLine(head))
		if pv.Rows > 0 {
			fmt.Fprintln(w, "  "+undo.SummaryOf(pv.Rows, pv.Tables, "this action"))
		}
		for _, c := range pv.Checks {
			if c.Status == undo.StatusSkip {
				continue
			}
			fmt.Fprintln(w, checkLine(st, c))
			pad := strings.Repeat(" ", 22)
			if c.Status == undo.StatusFail && c.FixSQL != "" {
				for i, l := range strings.Split(c.FixSQL, "\n") {
					if i == 0 {
						fmt.Fprintln(w, pad+"Fix: "+clean(l))
					} else {
						fmt.Fprintln(w, pad+"     "+clean(l))
					}
				}
			} else if c.Status == undo.StatusFail && c.Fix != "" && c.Fix != undo.FixFailing {
				fmt.Fprintln(w, pad+clean(c.Fix))
			}
		}
		fmt.Fprintln(w, "  Next: "+undoNext(pv, cmd))
		return
	}
	fmt.Fprintln(w, st.OKLine("Undo available "+ui.Sep+" "+clean(pv.Headline)))
	row := func(k, v string) { fmt.Fprintf(w, "  %s %s\n", ui.Pad(k, 8), v) }
	if pv.PlanDigest != nil {
		row("plan", shortDigest(*pv.PlanDigest))
	}
	for i, l := range sampleLines(pv, shown) {
		k := ""
		if i == 0 {
			k = "sample"
		}
		row(k, l)
	}
	for i, n := range pv.Notes {
		k := ""
		if i == 0 {
			k = "notes"
		}
		row(k, clean(n))
	}
	for _, c := range pv.Checks {
		if c.Status == undo.StatusWarn {
			row("warning", st.Amber(ui.Unknown)+" "+clean(c.Detail))
		}
	}
	if p := pv.Policy; p != nil {
		row("policy", policyWords(st, p))
	}
	fmt.Fprintln(w, "  Next: "+undoNext(pv, cmd))
}

// checkLine is one check: its symbol, its label, its detail.
func checkLine(st ui.Style, c undo.Check) string {
	sym := st.Teal(ui.OK)
	switch c.Status {
	case undo.StatusFail:
		sym = st.Red(ui.Fail)
	case undo.StatusWarn:
		sym = st.Amber(ui.Unknown)
	}
	label := strings.ToLower(strings.ReplaceAll(c.ID, "_", " "))
	if c.Detail == "" {
		return "  " + sym + " " + label
	}
	return "  " + sym + " " + ui.Pad(label, 18) + clean(c.Detail)
}

// sampleLines are the first n steps: "id 42   status  shipped → paid", then "… 299 more".
func sampleLines(pv *undoapi.PreviewJSON, n int) []string {
	var out []string
	data := pv.Steps.Data
	for i, s := range data {
		if i >= n {
			break
		}
		out = append(out, clean(stepText(s)))
	}
	if more := pv.Steps.Total - len(out); more > 0 && len(out) > 0 {
		out[len(out)-1] += "      … " + undo.Thousands(more) + " more"
	}
	return out
}

func stepText(s undoapi.PreviewStep) string {
	keys := sortedKeys(s.Key)
	var k []string
	for _, c := range keys {
		k = append(k, c+" "+s.Key[c])
	}
	head := ui.Pad(strings.Join(k, " "), 7)
	switch s.Op {
	case string(undo.OpUpdate):
		var parts []string
		for _, c := range sortedKeys(s.Set) {
			parts = append(parts, c+"  "+valueWord(s.Expect[c])+" → "+valueWord(s.Set[c]))
		}
		return head + " " + strings.Join(parts, ", ")
	case string(undo.OpDelete):
		return head + " remove the row the action created"
	}
	return head + " put back the row the action removed"
}

func valueWord(v *string) string {
	if v == nil {
		return "NULL"
	}
	return *v
}

func policyWords(st ui.Style, p *undoapi.PolicyJSON) string {
	rule := "default"
	if p.RuleID != nil {
		rule = *p.RuleID
	}
	switch p.Decision {
	case "require_approval":
		return clean(rule) + " " + ui.Sep + " a person countersigns first"
	case "allow":
		return clean(rule) + " " + ui.Sep + " runs without a countersign"
	}
	why := ""
	if p.Reason != nil {
		why = ": " + strings.TrimSuffix(*p.Reason, ".")
	}
	return st.Red(ui.Fail) + " " + clean(rule) + " " + ui.Sep + " denied" + clean(why)
}

func undoNext(pv *undoapi.PreviewJSON, cmd string) string {
	if !pv.Available {
		return "fix the failing check, then preview again."
	}
	d := "<plan>"
	if pv.PlanDigest != nil {
		d = *pv.PlanDigest
	}
	return cmd + " undo run " + clean(pv.Action) + " --plan " + clean(d) + ` --reason "…"`
}

// --- undo run ---

var undoRunValues struct {
	plan, reason  string
	allowTriggers bool
	wait          time.Duration
}

func undoRunFlags(fs *flag.FlagSet) {
	undoRunValues = struct {
		plan, reason  string
		allowTriggers bool
		wait          time.Duration
	}{}
	fs.StringVar(&undoRunValues.plan, "plan", "", "the preview's plan digest (sha256:…)")
	fs.StringVar(&undoRunValues.reason, "reason", "", "why; the approver and the Ledger read it (up to 280 characters)")
	fs.BoolVar(&undoRunValues.allowTriggers, "allow-triggers", false, "let triggers on the tables fire again (admins only)")
	fs.DurationVar(&undoRunValues.wait, "wait", 0, "wait up to this long for the outcome (at most 25s)")
}

func runUndoRun(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass undo run", undoRunFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr(errors.New("deedglass undo run needs one action id"), `deedglass undo run act_… --plan sha256:… --reason "…"`)
	}
	id := strings.ToLower(strings.TrimSpace(pos[0]))
	if undoRunValues.plan == "" {
		return usageErr(errors.New("--plan is the preview's plan digest"), "deedglass undo preview "+clean(id))
	}
	if strings.TrimSpace(undoRunValues.reason) == "" {
		return usageErr(errors.New(strings.TrimSuffix(undo.MsgReason, ".")), `deedglass undo run `+clean(id)+` --plan … --reason "Trigger bug touched 300 extra orders."`)
	}
	if undoRunValues.wait < 0 || undoRunValues.wait > 25*time.Second {
		return usageErr(errors.New("--wait is at most 25s"), "deedglass undo run … --wait 25s")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	body := map[string]any{"plan_digest": undoRunValues.plan, "reason": undoRunValues.reason, "allow_triggers": undoRunValues.allowTriggers}
	if undoRunValues.wait > 0 {
		body["wait"] = undoRunValues.wait.Seconds()
	}
	var raw json.RawMessage
	if _, err := c.Do(ctx, "POST", "/v1/actions/"+url.PathEscape(id)+"/undo", body, &raw, deedglass.WithIdempotencyKey(undoIdemKey(id))); err != nil {
		return apiErr(e, c, err)
	}
	var a ledger.Action
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	if e.Globals.JSON {
		if _, err := e.Stdout.Write(append(raw, '\n')); err != nil {
			return err
		}
	} else {
		printUndoAction(e.Stdout, e.Style, &a, id, undoNextLines(ctx, e, &a))
	}
	return undoExit(&a)
}

// undoIdemKey is one key per command run: a retry of the same request (a lost answer) replays.
func undoIdemKey(id string) string {
	return "undo_" + id + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// undoNextLines are the lines under a pending undo (adoption-v1 F3): in dev on a local stack, the
// approve link for the approval it just made; otherwise how to get one. Then how to follow it.
func undoNextLines(ctx context.Context, e *Env, a *ledger.Action) []string {
	if a.Status != ledger.StatusPendingApproval {
		return nil
	}
	cmd := invocation(e)
	follow := "Then follow it: " + cmd + " actions show " + clean(a.ID) + " --wait"
	if a.Approval == nil {
		return []string{"Next: countersign it in Console › Approvals. " + follow}
	}
	apr := clean(a.Approval.ID)
	if link := pendingApprovalLink(ctx, e, a.Approval.ID); link != "" {
		return []string{"Approve it (one use, dev only): " + link, follow}
	}
	return []string{"Next: countersign it from the email, or print its link: " + cmd + " approvals open " + apr, follow}
}

// printUndoAction writes the undo action's state (S23 §5.1).
func printUndoAction(w io.Writer, st ui.Style, a *ledger.Action, undoes string, next []string) {
	switch a.Status {
	case ledger.StatusPendingApproval:
		fmt.Fprintln(w, st.Violet(ui.Pending)+" pending approval "+ui.Sep+" "+clean(a.Tool))
		if d := a.Decision; d != nil && d.RuleID != nil {
			line := fmt.Sprintf("  Waiting for approval under policy %q", *d.RuleID)
			if d.Reason != nil {
				line += ": " + clean(*d.Reason)
			}
			fmt.Fprintln(w, line)
		}
		apr := ""
		if a.Approval != nil {
			apr = "approval " + undo.ShortID(a.Approval.ID) + "  " + ui.Sep + "  "
		}
		fmt.Fprintln(w, "  "+apr+"undo "+undo.ShortID(a.ID))
		for _, l := range next {
			fmt.Fprintln(w, "  "+l)
		}
	case ledger.StatusProcessing:
		fmt.Fprintln(w, st.Violet(ui.Pending)+" running "+ui.Sep+" "+clean(a.Tool))
		fmt.Fprintln(w, "  Next: deedglass actions show "+clean(a.ID)+" --wait")
	case ledger.StatusExecuted:
		line := "executed " + ui.Sep + " " + clean(a.Tool)
		if a.Execution != nil && a.Execution.LatencyMS != nil {
			line += " " + ui.Sep + " " + strconv.FormatInt(*a.Execution.LatencyMS, 10) + " ms"
		}
		ok := a.Witness != nil && a.Witness.Verdict == "VERIFIED"
		if ok {
			fmt.Fprintln(w, st.OKLine(line))
		} else {
			fmt.Fprintln(w, st.FailLine(line))
		}
		fmt.Fprintf(w, "  %s %s\n", ui.Pad("undo", 8), undoResultText(a, undoes))
		if wt := a.Witness; wt != nil {
			t := witness.PlainLevel(wt.Level) + " " + ui.Sep + " " + witness.PlainVerdict(wt.Verdict)
			if wt.Summary != nil && *wt.Summary != "" {
				t += " " + ui.Sep + " " + clean(strings.TrimSuffix(*wt.Summary, "."))
			}
			fmt.Fprintf(w, "  %s %s\n", ui.Pad("witness", 8), t)
		}
		if a.Deed != nil {
			fmt.Fprintf(w, "  %s %s %s sealed\n", ui.Pad("record", 8), undo.ShortID(*a.Deed), ui.Sep)
		}
	default:
		fmt.Fprintln(w, st.FailLine(string(a.Status)+" "+ui.Sep+" "+clean(a.Tool)))
		if a.Execution != nil && a.Execution.ErrorMessage != nil {
			fmt.Fprintln(w, "  "+clean(*a.Execution.ErrorMessage))
		} else if d := a.Decision; d != nil && d.Message != nil {
			fmt.Fprintln(w, "  "+clean(*d.Message))
		}
		if a.Deed != nil {
			fmt.Fprintf(w, "  %s %s %s sealed\n", ui.Pad("record", 8), undo.ShortID(*a.Deed), ui.Sep)
		}
		fmt.Fprintln(w, "  Next: preview the undo again: deedglass undo preview "+clean(undoes))
	}
}

// undoResultText is "301 rows restored in public.orders · undoes act_01j9zw6k…".
func undoResultText(a *ledger.Action, undoes string) string {
	var out struct {
		Rows   int      `json:"rows_restored"`
		Tables []string `json:"tables"`
	}
	text := ""
	if a.Execution != nil && len(a.Execution.Result) > 0 && json.Unmarshal(a.Execution.Result, &out) == nil && out.Rows > 0 {
		text = undo.ResultText(out.Rows, out.Tables) + " " + ui.Sep + " "
	}
	return clean(text + "undoes " + undo.ShortID(undoes))
}

func undoExit(a *ledger.Action) error {
	switch {
	case a.Status == ledger.StatusExecuted && a.Witness != nil && a.Witness.Verdict == "VERIFIED":
		return nil
	case a.Status == ledger.StatusPendingApproval || a.Status == ledger.StatusProcessing:
		return &ExitError{Code: ExitUsage, ErrCode: "pending", printed: true, Err: errors.New("the undo is waiting")}
	}
	return &ExitError{Code: ExitFailure, ErrCode: "undo_not_verified", printed: true, Err: errors.New("the undo didn't run or wasn't verified")}
}
