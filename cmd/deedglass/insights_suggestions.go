package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/learnstore"
)

// S28: deedglass insights suggestions list|show|accept|reject|generate, insights digest --preview,
// and policy diff. Every suggestion command talks to the server with an org admin key; the digest
// preview reads the database directly and never sends.

// --- insights suggestions generate ---------------------------------------------------------------------

func runSuggestionsGenerate(ctx context.Context, e *Env, args []string) error {
	var jsonOut bool
	pos, err := parseInterleaved("deedglass insights suggestions generate", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageErr(errors.New("deedglass insights suggestions generate takes flags only"), "deedglass insights suggestions generate")
	}
	cl, err := apiClient(e)
	if err != nil {
		return err
	}
	var run learn.LearnRun
	if _, err := cl.Do(ctx, "POST", "/v1/suggestions/generate", nil, &run); err != nil {
		return apiErr(e, cl, err)
	}
	if jsonOut || e.Globals.JSON {
		return writeJSON(e, run)
	}
	printLearnRun(e.Stdout, e.Style, &run)
	return nil
}

func printLearnRun(w io.Writer, st ui.Style, run *learn.LearnRun) {
	if run.Notice != nil {
		fmt.Fprintln(w, st.UnknownLine(*run.Notice))
		return
	}
	if run.Policy != nil {
		fmt.Fprintln(w, st.Violet(ui.Pending)+fmt.Sprintf(" Read %d days of %s traffic (%s actions) against %s v%d",
			int(learn.SuggestWindow.Hours()/24), run.Policy.Name, deed.FormatCount(int64(run.Actions)), run.Policy.Name, run.Policy.BaseVersion))
	}
	fmt.Fprintln(w, st.OKLine(learn.GenerateLine(len(run.Created), len(run.Dropped))))
	kindW, targetW, titleW := 4, 6, 10
	for _, c := range run.Created {
		kindW = max(kindW, ui.Width(c.Kind))
		targetW = max(targetW, ui.Width(c.Target))
		titleW = max(titleW, min(ui.Width(c.Title), 72))
	}
	for _, c := range run.Created {
		title := c.Title
		if ui.Width(title) > 72 {
			title = string([]rune(title)[:71]) + "…"
		}
		change := deed.FormatCount(int64(c.Impact)) + " would change"
		if c.Kind == learn.KindAdvisory {
			change = "advice"
		}
		fmt.Fprintf(w, "  %s  %s  %s  %s\n", ui.Pad(c.Kind, kindW), ui.Pad(clean(c.Target), targetW), ui.Pad(clean(title), titleW), change)
	}
	for _, d := range run.Dropped {
		on := ""
		if d.Target != "" {
			on = " on " + clean(d.Target)
		}
		fmt.Fprintln(w, st.Dim("  "+ui.Sep+" dropped "+d.Generator+on+": "+clean(d.Reason)))
	}
	if run.Superseded > 0 {
		fmt.Fprintf(w, "  %s %d superseded: the enforced policy changed since they were written\n", ui.Sep, run.Superseded)
	}
	if run.Unchanged > 0 {
		fmt.Fprintf(w, "  %s %d already open, unchanged\n", ui.Sep, run.Unchanged)
	}
	if run.Waiting > 0 {
		fmt.Fprintf(w, "  %s %d more wait for room (at most %d open)\n", ui.Sep, run.Waiting, learn.MaxOpenSuggestions)
	}
	if len(run.Created) > 0 {
		fmt.Fprintf(w, "  next      deedglass insights suggestions show %s\n", run.Created[0].ID)
	}
}

// --- insights suggestions list ----------------------------------------------------------------------

