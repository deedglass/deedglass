package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/buildinfo"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/ids"
)

// buildInfo is replaced in tests so goldens don't depend on the toolchain.
var buildInfo = buildinfo.Get

// --- version ---

type versionOpts struct{ check bool }

var versionFlagValues versionOpts

func versionFlags(fs *flag.FlagSet) {
	versionFlagValues = versionOpts{}
	fs.BoolVar(&versionFlagValues.check, "check", false, "check GitHub releases for a newer version (S35)")
}

func runVersion(_ context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass version takes no arguments (got %q)", args[0]), "Run deedglass help version.")
	}
	if versionFlagValues.check {
		return &ExitError{Code: ExitUsage, ErrCode: "not_built", Plain: true,
			Err:  errors.New("deedglass version --check isn't in this build yet"),
			Next: "Run deedglass help to see what this build can do."}
	}
	in := buildInfo()
	if e.Globals.JSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(in)
	}
	commit := in.Commit
	if in.Modified {
		commit += " (modified)"
	}
	fmt.Fprintf(e.Stdout, "deedglass %s %s commit %s %s api %s %s %s %s %s/%s\n",
		in.Version, ui.Sep, commit, ui.Sep, strings.Join(in.APIVersions, ", "), ui.Sep, in.Go, ui.Sep, in.OS, in.Arch)
	if e.Globals.Server != "" {
		fmt.Fprintln(e.Stdout, e.Style.Dim("server  not checked "+ui.Sep+" this build has no API client yet (S03 adds GET /v1/version)"))
	}
	return nil
}

// --- config print ---

func runConfigPrint(_ context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass config print takes no arguments (got %q)", args[0]), "Run deedglass help config print.")
	}
	cfg, warnings, err := config.Load(e.LoadOptions())
	var ps *config.Problems
	if errors.As(err, &ps) {
		return configProblems(e, ps, warnings)
	}
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		return cfg.PrintWarnings(e.Stdout, true, warnings)
	}
	s := e.Style
	w := e.Stdout
	fmt.Fprintf(w, "%s %s env %s\n\n", s.Bold("deedglass config"), ui.Sep, cfg.Env)
	rows := cfg.Rows()
	nameW, valW := 0, 0
	for _, r := range rows {
		nameW = max(nameW, ui.Width(r.Name))
		valW = max(valW, ui.Width(value(r)))
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%s  %s  %s\n", ui.Pad(r.Name, nameW), ui.Pad(value(r), valW), s.Dim(string(r.Source)))
	}
	fmt.Fprintln(w)
	printWarnings(e, warnings)
	footer := "Valid for " + string(cfg.Env)
	if used, generated := cfg.DevSecrets(); used > 0 {
		verb := "from"
		if generated {
			verb = "generated in"
		}
		footer += fmt.Sprintf(" %s %d local %s %s .deedglass/ (never used in prod)", ui.Sep, used, plural(used, "secret", "secrets"), verb)
	}
	fmt.Fprintln(w, s.OKLine(footer))
	return nil
}

func value(r config.Row) string {
	if !r.Set {
		return ui.None
	}
	return r.Value
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func printWarnings(e *Env, ws []config.Warning) {
	for _, w := range ws {
		fmt.Fprintln(e.Stderr, e.Style.UnknownLine(w.String()))
	}
}

// configProblems prints every problem, aligned, and returns exit 1.
func configProblems(e *Env, ps *config.Problems, ws []config.Warning) error {
	if e.Globals.JSON {
		list := make([]map[string]string, 0, len(ps.List))
		for _, p := range ps.List {
			list = append(list, map[string]string{"key": p.Key, "message": p.Message, "next": p.Next})
		}
		title := fmt.Sprintf("%d problems in the %s config", len(ps.List), ps.Env)
		if len(ps.List) == 1 {
			title = "1 problem in the " + string(ps.Env) + " config"
		}
		return &ExitError{Code: ExitFailure, ErrCode: "config_invalid", Err: errors.New(title),
			Next: "Fix each one; see docs/CONFIG.md.", Extra: map[string]any{"problems": list}}
	}
	printWarnings(e, ws)
	s := e.Style
	if len(ps.List) == 1 {
		p := ps.List[0]
		fmt.Fprintln(e.Stderr, s.FailLine(p.Key+" "+p.String()))
		return &ExitError{Code: ExitFailure, Err: ps, printed: true}
	}
	fmt.Fprintln(e.Stderr, s.FailLine(fmt.Sprintf("%d problems in the %s config", len(ps.List), ps.Env)))
	keyW := 0
	for _, p := range ps.List {
		keyW = max(keyW, ui.Width(p.Key))
	}
	for _, p := range ps.List {
		fmt.Fprintf(e.Stderr, "  %s  %s\n", ui.Pad(p.Key, keyW), p.String())
	}
	return &ExitError{Code: ExitFailure, Err: ps, printed: true}
}

// --- dev id (hidden helper) ---

func runDevID(_ context.Context, e *Env, args []string) error {
	if len(args) != 1 {
		return usageErr(errors.New("deedglass dev id needs one prefix, for example agt"), "See docs/IDS.md for every prefix.")
	}
	prefix := strings.TrimSuffix(ids.Normalize(args[0]), "_")
	id, err := ids.NewString(prefix)
	if err != nil {
		return usageErr(fmt.Errorf("%q isn't an id prefix", args[0]), "See docs/IDS.md for every prefix.")
	}
	if e.Globals.JSON {
		return json.NewEncoder(e.Stdout).Encode(map[string]string{"object": "id", "id": id})
	}
	fmt.Fprintln(e.Stdout, id)
	return nil
}
