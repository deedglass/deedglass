package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/learn/sim"
	"github.com/deedglass/deedglass/internal/store"
)

// deedglass dev simulate (S27 §3.3): a week of believable, seeded traffic through the real Gate, in
// a fresh simulated org (or appended to one). Dev only.

type simulateOpts struct {
	scenario, end, org, orgSlug string
	days                        int
	seed                        uint64
	appendTo, quiet             bool
	pace                        time.Duration
}

var simulateValues simulateOpts

func simulateFlags(fs *flag.FlagSet) {
	o := &simulateValues
	*o = simulateOpts{}
	fs.StringVar(&o.scenario, "scenario", "", "the scenario file (default: the embedded examples/sim/acme.yaml)")
	fs.IntVar(&o.days, "days", 7, "how many days to simulate (1 to 90)")
	fs.StringVar(&o.end, "end", "now", "when the window ends: now, -2d, -12h, or an RFC 3339 time (truncated to the org-local hour)")
	fs.Uint64Var(&o.seed, "seed", 7, "the same seed and scenario give the same tools, arguments, times, outcomes and approval waits")
	fs.StringVar(&o.org, "org", "", "with --append: the simulated org (org_…)")
	fs.StringVar(&o.orgSlug, "org-slug", "", "the fresh org's slug (default the scenario's), or with --append the org to add to")
	fs.BoolVar(&o.appendTo, "append", false, "add days to an existing simulated org instead of creating a fresh one")
	fs.DurationVar(&o.pace, "pace", 0, "pause between actions (live demos, like 200ms)")
	fs.BoolVar(&o.quiet, "quiet", false, "print only the result line")
}

// The simulator's guard and refusals (S27 §5), each with a next step.
const (
	msgSimGuard    = "dev simulate only runs with DEEDGLASS_ENV=dev. It writes a simulated org into this database."
	nextSimGuard   = "Use a throwaway database: make dev, then DEEDGLASS_ENV=dev deedglass dev simulate"
	nextSimNotSim  = "Run without --org to create a fresh simulated org."
	nextSimOverlap = "Use --end now with --days 1 or fewer, or simulate into a fresh org."
)

func runDevSimulate(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass dev simulate takes flags only (got %q)", args[0]), "deedglass dev simulate --days 7 --seed 7")
	}
	// The guard comes first: nothing is loaded or opened outside dev.
	if e.Getenv("DEEDGLASS_ENV") != string(config.Dev) {
		return failNext(e, ExitFailure, "dev_only", msgSimGuard, nextSimGuard)
	}
	o := simulateValues
	o.org = firstNonEmpty(o.org, e.Globals.Org) // --org is also a global flag
	if o.days < 1 || o.days > 90 {
		return usageErr(errors.New("--days must be 1 to 90"), "deedglass dev simulate --days 7")
	}
	if o.org != "" && !o.appendTo {
		return usageErr(errors.New("--org names an org to append to; add --append"), "deedglass dev simulate --append --org org_… --days 1")
	}
	end, err := parseSimEnd(o.end, time.Now())
	if err != nil {
		return usageErr(err, "deedglass dev simulate --end now, --end -2d or --end 2026-10-09T00:00:00Z")
	}
	sc := sim.Default()
	if o.scenario != "" {
		if sc, err = sim.Load(o.scenario); err != nil {
			return &ExitError{Code: ExitUsage, ErrCode: "scenario_invalid", Err: err, Next: "Fix the scenario file; docs/SIMULATOR.md lists every key."}
		}
	}
	cfg, warnings, err := config.Load(e.LoadOptions())
	var ps *config.Problems
	if errors.As(err, &ps) {
		return configProblems(e, ps, warnings)
	}
	if err != nil {
		return err
	}
	if cfg.Env != config.Dev {
		return failNext(e, ExitFailure, "dev_only", msgSimGuard, nextSimGuard)
	}
	// A throwaway dev database: commits don't wait for the disk (store.Options.AsyncCommit).
	_, db, led, people, done, err := openPeopleWith(ctx, e, store.Options{MaxConns: 8, AsyncCommit: true})
	if err != nil {
		return err
	}
	defer done()
	st := e.Style
	t := targetOf(cfg.DatabaseURL)
	plain := !o.quiet && !e.Globals.JSON
	if plain {
		fmt.Fprintf(e.Stdout, "%s Simulating %d %s for %s %s seed %d %s database %s\n", st.Violet(ui.Pending), o.days, plural(o.days, "day", "days"),
			orgLabel(o, sc), ui.Sep, o.seed, ui.Sep, t.addr())
	}
	var progress func(done, total int)
	if e.IsTTY && plain {
		progress = func(n, total int) {
			if n%250 == 0 || n == total {
				fmt.Fprintf(e.Stderr, "\r  %s of %s actions", deed.FormatCount(int64(n)), deed.FormatCount(int64(total)))
				if n == total {
					fmt.Fprint(e.Stderr, "\r\033[K")
				}
			}
		}
	}
	res, err := sim.Run(ctx, sim.Env{DB: db, Ledger: led, People: people, Log: slog.New(slog.DiscardHandler)}, sim.Options{
		Scenario: sc, Seed: o.seed, Days: o.days, End: end, OrgID: o.org, OrgSlug: o.orgSlug, Append: o.appendTo, Pace: o.pace, Progress: progress})
	var oe *sim.OverlapError
	switch {
	case errors.Is(err, sim.ErrNotSimulated):
		name := firstNonEmpty(o.orgSlug, o.org)
		return failNext(e, ExitFailure, "not_simulated", name+" is not a simulated org. dev simulate only appends to orgs it created.", nextSimNotSim)
	case errors.As(err, &oe):
		return failNext(e, ExitFailure, "simulation_overlap", fmt.Sprintf("%s already has actions up to %s. The new days would overlap.",
			oe.Slug, oe.Until.In(sc.Org.Location).Format("2006-01-02 15:04 MST")), nextSimOverlap)
	case errors.Is(err, sim.ErrNoOrg):
		return failNext(e, ExitFailure, "not_found", "No org "+firstNonEmpty(o.org, o.orgSlug)+" in this database.", nextSimNotSim)
	case err != nil:
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, simJSON(res))
	}
	printSimulation(e, res, o.quiet)
	if !res.ChainIntact {
		return &ExitError{Code: ExitFailure, Err: errors.New("the deed chain didn't check out"), printed: true}
	}
	return nil
}

