package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// The Ledger commands run in direct database mode (DEEDGLASS_DATABASE_URL): actions list|show,
// deeds list|show|export, ledger check|prune. API mode (--server, --api-key) arrives with S09's
// keys.

// failNext prints a refusal in the brand shape (✕ sentence, then "  next  step") and returns code;
// with --json it returns the usual error object instead.
func failNext(e *Env, exit int, code, msg, next string) error {
	if e.Globals.JSON {
		return &ExitError{Code: exit, ErrCode: code, Err: errors.New(strings.TrimSuffix(msg, ".")), Next: next}
	}
	fmt.Fprintln(e.Stderr, e.Style.FailLine(msg))
	if next != "" {
		fmt.Fprintln(e.Stderr, "  next  "+next)
	}
	return &ExitError{Code: exit, ErrCode: code, Err: errors.New(msg), printed: true}
}

// ledgerSession is one DB-direct Ledger command's database, org and Ledger.
type ledgerSession struct {
	cfg *config.Config
	db  *store.DB
	org store.Org
	led *ledger.Ledger
}

func (s *ledgerSession) Close() { s.db.Close() }

// openLedger loads the config, connects as the runtime role and picks the org (--org, or the only
// org). With sign set it also loads the seal key, for commands that seal (prune, export, fake-action).
func openLedger(ctx context.Context, e *Env, sign bool) (*ledgerSession, error) {
	cfg, warnings, err := config.Load(e.LoadOptions())
	var ps *config.Problems
	if errors.As(err, &ps) {
		return nil, configProblems(e, ps, warnings)
	}
	if err != nil {
		return nil, err
	}
	if err := cfg.Require(config.NeedDatabase); errors.As(err, &ps) {
		return nil, configProblems(e, ps, warnings)
	}
	db, err := store.Open(ctx, cfg.DatabaseURL, store.Options{MaxConns: 4})
	if err != nil {
		return nil, failNext(e, ExitUsage, "database_unreachable", "Can't reach the database in DEEDGLASS_DATABASE_URL.",
			"Check that Postgres is running and that migrations ran (deedglass migrate up).")
	}
	org, err := pickOrg(ctx, e, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	log := slog.New(slog.DiscardHandler)
	if e.Globals.Verbose > 0 {
		log = httpx.NewLogger(e.Stderr, false, cfg.LogLevel)
	}
	var signer deed.Signer
	if sign {
		s, err := loadSealKeyQuiet(ctx, e, cfg, db, log, true)
		if err != nil {
			db.Close()
			return nil, err
		}
		signer = s
	}
	return &ledgerSession{cfg: cfg, db: db, org: org, led: newLedger(db, signer, cfg, log)}, nil
}

// pickOrg resolves --org (an id or a slug), or the only org in the database.
func pickOrg(ctx context.Context, e *Env, db *store.DB) (store.Org, error) {
	var orgs []store.Org
	err := db.SystemTx(ctx, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		orgs, err = store.ListOrgs(ctx, tx)
		return err
	})
	if err != nil {
		return store.Org{}, failNext(e, ExitFailure, "database_error", "Can't read the orgs in this database.",
			"Run deedglass migrate up, and use the runtime role deedglass_app (docs/DATABASE_SETUP.md).")
	}
	if want := strings.TrimSpace(e.Globals.Org); want != "" {
		for _, o := range orgs {
			if o.ID.String() == want || o.Slug == want {
				return o, nil
			}
		}
		return store.Org{}, failNext(e, ExitUsage, "org_not_found", fmt.Sprintf("No org %s in this database.", want), orgList(orgs))
	}
	switch len(orgs) {
	case 0:
		return store.Org{}, failNext(e, ExitFailure, "no_orgs", "This database has no orgs yet.",
			"deedglass dev seed (dev), or deedglass dev fake-action to create a demo org with actions.")
	case 1:
		return orgs[0], nil
	}
	return store.Org{}, failNext(e, ExitUsage, "org_required", fmt.Sprintf("This database has %d orgs. Pick one with --org.", len(orgs)), orgList(orgs))
}

func orgList(orgs []store.Org) string {
	if len(orgs) == 0 {
		return "deedglass dev seed creates a demo org."
	}
	parts := make([]string, 0, len(orgs))
	for i, o := range orgs {
		if i == 8 {
			parts = append(parts, fmt.Sprintf("and %d more", len(orgs)-8))
			break
		}
		parts = append(parts, shortID(o.ID.String())+" ("+o.Slug+")")
	}
	return strings.Join(parts, " "+ui.Sep+" ")
}

// ledgerErr maps a Ledger error to the CLI's exit codes and copy.
func ledgerErr(e *Env, err error) error {
	var pe *ledger.ParamError
	switch {
	case errors.As(err, &pe):
		return failNext(e, ExitUsage, "validation_failed", pe.Message, "Run deedglass help for this command's flags.")
	case errors.Is(err, context.Canceled):
		return failNext(e, ExitFailure, "interrupted", "Stopped before finishing.", "Run the command again.")
	}
	return err
}

// writeAPIJSON writes v exactly as the API would (indented, deterministic).
func writeAPIJSON(e *Env, v any) error {
	b, err := httpx.MarshalJSON(v)
	if err != nil {
		return err
	}
	_, err = e.Stdout.Write(b)
	return err
}

