package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/cli/mark"
	"github.com/deedglass/deedglass/internal/setup"
)

// deedglass init (S32 §3.9): flags, the plan prompt, rendering and the process Runner;
// internal/setup does the steps.

var initValues struct {
	yes, noOpen, noSmoke, smoke, resetSecrets, stop    bool
	trigger, quiet                                     bool
	dir, client, listen, postgres, ownerEmail, project string
	pgPort                                             int
}

var initClients = []string{"claude-code", "claude-desktop", "cursor", "vscode", "windsurf", "codex", "gemini", "copilot", "sdk-python", "sdk-typescript", "none"}

func initFlags(fs *flag.FlagSet) {
	v := &initValues
	*v = struct {
		yes, noOpen, noSmoke, smoke, resetSecrets, stop    bool
		trigger, quiet                                     bool
		dir, client, listen, postgres, ownerEmail, project string
		pgPort                                             int
	}{}
	fs.BoolVar(&v.yes, "yes", false, "don't ask; run every step")
	fs.StringVar(&v.dir, "dir", ".", "the directory to set up (.deedglass/ goes here)")
	fs.StringVar(&v.client, "client", "", "the agent client to connect: "+strings.Join(initClients, ", "))
	fs.StringVar(&v.postgres, "postgres", "docker", "where Postgres runs: docker")
	fs.StringVar(&v.listen, "listen", "127.0.0.1:8080", "where deedglass serve listens")
	fs.IntVar(&v.pgPort, "pg-port", 55432, "the local Postgres port (127.0.0.1)")
	fs.StringVar(&v.ownerEmail, "owner-email", setup.DefaultOwner, "the local org's owner")
	fs.StringVar(&v.project, "project", "", "the Docker name prefix (default: from the directory)")
	fs.BoolVar(&v.noOpen, "no-open", false, "don't open the Console")
	fs.BoolVar(&v.noSmoke, "no-smoke", false, "skip the three smoke actions")
	fs.BoolVar(&v.smoke, "smoke", false, "run the smoke actions even when nothing changed")
	fs.BoolVar(&v.resetSecrets, "reset-secrets", false, "replace the vault key and pepper (asks you to type reset; every API key stops working)")
	fs.BoolVar(&v.stop, "stop", false, "stop the local stack this directory runs (the data is kept)")
	fs.BoolVar(&v.quiet, "quiet", false, "don't print the quickstart step line")
	fs.BoolVar(&v.trigger, "trigger", false, "on the stack init made: turn on the demo's legacy trigger, ship order 42 (1 row reported, 301 changed), then show the undo preview")
}

