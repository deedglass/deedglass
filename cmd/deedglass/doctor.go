package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/cli/mark"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/doctor"
	"github.com/deedglass/deedglass/internal/setup"
	"github.com/deedglass/deedglass/internal/store/migrate"
	"github.com/deedglass/deedglass/internal/store/migrations"
)

// deedglass doctor (S32 §3.10): sixteen read-only checks. Exit 0 all OK (warnings allowed), 1 any
// problem, 2 doctor itself couldn't run, 3 the server refused the API key.

var doctorValues struct {
	only  string
	quiet bool
}

func doctorFlags(fs *flag.FlagSet) {
	doctorValues.only, doctorValues.quiet = "", false
	fs.StringVar(&doctorValues.only, "only", "", "run only these checks: ids or prefixes, like db.* or seal.key,clock.skew")
	fs.BoolVar(&doctorValues.quiet, "quiet", false, "don't print the quickstart step line")
}

// withInitStack fills what deedglass init wrote in .deedglass/stack.json into the settings, under
// whatever the environment already sets: dev, the runtime role's database URL, the seal key and the
// listen address. Doctor and the DB-direct Ledger commands (actions list, deeds export, ledger
// check, …) use it, so they work in a directory deedglass init set up (S33's Quickstart test).
func withInitStack(opts config.LoadOptions) (config.LoadOptions, *setup.Stack, error) {
	env := maps.Clone(opts.Env)
	if env == nil {
		env = map[string]string{}
	}
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}
	st, err := setup.ReadStack(dir)
	if err != nil {
		return opts, nil, err
	}
	if st != nil {
		setDefault := func(k, v string) {
			if env[k] == "" {
				env[k] = v
			}
		}
		setDefault("DEEDGLASS_ENV", "dev")
		setDefault("DEEDGLASS_DATABASE_URL", st.AppURL())
		setDefault("DEEDGLASS_MIGRATE_DATABASE_URL", st.MigratorURL())
		setDefault("DEEDGLASS_SEAL_KEY_FILE", st.SealKeyFile)
		setDefault("DEEDGLASS_LISTEN", st.Listen)
		setDefault("DEEDGLASS_BASE_URL", st.Server())
	}
	opts.Env = env
	return opts, st, nil
}

// doctorEnv resolves what doctor checks: the environment first, then what deedglass init wrote in
// .deedglass/ (stack.json and admin.key) when this directory has it.
func doctorEnv(e *Env) (*doctor.Env, error) {
	opts, _, err := stackLoadOptions(e)
	if err != nil {
		return nil, err
	}
	env := opts.Env
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}
	cfg, _, cerr := config.Load(opts)
	d := &doctor.Env{Dir: dir, ConfigErr: cerr, EnvName: "dev", CLIVersion: buildInfo().Version, APIVersion: currentAPIVersion(),
		Now: time.Now, Statfs: statfsFree, Connect: nil}
	if cfg != nil {
		d.Prod = cfg.Env == config.Prod
		d.EnvName = string(cfg.Env)
		d.DBURL = cfg.DatabaseURL.Reveal()
		d.SealKeyFile = cfg.SealKeyFile
		d.VaultKey = cfg.VaultKey.Reveal()
		d.SlotLagAlertMB = cfg.SlotLagAlertMB
	} else {
		d.DBURL = env["DEEDGLASS_DATABASE_URL"]
		d.SealKeyFile = env["DEEDGLASS_SEAL_KEY_FILE"]
	}
	t := resolveServer(e)
	d.Server = t.Server
	d.APIKey = t.keyFor(e, setup.AdminKeyFile)
	d.HTTP = egress.FirstParty(egress.FirstPartyOptions{Timeout: doctor.Timeout})
	ms, err := migrate.Load(migrations.FS)
	if err != nil {
		return nil, err
	}
	d.Migrations = ms
	return d, nil
}