// --- actions list ---

type actionsListOpts struct {
	agent, tool, state, decision, verdict, level, obo, q, cursor, connector string
	since                                                                   time.Duration
	limit                                                                   int
	follow                                                                  bool
}

var actionsListValues actionsListOpts

func actionsListFlags(fs *flag.FlagSet) {
	o := &actionsListValues
	*o = actionsListOpts{}
	fs.StringVar(&o.agent, "agent", "", "an agent id (agt_…)")
	fs.StringVar(&o.tool, "tool", "", "a tool name, or a glob with * (stripe.*)")
	fs.StringVar(&o.state, "state", "", "comma list of states")
	fs.StringVar(&o.decision, "decision", "", "comma list: allow, deny, require_approval")
	fs.StringVar(&o.verdict, "verdict", "", "comma list: VERIFIED, MISMATCH, UNVERIFIABLE")
	fs.StringVar(&o.level, "level", "", "comma list: L0, L1, L2")
	fs.StringVar(&o.obo, "on-behalf-of", "", "an on-behalf-of reference")
	fs.StringVar(&o.q, "q", "", "text in the tool name or error code")
	fs.DurationVar(&o.since, "since", 0, "only actions newer than this (1h, 30m)")
	fs.IntVar(&o.limit, "limit", 25, "page size, 1 to 100")
	fs.StringVar(&o.cursor, "cursor", "", "next_cursor from the previous page")
	fs.BoolVar(&o.follow, "follow", false, "keep printing actions as they are decided and sealed (Ctrl-C to stop)")
	fs.StringVar(&o.connector, "connector", "", "only this connector's tools (<connector>.*)")
}

func (o actionsListOpts) values(now time.Time) url.Values {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	set("agent", o.agent)
	set("tool", o.tool)
	if o.connector != "" && o.tool == "" {
		v.Set("tool", o.connector+".*")
	}
	set("state", o.state)
	set("decision", o.decision)
	set("verdict", o.verdict)
	set("level", o.level)
	set("on_behalf_of", o.obo)
	set("q", o.q)
	set("cursor", o.cursor)
	v.Set("limit", strconv.Itoa(o.limit))
	if o.since > 0 {
		v.Set("created_at[gte]", now.Add(-o.since).UTC().Format(time.RFC3339Nano))
	}
	return v
}

func runActionsList(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass actions list takes no arguments (got %q)", args[0]), "Filter with flags: deedglass help actions list.")
	}
	now := time.Now()
	f, p, err := ledger.ParseActionQuery(actionsListValues.values(now))
	if err != nil {
		return ledgerErr(e, err)
	}
	if o := actionsListValues; o.follow && (e.Globals.JSON || o.cursor != "") {
		return usageErr(errors.New("--follow prints lines as they happen; it doesn't take --json or --cursor"), "Run deedglass actions list --follow --connector orders.")
	}
	if o := actionsListValues; o.connector != "" && o.tool != "" {
		return usageErr(errors.New("use --connector or --tool, not both"), "For example: deedglass actions list --connector orders.")
	}
	s, err := openLedger(ctx, e, false)
	if err != nil {
		return err
	}
	defer s.Close()
	if actionsListValues.follow {
		return runActionsFollow(ctx, e, s)
	}
	list, err := s.led.ListActions(ctx, s.org.ID, f, p)
	if err != nil {
		return ledgerErr(e, err)
	}
	if e.Globals.JSON {
		return writeAPIJSON(e, list)
	}
	if len(list.Data) == 0 {
		if p.Cursor == "" && len(actionsListValues.values(now)) == 1 { // only limit: no filters
			fmt.Fprintln(e.Stdout, e.Style.UnknownLine("No actions yet in "+s.org.Slug+"."))
			fmt.Fprintln(e.Stdout, "  next  In dev: DEEDGLASS_ENV=dev deedglass dev fake-action --n 20")
			return nil
		}
		fmt.Fprintln(e.Stdout, e.Style.UnknownLine("No actions match these filters."))
		return nil
	}
	total, err := s.led.CountActions(ctx, s.org.ID, f)
	if err != nil {
		return err
	}
	st := e.Style
	fmt.Fprintln(e.Stdout, st.Dim(ui.Pad("TIME", 9)+ui.Pad("AGENT", 14)+ui.Pad("TOOL", 24)+ui.Pad("DECISION", 11)+ui.Pad("WITNESS", 20)+"LATENCY"))
	for _, a := range list.Data {
		chip, plain := witnessChip(st, a)
		fmt.Fprintln(e.Stdout, ui.Pad(relTime(now, time.Time(a.CreatedAt)), 9)+ui.Pad(clip(a.Agent.Name, 13), 14)+
			ui.Pad(clip(a.Tool, 23), 24)+ui.Pad(decisionWord(a.Policy.Decision), 11)+chip+strings.Repeat(" ", max(1, 20-ui.Width(plain)))+latency(a))
	}
	footer := fmt.Sprintf("%s of %s", deed.FormatCount(int64(len(list.Data))), deed.FormatCount(total))
	if list.NextCursor != nil {
		footer += " " + ui.Sep + " next page: deedglass actions list --cursor " + *list.NextCursor
	}
	fmt.Fprintln(e.Stdout, st.Dim(footer))
	return nil
}

