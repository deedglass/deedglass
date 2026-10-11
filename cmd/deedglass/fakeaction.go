package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/ledger/fake"
	"github.com/deedglass/deedglass/internal/store"
)

type fakeOpts struct {
	orgSlug string
	n       int
	seed    uint64
	kinds   string
	agents  int
	batch   int
	pace    time.Duration
	quiet   bool
}

var fakeValues fakeOpts

func fakeActionFlags(fs *flag.FlagSet) {
	o := &fakeValues
	*o = fakeOpts{}
	fs.StringVar(&o.orgSlug, "org-slug", "demo", "the demo org's slug (created when missing); --org picks an existing org instead")
	fs.IntVar(&o.n, "n", 20, "how many actions")
	fs.Uint64Var(&o.seed, "seed", 42, "the same seed gives the same tools, arguments, latencies and verdicts")
	fs.StringVar(&o.kinds, "kinds", "", "comma list: allow, deny, mismatch, unverifiable, failed, pending, amend")
	fs.IntVar(&o.agents, "agents", 3, "how many demo agents (1 to 8)")
	fs.IntVar(&o.batch, "batch", 1, "actions per transaction (500 for bulk seeding)")
	fs.DurationVar(&o.pace, "pace", 0, "pause between actions for live demos (200ms)")
	fs.BoolVar(&o.quiet, "quiet", false, "print only the final line")
}

const (
	msgFakeGuard  = "dev fake-action only runs with DEEDGLASS_ENV=dev. This database may hold real records."
	nextFakeGuard = "Use a throwaway database: make dev, then DEEDGLASS_ENV=dev deedglass dev fake-action"
	demoOwner     = "demo@example.test"
)

func runFakeAction(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass dev fake-action takes no arguments (got %q)", args[0]), "deedglass dev fake-action --n 20 --seed 42")
	}
	// The guard comes first: nothing is loaded or opened outside dev.
	if e.Getenv("DEEDGLASS_ENV") != string(config.Dev) {
		return failNext(e, ExitFailure, "dev_only", msgFakeGuard, nextFakeGuard)
	}
	o := fakeValues
	kinds, err := fake.ParseKinds(o.kinds)
	if err != nil {
		return usageErr(err, "deedglass dev fake-action --kinds allow,deny,mismatch")
	}
	switch {
	case o.n < 1 || o.n > 1_000_000:
		return usageErr(errors.New("--n must be 1 to 1,000,000"), "deedglass dev fake-action --n 20")
	case o.agents < 1 || o.agents > len(fake.AgentNames):
		return usageErr(fmt.Errorf("--agents must be 1 to %d", len(fake.AgentNames)), "deedglass dev fake-action --agents 3")
	case o.batch < 1 || o.batch > 5000:
		return usageErr(errors.New("--batch must be 1 to 5,000"), "deedglass dev fake-action --batch 500")
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
		return failNext(e, ExitFailure, "dev_only", msgFakeGuard, nextFakeGuard)
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
	org, err := fakeOrg(ctx, e, db, o.orgSlug)
	if err != nil {
		return err
	}
	agents, err := fakeAgents(ctx, db, org, o.agents)
	if err != nil {
		return seedErr(err)
	}
	log := slog.New(slog.DiscardHandler)
	signer, err := loadSealKeyQuiet(ctx, e, cfg, db, log, true)
	if err != nil {
		return err
	}
	led := newLedger(db, signer, cfg, log)
	st := e.Style
	t := targetOf(cfg.DatabaseURL)
	if !o.quiet && !e.Globals.JSON {
		fmt.Fprintf(e.Stdout, "%s %s fake %s for %s (%s) %s seed %d %s database %s\n", st.Violet(ui.Pending), deed.FormatCount(int64(o.n)),
			plural(o.n, "action", "actions"), shortID(org.ID.String()), org.Slug, ui.Sep, o.seed, ui.Sep, t.addr())
	}
	plans := fake.Generate(o.seed, o.n, kinds, o.agents)
	started := time.Now()
	progress := func(done, total int) {}
	if e.IsTTY && !o.quiet && !e.Globals.JSON && o.n >= 2000 {
		progress = func(done, total int) {
			fmt.Fprintf(e.Stderr, "\r  %s of %s", deed.FormatCount(int64(done)), deed.FormatCount(int64(total)))
			if done == total {
				fmt.Fprint(e.Stderr, "\r\x1b[K")
			}
		}
	}
	res, err := fake.Run(ctx, led, org.ID, agents, plans, fake.Options{Batch: o.batch, Pace: o.pace, OnProgress: progress})
	if err != nil {
		return ledgerErr(e, err)
	}
	took := time.Since(started)
	chain := "chain not checked"
	var check *ledger.ChainCheck
	if res.Deeds > 0 {
		check, err = led.CheckChain(ctx, org.ID, ledger.Range{}, ledger.CheckOptions{})
		if err != nil {
			return err
		}
		chain = "chain " + check.Status
	}
	if e.Globals.JSON {
		counts := map[string]int{}
		for k, n := range res.Counts {
			counts[string(k)] = n
		}
		out := map[string]any{"object": "fake_action_run", "org": org.ID.String(), "seed": o.seed, "actions": res.Actions,
			"deeds": res.Deeds, "first_seq": res.FirstSeq, "last_seq": res.LastSeq, "kinds": counts, "chain": check,
			"duration_ms": took.Milliseconds()}
		return writeAPIJSON(e, out)
	}
	if !o.quiet {
		fakeSummary(e, res)
	}
	if res.Deeds == 0 {
		fmt.Fprintln(e.Stdout, st.UnknownLine("No records sealed "+ui.Sep+" every action is waiting for approval"))
		return nil
	}
	line := fmt.Sprintf("%s sealed %s seq %s → %s %s %s", deedsWord(int64(res.Deeds)), ui.Sep, deed.FormatCount(res.FirstSeq),
		deed.FormatCount(res.LastSeq), ui.Sep, chain)
	if check != nil && check.Status != ledger.CheckIntact {
		fmt.Fprintln(e.Stdout, st.FailLine(line))
		return &ExitError{Code: ExitFailure, Err: errors.New(chain), printed: true}
	}
	fmt.Fprintln(e.Stdout, st.OKLine(line))
	if !o.quiet {
		fmt.Fprintf(e.Stdout, "  %s deedglass actions list   %s   deedglass deeds export --out %s.tar.gz\n", ui.Pad("next", 10), ui.Sep, org.Slug)
	}
	return nil
}

