package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/buildinfo"
	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/ids"
)

// buildInfo is replaced in tests so goldens don't depend on the toolchain.
var buildInfo = buildinfo.Get

// --- version ---

type versionOpts struct{ check, pre, quiet bool }

var versionFlagValues versionOpts

func versionFlags(fs *flag.FlagSet) {
	versionFlagValues = versionOpts{}
	fs.BoolVar(&versionFlagValues.check, "check", false, "ask GitHub's releases API whether a newer version exists (opt-in; sends only a User-Agent)")
	fs.BoolVar(&versionFlagValues.pre, "pre", false, "with --check, count prereleases too")
	fs.BoolVar(&versionFlagValues.quiet, "q", false, "no banner")
	fs.BoolVar(&versionFlagValues.quiet, "quiet", false, "no banner")
}

func runVersion(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass version takes no arguments (got %q)", args[0]), "Run deedglass help version.")
	}
	if versionFlagValues.pre && !versionFlagValues.check {
		return usageErr(errors.New("--pre only works with --check"), "Run deedglass version --check --pre.")
	}
	if versionFlagValues.check {
		return runVersionCheck(ctx, e, buildInfo().Version)
	}
	in := buildInfo()
	if e.Globals.ServerFlag && e.Globals.Server == "" {
		return usageErr(errors.New("flag --server needs a value"), "For example: deedglass version --server http://127.0.0.1:8080")
	}
	if e.Globals.Server != "" {
		return versionWithServer(ctx, e, in, e.Globals.Server)
	}
	if e.Globals.JSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(in)
	}
	if !e.Mark.Quiet && !versionFlagValues.quiet {
		fmt.Fprint(e.Stdout, versionBanner(e, in))
		return nil
	}
	fmt.Fprintln(e.Stdout, clientLine(in))
	return nil
}

func clientLine(in buildinfo.Info) string {
	commit := in.Commit
	if in.Modified {
		commit += " (modified)"
	}
	return fmt.Sprintf("deedglass %s %s commit %s %s api %s %s %s %s %s/%s",
		in.Version, ui.Sep, commit, ui.Sep, strings.Join(in.APIVersions, ", "), ui.Sep, in.Go, ui.Sep, in.OS, in.Arch)
}

// versionTimeout bounds `version --server`; the image healthcheck relies on it.
const versionTimeout = 2 * time.Second

// serverVersion is GET /v1/version as the client reads it.
type serverVersion struct {
	Object            string   `json:"object"`
	Version           string   `json:"version"`
	Commit            string   `json:"commit"`
	BuiltAt           *string  `json:"built_at"`
	Go                string   `json:"go"`
	APIVersions       []string `json:"api_versions"`
	DefaultAPIVersion string   `json:"default_api_version"`
}

// versionWithServer prints the client and the server side by side. Exit 0 when the server
// answers, 1 when it doesn't (the container healthcheck uses this: distroless has no curl).
func versionWithServer(ctx context.Context, e *Env, in buildinfo.Info, server string) error {
	u, err := url.Parse(strings.TrimSuffix(server, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return usageErr(fmt.Errorf("--server must be an http or https URL (got %q)", server), "For example --server http://127.0.0.1:8080.")
	}
	unreachable := func(reason string) error {
		return &ExitError{Code: ExitFailure, ErrCode: "server_unreachable",
			Err:  fmt.Errorf("Can't reach the server at %s: %s", u.Redacted(), reason),
			Next: "Check the URL, and that deedglass serve is running (make dev)."}
	}
	rctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(rctx, http.MethodGet, u.String()+"/v1/version", nil)
	req.Header.Set("User-Agent", "deedglass/"+in.Version)
	client := egress.FirstParty(egress.FirstPartyOptions{Timeout: versionTimeout})
	res, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(rctx.Err(), context.DeadlineExceeded) {
			return unreachable("no answer within 2 s")
		}
		return unreachable(lastPart(err))
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var sv serverVersion
	if res.StatusCode != http.StatusOK || json.Unmarshal(body, &sv) != nil || sv.Object != "version" {
		return unreachable(fmt.Sprintf("it answered %d, not like a Deedglass server", res.StatusCode))
	}
	if e.Globals.JSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(map[string]any{"object": "version", "client": in, "server": sv, "server_url": u.String()})
	}
	fmt.Fprintln(e.Stdout, "client  "+clientLine(in))
	fmt.Fprintf(e.Stdout, "server  deedglass %s %s commit %s %s api %s %s %s %s %s\n",
		sv.Version, ui.Sep, sv.Commit, ui.Sep, strings.Join(sv.APIVersions, ", "), ui.Sep, sv.Go, ui.Sep, u.String())
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