func currentAPIVersion() string {
	in := buildInfo()
	if len(in.APIVersions) > 0 {
		return in.APIVersions[len(in.APIVersions)-1]
	}
	return "2026-10-01"
}

type doctorReport struct {
	Object  string          `json:"object"`
	Results []doctor.Result `json:"results"`
	Summary doctor.Summary  `json:"summary"`
	TookMS  int64           `json:"took_ms"`
}

func runDoctor(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass doctor takes no arguments (got %q)", args[0]), "Run deedglass doctor, or deedglass doctor --only db.*.")
	}
	checks := doctor.Filter(doctor.Checks(), doctorValues.only)
	if len(checks) == 0 {
		return usageErr(fmt.Errorf("--only %q matches no check", doctorValues.only), "Check ids look like db.reachable or db.*; deedglass help doctor lists them.")
	}
	env, err := doctorEnv(e)
	if err != nil {
		return &ExitError{Code: ExitUsage, ErrCode: "doctor_failed", Err: fmt.Errorf("doctor couldn't start: %w", err),
			Next: "Check .deedglass/stack.json (deedglass init writes it), then run deedglass doctor again."}
	}
	t0 := time.Now()
	rs := doctor.Run(ctx, env, checks)
	took := time.Since(t0)
	sum := doctor.Summarize(rs)
	if e.Globals.JSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doctorReport{Object: "doctor_report", Results: rs, Summary: sum, TookMS: took.Milliseconds()}); err != nil {
			return err
		}
	} else {
		mo := e.Mark
		mo.Command = "doctor"
		if il := mark.Inline(mo); il != "" {
			fmt.Fprintln(e.Stdout, il)
		}
		fmt.Fprint(e.Stdout, renderDoctor(e.Style, rs, sum, took))
	}
	if sum.Fail == 0 && !env.Unauthenticated.Load() {
		// A clean full run is the quickstart's step 2; a run with --only doesn't count as one.
		printProgress(e, doctorValues.quiet, func() setup.Facts {
			f := setup.ReadFacts(ctx, env.Dir)
			f.Checked = doctorValues.only == ""
			return f
		})
	}
	switch {
	case env.Unauthenticated.Load():
		return &ExitError{Code: ExitNotAuthed, ErrCode: "invalid_api_key", printed: !e.Globals.JSON, Err: errors.New("the server refused the API key")}
	case sum.Fail > 0:
		return &ExitError{Code: ExitFailure, ErrCode: "doctor_problems", printed: !e.Globals.JSON, Err: fmt.Errorf("%d %s", sum.Fail, plural(sum.Fail, "problem", "problems"))}
	}
	return nil
}

// renderDoctor is the human report (S32 §5): one line per check, the fix under it.
func renderDoctor(st ui.Style, rs []doctor.Result, sum doctor.Summary, took time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "deedglass doctor %s %d %s %s %.1f s\n\n", ui.Sep, len(rs), plural(len(rs), "check", "checks"), ui.Sep, took.Seconds())
	for _, r := range rs {
		sym := st.Teal(ui.OK)
		switch r.Status {
		case doctor.Warn:
			sym = st.Amber(ui.Unknown)
		case doctor.Fail:
			sym = st.Red(ui.Fail)
		case doctor.Skip:
			sym = st.Dim(ui.Sep)
		}
		fmt.Fprintf(&b, "%s %s %s\n", sym, ui.Pad(r.Title, 20), r.Detail)
		if r.Fix != "" && r.Status != doctor.OK {
			fmt.Fprintf(&b, "  %s %s\n", ui.Pad("", 20), r.Fix)
		}
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("%d ok", sum.OK))
	if sum.Warn > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", sum.Warn, plural(sum.Warn, "warning", "warnings")))
	}
	if sum.Fail > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", sum.Fail, plural(sum.Fail, "problem", "problems")))
	}
	if sum.Skip > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", sum.Skip))
	}
	fmt.Fprintf(&b, "\n%s\n", strings.Join(parts, " "+ui.Sep+" "))
	return b.String()
}