func clip(s string, n int) string {
	if ui.Width(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func decisionWord(d *string) string {
	if d == nil {
		return ui.None
	}
	if *d == "require_approval" {
		return "approval"
	}
	return *d
}

func latency(a ledger.Action) string {
	if a.Execution == nil || a.Execution.LatencyMS == nil {
		return ui.None
	}
	return fmt.Sprintf("%d ms", *a.Execution.LatencyMS)
}

// witnessChip is the brand §12 chip: the symbol and words say it; colour only repeats it.
// It returns the styled chip and its plain text (for alignment).
func witnessChip(st ui.Style, a ledger.Action) (string, string) {
	if w := a.Witness; w != nil {
		text := w.Level + " " + ui.Sep + " " + w.Verdict
		switch w.Verdict {
		case "VERIFIED":
			return st.Teal(ui.OK + " " + text), ui.OK + " " + text
		case "MISMATCH":
			return st.Bold(st.Red(ui.Fail + " " + text)), ui.Fail + " " + text
		default:
			return st.Amber(ui.Unknown + " " + text), ui.Unknown + " " + text
		}
	}
	switch a.State {
	case "pending_approval":
		return st.Violet(ui.Pending + " pending"), ui.Pending + " pending"
	case "denied":
		return st.Red(ui.Fail + " denied"), ui.Fail + " denied"
	}
	t := ui.Sep + " " + strings.ReplaceAll(a.State, "_", " ")
	if a.State == "sealed" {
		t = ui.Sep + " sealed"
		if a.Execution != nil && a.Execution.Status != nil && *a.Execution.Status == "failed" {
			t = ui.Sep + " failed"
		} else if a.Policy.Decision != nil && *a.Policy.Decision == "deny" {
			return st.Red(ui.Fail + " denied"), ui.Fail + " denied"
		}
	}
	return t, t
}

// relTime is "2m ago" for people; --json carries RFC 3339.
func relTime(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// --- actions show ---

// actionsShowWait is actions show --wait: wait until the action is sealed or waiting for a person
// (and, when it already waits for a person, for its next change), at most 25 s (S13).
var actionsShowWait bool

func actionsShowFlags(fs *flag.FlagSet) {
	actionsShowWait = false
	fs.BoolVar(&actionsShowWait, "wait", false, "wait (up to 25 s) until the action is sealed or waits for a person")
}

// showWaitPoll is how often actions show --wait re-reads the action (a DB-direct command has no
// LISTEN connection).
var showWaitPoll = 250 * time.Millisecond

func runActionsShow(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass actions show", actionsShowFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr(errors.New("deedglass actions show needs one action id"), "deedglass actions show act_…  (deedglass actions list shows ids with --json)")
	}
	id := strings.ToLower(strings.TrimSpace(pos[0]))
	s, err := openLedger(ctx, e, false)
	if err != nil {
		return err
	}
	defer s.Close()
	d, err := s.led.GetAction(ctx, s.org.ID, id)
	if err == nil && actionsShowWait {
		d, err = waitAction(ctx, s, id, d)
	}
	if errors.Is(err, ledger.ErrNotFound) {
		return failNext(e, ExitFailure, "not_found", fmt.Sprintf("No action %s in %s.", clean(id), s.org.Slug), "deedglass actions list")
	}
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeAPIJSON(e, d)
	}
	st := e.Style
	head := d.Tool + " " + ui.Sep + " " + d.Agent.Name
	if d.OnBehalfOf != nil {
		head += " on behalf of " + *d.OnBehalfOf
	}
	head += " " + ui.Sep + " " + time.Time(d.CreatedAt).UTC().Format("2006-01-02 15:04:05") + " UTC"
	fmt.Fprintln(e.Stdout, st.Bold(head))
	row := func(k, v string) { fmt.Fprintf(e.Stdout, "  %s %s\n", ui.Pad(k, 10), v) }
	dec := decisionWord(d.Policy.Decision)
	if d.Policy.RuleID != nil {
		dec += " " + ui.Sep + " rule " + *d.Policy.RuleID
	}
	if d.Policy.VersionID != nil {
		dec += " " + ui.Sep + " " + shortID(*d.Policy.VersionID)
	}
	row("decision", dec)
	row("execution", executionLine(d))
	if w := d.Witness; w != nil {
		// S19: the claim and its method, then every reason with its level, and field tables.
		row("witness", witnessClaim(st, w, true))
		for _, l := range witnessReasons(st, w, true, "             ") {
			fmt.Fprintln(e.Stdout, l)
		}
	} else {
		chip, _ := witnessChip(st, ledger.Action{State: d.State, Policy: d.Policy, Execution: d.Execution})
		row("witness", chip)
	}
	for _, dr := range d.Deeds {
		line := fmt.Sprintf("seq %s %s %s", deed.FormatCount(dr.Seq), ui.Sep, shortID(dr.ID))
		if sd, err := s.led.GetDeed(ctx, s.org.ID, dr.ID); err == nil {
			if env, err := deed.ParseEnvelope(sd.Envelope); err == nil {
				line += " " + ui.Sep + " sealed with " + shortID(env.SealKeyID)
			}
		}
		if dr.Kind == "amendment" {
			line += " " + ui.Sep + " amendment"
		}
		row("deed", line)
	}
	if d.PayloadPrunedAt != nil {
		row("payloads", "removed "+time.Time(*d.PayloadPrunedAt).UTC().Format("2006-01-02")+" by retention "+ui.Sep+" digests kept")
	}
	states := []string{}
	for _, ev := range d.Events.Data {
		if ev.From == nil || *ev.From != ev.To {
			states = append(states, ev.To)
		}
	}
	row("events", strings.Join(states, " → "))
	if d.Deed != nil {
		row("next", "deedglass deeds show "+*d.Deed)
	}
	return nil
}

// waitAction re-reads an action until it settles or changes (see actionsShowWait).
func waitAction(ctx context.Context, s *ledgerSession, id string, d *ledger.ActionDetail) (*ledger.ActionDetail, error) {
	start := d.State
	settled := func(st string) bool { return st == "sealed" || st == "pending_approval" && start != "pending_approval" }
	deadline := time.Now().Add(ledger.PollWait * time.Second)
	for !settled(d.State) && (start != "pending_approval" || d.State == start) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return d, nil
		case <-time.After(showWaitPoll):
		}
		next, err := s.led.GetAction(ctx, s.org.ID, id)
		if err != nil {
			return d, err
		}
		d = next
	}
	return d, nil
}

