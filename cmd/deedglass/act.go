package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/deedglass/deedglass/internal/witness"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/setup"
	"github.com/deedglass/deedglass/internal/witness/l2"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// deedglass act and deedglass actions cancel (S13): a dev helper that sends one action through the
// Gate with an agent key, and the cancel of a pending action. Both talk to the server's API.

type actOpts struct {
	args, mode, key string
	wait            time.Duration
}

var actValues actOpts

// actDefaultWait is how long deedglass act follows an action before it hands back with exit 2.
const actDefaultWait = 60 * time.Second

func actFlags(fs *flag.FlagSet) {
	o := &actValues
	*o = actOpts{}
	fs.StringVar(&o.args, "args", "{}", "the tool's arguments, a JSON object")
	fs.StringVar(&o.mode, "mode", "gateway", "gateway (Deedglass runs it) or proxy (you run it at the edge and report)")
	fs.DurationVar(&o.wait, "wait", actDefaultWait, "how long to wait for the action to finish")
	fs.StringVar(&o.key, "idempotency-key", "", "an Idempotency-Key, to make a retry safe")
}

// agentClient is apiClient for commands that act as an agent.
func agentClient(e *Env) (*deedglass.Client, error) {
	t := resolveServer(e)
	if err := t.conflictErr(); err != nil {
		return nil, err
	}
	server := t.Server
	key := t.keyFor(e, setup.AgentKeyFile)
	if key == "" {
		return nil, &ExitError{Code: ExitNotAuthed, ErrCode: "authentication_required",
			Err:  errors.New("deedglass act needs an agent's API key"),
			Next: "Set DEEDGLASS_API_KEY or pass --api-key. Create one with deedglass agent keys create <agent>."}
	}
	return &deedglass.Client{BaseURL: server, APIKey: key, UserAgent: "deedglass-cli/" + buildInfo().Version}, nil
}

// actBody is POST /v1/actions as the CLI sends it.
type actBody struct {
	Tool    string          `json:"tool"`
	Args    json.RawMessage `json:"args"`
	Mode    string          `json:"mode"`
	Wait    float64         `json:"wait"`
	Context map[string]any  `json:"context"`
}

func runAct(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass act", actFlags, args)
	if err != nil {
		return err
	}
	tool, err := oneArg("deedglass act", "a tool name", `deedglass act fake.echo --args '{"message":"hello"}'`, pos)
	if err != nil {
		return err
	}
	o := actValues
	if !json.Valid([]byte(o.args)) || !strings.HasPrefix(strings.TrimSpace(o.args), "{") {
		return usageErr(errors.New("--args must be a JSON object"), `For example --args '{"message":"hello"}'.`)
	}
	if o.mode != "gateway" && o.mode != "proxy" {
		return usageErr(fmt.Errorf("--mode must be gateway or proxy (got %q)", o.mode), "Leave it out for gateway.")
	}
	if o.wait < 0 {
		return usageErr(errors.New("--wait must be positive"), "For example --wait 30s.")
	}
	c, err := agentClient(e)
	if err != nil {
		return err
	}
	start := time.Now()
	body := actBody{Tool: tool, Args: json.RawMessage(o.args), Mode: o.mode, Wait: min(o.wait, gate.MaxWait).Seconds(),
		Context: map[string]any{"client": map[string]string{"name": "deedglass-cli", "version": buildInfo().Version}, "transport": "cli"}}
	var opts []deedglass.Option
	if o.key != "" {
		opts = append(opts, deedglass.WithIdempotencyKey(o.key))
	}
	var a ledger.Action
	_, err = c.Do(ctx, http.MethodPost, "/v1/actions", body, &a, opts...)
	var de *deedglass.Error
	if errors.As(err, &de) && len(de.Action) > 0 {
		if jerr := json.Unmarshal(de.Action, &a); jerr != nil {
			return apiErr(e, c, err)
		}
		return printAct(ctx, e, c, &a, de)
	}
	if err != nil {
		return apiErr(e, c, err)
	}
	// Still running: follow it until it finishes or --wait passes.
	for a.Status == ledger.StatusProcessing && time.Since(start) < o.wait {
		left := min(o.wait-time.Since(start), gate.MaxWait)
		var d ledger.Action
		if _, err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/v1/actions/%s?wait=%d", url.PathEscape(a.ID), max(int(left.Seconds()), 1)), nil, &d); err != nil {
			return apiErr(e, c, err)
		}
		d.Execution = keepResult(a.Execution, d.Execution)
		a = d
	}
	return printAct(ctx, e, c, &a, nil)
}

// hasL2Reason reports whether a witness lists an L2 gap or violation (an L2 wait ran).
func hasL2Reason(w *ledger.ActionWitness) bool {
	if w.Reasons == nil {
		return false
	}
	for _, r := range *w.Reasons {
		if r.Level == "L2" {
			return true
		}
	}
	return false
}