func runInit(ctx context.Context, e *Env, args []string) error {
	v := initValues
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass init takes no arguments (got %q)", args[0]), "Run deedglass init, or deedglass init --dir <dir>.")
	}
	if v.postgres != "docker" {
		return usageErr(fmt.Errorf("--postgres %s isn't in this build", v.postgres), "Run deedglass init (Postgres in Docker); an existing database lands in the next release.")
	}
	dir, err := filepath.Abs(v.dir)
	if err != nil {
		return usageErr(err, "Check the path given to --dir.")
	}
	if v.client != "" {
		ok := false
		for _, c := range initClients {
			ok = ok || c == v.client
		}
		if !ok {
			return usageErr(fmt.Errorf("--client takes %s (got %q)", strings.Join(initClients, ", "), v.client), "For example: deedglass init --client claude-code.")
		}
	}
	r := &setup.ProcRunner{Dir: dir}
	if v.stop {
		return stopStack(ctx, e, r, dir)
	}
	if v.trigger {
		return runInitTrigger(ctx, e, r, dir)
	}
	if v.resetSecrets {
		if v.yes || !e.IsTTY {
			return usageErr(errors.New("--reset-secrets needs you to type reset, so it can't run with --yes or without a terminal"), "Run deedglass init --reset-secrets in a terminal.")
		}
		fmt.Fprintln(e.Stdout, "This replaces the vault key and the API key pepper. Every API key stops working, and stored credentials can't be read.")
		fmt.Fprint(e.Stdout, "Type reset to continue: ")
		line, _ := bufio.NewReader(e.stdin()).ReadString('\n')
		if strings.TrimSpace(line) != "reset" {
			return &ExitError{Code: ExitFailure, ErrCode: "cancelled", Plain: true, Err: errors.New("Nothing was changed")}
		}
	}
	label := dir
	if home := e.Getenv("HOME"); home != "" && strings.HasPrefix(dir, home) {
		label = "~" + strings.TrimPrefix(dir, home)
	}
	o := setup.Options{Dir: dir, Yes: v.yes, Client: v.client, Listen: v.listen, PGPort: v.pgPort, Project: v.project,
		OwnerEmail: v.ownerEmail, NoSmoke: v.noSmoke, NoOpen: v.noOpen, Smoke: v.smoke, TTY: e.IsTTY, Runner: r, ResetSecrets: v.resetSecrets}
	if o.Client == "" {
		o.Client = "none"
	}
	text := !e.Globals.JSON
	if text {
		mo := e.Mark
		mo.Command = "init"
		mo.Next = "Setting up Deedglass in `" + label + "`."
		if b := mark.Banner(mo); b != "" && !mo.Quiet {
			fmt.Fprint(e.Stdout, b)
		} else {
			fmt.Fprintf(e.Stdout, "deedglass init %s setting up Deedglass in %s\n", "·", label)
		}
		fmt.Fprintln(e.Stdout)
		create, have := setup.Plan(o)
		if len(create) > 0 {
			fmt.Fprintln(e.Stdout, "  Will create  "+wrapJoin(create, 4))
		}
		if len(have) > 0 && len(create) > 0 {
			fmt.Fprintln(e.Stdout, "  Already set  "+wrapJoin(have, 4))
		}
		if len(create) > 0 && !v.yes && e.IsTTY {
			fmt.Fprint(e.Stdout, "Continue? [Y/n] ")
			line, _ := bufio.NewReader(e.stdin()).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(line)); a == "n" || a == "no" {
				fmt.Fprintln(e.Stdout, "Nothing was changed.")
				return nil
			}
		}
		if len(create) > 0 {
			fmt.Fprintln(e.Stdout)
		}
		o.Progress = func(s setup.StepResult) { renderStep(e, s) }
	}
	if fi, err := os.Lstat(filepath.Join(dir, setup.DirName)); err == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
		_ = fi
	}
	if err := os.MkdirAll(filepath.Join(dir, setup.DirName), 0o700); err == nil {
		unlock, err := lockInit(filepath.Join(dir, setup.DirName, setup.LockFile))
		if err != nil {
			return &ExitError{Code: ExitFailure, ErrCode: "init_running", Err: errors.New("another deedglass init is running in this directory"), Next: "Wait for it to finish, then run deedglass init again."}
		}
		defer unlock()
	}
	res, err := setup.Init(ctx, o)
	if e.Globals.JSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	}
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "init_failed", printed: true, Err: err}
	}
	if !text {
		return nil
	}
	if !res.Changed {
		fmt.Fprintln(e.Stdout)
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Nothing to change. Deedglass is set up in %s (org %s, agent %s).", label, setup.OrgName, setup.AgentName)))
		printProgress(e, v.quiet, func() setup.Facts { return setup.ReadFacts(ctx, dir) })
		return nil
	}
	verified := false
	for _, s := range res.Steps {
		verified = verified || (s.ID == "smoke_update" && strings.Contains(s.Detail, "Proven · Matches"))
	}
	fmt.Fprintln(e.Stdout)
	if verified {
		mo := e.Mark
		_ = mark.Sealed(e.Stdout, mo)
	}
	fmt.Fprintf(e.Stdout, "Ready in %d s.\n", (res.ElapsedMS+500)/1000)
	fmt.Fprintln(e.Stdout, "Next")
	for _, n := range res.Next {
		fmt.Fprintln(e.Stdout, "  "+n)
	}
	printProgress(e, v.quiet, func() setup.Facts { return setup.ReadFacts(ctx, dir) })
	return nil
}

// printProgress is the quickstart step line under init and doctor (PRD-06): the step of five this
// directory is on, read from its files and its Ledger, and the command for it. Nothing with --quiet
// or --json (and then read isn't called, so the Ledger isn't asked), in a directory init didn't set
// up, or once all five are done.
func printProgress(e *Env, quiet bool, read func() setup.Facts) {
	if quiet || e.Globals.JSON {
		return
	}
	f := read()
	if !f.SetUp {
		return
	}
	if l := progressLine(f); l != "" {
		fmt.Fprintln(e.Stdout)
		fmt.Fprintln(e.Stdout, l)
	}
}