// fakeSummary prints one row per outcome (S06 §5).
func fakeSummary(e *Env, res *fake.Result) {
	st := e.Style
	w := 2
	for _, n := range res.Counts {
		w = max(w, len(deed.FormatCount(int64(n))))
	}
	row := func(sym string, n int, what, detail string) {
		if n == 0 {
			return
		}
		c := deed.FormatCount(int64(n))
		fmt.Fprintf(e.Stdout, "  %s %s%s %s %s\n", sym, strings.Repeat(" ", w-len(c)), c, ui.Pad(what, 14), detail)
	}
	counts := func(m map[string]int, keys ...string) string {
		var parts []string
		for _, k := range keys {
			if m[k] > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
			}
		}
		var rest []string // anything not listed, sorted
		for k, v := range m {
			if !slices.Contains(keys, k) && v > 0 {
				rest = append(rest, fmt.Sprintf("%s %d", k, v))
			}
		}
		slices.Sort(rest)
		parts = append(parts, rest...)
		return strings.Join(parts, " "+ui.Sep+" ")
	}
	row(st.Teal(ui.OK), res.Counts[fake.Allow], "matches", counts(res.Levels, "L0", "L1", "L2"))
	row(st.Red(ui.Fail), res.Counts[fake.Deny], "denied", counts(res.Rules, "block-destructive-sql", "refund-limit", "budget"))
	row(st.Bold(st.Red(ui.Fail)), res.Counts[fake.Mismatch], "doesn't match", "Proven "+ui.Sep+" postgres.execute")
	row(st.Amber(ui.Unknown), res.Counts[fake.Unverifiable], "can't check", "timeout")
	row(ui.Sep, res.Counts[fake.Failed], "failed", counts(res.Errors, "upstream_error", "timeout"))
	row(st.Violet(ui.Pending), res.Counts[fake.Pending], "pending", "waiting for approval")
	row(st.Teal(ui.OK), res.Counts[fake.Amend], "amended", "Can't check → Matches, sealed as amendments")
}

// fakeOrg picks --org, or finds or creates the demo org by slug.
func fakeOrg(ctx context.Context, e *Env, db *store.DB, slug string) (store.Org, error) {
	if e.Globals.Org != "" {
		return pickOrg(ctx, e, db)
	}
	var org store.Org
	err := db.SystemTx(ctx, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		org, err = store.GetOrgBySlug(ctx, tx, slug)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		org, _, _, err = db.CreateOrg(ctx, store.NewOrg{Name: "Demo", Slug: slug, OwnerEmail: demoOwner, OwnerName: "Demo"})
	}
	if err != nil {
		return org, seedErr(err)
	}
	return org, nil
}

// fakeAgents finds or creates the first n demo agents.
func fakeAgents(ctx context.Context, db *store.DB, org store.Org, n int) ([]store.Agent, error) {
	var out []store.Agent
	err := db.OrgTx(ctx, org.ID, func(ctx context.Context, tx pgx.Tx) error {
		out = out[:0]
		for _, name := range fake.AgentNames[:n] {
			a, err := store.GetAgentByName(ctx, tx, org.ID, name)
			if errors.Is(err, store.ErrNotFound) {
				a, err = store.CreateAgent(ctx, tx, org.ID, store.NewAgent{Name: name, Description: "A demo agent (dev fake-action).",
					Labels: map[string]string{"demo": "true"}, CreatedBy: "system"})
			}
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return nil
	})
	return out, err
}