// actDetail reads the action's witness_detail (GET ?expand[]=witness.detail); nil when it can't.
func actDetail(ctx context.Context, c *deedglass.Client, id string) json.RawMessage {
	var d struct {
		Witness *struct {
			Detail json.RawMessage `json:"detail"`
		} `json:"witness"`
	}
	if _, err := c.Do(ctx, http.MethodGet, "/v1/actions/"+url.PathEscape(id)+"?expand[]=witness.detail", nil, &d); err != nil || d.Witness == nil {
		return nil
	}
	return d.Witness.Detail
}

// keepResult keeps the result the create call returned when a later read leaves it out.
func keepResult(prev, next *ledger.ActionExecution) *ledger.ActionExecution {
	if next != nil && prev != nil && next.Result == nil {
		next.Result = prev.Result
	}
	return next
}

// printAct renders an action (S13 §5) and returns its exit code: 0 executed (VERIFIED or
// UNVERIFIABLE) or authorized, 1 denied, failed, MISMATCH, expired or cancelled, 2 pending or still
// processing.
func printAct(ctx context.Context, e *Env, c *deedglass.Client, a *ledger.Action, refusal *deedglass.Error) error {
	code := actExit(a)
	if e.Globals.JSON {
		if err := writeAPIJSON(e, a); err != nil {
			return err
		}
		if code != ExitOK {
			return &ExitError{Code: code, ErrCode: a.Status, Err: errors.New(a.Status), printed: true}
		}
		return nil
	}
	s := e.Style
	ga := &gate.Action{Action: *a}
	row := func(k, v string) { fmt.Fprintf(e.Stdout, "  %s %s\n", ui.Pad(k, 8), v) }
	head := func(sym, word string) {
		line := sym + " " + word + " " + ui.Sep + " " + clean(a.Tool)
		if a.Execution != nil && a.Execution.LatencyMS != nil {
			line += " " + ui.Sep + " " + latencyWords(*a.Execution.LatencyMS)
		}
		fmt.Fprintln(e.Stdout, line)
	}
	deedRow := func() {
		if a.Deed == nil {
			return
		}
		line := shortID(*a.Deed)
		if seq := deedSeq(ctx, c, *a.Deed); seq != "" {
			line += " " + ui.Sep + " seq " + seq
		}
		row("record", line+" "+ui.Sep+" sealed")
	}
	next := func() {
		if n := gate.NextStep(ga); n != "" {
			fmt.Fprintln(e.Stdout, "  "+n)
		}
	}
	switch a.Status {
	case ledger.StatusExecuted:
		// S19: the first line's symbol follows the verdict; the witness line is
		// "<level> · <VERDICT> · <summary>", then the reasons that matter and any field table.
		w := a.Witness
		if w == nil {
			head(s.Teal(ui.OK), "executed")
			row("action", a.ID)
			deedRow()
			break
		}
		head(verdictSymbol(s, w.Verdict), "executed")
		if w.Verdict == gate.VerdictVerified {
			row("action", a.ID)
		}
		claim := witnessClaim(s, w, false)
		if w.Verdict == gate.VerdictUnverifiable && w.Cause() != "" {
			claim = witness.PlainLevel(w.Level) + " " + ui.Sep + " " + verdictWord(s, w.Verdict) + " " + ui.Sep + " " + unknownSentence(a)
		}
		row("check", claim)
		for _, l := range witnessReasons(s, w, false, "           ") {
			fmt.Fprintln(e.Stdout, l)
		}
		// S21: an L2 claim shows where it came from; a MISMATCH shows the diff.
		if w.Level == "L2" || hasL2Reason(w) {
			if detail := actDetail(ctx, c, a.ID); detail != nil {
				if w.Verdict == gate.VerdictMismatch && w.Level == "L2" {
					if lines := l2Lines(e, detail, "  ", false); len(lines) > 0 {
						fmt.Fprintln(e.Stdout)
						for _, l := range lines {
							fmt.Fprintln(e.Stdout, l)
						}
					}
				} else if lines := l2Lines(e, detail, "           ", true); len(lines) > 0 && w.Level == "L2" {
					fmt.Fprintln(e.Stdout, lines[0])
				}
			}
		}
		deedRow()
		if w.Verdict == gate.VerdictUnverifiable && hasL2Reason(w) {
			fmt.Fprintln(e.Stdout, "  "+l2.CopyKeepsWatching)
		}
		if n := witnessNext(a); n != "" {
			fmt.Fprintln(e.Stdout, "  "+n)
		} else if w.Verdict != gate.VerdictVerified {
			next()
		}
	case ledger.StatusDenied, ledger.StatusExpired, ledger.StatusCancelled:
		head(s.Red(ui.Fail), strings.ReplaceAll(a.Status, "_", " "))
		msg := ""
		if a.Decision != nil && a.Decision.Message != nil {
			msg = *a.Decision.Message
		} else if refusal != nil {
			msg = refusal.Message
		}
		if msg != "" {
			fmt.Fprintln(e.Stdout, "  "+clean(msg))
		}
		row("action", a.ID)
		deedRow()
		next()
	case ledger.StatusFailed:
		head(s.Red(ui.Fail), "failed")
		if x := a.Execution; x != nil {
			line := ""
			if x.ErrorCode != nil {
				line = *x.ErrorCode
			}
			if x.ErrorMessage != nil {
				line += " " + ui.Sep + " " + clean(*x.ErrorMessage)
			}
			fmt.Fprintln(e.Stdout, "  "+strings.TrimPrefix(line, " "+ui.Sep+" "))
		}
		row("action", a.ID)
		deedRow()
		next()
	case ledger.StatusPendingApproval:
		head(s.Violet(ui.Pending), "pending approval")
		fmt.Fprintln(e.Stdout, "  "+clean(gate.PendingMessage(ga, time.Local)))
		if a.Approval != nil {
			row("approval", shortID(a.Approval.ID))
		}
		next()
	case ledger.StatusAuthorized:
		head(s.Violet(ui.Pending), "authorized")
		if a.NextAction != nil && a.NextAction.Deadline != nil {
			t := time.Time(*a.NextAction.Deadline).In(time.Local)
			z, _ := t.Zone()
			fmt.Fprintf(e.Stdout, "  Run it at the edge, then report the result before %s %s.\n", t.Format("15:04"), z)
		}
		row("action", a.ID)
		next()
	default:
		head(s.Violet(ui.Pending), "processing")
		row("action", a.ID)
		next()
	}
	if code != ExitOK {
		return &ExitError{Code: code, ErrCode: a.Status, Err: errors.New(a.Status), printed: true}
	}
	return nil
}