func runSuggestionsList(ctx context.Context, e *Env, args []string) error {
	var (
		status, kind string
		limit        = 25
		jsonOut      bool
	)
	pos, err := parseInterleaved("deedglass insights suggestions list", args, func(fs *flag.FlagSet) {
		fs.StringVar(&status, "status", status, "open (default, with accepting), accepted, rejected, superseded or all")
		fs.StringVar(&kind, "kind", kind, "loosen, tighten, budget, explicit_rule or advisory")
		fs.IntVar(&limit, "limit", limit, "at most this many (1 to 100)")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageErr(errors.New("deedglass insights suggestions list takes flags only"), "deedglass insights suggestions list --status all")
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if status != "" {
		q.Set("status", status)
	}
	if kind != "" {
		q.Set("kind", kind)
	}
	cl, err := apiClient(e)
	if err != nil {
		return err
	}
	var l learn.List[learn.Suggestion]
	if _, err := cl.Do(ctx, "GET", "/v1/suggestions?"+q.Encode(), nil, &l); err != nil {
		return apiErr(e, cl, err)
	}
	if jsonOut || e.Globals.JSON {
		return writeJSON(e, l)
	}
	w := e.Stdout
	if len(l.Data) == 0 {
		fmt.Fprintln(w, learn.MsgNoSuggestions+" Generate now: deedglass insights suggestions generate")
		return nil
	}
	for _, s := range l.Data {
		target := ""
		if s.TargetRuleID != nil {
			target = *s.TargetRuleID
		}
		fmt.Fprintf(w, "%s  %s  %s  %s  %s\n", s.ID, ui.Pad(s.Status, 10), ui.Pad(s.Kind, 13), ui.Pad(clean(target), 22), clean(s.Title))
	}
	if l.HasMore && l.NextCursor != nil {
		fmt.Fprintln(w, e.Style.Dim("  more: --limit 100, or GET /v1/suggestions?cursor="+*l.NextCursor))
	}
	return nil
}

// --- insights suggestions show ----------------------------------------------------------------------

func runSuggestionsShow(ctx context.Context, e *Env, args []string) error {
	var (
		jsonOut, noDiff bool
	)
	pos, err := parseInterleaved("deedglass insights suggestions show", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&noDiff, "no-diff", noDiff, "leave the policy change out")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr(errors.New("deedglass insights suggestions show takes one suggestion id"), "deedglass insights suggestions show sug_…")
	}
	cl, err := apiClient(e)
	if err != nil {
		return err
	}
	var s learn.Suggestion
	if _, err := cl.Do(ctx, "GET", "/v1/suggestions/"+url.PathEscape(pos[0]), nil, &s); err != nil {
		return apiErr(e, cl, err)
	}
	if jsonOut || e.Globals.JSON {
		return writeJSON(e, s)
	}
	printSuggestion(e.Stdout, e.Style, &s, !noDiff)
	return nil
}

func printSuggestion(w io.Writer, st ui.Style, s *learn.Suggestion, withDiff bool) {
	head := strings.ToUpper(s.Kind)
	if s.TargetRuleID != nil {
		head += " " + ui.Sep + " " + clean(*s.TargetRuleID)
	}
	fmt.Fprintf(w, "%s   %s   %s\n", head, s.ID, s.Status)
	fmt.Fprintln(w, st.Bold(clean(s.Title)))
	fmt.Fprintln(w, clean(s.Rationale))
	if s.Phrasing.Source == "claude" {
		fmt.Fprintln(w, st.Dim(fmt.Sprintf(learn.MsgWordedBy, s.Phrasing.Model)))
	}
	fmt.Fprintln(w)
	row := func(k, v string) { fmt.Fprintf(w, "  %s %s\n", ui.Pad(k, 10), v) }
	row("policy", fmt.Sprintf("%s v%d %s %s", s.Policy.Name, s.Policy.BaseVersion, ui.Sep, shortDigest(s.Policy.BaseVersionHash)))
	var ev map[string]any
	if json.Unmarshal(s.Evidence, &ev) == nil {
		if w, ok := ev["window"].(map[string]any); ok {
			row("window", fmt.Sprintf("%v to %v", w["from"], w["to"]))
		}
		if a, ok := ev["approvals"].(map[string]any); ok {
			row("approvals", fmt.Sprintf("%v requested %s %v approved %s %v denied %s %v expired %s %v pending",
				a["requested"], ui.Sep, a["approved"], ui.Sep, a["denied"], ui.Sep, a["expired"], ui.Sep, a["pending"]))
		}
		if p, ok := ev["pattern"].(map[string]any); ok {
			keys := make([]string, 0, len(p))
			for k := range p {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var parts []string
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s %v", k, p[k]))
			}
			row("pattern", clean(strings.Join(parts, " "+ui.Sep+" ")))
		}
	}
	row("evidence", shortDigest(s.EvidenceDigest))
	if s.Replay != nil {
		r := s.Replay
		row("replay", fmt.Sprintf("%s %s %s actions %s %s", r.ID, ui.Sep, deed.FormatCount(r.Summary.Actions), ui.Sep, learn.ReplaySummaryLine(r.Summary)))
		for _, t := range r.Transitions {
			line := fmt.Sprintf("%s → %s  %s", t.From, t.To, deed.FormatCount(t.Count))
			if h := learn.HumanOutcomeLine(t.HumanOutcome); h != "" {
				line += "  (" + h + ")"
			}
			row("", line)
		}
	}
	row("risk", clean(s.RiskNote))
	for _, d := range s.Warnings {
		row("warning", clean(d.Code+": "+d.Message))
	}
	if withDiff && s.Diff != nil && len(s.Diff.Hunks) > 0 {
		fmt.Fprintf(w, "\nPolicy change  +%d %s, −%d\n", s.Diff.Added, plural(s.Diff.Added, "line", "lines"), s.Diff.Removed)
		printPolicyDiff(w, st, *s.Diff)
	}
	if s.Status == learn.SugOpen && s.Kind != learn.KindAdvisory {
		fmt.Fprintf(w, "\n  next      deedglass insights suggestions accept %s --reason \"…\"  %s  reject %s --reason \"…\"\n", s.ID, ui.Sep, s.ID)
	}
}

// printPolicyDiff prints unified hunks; added lines plain, removed dim, context dim (no red: a policy
// diff isn't a verdict).
func printPolicyDiff(w io.Writer, st ui.Style, u policy.Unified) {
	for _, h := range u.Hunks {
		fmt.Fprintln(w, st.Dim(fmt.Sprintf("@@ lines %d to %d @@", h.NewStart, h.NewStart+max(h.NewLines-1, 0))))
		for _, l := range h.Lines {
			switch l.Op {
			case "+":
				fmt.Fprintln(w, st.Teal("+")+" "+clean(l.Text))
			case "-":
				fmt.Fprintln(w, st.Dim("− "+clean(l.Text)))
			default:
				fmt.Fprintln(w, st.Dim("  "+clean(l.Text)))
			}
		}
	}
}

// --- insights suggestions accept | reject --------------------------------------------------------------

func runSuggestionsAccept(ctx context.Context, e *Env, args []string) error {
	var (
		reason           string
		replace, jsonOut bool
	)
	pos, err := parseInterleaved("deedglass insights suggestions accept", args, func(fs *flag.FlagSet) {
		fs.StringVar(&reason, "reason", reason, "why (optional, up to 280 characters; sealed in the record)")
		fs.BoolVar(&replace, "replace-shadow", replace, "replace the version running in shadow now")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr(errors.New("deedglass insights suggestions accept takes one suggestion id"),
			`deedglass insights suggestions accept sug_… --reason "Ops approves these every time"`)
	}
	cl, err := apiClient(e)
	if err != nil {
		return err
	}
	var s learn.Suggestion
	if _, err := cl.Do(ctx, "POST", "/v1/suggestions/"+url.PathEscape(pos[0])+"/accept", learn.AcceptRequest{Reason: reason, ReplaceShadow: replace}, &s); err != nil {
		return apiErr(e, cl, err)
	}
	if jsonOut || e.Globals.JSON {
		return writeJSON(e, s)
	}
	w := e.Stdout
	v := 0
	if s.ResultingVersion != nil {
		v = s.ResultingVersion.Version
	}
	line := fmt.Sprintf("%s v%d created from %s %s running in shadow", s.Policy.Name, v, s.ID, ui.Sep)
	if s.ReplacedShadow != nil {
		line += fmt.Sprintf(" (replaced v%d)", *s.ReplacedShadow)
	}
	fmt.Fprintln(w, e.Style.Violet(ui.Pending)+" "+line)
	if s.Note != nil {
		fmt.Fprintln(w, "  "+clean(*s.Note))
	}
	fmt.Fprintln(w, e.Style.OKLine(fmt.Sprintf("%d %s sealed %s policy.activated (shadow) %s suggestion.accepted", len(s.Deeds), plural(len(s.Deeds), "record", "records"), ui.Sep, ui.Sep)))
	fmt.Fprintf(w, "  Nothing is enforced yet. Watch it: deedglass insights shadow %s promote: deedglass policy activate %s --version %d --mode enforce\n",
		ui.Sep, s.Policy.Name, v)
	return nil
}

func runSuggestionsReject(ctx context.Context, e *Env, args []string) error {
	var (
		reason  string
		jsonOut bool
	)
	pos, err := parseInterleaved("deedglass insights suggestions reject", args, func(fs *flag.FlagSet) {
		fs.StringVar(&reason, "reason", reason, "why (required, 3 to 280 characters; sealed in the record)")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr(errors.New("deedglass insights suggestions reject takes one suggestion id"),
			`deedglass insights suggestions reject sug_… --reason "We review these by hand"`)
	}
	cl, err := apiClient(e)
	if err != nil {
		return err
	}
	var s learn.Suggestion
	if _, err := cl.Do(ctx, "POST", "/v1/suggestions/"+url.PathEscape(pos[0])+"/reject", learn.RejectRequest{Reason: reason}, &s); err != nil {
		return apiErr(e, cl, err)
	}
	if jsonOut || e.Globals.JSON {
		return writeJSON(e, s)
	}
	d := ""
	if len(s.Deeds) > 0 {
		d = " " + ui.Sep + " record " + shortID(s.Deeds[0]) + " sealed"
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine("Rejected "+s.ID+d))
	fmt.Fprintln(e.Stdout, "  "+learn.MsgRejectHint)
	return nil
}

// --- insights digest --preview --------------------------------------------------------------------------

func runInsightsDigest(ctx context.Context, e *Env, args []string) error {
	var (
		preview          bool
		orgRef, htmlPath string
		jsonOut          bool
	)
	pos, err := parseInterleaved("deedglass insights digest", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&preview, "preview", preview, "render this week's digest without sending it (required)")
		fs.StringVar(&orgRef, "org", orgRef, "the org (org_… or its slug); default the only org, or the newest")
		fs.StringVar(&htmlPath, "html", htmlPath, "also write the HTML version to this file")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	if len(pos) > 0 || !preview {
		return usageErr(errors.New("deedglass insights digest only previews: pass --preview"), "deedglass insights digest --preview --html /tmp/digest.html")
	}
	orgRef = firstNonEmpty(orgRef, e.Globals.Org)
	cfg, warnings, err := config.Load(e.LoadOptions())
	var ps *config.Problems
	if errors.As(err, &ps) {
		return configProblems(e, ps, warnings)
	}
	if err != nil {
		return err
	}
	if err := cfg.Require(config.NeedDatabase); errors.As(err, &ps) {
		return configProblems(e, ps, warnings)
	}
	db, err := store.Open(ctx, cfg.DatabaseURL, store.Options{MaxConns: 4})
	if err != nil {
		return failNext(e, ExitUsage, "database_unreachable", "Can't reach the database in DEEDGLASS_DATABASE_URL.",
			"Check that Postgres is running and that migrations ran (deedglass migrate up).")
	}
	defer db.Close()
	o, err := digestOrg(ctx, db, orgRef)
	if err != nil {
		return failNext(e, ExitFailure, "not_found", "No org "+orgRef+" in this database.", "Check the slug or id: deedglass org list")
	}
	led := newLedger(db, nil, cfg, slog.New(slog.DiscardHandler))
	ls := learnstore.New(db, led)
	svc := &learn.Service{Store: ls, Inputs: ls.Inputs, Log: slog.New(slog.DiscardHandler)}
	g := &learn.Digester{Store: ls, Insights: svc, BaseURL: baseURLString(cfg)}
	info, err := ls.Org(ctx, o)
	if err != nil {
		return err
	}
	// This week so far: from its Monday, org-local.
	loc, _ := time.LoadLocation(info.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	monday := time.Date(now.Year(), now.Month(), now.Day()-(int(now.Weekday())+6)%7, 0, 0, 0, 0, loc)
	msgs, data, err := g.Build(ctx, o, monday)
	if err != nil {
		return err
	}
	if data == nil {
		fmt.Fprintln(e.Stdout, "The digest is off for "+info.Name+". Turn it on in Console › Insights › Settings.")
		return nil
	}
	if len(msgs) == 0 {
		fmt.Fprintln(e.Stdout, e.Style.UnknownLine("No active owners to send it to in "+info.Name+"."))
		return nil
	}
	m := msgs[0]
	if htmlPath != "" {
		if err := os.MkdirAll(filepath.Dir(htmlPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(htmlPath, []byte(m.HTML), 0o644); err != nil {
			return err
		}
	}
	if jsonOut || e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "digest_preview", "subject": m.Subject, "recipients": len(msgs), "text": m.Text, "html_path": htmlPath})
	}
	fmt.Fprintln(e.Stdout, "Subject: "+m.Subject)
	fmt.Fprintf(e.Stdout, "To: %d %s (not sent: preview)\n\n", len(msgs), plural(len(msgs), "recipient", "recipients"))
	fmt.Fprint(e.Stdout, m.Text)
	if htmlPath != "" {
		fmt.Fprintln(e.Stdout, e.Style.OKLine("HTML written to "+htmlPath))
	}
	return nil
}

func digestOrg(ctx context.Context, db *store.DB, ref string) (ids.OrgID, error) {
	if ref != "" {
		o, err := orgByRef(ctx, db, ref)
		return o.ID, err
	}
	orgs, err := db.LearnOrgs(ctx, 365*24*time.Hour)
	if err != nil {
		return ids.OrgID{}, err
	}
	if len(orgs) == 0 {
		return ids.OrgID{}, errors.New("no org")
	}
	return orgs[len(orgs)-1], nil
}

// --- policy diff ------------------------------------------------------------------------------------------

func runPolicyDiff(ctx context.Context, e *Env, args []string) error {
	var (
		contextLines = 3
		jsonOut      bool
	)
	pos, err := parseInterleaved("deedglass policy diff", args, func(fs *flag.FlagSet) {
		fs.IntVar(&contextLines, "context", contextLines, "lines of context around each change (0 to 20)")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	if contextLines < 0 || contextLines > 20 {
		return usageErr(errors.New("--context must be 0 to 20"), "deedglass policy diff old.yaml new.yaml --context 3")
	}
	var a, b []byte
	var la, lb string
	switch len(pos) {
	case 2: // two files
		if a, err = os.ReadFile(pos[0]); err != nil {
			return failNext(e, ExitFailure, "file_unreadable", "Can't read "+pos[0]+".", "Check the path.")
		}
		if b, err = os.ReadFile(pos[1]); err != nil {
			return failNext(e, ExitFailure, "file_unreadable", "Can't read "+pos[1]+".", "Check the path.")
		}
		la, lb = pos[0], pos[1]
	case 3: // a policy and two versions, from the server
		va, errA := strconv.Atoi(strings.TrimPrefix(pos[1], "v"))
		vb, errB := strconv.Atoi(strings.TrimPrefix(pos[2], "v"))
		if errA != nil || errB != nil || va < 1 || vb < 1 {
			return usageErr(errors.New("versions are numbers, like 1 and 3"), "deedglass policy diff acme-sim 1 3")
		}
		cl, err := apiClient(e)
		if err != nil {
			return err
		}
		get := func(v int) ([]byte, error) {
			var pv policy.Version
			if _, err := cl.Do(ctx, "GET", "/v1/policies/"+url.PathEscape(pos[0])+"/versions/"+strconv.Itoa(v), nil, &pv); err != nil {
				return nil, apiErr(e, cl, err)
			}
			if pv.Source == nil {
				return nil, failNext(e, ExitFailure, "source_missing", "The server didn't return the source of v"+strconv.Itoa(v)+".", "Read it with deedglass policy show.")
			}
			return []byte(*pv.Source), nil
		}
		if a, err = get(va); err != nil {
			return err
		}
		if b, err = get(vb); err != nil {
			return err
		}
		la, lb = fmt.Sprintf("%s v%d", pos[0], va), fmt.Sprintf("%s v%d", pos[0], vb)
	default:
		return usageErr(errors.New("deedglass policy diff takes two files, or a policy and two versions"),
			"deedglass policy diff old.yaml new.yaml · deedglass policy diff acme-sim 1 3")
	}
	u := policy.Diff(a, b, contextLines)
	if jsonOut || e.Globals.JSON {
		return writeJSON(e, u)
	}
	if u.Empty() {
		fmt.Fprintln(e.Stdout, e.Style.OKLine("No changes between "+la+" and "+lb))
		return nil
	}
	fmt.Fprintf(e.Stdout, "%s → %s  +%d %s, −%d\n", la, lb, u.Added, plural(u.Added, "line", "lines"), u.Removed)
	printPolicyDiff(e.Stdout, e.Style, u)
	return nil
}
