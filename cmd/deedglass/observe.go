package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/caught"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/observe"
	"github.com/deedglass/deedglass/internal/observeapi"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/witness/undo"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// deedglass observe start|stop|list|show|undo|exclude (S50 §3.2, §5): watched sessions over a
// Postgres connector's change log, through the API with an org admin key. observe run is in
// observe_run.go (it starts the agent's command).

// sealWait is how long observe run and observe stop --wait wait for the seal (a var for tests).
var sealWait = 45 * time.Second

// --- observe start ---

var observeStartValues struct {
	connector, label, agent string
}

func observeStartFlags(fs *flag.FlagSet) {
	observeStartValues = struct{ connector, label, agent string }{}
	fs.StringVar(&observeStartValues.connector, "connector", "", "the Postgres connector to watch (its name or con_ id)")
	fs.StringVar(&observeStartValues.label, "label", "", "a name for the session (one line, up to 80 characters)")
	fs.StringVar(&observeStartValues.agent, "agent", "", "an agent to show beside the session (display only; it attributes nothing)")
}

func runObserveStart(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass observe start", observeStartFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || observeStartValues.connector == "" {
		return usageErr(errors.New("deedglass observe start needs --connector"), `deedglass observe start --connector pg-demo [--label "nightly agent"]`)
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	x, raw, err := openSession(ctx, c, observeStartValues.connector, observeStartValues.label, observeStartValues.agent)
	if err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		_, err := e.Stdout.Write(append(raw, '\n'))
		return err
	}
	fmt.Fprintln(e.Stdout, watchingLine(e.Style, x))
	fmt.Fprintln(e.Stdout, "  Next: let the agent work, then run: deedglass observe stop "+clean(x.ID))
	return nil
}

func openSession(ctx context.Context, c *deedglass.Client, connector, label, agent string) (*observeapi.SessionJSON, json.RawMessage, error) {
	body := map[string]any{"connector": connector}
	if label != "" {
		body["label"] = label
	}
	if agent != "" {
		body["agent"] = agent
	}
	var raw json.RawMessage
	if _, err := c.Do(ctx, "POST", "/v1/observe/sessions", body, &raw); err != nil {
		return nil, nil, err
	}
	var x observeapi.SessionJSON
	if err := json.Unmarshal(raw, &x); err != nil {
		return nil, nil, err
	}
	return &x, raw, nil
}

// watchingLine is "Watching pg-demo (public.orders, public.order_events) · session obs_01k…".
func watchingLine(st ui.Style, x *observeapi.SessionJSON) string {
	t := ""
	if len(x.Watched) > 0 {
		t = " (" + clean(strings.Join(x.Watched, ", ")) + ")"
	}
	return st.Violet(ui.Pending) + " Watching " + clean(x.Connector.Name) + t + " " + ui.Sep + " session " + clean(x.ID)
}

// --- observe stop ---

var observeStopValues struct{ wait bool }

func observeStopFlags(fs *flag.FlagSet) {
	observeStopValues.wait = true
	fs.BoolVar(&observeStopValues.wait, "wait", true, "wait for the seal (up to 45 s), then print the summary")
}

func runObserveStop(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass observe stop", observeStopFlags, args)
	if err != nil {
		return err
	}
	id, err := oneArg("deedglass observe stop", "one session id", "deedglass observe stop obs_…", pos)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	x, raw, err := closeSession(ctx, c, id, observeStopValues.wait)
	if err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		_, err := e.Stdout.Write(append(raw, '\n'))
		return err
	}
	printSealed(e.Stdout, e.Style, x, runEnd{})
	return nil
}

