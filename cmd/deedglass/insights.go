package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/learnstore"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// The Insights commands (S27 §5): policy replay, insights stats|shadow|anomalies|settings (server,
// an org admin key) and insights detect (database-direct, the operator's). Numbers first, no
// adjectives, no alarm: anomalies print amber or neutral, never red.

// --- policy replay ---------------------------------------------------------------------------------

func runPolicyReplay(ctx context.Context, e *Env, args []string) error {
	var (
		version              = 0
		from, to, baseline   = "7d", "now", "recorded"
		wait, jsonOut, quiet bool
	)
	// parseInterleaved binds once per segment, so every default is the variable's current value.
	pos, err := parseInterleaved("deedglass policy replay", args, func(fs *flag.FlagSet) {
		fs.IntVar(&version, "version", version, "the version to replay (required)")
		fs.StringVar(&from, "from", from, "the window's start: a duration back (7d, 48h) or an RFC 3339 time")
		fs.StringVar(&to, "to", to, "the window's end: now, or an RFC 3339 time")
		fs.StringVar(&baseline, "baseline", baseline, "recorded (the decisions made then) or vN (re-evaluate version N too)")
		fs.BoolVar(&wait, "wait", wait, "wait for the result, with progress")
		fs.BoolVar(&quiet, "quiet", quiet, "no progress bar")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	jsonOut = jsonOut || e.Globals.JSON
	if len(pos) != 1 || version < 1 {
		return usageErr(errors.New("deedglass policy replay takes a policy and --version"), "For example: deedglass policy replay acme-sim --version 3 --from 7d --wait")
	}
	now := time.Now().UTC()
	tt, err := replayTime(to, now, true)
	if err != nil {
		return usageErr(err, "--to now, or --to 2026-10-09T00:00:00Z")
	}
	ft, err := replayTime(from, tt, false)
	if err != nil {
		return usageErr(err, "--from 7d, or --from 2026-10-02T00:00:00Z")
	}
	body := map[string]any{"from": ft.Format(time.RFC3339), "to": tt.Format(time.RFC3339)}
	switch {
	case baseline == "" || baseline == learn.BaselineRecorded:
	case strings.HasPrefix(baseline, "v"):
		n, err := strconv.Atoi(baseline[1:])
		if err != nil || n < 1 {
			return usageErr(fmt.Errorf("--baseline %q isn't recorded or vN", baseline), "--baseline recorded, or --baseline v2")
		}
		body["baseline"] = map[string]any{"type": learn.BaselineVersion, "version": n}
	default:
		return usageErr(fmt.Errorf("--baseline %q isn't recorded or vN", baseline), "--baseline recorded, or --baseline v2")
	}
	cl, err := apiClient(e)
	if err != nil {
		return err
	}
	var r learn.Replay
	if _, err := cl.Do(ctx, "POST", apiPath(pos[0], "versions", strconv.Itoa(version), "replay"), body, &r); err != nil {
		return apiErr(e, cl, err)
	}
	st := e.Style
	label := fmt.Sprintf("%s v%d", r.Candidate.Name, r.Candidate.Version)
	loc := time.Local
	if !jsonOut {
		total := "the actions"
		if r.Progress.Total != nil {
			total = deed.FormatCount(*r.Progress.Total) + " " + plural(int(*r.Progress.Total), "action", "actions")
		}
		fmt.Fprintf(e.Stdout, "%s Replaying %s against %s (%s to %s) %s baseline: %s\n", st.Violet(ui.Pending), label, total,
			r.Window.From.In(loc).Format("2006-01-02"), r.Window.To.In(loc).Format("2006-01-02"), ui.Sep, baselineWords(r.Baseline))
	}
	if !wait {
		if jsonOut {
			return writeJSON(e, r)
		}
		fmt.Fprintf(e.Stdout, "  queued     %s\n  next       deedglass policy replay %s --version %d --wait, or GET /v1/replays/%s\n",
			r.ID, pos[0], version, r.ID)
		return nil
	}
	started := time.Now()
	bar := e.IsTTY && !jsonOut && !quiet
	for r.Status == learn.ReplayQueued || r.Status == learn.ReplayRunning {
		if bar {
			fmt.Fprintf(e.Stderr, "\r  %s %d%%", progressBar(r.Progress.Percent, 32), r.Progress.Percent)
		}
		select {
		case <-ctx.Done():
			return apiErr(e, cl, ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
		if _, err := cl.Do(ctx, "GET", "/v1/replays/"+url.PathEscape(r.ID), nil, &r); err != nil {
			return apiErr(e, cl, err)
		}
	}
	if bar {
		fmt.Fprintf(e.Stderr, "\r\033[K")
	}
	if jsonOut {
		return writeJSON(e, r)
	}
	switch r.Status {
	case learn.ReplayCancelled:
		return failNext(e, ExitFailure, "replay_cancelled", "The replay "+r.ID+" was cancelled.", "Start it again: deedglass policy replay "+pos[0]+" --version "+strconv.Itoa(version)+" --wait")
	case learn.ReplayFailed:
		msg := "The replay failed."
		if r.Error != nil {
			msg = r.Error.Message
		}
		return failNext(e, ExitFailure, "replay_failed", msg, "")
	}
	if !quiet {
		fmt.Fprintf(e.Stdout, "  %s 100%% %s %.1f s\n", progressBar(100, 32), ui.Sep, time.Since(started).Seconds())
	}
	printReplay(e, &r, pos[0])
	return nil
}

func baselineWords(b learn.ReplayBaseline) string {
	if b.Type == learn.BaselineVersion {
		return fmt.Sprintf("v%d re-evaluated", b.Version)
	}
	return "recorded decisions"
}

func progressBar(pct, width int) string {
	n := min(width, max(0, pct*width/100))
	return strings.Repeat("█", n) + strings.Repeat("░", width-n)
}

// replayTime reads now, a duration back from base (7d, 48h), or an RFC 3339 time.
func replayTime(s string, base time.Time, isTo bool) (time.Time, error) {
	switch {
	case s == "now" && isTo:
		return base, nil
	case strings.HasSuffix(s, "d"):
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err == nil && n > 0 && n <= 90 && !isTo {
			return base.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 && !isTo {
		return base.Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q isn't a time this command reads", s)
	}
	return t.UTC(), nil
}

// printReplay prints a finished replay (S27 §5).
func printReplay(e *Env, r *learn.Replay, policyRef string) {
	st, w := e.Style, e.Stdout
	s := r.Summary
	if s == nil {
		return
	}
	fmt.Fprintln(w, st.OKLine(learn.ReplaySummaryLine(*s)))
	width := 0
	for _, t := range r.Transitions {
		width = max(width, ui.Width(t.From+" → "+t.To))
	}
	for _, t := range r.Transitions {
		var rules []string
		for i, br := range t.ByRule {
			if i == 3 {
				break
			}
			rules = append(rules, ruleWord(br.RuleID))
		}
		line := strings.Join(rules, ", ")
		if h := learn.HumanOutcomeLine(t.HumanOutcome); h != "" {
			line += " " + ui.Sep + " " + h
		}
		fmt.Fprintf(w, "  %s  %6s   %s\n", ui.Pad(t.From+" → "+t.To, width), deed.FormatCount(t.Count), line)
	}
	if u := learn.UnknownLine(derefUnknown(r.Unknown)); u != "" {
		fmt.Fprintln(w, "  "+st.Amber(ui.Unknown)+" "+deed.FormatCount(s.Unknown)+" unknown   "+u)
	}
	if r.SQLGuard != nil && len(r.SQLGuard.Recorded) > 0 && (len(r.SQLGuard.Recorded) > 1 || r.SQLGuard.Recorded[0] != r.SQLGuard.Current) {
		fmt.Fprintf(w, "  sqlguard   recorded %s %s replayed with %s\n", strings.Join(r.SQLGuard.Recorded, ", "), ui.Sep, r.SQLGuard.Current)
	}
	if r.ResultDigest != nil {
		fmt.Fprintf(w, "  result %s %s same inputs and version always give this digest\n", shortDigest(*r.ResultDigest), ui.Sep)
	}
	fmt.Fprintf(w, "  next       deedglass policy activate %s --version %d --mode shadow --reason \"…\"\n", policyRef, r.Candidate.Version)
}

func ruleWord(id string) string {
	if id == "" {
		return "the default"
	}
	return id
}

func derefUnknown(u *learn.UnknownCounts) learn.UnknownCounts {
	if u == nil {
		return learn.UnknownCounts{}
	}
	return *u
}

// --- insights stats ----------------------------------------------------------------------------------

func runInsightsStats(ctx context.Context, e *Env, args []string) error {
	var (
		window, by            = "7d", "rule"
		agent, tool, from, to string
		compare, jsonOut      bool
	)
	pos, err := parseInterleaved("deedglass insights stats", args, func(fs *flag.FlagSet) {
		fs.StringVar(&window, "window", window, "7d or 30d")
		fs.StringVar(&from, "from", from, "RFC 3339 start (with --to, instead of --window)")
		fs.StringVar(&to, "to", to, "RFC 3339 end")
		fs.StringVar(&by, "by", by, "rule, tool, agent or none")
		fs.StringVar(&agent, "agent", agent, "only this agent (agt_…)")
		fs.StringVar(&tool, "tool", tool, "only this tool, or a glob like stripe.*")
		fs.BoolVar(&compare, "compare", compare, "compare with the previous window")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageErr(errors.New("deedglass insights stats takes flags only"), "deedglass insights stats --window 7d --by rule")
	}
	q := url.Values{"by": {by}}
	if from != "" || to != "" {
		q.Set("from", from)
		q.Set("to", to)
	} else {
		q.Set("window", window)
	}
	if agent != "" {
		q.Set("agent", agent)
	}
	if tool != "" {
		q.Set("tool", tool)
	}
	if compare {
		q.Set("compare", "previous")
	}
	cl, err := apiClient(e)
	if err != nil {
		return err
	}
	var s learn.StatsObject
	if _, err := cl.Do(ctx, "GET", "/v1/insights/stats?"+q.Encode(), nil, &s); err != nil {
		return apiErr(e, cl, err)
	}
	if jsonOut || e.Globals.JSON {
		return writeJSON(e, s)
	}
	printStats(e, &s)
	return nil
}

func pct(x float64) string {
	v := x * 100
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f%%", v)
	}
	return fmt.Sprintf("%.1f%%", v)
}

func secondsWords(s *int64) string {
	if s == nil {
		return ui.None
	}
	d := time.Duration(*s) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", *s)
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}

func printStats(e *Env, s *learn.StatsObject) {
	w, st := e.Stdout, e.Style
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		loc = time.UTC
	}
	t := s.Totals
	fmt.Fprintf(w, "Insights %s %s to %s %s %s %s actions\n", ui.Sep, s.Window.From.In(loc).Format("2006-01-02 15:04"),
		s.Window.To.In(loc).Format("2006-01-02 15:04 MST"), ui.Sep, fmtDays(s.Window.Days), deed.FormatCount(t.Actions))
	if s.Empty != nil {
		fmt.Fprintln(w, st.UnknownLine(*s.Empty))
		return
	}
	fmt.Fprintf(w, "  decisions  allow %s %s deny %s %s require_approval %s %s deny rate %s\n", deed.FormatCount(t.Decisions.Allow), ui.Sep,
		deed.FormatCount(t.Decisions.Deny), ui.Sep, deed.FormatCount(t.Decisions.RequireApproval), ui.Sep, pct(t.DenyRate))
	if t.Approval.Requested > 0 {
		rate := ui.None
		if t.Approval.Rate != nil {
			rate = pct(*t.Approval.Rate)
		}
		fmt.Fprintf(w, "  approvals  %s requested %s %s approved (%s of decided) %s %s expired %s median wait %s %s p95 %s\n",
			deed.FormatCount(t.Approval.Requested), ui.Sep, deed.FormatCount(t.Approval.Approved), rate, ui.Sep,
			deed.FormatCount(t.Approval.Expired), ui.Sep, secondsWords(t.Approval.MedianSeconds), ui.Sep, secondsWords(t.Approval.P95Seconds))
	}
	fmt.Fprintf(w, "  verdicts   %s VERIFIED %s %s MISMATCH %s %s UNVERIFIABLE %s %s not witnessed\n", deed.FormatCount(t.Verdicts.Verified), ui.Sep,
		deed.FormatCount(t.Verdicts.Mismatch), ui.Sep, deed.FormatCount(t.Verdicts.Unverifiable), ui.Sep, deed.FormatCount(t.Verdicts.None))
	if t.LatencyMS.P50 != nil {
		fmt.Fprintf(w, "  latency    p50 %d ms %s p95 %d ms\n", *t.LatencyMS.P50, ui.Sep, derefI(t.LatencyMS.P95))
	}
	if len(s.Groups) > 0 {
		head := map[string]string{learn.ByRule: "RULE", learn.ByTool: "TOOL", learn.ByAgent: "AGENT"}[s.By]
		rows := [][]string{{head, "ACTIONS", "DENY", "APPROVED", "MEDIAN WAIT", "VERIFIED", "MISMATCH", "UNVERIFIABLE"}}
		for _, g := range s.Groups {
			key := ""
			switch {
			case g.Key == nil:
			case g.Key.RuleID != nil:
				key = *g.Key.RuleID
			case g.Key.Tool != nil:
				key = *g.Key.Tool
			case g.Key.AgentName != nil:
				key = *g.Key.AgentName
			}
			appr := ui.None
			if g.Approval.Rate != nil {
				appr = fmt.Sprintf("%s (%s)", deed.FormatCount(g.Approval.Approved), pct(*g.Approval.Rate))
			}
			rows = append(rows, []string{key, deed.FormatCount(g.Actions), deed.FormatCount(g.Decisions.Deny), appr,
				secondsWords(g.Approval.MedianSeconds), deed.FormatCount(g.Verdicts.Verified), deed.FormatCount(g.Verdicts.Mismatch),
				deed.FormatCount(g.Verdicts.Unverifiable)})
		}
		fmt.Fprintf(w, "  by %s\n", s.By)
		var b strings.Builder
		printColumns(&b, rows)
		for _, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
			fmt.Fprintln(w, "    "+line)
		}
	}
	for _, b := range s.Budgets {
		who := "org"
		if b.AgentName != nil && *b.AgentName != "" {
			who = *b.AgentName
		}
		fmt.Fprintf(w, "  budget     %s (%s) %s max %s %s %d %s over 80%%\n", b.Budget, who, ui.Sep, pct(b.MaxUtilisation), ui.Sep,
			b.DaysOver80, plural(b.DaysOver80, "day", "days"))
	}
}

func fmtDays(d float64) string {
	if d == math.Trunc(d) {
		return fmt.Sprintf("%.0f %s", d, plural(int(d), "day", "days"))
	}
	return fmt.Sprintf("%.1f days", d)
}

func derefI(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// --- insights shadow -----------------------------------------------------------------------------------

func runInsightsShadow(ctx context.Context, e *Env, args []string) error {
	var (
		version, from, to string
		jsonOut           bool
	)
	pos, err := parseInterleaved("deedglass insights shadow", args, func(fs *flag.FlagSet) {
		fs.StringVar(&version, "shadow-version", version, "a shadow version id (pv_…); default the live shadow")
		fs.StringVar(&from, "from", from, "RFC 3339 start")
		fs.StringVar(&to, "to", to, "RFC 3339 end")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageErr(errors.New("deedglass insights shadow takes flags only"), "deedglass insights shadow")
	}
	q := url.Values{}
	for k, v := range map[string]string{"shadow_version": version, "from": from, "to": to} {
		if v != "" {
			q.Set(k, v)
		}
	}
	cl, err := apiClient(e)
	if err != nil {
		return err
	}
	var r learn.ShadowReport
	path := "/v1/insights/shadow"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	if _, err := cl.Do(ctx, "GET", path, nil, &r); err != nil {
		return apiErr(e, cl, err)
	}
	if jsonOut || e.Globals.JSON {
		return writeJSON(e, r)
	}
	w := e.Stdout
	fmt.Fprintln(w, r.Sentence)
	fmt.Fprintf(w, "  agreement  %s of %s actions %s %s disagree\n", pct(r.Summary.Agreement), deed.FormatCount(r.Summary.Evaluated), ui.Sep,
		deed.FormatCount(r.Summary.Disagree))
	for _, t := range r.Transitions {
		var rules []string
		for i, br := range t.ByRule {
			if i == 3 {
				break
			}
			rules = append(rules, fmt.Sprintf("%s %s", ruleWord(br.RuleID), deed.FormatCount(br.Count)))
		}
		line := strings.Join(rules, ", ")
		if h := learn.HumanOutcomeLine(t.HumanOutcome); h != "" {
			line += " " + ui.Sep + " " + h
		}
		fmt.Fprintf(w, "  %s → %s  %s   %s\n", t.Enforce, t.Shadow, deed.FormatCount(t.Count), line)
	}
	return nil
}

// --- insights anomalies -------------------------------------------------------------------------------

func runInsightsAnomalies(ctx context.Context, e *Env, args []string) error {
	var (
		status, agent, kind string
		days, limit         = 14, 50
		jsonOut             bool
	)
	pos, err := parseInterleaved("deedglass insights anomalies", args, func(fs *flag.FlagSet) {
		fs.StringVar(&status, "status", status, "open, acknowledged, resolved or all (default open and acknowledged)")
		fs.StringVar(&agent, "agent", agent, "only this agent (agt_…)")
		fs.StringVar(&kind, "kind", kind, "only this kind, like volume_spike")
		fs.IntVar(&days, "days", days, "findings from the last N days")
		fs.IntVar(&limit, "limit", limit, "at most this many (1 to 100)")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageErr(errors.New("deedglass insights anomalies takes flags only"), "deedglass insights anomalies --status open")
	}
	now := time.Now().UTC()
	q := url.Values{"limit": {strconv.Itoa(min(max(limit, 1), 100))}, "since": {now.Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)}}
	for k, v := range map[string]string{"status": status, "agent": agent, "kind": kind} {
		if v != "" {
			q.Set(k, v)
		}
	}
	cl, err := apiClient(e)
	if err != nil {
		return err
	}
	var l learn.AnomalyList
	if _, err := cl.Do(ctx, "GET", "/v1/insights/anomalies?"+q.Encode(), nil, &l); err != nil {
		return apiErr(e, cl, err)
	}
	if jsonOut || e.Globals.JSON {
		return writeJSON(e, l)
	}
	printAnomalies(e, &l, days, now)
	return nil
}

func printAnomalies(e *Env, l *learn.AnomalyList, days int, now time.Time) {
	w, st := e.Stdout, e.Style
	active := "none active now"
	if l.ActiveNow > 0 {
		active = fmt.Sprintf("%d active now", l.ActiveNow)
	}
	fmt.Fprintf(w, "%d %s %s %s %s last %d days\n", len(l.Data), plural(len(l.Data), "anomaly", "anomalies"), ui.Sep, active, ui.Sep, days)
	rows := [][]string{}
	for _, a := range l.Data {
		sev := a.Severity
		if a.Severity != learn.SeverityInfo {
			sev = st.Amber(a.Severity) // signals, not verdicts: amber, never red
		}
		status := ""
		if a.Status != learn.StatusOpen {
			status = " (" + a.Status + ")"
		}
		rows = append(rows, []string{sev, a.Kind, a.Agent.Name, a.Title + status, agoWords(now.Sub(a.Window.End)), shortID(a.ID)})
	}
	if len(rows) > 0 {
		var b strings.Builder
		printColumns(&b, rows)
		for _, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
			fmt.Fprintln(w, "  "+line)
		}
	}
	for _, bb := range l.BaselineBuilding {
		fmt.Fprintln(w, "  "+bb.Sentence)
	}
	switch {
	case l.Notice != nil:
		fmt.Fprintln(w, "  "+*l.Notice)
	case l.AnomalyFacts:
		fmt.Fprintln(w, "  "+learn.AnomalyFactsOnLine)
	default:
		fmt.Fprintln(w, "  "+learn.AnomalyFactsOffLine)
	}
}

// --- insights settings ---------------------------------------------------------------------------------

func runInsightsSettings(ctx context.Context, e *Env, args []string) error {
	var (
		facts, reason         string
		activeMins, staleMins int
		jsonOut               bool
	)
	pos, err := parseInterleaved("deedglass insights settings", args, func(fs *flag.FlagSet) {
		fs.StringVar(&facts, "anomaly-facts", facts, "on or off: let this org's policies read anomaly.*")
		fs.IntVar(&activeMins, "active-minutes", activeMins, "how long an anomaly stays active after its last finding (5 to 1,440)")
		fs.IntVar(&staleMins, "stale-minutes", staleMins, "how old detection may be before policies treat agents as anomalous (10 to 240)")
		fs.StringVar(&reason, "reason", reason, "why (required for a change; sealed in the deed)")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageErr(errors.New("deedglass insights settings takes flags only"), `deedglass insights settings --anomaly-facts on --reason "…"`)
	}
	cl, err := apiClient(e)
	if err != nil {
		return err
	}
	var o learn.SettingsObject
	change := facts != "" || activeMins != 0 || staleMins != 0
	if change {
		body := map[string]any{"reason": reason}
		switch facts {
		case "":
		case "on":
			body["anomaly_facts"] = true
		case "off":
			body["anomaly_facts"] = false
		default:
			return usageErr(fmt.Errorf("--anomaly-facts is on or off (got %q)", facts), `deedglass insights settings --anomaly-facts on --reason "…"`)
		}
		if activeMins != 0 {
			body["anomaly_active_minutes"] = activeMins
		}
		if staleMins != 0 {
			body["anomaly_stale_after_minutes"] = staleMins
		}
		if _, err := cl.Do(ctx, "PATCH", "/v1/insights/settings", body, &o); err != nil {
			return apiErr(e, cl, err)
		}
	} else if _, err := cl.Do(ctx, "GET", "/v1/insights/settings", nil, &o); err != nil {
		return apiErr(e, cl, err)
	}
	if jsonOut || e.Globals.JSON {
		return writeJSON(e, o)
	}
	w, st := e.Stdout, e.Style
	on := "off"
	if o.AnomalyFacts {
		on = "on"
	}
	fmt.Fprintf(w, "  anomaly facts  %s %s active %d min after a finding %s stale after %d min\n", on, ui.Sep, o.AnomalyActiveMinutes, ui.Sep,
		o.AnomalyStaleAfterMinutes)
	if o.Notice != nil {
		fmt.Fprintln(w, "  "+*o.Notice)
	}
	if o.Deed != nil {
		fmt.Fprintln(w, st.OKLine("Settings changed "+ui.Sep+" deed "+shortID(*o.Deed)+" sealed"))
	}
	return nil
}

// --- insights detect (operator, database-direct) ------------------------------------------------------

func runInsightsDetect(ctx context.Context, e *Env, args []string) error {
	var (
		backfill, orgRef string
		jsonOut          bool
	)
	pos, err := parseInterleaved("deedglass insights detect", args, func(fs *flag.FlagSet) {
		fs.StringVar(&backfill, "backfill", backfill, "walk this far back hour by hour, like 7d or 48h (dev: simulated history)")
		fs.StringVar(&orgRef, "org", orgRef, "the org (org_… or its slug); default every org with recent actions")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageErr(errors.New("deedglass insights detect takes flags only"), "deedglass insights detect --backfill 7d --org acme-sim")
	}
	jsonOut = jsonOut || e.Globals.JSON
	orgRef = firstNonEmpty(orgRef, e.Globals.Org) // --org is also a global flag
	cfg, warnings, err := config.Load(e.LoadOptions())
	var ps *config.Problems
	if errors.As(err, &ps) {
		return configProblems(e, ps, warnings)
	}
	if err != nil {
		return err
	}
	if backfill != "" && cfg.Env != config.Dev {
		return failNext(e, ExitFailure, "dev_only", "insights detect --backfill only runs with DEEDGLASS_ENV=dev. It judges past hours as if they were now.",
			"Run deedglass insights detect without --backfill; the server runs it every 5 minutes.")
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
	led := newLedger(db, nil, cfg, slog.New(slog.DiscardHandler))
	det := &learn.Detector{Store: learnstore.New(db, led), Log: slog.New(slog.DiscardHandler)}
	var orgs []ids.OrgID
	if orgRef != "" {
		o, err := orgByRef(ctx, db, orgRef)
		if err != nil {
			return failNext(e, ExitFailure, "not_found", "No org "+orgRef+" in this database.", "Check the slug or id: deedglass org list")
		}
		orgs = []ids.OrgID{o.ID}
	} else if orgs, err = db.LearnOrgs(ctx, learn.DetectRecent); err != nil {
		return err
	}
	now := time.Now().UTC()
	var total learn.PassStats
	for _, org := range orgs {
		var st learn.PassStats
		if backfill != "" {
			d, err := backfillSpan(backfill)
			if err != nil {
				return usageErr(err, "--backfill 7d or --backfill 48h")
			}
			st, err = det.Backfill(ctx, org, now.Add(-d), now)
			if err != nil {
				return err
			}
		} else if st, err = det.DetectOrg(ctx, org, now); err != nil {
			return err
		}
		total.Orgs += st.Orgs
		total.Windows += st.Windows
		total.Findings += st.Findings
		total.Inserted += st.Inserted
		total.Extended += st.Extended
		total.Published += st.Published
		total.Closed += st.Closed
		total.Skipped += st.Skipped
	}
	if jsonOut {
		return writeJSON(e, map[string]any{"object": "detect_run", "backfill": backfill, "stats": total})
	}
	what := "the last hour"
	if backfill != "" {
		what = fmt.Sprintf("%s %s, hour by hour", deed.FormatCount(int64(total.Windows)), plural(total.Windows, "hour", "hours"))
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Detection ran over %s for %d %s %s %d %s opened %s %d extended %s %d %s",
		what, total.Orgs, plural(total.Orgs, "org", "orgs"), ui.Sep, total.Inserted, plural(total.Inserted, "anomaly", "anomalies"), ui.Sep,
		total.Extended, ui.Sep, total.Findings, plural(total.Findings, "finding", "findings"))))
	fmt.Fprintln(e.Stdout, "  next       deedglass insights anomalies")
	return nil
}

func backfillSpan(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err == nil && n >= 1 && n <= 30 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
		return 0, fmt.Errorf("--backfill %q must be 1d to 30d", s)
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < time.Hour || d > learn.MaxBackfill {
		return 0, fmt.Errorf("--backfill %q must be 1h to 30d", s)
	}
	return d, nil
}

// orgByRef finds an org by id or slug (operator commands).
func orgByRef(ctx context.Context, db *store.DB, ref string) (store.Org, error) {
	var o store.Org
	err := db.SystemTx(ctx, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if id, perr := ids.Parse[ids.OrgKind](ref); perr == nil {
			o, err = store.GetOrg(ctx, tx, id)
		} else {
			o, err = store.GetOrgBySlug(ctx, tx, ref)
		}
		return err
	})
	return o, err
}

var _ = deedglass.Error{}