func executionLine(d *ledger.ActionDetail) string {
	x := d.Execution
	if x == nil {
		return "did not run"
	}
	status := "executed"
	if x.Status != nil {
		switch *x.Status {
		case "failed":
			status = "failed"
		case "unknown":
			status = "executed " + ui.Sep + " outcome unknown"
		}
	}
	if x.ErrorCode != nil {
		status += " " + ui.Sep + " " + *x.ErrorCode
	}
	if x.LatencyMS != nil {
		status += fmt.Sprintf(" %s %d ms", ui.Sep, *x.LatencyMS)
	}
	return status
}

// exportToStdout writes the export to stdout (--out -), so it can be piped into deedglass verify -.
// The summary goes to stderr; with --json there is none.
func exportToStdout(ctx context.Context, e *Env, s *ledgerSession, r ledger.Range, format string) error {
	x := &exportTarget{e: &Env{Stdout: e.Stderr, Globals: e.Globals, Style: e.Style}, w: e.Stdout}
	var count int64
	var err error
	if format == "jsonl" {
		_, _, count, err = s.led.ExportJSONL(ctx, s.org.ID, r, x)
	} else {
		var m *archive.Manifest
		if m, err = s.led.Export(ctx, s.org.ID, r, x); err == nil {
			count = m.Range.Count
		}
	}
	var empty *ledger.EmptyRangeError
	if errors.As(err, &empty) {
		return &ExitError{Code: ExitFailure, ErrCode: "not_found", Err: errors.New("No deeds in this range"), Next: "Widen --since, or leave it out."}
	}
	if err != nil {
		return ledgerErr(e, err)
	}
	if !e.Globals.JSON {
		fmt.Fprintln(e.Stderr, e.Style.OKLine(fmt.Sprintf("%s %s %s to stdout", deedsWord(count), ui.Sep, sizeWords(x.n))))
	}
	return nil
}

// --- deeds list | show ---

type deedsListOpts struct {
	from, to      int64
	kind, action  string
	order, cursor string
	limit         int
}

var deedsListValues deedsListOpts

func deedsListFlags(fs *flag.FlagSet) {
	o := &deedsListValues
	*o = deedsListOpts{}
	fs.Int64Var(&o.from, "from-seq", 0, "first seq")
	fs.Int64Var(&o.to, "to-seq", 0, "last seq")
	fs.StringVar(&o.kind, "kind", "", "comma list: action, admin, amendment")
	fs.StringVar(&o.action, "action", "", "an action id: its deed and amendments")
	fs.StringVar(&o.order, "order", "", "asc (default) or desc")
	fs.IntVar(&o.limit, "limit", 25, "page size, 1 to 100")
	fs.StringVar(&o.cursor, "cursor", "", "next_cursor from the previous page")
}