// closeSession closes a session and, with wait, polls until it is sealed (or sealWait passes).
func closeSession(ctx context.Context, c *deedglass.Client, id string, wait bool) (*observeapi.SessionJSON, json.RawMessage, error) {
	var raw json.RawMessage
	if _, err := c.Do(ctx, "POST", "/v1/observe/sessions/"+url.PathEscape(id)+"/close", map[string]any{}, &raw); err != nil {
		return nil, nil, err
	}
	deadline := time.Now().Add(sealWait)
	for {
		var x observeapi.SessionJSON
		if err := json.Unmarshal(raw, &x); err != nil {
			return nil, nil, err
		}
		if !wait || x.State != observe.StateClosing || time.Now().After(deadline) {
			return &x, raw, nil
		}
		select {
		case <-ctx.Done():
			return &x, raw, nil
		case <-time.After(observe.AttachPeriod):
		}
		raw = nil
		if _, err := c.Do(ctx, "GET", "/v1/observe/sessions/"+url.PathEscape(id)+"?diff_frames=0", nil, &raw); err != nil {
			return nil, nil, err
		}
	}
}

// printSealed writes the closed session (S50 §5): the sealed line, a line per table, the caught
// lines with both ways back, the any-writer note when anything was committed, and the observed
// note. end.undoLine, when set, is the free way back observe run --undo-sql already took (the file
// it saved, or why it saved none); it replaces the generic free path. end.notes print with the
// session's own notes.
func printSealed(w io.Writer, st ui.Style, x *observeapi.SessionJSON, end runEnd) {
	sum := observe.Summary{Transactions: x.Summary.Transactions, Tables: x.Summary.Tables}
	window := observe.WindowText(x.OpenedAt, x.ClosedAt, nil)
	if x.Summary.FirstCommit != nil && x.Summary.LastCommit != nil {
		window = x.Summary.FirstCommit.UTC().Format("15:04:05") + "–" + x.Summary.LastCommit.UTC().Format("15:04:05")
	}
	switch x.State {
	case observe.StateClosing:
		fmt.Fprintln(w, st.Violet(ui.Pending)+" Session closing "+ui.Sep+" "+obsPlural(x.Summary.Transactions, "transaction")+" so far "+ui.Sep+" waiting for the decoder")
		fmt.Fprintln(w, "  Next: deedglass observe show "+clean(x.ID))
	default:
		fmt.Fprintln(w, st.OKLine("Session "+clean(x.State)+" "+ui.Sep+" "+obsPlural(x.Summary.Transactions, "transaction")+" "+ui.Sep+" "+window))
	}
	printTables(w, sum)
	printCaught(w, st, x, end.undoLine, end.refused)
	if len(x.Caught) == 0 && end.undoLine != "" {
		fmt.Fprintln(w, end.undoLine)
	}
	if x.Summary.Transactions > 0 {
		// Postgres's change log doesn't say who wrote a row: say so wherever the rows are counted.
		fmt.Fprintln(w, "  "+observe.NoteAttribution)
	}
	for _, n := range x.Notes {
		fmt.Fprintln(w, "  "+clean(n))
	}
	for _, n := range end.notes {
		fmt.Fprintln(w, n)
	}
	fmt.Fprintln(w, observe.NoteObserved)
}

func printTables(w io.Writer, sum observe.Summary) {
	lines := observe.TableLines(sum)
	width := 0
	for _, l := range lines {
		width = max(width, len([]rune(l[0])))
	}
	for _, l := range lines {
		fmt.Fprintf(w, "  %s  %s\n", ui.Pad(clean(l[0]), width), clean(l[1]))
	}
}

// printCaught writes every caught line with the Pro path and the free path beside it (a caught
// line is never shown without the free path: S50 §7). freeLine, when set, is the free path already
// taken (observe run --undo-sql); otherwise it is the command that prints the undo SQL. refused
// (the undo preview refused the plan) leaves out the one-step undo, which would refuse too.
func printCaught(w io.Writer, st ui.Style, x *observeapi.SessionJSON, freeLine string, refused bool) {
	if len(x.Caught) == 0 {
		return
	}
	for _, it := range x.Caught {
		fmt.Fprintln(w, st.Amber(clean(it.Line)))
	}
	if x.State == observe.StateSealed && x.Undo.Possible && !refused {
		fmt.Fprintln(w, "  Undo this session in one step (Pro): deedglass observe undo "+clean(x.ID))
	}
	if freeLine == "" {
		freeLine = "  Or print the undo SQL and run it yourself (free): " + clean(caught.FreeSessionPath(x.ID))
	}
	fmt.Fprintln(w, freeLine)
}