func progressLine(f setup.Facts) string {
	n, next := f.Progress()
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("Quickstart step %d of %d %s next: %s", n, setup.ProgressSteps, ui.Sep, next)
}

func wrapJoin(items []string, indent int) string {
	var lines []string
	cur := ""
	for _, it := range items {
		if cur != "" && len(cur)+3+len(it) > 84 {
			lines = append(lines, cur)
			cur = it
			continue
		}
		if cur != "" {
			cur += " · "
		}
		cur += it
	}
	lines = append(lines, cur)
	return strings.Join(lines, "\n"+strings.Repeat(" ", indent+11))
}

func renderStep(e *Env, s setup.StepResult) {
	st := e.Style
	sym := st.Teal("✓")
	switch {
	case s.Status == setup.Failed:
		sym = st.Red("✕")
	case s.Symbol == "✕":
		sym = st.Red("✕")
	case s.Symbol == "◐":
		sym = st.Violet("◐")
	case s.Symbol == "○":
		sym = st.Amber("○")
	}
	if s.Status == setup.Failed {
		fmt.Fprintf(e.Stdout, "%s %s\n", sym, strings.TrimSuffix(s.Detail, ".")+".")
		if s.Fix != "" {
			fmt.Fprintf(e.Stdout, "  %s\n", s.Fix)
		}
		return
	}
	fmt.Fprintf(e.Stdout, "%s %-23s %s\n", sym, s.Title, s.Detail)
	if s.Secret != "" {
		fmt.Fprintf(e.Stdout, "    %s   copy it now; it isn't shown again\n", s.Secret)
	}
	for _, l := range s.Lines {
		fmt.Fprintf(e.Stdout, "  %s\n", l)
	}
}

func stopStack(ctx context.Context, e *Env, r *setup.ProcRunner, dir string) error {
	stack, err := setup.ReadStack(dir)
	if err != nil || stack == nil {
		return &ExitError{Code: ExitFailure, ErrCode: "not_set_up", Err: errors.New("there's no local stack in this directory"), Next: "Run deedglass init first."}
	}
	pidFile := setup.Path(dir, setup.ServePID)
	if b, err := os.ReadFile(pidFile); err == nil {
		var pid int
		fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pid)
		if pid > 0 && r.Alive(pid) {
			_ = r.Stop(pid)
		}
		_ = os.Remove(pidFile)
	}
	_, _ = r.Docker(ctx, nil, "stop", stack.Container, stack.DemoContainer)
	fmt.Fprintln(e.Stdout, e.Style.OKLine("Stopped deedglass serve, "+stack.Container+" and "+stack.DemoContainer+". The data is kept; deedglass init starts them again."))
	return nil
}

// runInitTrigger is deedglass init --trigger: the quickstart's last step (setup.Trigger does the work).
// It exits 0 when the check said Doesn't match, because that is what the step is there to show.
func runInitTrigger(ctx context.Context, e *Env, r *setup.ProcRunner, dir string) error {
	o := setup.Options{Dir: dir, Runner: r, Yes: true, NoOpen: true, TTY: e.IsTTY}
	text := !e.Globals.JSON
	if text {
		fmt.Fprintln(e.Stdout, "deedglass init --trigger · one UPDATE, a legacy trigger, and what Postgres really committed")
		fmt.Fprintln(e.Stdout)
		o.Progress = func(s setup.StepResult) { renderStep(e, s) }
	}
	res, err := setup.Trigger(ctx, o)
	if e.Globals.JSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	}
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "init_failed", printed: true, Err: err}
	}
	if text {
		fmt.Fprintln(e.Stdout)
		fmt.Fprintln(e.Stdout, "The agent said 1 row. Postgres's change log says 301. Nothing was undone yet.")
		fmt.Fprintln(e.Stdout, "Next")
		for _, n := range res.Next {
			fmt.Fprintln(e.Stdout, "  "+n)
		}
	}
	return nil
}