func runDeedsList(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass deeds list takes no arguments (got %q)", args[0]), "Filter with flags: deedglass help deeds list.")
	}
	o := deedsListValues
	v := url.Values{"limit": {strconv.Itoa(o.limit)}}
	if o.from > 0 {
		v.Set("from_seq", strconv.FormatInt(o.from, 10))
	}
	if o.to > 0 {
		v.Set("to_seq", strconv.FormatInt(o.to, 10))
	}
	for k, val := range map[string]string{"kind": o.kind, "action": o.action, "order": o.order, "cursor": o.cursor} {
		if val != "" {
			v.Set(k, val)
		}
	}
	f, p, err := ledger.ParseDeedQuery(v)
	if err != nil {
		return ledgerErr(e, err)
	}
	s, err := openLedger(ctx, e, false)
	if err != nil {
		return err
	}
	defer s.Close()
	list, err := s.led.ListDeeds(ctx, s.org.ID, f, p)
	if err != nil {
		return ledgerErr(e, err)
	}
	if e.Globals.JSON {
		return writeAPIJSON(e, list)
	}
	if len(list.Data) == 0 {
		fmt.Fprintln(e.Stdout, e.Style.UnknownLine("No deeds match."))
		return nil
	}
	now := time.Now()
	fmt.Fprintln(e.Stdout, e.Style.Dim(ui.Pad("SEQ", 8)+ui.Pad("KIND", 11)+ui.Pad("DEED", 17)+ui.Pad("ABOUT", 28)+"SEALED"))
	for _, raw := range list.Data {
		env, err := deed.ParseEnvelope(raw)
		if err != nil {
			fmt.Fprintln(e.Stdout, e.Style.Red(ui.Fail+" a stored deed doesn't parse; run deedglass ledger check"))
			continue
		}
		b := env.Body
		about := ui.None
		switch {
		case b.Admin != nil:
			about = b.Admin.Action
		case b.Action != nil:
			about = b.Action.Tool
			if b.Witness != nil {
				about += " " + ui.Sep + " " + b.Witness.Verdict
			}
		}
		at, _ := deed.ParseTime(b.IssuedAt)
		fmt.Fprintln(e.Stdout, ui.Pad(deed.FormatCount(b.Seq), 8)+ui.Pad(string(b.Kind), 11)+ui.Pad(shortID(env.ID), 17)+
			ui.Pad(clip(about, 27), 28)+relTime(now, at))
	}
	if list.NextCursor != nil {
		fmt.Fprintln(e.Stdout, e.Style.Dim("next page: deedglass deeds list --cursor "+*list.NextCursor))
	}
	return nil
}

func runDeedsShow(ctx context.Context, e *Env, args []string) error {
	if len(args) != 1 {
		return usageErr(errors.New("deedglass deeds show needs one deed id or seq"), "deedglass deeds show deed_…  or  deedglass deeds show 812")
	}
	s, err := openLedger(ctx, e, false)
	if err != nil {
		return err
	}
	defer s.Close()
	ref := strings.ToLower(strings.TrimSpace(args[0]))
	d, err := s.led.GetDeed(ctx, s.org.ID, ref)
	if errors.Is(err, ledger.ErrNotFound) {
		return failNext(e, ExitFailure, "not_found", fmt.Sprintf("No deed %s in %s.", clean(ref), s.org.Slug), "deedglass deeds list")
	}
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		_, err := e.Stdout.Write(append(d.Envelope, '\n')) // the stored bytes, verbatim
		return err
	}
	st := e.Style
	fmt.Fprintln(e.Stdout, st.Bold(fmt.Sprintf("Deed %s %s %s %s %s", deed.FormatCount(d.Seq), ui.Sep, d.Kind, ui.Sep, d.ID)))
	row := func(k, v string) { fmt.Fprintf(e.Stdout, "  %s %s\n", ui.Pad(k, 10), v) }
	row("sealed", d.IssuedAt.UTC().Format("2006-01-02 15:04:05.000")+" UTC")
	row("body_hash", shortHash(d.BodyHash))
	if env, err := deed.ParseEnvelope(d.Envelope); err == nil {
		row("seal key", env.SealKeyID)
		b := env.Body
		switch {
		case b.Action != nil:
			about := b.Action.Tool + " " + ui.Sep + " " + b.Action.ID
			row("action", about)
			if b.Witness != nil {
				row("witness", b.Witness.Level+" "+ui.Sep+" "+b.Witness.Verdict)
			}
			if b.Amends != nil {
				row("amends", *b.Amends)
			}
		case b.Admin != nil:
			row("admin", b.Admin.Action+" "+ui.Sep+" by "+b.Admin.Actor.Type+"/"+b.Admin.Actor.ID)
		}
	} else {
		row("check", st.Red(ui.Fail+" this deed does not parse ("+string(deed.CodeOf(err))+")"))
	}
	row("next", "deedglass deeds show "+d.ID+" --json | deedglass verify - --keys <issuer>/.well-known/deedglass/keys.json")
	return nil
}

// --- deeds export ---

type exportOpts struct {
	out, format string
	from, to    int64
	since       time.Duration
}

var exportValues exportOpts

func deedsExportFlags(fs *flag.FlagSet) {
	o := &exportValues
	*o = exportOpts{}
	fs.StringVar(&o.out, "out", "", "file to write (default deedglass-<org>-<first>-<last>.tar.gz)")
	fs.StringVar(&o.format, "format", "tar.gz", "tar.gz (a sealed bundle) or jsonl (deeds only)")
	fs.Int64Var(&o.from, "from-seq", 0, "first seq")
	fs.Int64Var(&o.to, "to-seq", 0, "last seq")
	fs.DurationVar(&o.since, "since", 0, "only deeds issued in the last 1h, 30m, … (S13)")
}

// exportTarget counts what Export writes and prints the progress line once the range is known.
type exportTarget struct {
	e *Env
	w io.Writer
	n int64
}

func (x *exportTarget) Write(p []byte) (int, error) {
	n, err := x.w.Write(p)
	x.n += int64(n)
	return n, err
}