// --- observe list ---

var observeListValues struct {
	connector, state string
	caught           bool
	limit            int
}

func observeListFlags(fs *flag.FlagSet) {
	observeListValues = struct {
		connector, state string
		caught           bool
		limit            int
	}{}
	fs.StringVar(&observeListValues.connector, "connector", "", "only this connector's sessions")
	fs.StringVar(&observeListValues.state, "state", "", "only this state: open, closing, sealed, undoing or undone")
	fs.BoolVar(&observeListValues.caught, "caught", false, "only sessions Deedglass caught something in")
	fs.IntVar(&observeListValues.limit, "limit", 20, "at most this many (1 to 100)")
}

func runObserveList(ctx context.Context, e *Env, args []string) error {
	if _, err := mixedArgs("deedglass observe list", observeListFlags, args); err != nil {
		return err
	}
	if observeListValues.limit < 1 || observeListValues.limit > 100 {
		return usageErr(errors.New("--limit is a number from 1 to 100"), "deedglass observe list --limit 20")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	q := url.Values{"limit": {strconv.Itoa(observeListValues.limit)}}
	if observeListValues.connector != "" {
		q.Set("connector", observeListValues.connector)
	}
	if observeListValues.state != "" {
		q.Set("state", observeListValues.state)
	}
	if observeListValues.caught {
		q.Set("caught", "true")
	}
	var raw json.RawMessage
	if _, err := c.Do(ctx, "GET", "/v1/observe/sessions?"+q.Encode(), nil, &raw); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		_, err := e.Stdout.Write(append(raw, '\n'))
		return err
	}
	var l observeapi.List
	if err := json.Unmarshal(raw, &l); err != nil {
		return err
	}
	if len(l.Data) == 0 {
		fmt.Fprintln(e.Stdout, "No sessions yet. Run deedglass observe run --connector <name> -- <your agent command>.")
		return nil
	}
	for _, x := range l.Data {
		label := ""
		if x.Label != nil {
			label = " " + ui.Sep + " " + clean(*x.Label)
		}
		chip := ""
		if len(x.Caught) > 0 {
			chip = " " + ui.Sep + " " + e.Style.Amber("caught")
		}
		fmt.Fprintf(e.Stdout, "%s%s %s %s %s %s %s %s%s %s %s\n", clean(x.ID), label, ui.Sep, clean(x.Connector.Name), ui.Sep,
			observe.WindowText(x.OpenedAt, x.ClosedAt, nil), ui.Sep, obsPlural(x.Summary.Transactions, "transaction"), chip, ui.Sep, clean(x.State))
	}
	if l.HasMore {
		fmt.Fprintln(e.Stdout, "More: deedglass observe list --limit 100")
	}
	return nil
}

// --- observe show ---

var observeShowValues struct {
	undoSQL bool
}

func observeShowFlags(fs *flag.FlagSet) {
	observeShowValues.undoSQL = false
	fs.BoolVar(&observeShowValues.undoSQL, "undo-sql", false, "print the undo SQL to review and run yourself (free); Deedglass runs nothing")
}

func runObserveShow(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass observe show", observeShowFlags, args)
	if err != nil {
		return err
	}
	id, err := oneArg("deedglass observe show", "one session id", "deedglass observe show obs_… [--undo-sql]", pos)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	if observeShowValues.undoSQL {
		return printUndoSQL(ctx, e, c, id)
	}
	var raw json.RawMessage
	if _, err := c.Do(ctx, "GET", "/v1/observe/sessions/"+url.PathEscape(id)+"?diff_frames=0", nil, &raw); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		_, err := e.Stdout.Write(append(raw, '\n'))
		return err
	}
	var x observeapi.SessionJSON
	if err := json.Unmarshal(raw, &x); err != nil {
		return err
	}
	w := e.Stdout
	fmt.Fprintln(w, clean(observe.Headline(&observe.Session{Row: rowOf(&x), Connector: observe.ConnectorFacts{Name: x.Connector.Name}}, nil)))
	sum := observe.Summary{Transactions: x.Summary.Transactions, Tables: x.Summary.Tables}
	fmt.Fprintln(w, clean(observe.CountsLine(sum)))
	printTables(w, sum)
	if x.Seal != nil {
		fmt.Fprintf(w, "  %s %s %s sealed\n", ui.Pad("record", 8), undo.ShortID(x.Seal.Deed), ui.Sep)
	}
	if x.Frames != nil {
		for _, f := range x.Frames.Data {
			state := ""
			switch {
			case f.Excluded:
				state = " " + ui.Sep + " left out"
			case f.Expired:
				state = " " + ui.Sep + " expired"
			}
			fmt.Fprintf(w, "  %s %s %s%s\n", clean(f.ID), ui.Sep, obsPlural(f.Changes, "change"), state)
		}
	}
	printCaught(w, e.Style, &x, "", false)
	fmt.Fprintln(w, "  "+observe.NoteAttribution)
	for _, n := range x.Notes {
		fmt.Fprintln(w, "  "+clean(n))
	}
	fmt.Fprintln(w, observe.NoteObserved)
	return nil
}