func orgLabel(o simulateOpts, sc *sim.Scenario) string {
	if o.appendTo {
		return firstNonEmpty(o.org, o.orgSlug)
	}
	return "a new org (" + firstNonEmpty(o.orgSlug, sc.Org.Slug) + ")"
}

// parseSimEnd reads --end: now, -2d, -12h, or an RFC 3339 time.
func parseSimEnd(s string, now time.Time) (time.Time, error) {
	switch {
	case s == "" || s == "now":
		return now, nil
	case strings.HasPrefix(s, "-") && strings.HasSuffix(s, "d"):
		var n int
		if _, err := fmt.Sscanf(s, "-%dd", &n); err != nil || n < 0 || n > 365 {
			return time.Time{}, fmt.Errorf("--end %q isn't a number of days back", s)
		}
		return now.Add(-time.Duration(n) * 24 * time.Hour), nil
	case strings.HasPrefix(s, "-"):
		d, err := time.ParseDuration(s[1:])
		if err != nil || d < 0 {
			return time.Time{}, fmt.Errorf("--end %q isn't a duration back", s)
		}
		return now.Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--end %q isn't now, -2d or an RFC 3339 time", s)
	}
	return t, nil
}

func kvLine(kvs []sim.KV, limit int) string {
	var parts []string
	for i, k := range kvs {
		if i == limit {
			break
		}
		parts = append(parts, k.Key+" "+deed.FormatCount(k.Count))
	}
	return strings.Join(parts, " "+ui.Sep+" ")
}