func (x *exportTarget) ExportStarting(org string, first, last, _ int64) {
	if !x.e.Globals.JSON {
		fmt.Fprintln(x.e.Stdout, x.e.Style.Violet(ui.Pending)+fmt.Sprintf(" Exporting deeds %s → %s for %s",
			deed.FormatCount(first), deed.FormatCount(last), shortID(org)))
	}
}

func runDeedsExport(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass deeds export takes no arguments (got %q)", args[0]), "Use --out <file>.")
	}
	o := exportValues
	if o.format != "tar.gz" && o.format != "jsonl" {
		return usageErr(fmt.Errorf("--format must be tar.gz or jsonl (got %q)", o.format), "deedglass deeds export --format tar.gz")
	}
	v := url.Values{}
	if o.from > 0 {
		v.Set("from_seq", strconv.FormatInt(o.from, 10))
	}
	if o.to > 0 {
		v.Set("to_seq", strconv.FormatInt(o.to, 10))
	}
	if o.since < 0 {
		return usageErr(errors.New("--since must be a positive duration"), "For example --since 1h.")
	}
	if o.since > 0 {
		v.Set("issued_at[gte]", time.Now().Add(-o.since).UTC().Format(time.RFC3339Nano))
	}
	r, err := ledger.ParseRangeQuery(v)
	if err != nil {
		return ledgerErr(e, err)
	}
	s, err := openLedger(ctx, e, o.format == "tar.gz")
	if err != nil {
		return err
	}
	defer s.Close()
	if o.out == "-" {
		return exportToStdout(ctx, e, s, r, o.format)
	}

	// Write to a temporary file next to the target and rename it at the end, so a failed export
	// never leaves a half-written file under the final name.
	dir := "."
	if o.out != "" {
		dir = filepath.Dir(o.out)
	}
	tmp, err := os.CreateTemp(dir, ".deedglass-export-*.tmp")
	if err != nil {
		return failNext(e, ExitFailure, "export_failed", fmt.Sprintf("Can't write in %s.", dir), "Choose another directory with --out.")
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name()) // a no-op after the rename
	}()
	x := &exportTarget{e: e, w: tmp}
	var first, last, count int64
	if o.format == "jsonl" {
		first, last, count, err = s.led.ExportJSONL(ctx, s.org.ID, r, x)
	} else {
		var m *archive.Manifest
		if m, err = s.led.Export(ctx, s.org.ID, r, x); err == nil {
			first, last, count = m.Range.FirstSeq, m.Range.LastSeq, m.Range.Count
		}
	}
	var empty *ledger.EmptyRangeError
	if errors.As(err, &empty) {
		msg := "No deeds in this org yet."
		if empty.Head > 0 {
			msg = fmt.Sprintf("No deeds between seq %s and %s in %s. The chain ends at %s.", seqOr(empty.From, "1"), seqOr(empty.To, "the end"),
				shortID(s.org.ID.String()), deed.FormatCount(empty.Head))
		}
		if e.Globals.JSON {
			return &ExitError{Code: ExitFailure, ErrCode: "not_found", Err: errors.New(strings.TrimSuffix(msg, "."))}
		}
		fmt.Fprintln(e.Stderr, e.Style.UnknownLine(msg))
		return &ExitError{Code: ExitFailure, ErrCode: "not_found", Err: errors.New(msg), printed: true}
	}
	if err != nil {
		return ledgerErr(e, err)
	}
	out := o.out
	if out == "" {
		out = ledger.ExportName(s.org.ID.String(), first, last)
		if o.format == "jsonl" {
			out = strings.TrimSuffix(out, ".tar.gz") + ".jsonl"
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmp.Name(), 0o644) // digests only; readable like any export
	if err := os.Rename(tmp.Name(), out); err != nil {
		return failNext(e, ExitFailure, "export_failed", fmt.Sprintf("Can't write %s.", out), "Choose another path with --out.")
	}
	keysURL := strings.TrimSuffix(s.cfg.BaseURL.String(), "/") + "/.well-known/deedglass/keys.json"
	if e.Globals.JSON {
		return writeAPIJSON(e, map[string]any{"object": "deed_export", "org": s.org.ID.String(), "format": o.format,
			"file": out, "first_seq": first, "last_seq": last, "count": count, "bytes": x.n})
	}
	row := func(k, v string) { fmt.Fprintf(e.Stdout, "  %s %s\n", ui.Pad(k, 10), v) }
	if o.format == "tar.gz" {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("%s %s %s %s manifest sealed by %s", deedsWord(count), ui.Sep,
			sizeWords(x.n), ui.Sep, shortID(s.led.SignerKeyID()))))
		row("file", out)
		row("next", "deedglass verify "+out+" --keys "+keysURL)
		return nil
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("%s %s %s %s seq %s → %s", deedsWord(count), ui.Sep, sizeWords(x.n), ui.Sep,
		deed.FormatCount(first), deed.FormatCount(last))))
	row("file", out)
	row("next", "deedglass verify "+out+" --keys "+keysURL)
	return nil
}

func seqOr(n int64, def string) string {
	if n == 0 {
		return def
	}
	return deed.FormatCount(n)
}

func deedsWord(n int64) string {
	return deed.FormatCount(n) + " " + plural(int(n), "deed", "deeds")
}

// sizeWords is a decimal size for people: 2.7 MB.
func sizeWords(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1f kB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d %s", n, plural(int(n), "byte", "bytes"))
}