func rowOf(x *observeapi.SessionJSON) store.ObserveSession {
	return store.ObserveSession{ID: x.ID, Label: x.Label, OpenedAt: x.OpenedAt, ClosedAt: x.ClosedAt, State: x.State}
}

// undoPreview reads the session's undo preview, which carries the free undo SQL (or the refusals).
func undoPreview(ctx context.Context, c *deedglass.Client, id string) (*observeapi.PreviewJSON, json.RawMessage, error) {
	var raw json.RawMessage
	if _, err := c.Do(ctx, "GET", "/v1/observe/sessions/"+url.PathEscape(id)+"/undo_preview?allow_triggers=true", nil, &raw); err != nil {
		return nil, nil, err
	}
	var pv observeapi.PreviewJSON
	if err := json.Unmarshal(raw, &pv); err != nil {
		return nil, nil, err
	}
	return &pv, raw, nil
}

// undoRefusals is why a preview holds no SQL, never empty.
func undoRefusals(pv *observeapi.PreviewJSON) []string {
	if len(pv.Refusals) == 0 {
		return []string{"Nothing in this session can be put back from the record."}
	}
	return pv.Refusals
}

// printUndoSQL prints the session's undo SQL (S50 §3.5): the planner's statements, one transaction,
// each guarded by the values the session left. Refusals print instead of SQL, exit 1.
func printUndoSQL(ctx context.Context, e *Env, c *deedglass.Client, id string) error {
	pv, raw, err := undoPreview(ctx, c, id)
	if err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		_, err := e.Stdout.Write(append(raw, '\n'))
		return err
	}
	if pv.SQL == nil {
		reasons := undoRefusals(pv)
		return &ExitError{Code: ExitFailure, ErrCode: "undo_unavailable", Err: errors.New(strings.TrimSuffix(clean(reasons[0]), ".")),
			Detail: cleanAll(reasons[1:]), Next: "Undo those rows by hand, or leave out the transaction: deedglass observe exclude " + clean(id) + " dtx_…"}
	}
	_, err = io.WriteString(e.Stdout, *pv.SQL)
	return err
}

func cleanAll(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, clean(s))
	}
	return out
}

// --- observe undo (Pro) ---

var observeUndoValues struct {
	reason        string
	allowTriggers bool
	wait          time.Duration
}

func observeUndoFlags(fs *flag.FlagSet) {
	observeUndoValues = struct {
		reason        string
		allowTriggers bool
		wait          time.Duration
	}{}
	fs.StringVar(&observeUndoValues.reason, "reason", "", "why; the approver and the Ledger read it (up to 280 characters)")
	fs.BoolVar(&observeUndoValues.allowTriggers, "allow-triggers", false, "let triggers on the tables fire again (admins only)")
	fs.DurationVar(&observeUndoValues.wait, "wait", 0, "wait up to this long for the outcome (at most 25s)")
}