// printSimulation prints the summary (S27 §5).
func printSimulation(e *Env, r *sim.Result, quiet bool) {
	st, w := e.Style, e.Stdout
	chain := "chain intact"
	if !r.ChainIntact {
		chain = "chain " + r.ChainStatus
	}
	head := fmt.Sprintf("%s actions through the Gate %s %s deeds sealed %s %s", deed.FormatCount(int64(r.Submitted)), ui.Sep,
		deed.FormatCount(r.Deeds), ui.Sep, chain)
	if quiet {
		fmt.Fprintln(w, st.OKLine(head))
		return
	}
	loc := r.Scenario.Org.Location
	fmt.Fprintf(w, "  org        %s (%s) %s %s to %s\n", r.Slug, r.Org.String(), ui.Sep, r.Window.Start.In(loc).Format("2006-01-02 15:04"),
		r.Window.End.In(loc).Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(w, "  agents     %s\n", strings.Join(r.Agents, " "+ui.Sep+" "))
	pol := fmt.Sprintf("%s v%d enforced", r.Policy, r.EnforceVersion)
	if r.ShadowVersion > 0 {
		pol += fmt.Sprintf(" %s %s v%d in shadow from day %d", ui.Sep, r.Policy, r.ShadowVersion, r.ShadowFromDay)
	}
	fmt.Fprintf(w, "  policies   %s\n", pol)
	if r.ChainIntact {
		fmt.Fprintln(w, st.OKLine(head))
	} else {
		fmt.Fprintln(w, st.FailLine(head))
	}
	c := r.Counts
	n := func(v int64) string { return fmt.Sprintf("%5s", deed.FormatCount(v)) }
	fmt.Fprintf(w, "  %s %s verified      %s   (simulated witness)\n", st.Teal(ui.OK), n(c.Verified), kvLine(c.ByLevel, 3))
	fmt.Fprintf(w, "  %s %s denied        %s\n", st.Red(ui.Fail), n(c.Denied), kvLine(c.DeniedBy, 3))
	fmt.Fprintf(w, "  %s %s mismatch      %s\n", st.Red(ui.Fail), n(c.Mismatch), kvLine(c.MismatchBy, 2))
	fmt.Fprintf(w, "  %s %s unverifiable  %s\n", st.Amber(ui.Unknown), n(c.Unverifiable), kvLine(c.UnverBy, 2))
	fmt.Fprintf(w, "  %s %s failed        %s\n", ui.Sep, n(c.Failed), kvLine(c.FailedBy, 2))
	fmt.Fprintf(w, "  %s %s pending       waiting for approval\n", st.Violet(ui.Pending), n(c.Pending))
	a := r.Approvals
	fmt.Fprintf(w, "  approvals  %s requested %s %s approved %s %s denied %s %s expired %s median wait %d min\n",
		deed.FormatCount(a.Requested), ui.Sep, deed.FormatCount(a.Approved), ui.Sep, deed.FormatCount(a.Denied), ui.Sep,
		deed.FormatCount(a.Expired), ui.Sep, int(math.Round(a.MedianWaitMin)))
	var inc []string
	for _, i := range r.Incidents {
		inc = append(inc, incidentWords(i))
	}
	if len(inc) > 0 {
		fmt.Fprintf(w, "  incidents  %s\n", strings.Join(inc, " "+ui.Sep+" "))
	}
	fmt.Fprintf(w, "  next       deedglass insights detect --backfill %dd --org %s   %s   deedglass insights stats --window 7d\n",
		r.Window.Days, r.Slug, ui.Sep)
}

func incidentWords(i sim.IncidentSum) string {
	days := make([]string, len(i.Days))
	for k, d := range i.Days {
		days[k] = fmt.Sprint(d)
	}
	when := "day " + strings.Join(days, ", ")
	if len(i.Days) > 1 {
		when = "days " + strings.Join(days, ", ")
	}
	switch i.Kind {
	case "mismatch":
		return fmt.Sprintf("%s %d mismatches (%s)", i.Agent, i.Count, when)
	case "first_use":
		return fmt.Sprintf("%s first %s (%s)", i.Agent, i.Tool, when)
	case "spike":
		return fmt.Sprintf("%s spike ×%g (%s)", i.Agent, i.Factor, when)
	case "off_hours":
		return fmt.Sprintf("%s %d off-hours calls (%s)", i.Agent, i.Count, when)
	case "new_user_refs":
		return fmt.Sprintf("%s %d new users (%s)", i.Agent, i.Count, when)
	case "budget_pressure":
		return fmt.Sprintf("%s budget pressure (%s)", i.Agent, when)
	}
	return i.Agent + " " + i.Kind + " (" + when + ")"
}

func simJSON(r *sim.Result) map[string]any {
	return map[string]any{"object": "simulation", "org": r.Org.String(), "slug": r.Slug, "appended": r.Appended, "seed": r.Seed,
		"window": map[string]any{"start": r.Window.Start, "end": r.Window.End, "days": r.Window.Days}, "planned": r.Planned,
		"plan_digest": r.PlanDigest, "actions": r.Submitted, "deeds": r.Deeds, "chain_intact": r.ChainIntact, "agents": r.Agents,
		"policy": r.Policy, "enforce_version": r.EnforceVersion, "shadow_version": r.ShadowVersion, "counts": r.Counts,
		"approvals": r.Approvals, "elapsed_ms": r.Elapsed.Milliseconds()}
}