// --- ledger check ---

type checkOpts struct{ from, to int64 }

var checkValues checkOpts

func ledgerCheckFlags(fs *flag.FlagSet) {
	checkValues = checkOpts{}
	fs.Int64Var(&checkValues.from, "from-seq", 0, "first seq (default: the whole chain)")
	fs.Int64Var(&checkValues.to, "to-seq", 0, "last seq")
}

func runLedgerCheck(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass ledger check takes no arguments (got %q)", args[0]), "Use --from-seq and --to-seq for a range.")
	}
	v := url.Values{}
	if checkValues.from > 0 {
		v.Set("from_seq", strconv.FormatInt(checkValues.from, 10))
	}
	if checkValues.to > 0 {
		v.Set("to_seq", strconv.FormatInt(checkValues.to, 10))
	}
	r, err := ledger.ParseRangeQuery(v)
	if err != nil {
		return ledgerErr(e, err)
	}
	s, err := openLedger(ctx, e, false)
	if err != nil {
		return err
	}
	defer s.Close()
	res, err := s.led.CheckChain(ctx, s.org.ID, r, ledger.CheckOptions{})
	switch {
	case errors.Is(err, ledger.ErrEmptyRange):
		head, _ := s.led.HeadSeq(ctx, s.org.ID)
		return failNext(e, ExitFailure, "not_found", fmt.Sprintf("No deeds in that range. The chain ends at %s.", deed.FormatCount(head)),
			"deedglass ledger check (the whole chain)")
	case errors.Is(err, ledger.ErrNotFound):
		return failNext(e, ExitFailure, "anchor_missing", fmt.Sprintf("Deed %s, just before this range, is missing.", deed.FormatCount(r.FromSeq-1)),
			"deedglass ledger check (the whole chain names the first break)")
	case err != nil:
		return ledgerErr(e, err)
	}
	code := ExitOK
	switch res.Status {
	case ledger.CheckBroken:
		code = ExitFailure
	case ledger.CheckIncomplete:
		code = ExitUsage
	}
	if e.Globals.JSON {
		if err := writeAPIJSON(e, res); err != nil {
			return err
		}
		if code != ExitOK {
			return &ExitError{Code: code, Err: errors.New(res.Status), printed: true}
		}
		return nil
	}
	renderCheck(ctx, e, s, res)
	if code != ExitOK {
		return &ExitError{Code: code, Err: errors.New(res.Status), printed: true}
	}
	return nil
}

// renderCheck prints the brand §5 lines for a chain check.
func renderCheck(ctx context.Context, e *Env, s *ledgerSession, res *ledger.ChainCheck) {
	st := e.Style
	row := func(k, v string) { fmt.Fprintf(e.Stdout, "  %s %s\n", ui.Pad(k, 10), v) }
	secs := fmt.Sprintf("%.2f s", float64(res.DurationMS)/1000)
	keys := fmt.Sprintf("%d seal %s", len(res.SealKeys), plural(len(res.SealKeys), "key", "keys"))
	span := ""
	if res.FirstSeq != nil && res.LastSeq != nil {
		span = fmt.Sprintf(" %s seq %s → %s", ui.Sep, deed.FormatCount(*res.FirstSeq), deed.FormatCount(*res.LastSeq))
	}
	switch res.Status {
	case ledger.CheckBroken:
		b := res.Break
		at := "the chain"
		if b.Seq != nil {
			at = "deed " + deed.FormatCount(*b.Seq)
		}
		fmt.Fprintln(e.Stdout, st.FailLine(fmt.Sprintf("Chain broken at %s %s %s", at, ui.Sep, b.Code)))
		if b.Deed != nil {
			line := shortID(*b.Deed)
			if b.Seq != nil {
				if d, err := s.led.GetDeed(ctx, s.org.ID, strconv.FormatInt(*b.Seq, 10)); err == nil {
					line += " " + ui.Sep + " sealed " + d.IssuedAt.UTC().Format("2006-01-02 15:04") + " UTC"
				}
			}
			row("deed", line)
		}
		row("what", b.Message)
		next, link, _ := strings.Cut(b.Next, ": ")
		row("next", next+":")
		row("", link)
		if res.Checked > 0 {
			row("before it", fmt.Sprintf("%s intact", deedsWord(res.Checked)))
		}
	case ledger.CheckIncomplete:
		fmt.Fprintln(e.Stdout, st.UnknownLine(fmt.Sprintf("Checked %s%s %s stopped at the time limit %s %s", deedsWord(res.Checked), span, ui.Sep, ui.Sep, secs)))
		row("next", fmt.Sprintf("deedglass ledger check --from-seq %d", *res.NextFromSeq))
	default:
		if res.Checked == 0 {
			fmt.Fprintln(e.Stdout, st.UnknownLine("No deeds yet in "+s.org.Slug+". Nothing to check."))
			return
		}
		fmt.Fprintln(e.Stdout, st.OKLine(fmt.Sprintf("Chain intact %s %s%s %s %s %s %s", ui.Sep, deedsWord(res.Checked), span, ui.Sep, keys, ui.Sep, secs)))
		if h := res.Head; h != nil {
			row("head", fmt.Sprintf("seq %s %s %s %s keep this somewhere outside Deedglass", deed.FormatCount(h.Seq), ui.Sep, shortHash(h.BodyHash), ui.Sep))
		}
	}
}