func runObserveUndo(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass observe undo", observeUndoFlags, args)
	if err != nil {
		return err
	}
	id, err := oneArg("deedglass observe undo", "one session id", `deedglass observe undo obs_… --reason "…"`, pos)
	if err != nil {
		return err
	}
	if strings.TrimSpace(observeUndoValues.reason) == "" {
		return usageErr(errors.New(strings.TrimSuffix(undo.MsgReason, ".")), `deedglass observe undo `+clean(id)+` --reason "The agent's run went wrong."`)
	}
	if observeUndoValues.wait < 0 || observeUndoValues.wait > 25*time.Second {
		return usageErr(errors.New("--wait is at most 25s"), "deedglass observe undo … --wait 25s")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	q := ""
	if observeUndoValues.allowTriggers {
		q = "?allow_triggers=true"
	}
	var praw json.RawMessage
	if _, err := c.Do(ctx, "GET", "/v1/observe/sessions/"+url.PathEscape(id)+"/undo_preview"+q, nil, &praw); err != nil {
		return apiErr(e, c, err)
	}
	var pv observeapi.PreviewJSON
	if err := json.Unmarshal(praw, &pv); err != nil {
		return err
	}
	if !pv.Pro {
		return &ExitError{Code: ExitFailure, ErrCode: "pro_required", Plain: true,
			Err: errors.New("Undoing a whole session in one step is part of Pro. Per-action Undo and the undo SQL stay free: " + clean(caught.FreeSessionPath(id)))}
	}
	if !pv.Available || pv.PlanDigest == nil {
		reason := "undo isn't available"
		if pv.Conflicts > 0 {
			reason = obsPlural(pv.Conflicts, "row") + " changed again after the session ended, so the undo would overwrite newer data. Nothing ran"
		} else if len(pv.Refusals) > 0 {
			reason = strings.TrimSuffix(clean(pv.Refusals[0]), ".")
		}
		return &ExitError{Code: ExitFailure, ErrCode: "undo_unavailable", Err: errors.New(reason),
			Next: "Leave out a transaction (deedglass observe exclude " + clean(id) + " dtx_…), or undo those rows by hand: " + clean(caught.FreeSessionPath(id))}
	}
	body := map[string]any{"plan_digest": *pv.PlanDigest, "reason": observeUndoValues.reason, "allow_triggers": observeUndoValues.allowTriggers}
	if observeUndoValues.wait > 0 {
		body["wait"] = observeUndoValues.wait.Seconds()
	}
	var raw json.RawMessage
	if _, err := c.Do(ctx, "POST", "/v1/observe/sessions/"+url.PathEscape(id)+"/undo", body, &raw, deedglass.WithIdempotencyKey(undoIdemKey(id))); err != nil {
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
		printUndoAction(e.Stdout, e.Style, &a, id)
	}
	return undoExit(&a)
}

// --- observe exclude ---

var observeExcludeValues struct{ include bool }

func observeExcludeFlags(fs *flag.FlagSet) {
	observeExcludeValues.include = false
	fs.BoolVar(&observeExcludeValues.include, "include", false, "put the transaction back into the undo")
}

func runObserveExclude(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass observe exclude", observeExcludeFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageErr(errors.New("deedglass observe exclude needs a session id and a transaction id"), "deedglass observe exclude obs_… dtx_… [--include]")
	}
	id, dtx := strings.ToLower(strings.TrimSpace(pos[0])), strings.ToLower(strings.TrimSpace(pos[1]))
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if _, err := c.Do(ctx, "POST", "/v1/observe/sessions/"+url.PathEscape(id)+"/frames/"+url.PathEscape(dtx)+"/exclude",
		map[string]any{"excluded": !observeExcludeValues.include}, &raw); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		_, err := e.Stdout.Write(append(raw, '\n'))
		return err
	}
	if observeExcludeValues.include {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(clean(dtx)+" is back in the undo of "+clean(id)))
	} else {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(clean(dtx)+" left out of the undo of "+clean(id)+" "+ui.Sep+" sealed in the record"))
	}
	fmt.Fprintln(e.Stdout, "  Next: preview again: deedglass observe show "+clean(id)+" --undo-sql")
	return nil
}

// obsPlural is "1 transaction" / "3 transactions".
func obsPlural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return undo.Thousands(n) + " " + w + "s"
}