// reasonsOf is the witness's cause (timeout, edge_lost, …) and its reason codes.
func reasonsOf(w *ledger.ActionWitness) []string {
	if w == nil || w.Reasons == nil {
		return nil
	}
	return append([]string{w.Cause()}, w.Codes()...)
}

// unknownSentence is the honest line for an UNVERIFIABLE outcome. The CLI doesn't know the
// server's gate.execute_timeout, so a timeout names the time the tool had.
func unknownSentence(a *ledger.Action) string {
	for _, r := range reasonsOf(a.Witness) {
		if r == gate.ReasonTimeout && a.Execution != nil && a.Execution.LatencyMS != nil {
			return fmt.Sprintf("The tool did not answer within %s. It may have run.", latencyWords(*a.Execution.LatencyMS))
		}
	}
	return gate.UnknownOutcomeMessage(&gate.Action{Action: *a}, 0)
}

func actExit(a *ledger.Action) int {
	switch a.Status {
	case ledger.StatusExecuted:
		if a.Witness != nil && a.Witness.Verdict == gate.VerdictMismatch {
			return ExitFailure
		}
		return ExitOK
	case ledger.StatusAuthorized:
		return ExitOK
	case ledger.StatusPendingApproval, ledger.StatusProcessing:
		return ExitUsage
	}
	return ExitFailure
}

// latencyWords is "4 ms", or "30.0 s" from one second up.
func latencyWords(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return fmt.Sprintf("%.1f s", float64(ms)/1000)
}

// deedSeq reads a deed's seq (agent keys hold deeds:read for their own deeds); "" when it can't.
func deedSeq(ctx context.Context, c *deedglass.Client, id string) string {
	var env struct {
		Body struct {
			Seq int64 `json:"seq"`
		} `json:"body"`
	}
	if _, err := c.Do(ctx, http.MethodGet, "/v1/deeds/"+url.PathEscape(id), nil, &env); err != nil || env.Body.Seq == 0 {
		return ""
	}
	return fmt.Sprint(env.Body.Seq)
}

// --- actions cancel ---

func runActionsCancel(ctx context.Context, e *Env, args []string) error {
	id, err := oneArg("deedglass actions cancel", "an action id", "deedglass actions cancel act_01j9zq5m8x2k7w4c5v6b7n8p9r", args)
	if err != nil {
		return err
	}
	c, err := agentClient(e)
	if err != nil {
		return err
	}
	var a ledger.Action
	if _, err := c.Do(ctx, http.MethodPost, "/v1/actions/"+url.PathEscape(id)+"/cancel", nil, &a); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeAPIJSON(e, a)
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine("Cancelled "+ui.Sep+" "+clean(a.Tool)+" "+ui.Sep+" "+a.ID))
	if a.Deed != nil {
		fmt.Fprintf(e.Stdout, "  %s %s %s sealed\n", ui.Pad("record", 8), shortID(*a.Deed), ui.Sep)
	}
	return nil
}