// --- ledger prune ---

type pruneOpts struct {
	olderThan string
	dryRun    bool
	yes       bool
}

var pruneValues pruneOpts

func ledgerPruneFlags(fs *flag.FlagSet) {
	pruneValues = pruneOpts{}
	fs.StringVar(&pruneValues.olderThan, "older-than", "", "payload age to remove (90d, 24h; default: the org's retention, 90 days)")
	fs.BoolVar(&pruneValues.dryRun, "dry-run", false, "count only; nothing changes")
	fs.BoolVar(&pruneValues.yes, "yes", false, "don't ask for confirmation")
}

// parseAge reads 90d, 36h, 30m or 0s.
func parseAge(s string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 || n > 3650 {
			return 0, fmt.Errorf("--older-than %q isn't a number of days", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	v, err := time.ParseDuration(s)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("--older-than %q isn't a duration (90d, 36h)", s)
	}
	return v, nil
}

func runLedgerPrune(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass ledger prune takes no arguments (got %q)", args[0]), "deedglass ledger prune --older-than 90d --dry-run")
	}
	o := pruneValues
	var age time.Duration
	if o.olderThan != "" {
		var err error
		if age, err = parseAge(o.olderThan); err != nil {
			return usageErr(err, "deedglass ledger prune --older-than 90d")
		}
	}
	s, err := openLedger(ctx, e, !o.dryRun)
	if err != nil {
		return err
	}
	defer s.Close()
	days := 0
	if o.olderThan == "" {
		if days, err = s.led.RetentionDays(ctx, s.org.ID); err != nil {
			return err
		}
		age = time.Duration(days) * 24 * time.Hour
	}
	if s.cfg.Env == config.Prod && age < ledger.MinPruneAge {
		return failNext(e, ExitUsage, "validation_failed", "Prune only payloads at least 24 hours old.", "deedglass ledger prune --older-than 90d")
	}
	cutoff := time.Now().Add(-age)
	if !o.dryRun && !o.yes {
		if !e.IsTTY || e.Globals.JSON {
			return usageErr(errors.New("ledger prune changes data and needs confirmation"), "Add --yes, or run it in a terminal to confirm.")
		}
		fmt.Fprintf(e.Stdout, "Type the org id to remove payloads older than %s (deeds are kept): ", ageWords(age))
		line, _ := bufio.NewReader(e.stdin()).ReadString('\n')
		if strings.TrimSpace(line) != s.org.ID.String() {
			return failNext(e, ExitFailure, "not_confirmed", "That isn't the org id. Nothing changed.", "Type "+s.org.ID.String()+" exactly, or pass --yes.")
		}
	}
	res, err := s.led.Prune(ctx, s.org.ID, cutoff, ledger.PruneOptions{DryRun: o.dryRun, RetentionDays: days,
		Actor: deed.Ref{Type: "system", ID: "operator"}})
	if errors.Is(err, ledger.ErrPruneRunning) {
		return failNext(e, ExitFailure, "prune_running", "Another prune is running for this org.", "Wait for it to finish, then run this again.")
	}
	if err != nil {
		return ledgerErr(e, err)
	}
	if e.Globals.JSON {
		return writeAPIJSON(e, res)
	}
	st := e.Style
	row := func(k, v string) { fmt.Fprintf(e.Stdout, "  %s %s\n", ui.Pad(k, 15), v) }
	before := cutoff.UTC().Format("2006-01-02")
	if o.dryRun {
		fmt.Fprintln(e.Stdout, st.UnknownLine("Dry run "+ui.Sep+" nothing changes"))
		row(shortID(s.org.ID.String()), fmt.Sprintf("%s sealed %s before %s %s %s of payloads", deed.FormatCount(res.Actions),
			plural(int(res.Actions), "action", "actions"), before, ui.Sep, sizeWords(res.Bytes)))
		row("kept", "deeds, events, digests and ids")
		row("next", `Run again without --dry-run. Deedglass seals a "ledger.pruned" deed when it's done.`)
		return nil
	}
	if res.Actions == 0 {
		fmt.Fprintln(e.Stdout, st.OKLine("Nothing to remove "+ui.Sep+" no sealed action has payloads from before "+before))
		return nil
	}
	fmt.Fprintln(e.Stdout, st.OKLine(fmt.Sprintf("Removed payloads from %s %s %s %s (space returns after autovacuum)",
		deed.FormatCount(res.Actions), plural(int(res.Actions), "action", "actions"), ui.Sep, sizeWords(res.Bytes))))
	fmt.Fprintln(e.Stdout, st.OKLine(fmt.Sprintf("Deed sealed %s seq %s %s %s", ui.Sep, deed.FormatCount(*res.Seq), ui.Sep, ledger.AdminLedgerPruned)))
	fmt.Fprintln(e.Stdout, "  Every deed still verifies. Payloads are gone; their digests stay.")
	return nil
}

func ageWords(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		n := int(d / (24 * time.Hour))
		return fmt.Sprintf("%d %s", n, plural(n, "day", "days"))
	}
	return d.String()
}
