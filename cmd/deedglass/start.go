package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/cli/mark"
	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/play"
	"github.com/deedglass/deedglass/internal/setup"
)

// deedglass start: the one first command (first-run-v1, APPLE_SIMPLE_INSTALL §3). No questions.
// With Docker it runs init (every step, free ports picked) and then the trigger scene on the same
// stack, so the run ends on the catch: the agent said 1 row, Postgres changed 301. Without Docker
// (or with under 2 GiB of disk) it plays the same scene in-process (internal/play), every line
// labelled live or replay, and says how to get the live version.

var startValues struct {
	dir  string
	fast bool
}

func startFlags(fs *flag.FlagSet) {
	startValues.dir, startValues.fast = ".", false
	fs.StringVar(&startValues.dir, "dir", ".", "the directory the local stack lives in (.deedglass/ goes here)")
	fs.BoolVar(&startValues.fast, "fast", false, "without Docker: print the replayed scene at once, with no pauses")
}

// startDocker reports whether the live path can run here, and if not, why (one short phrase).
// Tests replace it.
var startDocker = func(ctx context.Context, r *setup.ProcRunner, dir string) (bool, string) {
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if v, err := r.Docker(dctx, nil, "info", "--format", "{{.ServerVersion}}"); err != nil || strings.TrimSpace(v) == "" {
		if err != nil && strings.Contains(err.Error(), "executable file not found") {
			return false, "Docker isn't installed"
		}
		return false, "Docker isn't running"
	}
	if free, err := r.FreeDisk(dir); err == nil && free < 2<<30 {
		return false, fmt.Sprintf("only %.1f GiB of disk is free (the live version needs 2 GiB)", float64(free)/(1<<30))
	}
	return true, ""
}

// startResult is start --json: which path ran and what it found.
type startResult struct {
	Object  string         `json:"object"`
	Mode    string         `json:"mode"` // live (Docker) or replay (in-process)
	Reason  string         `json:"reason,omitempty"`
	Console string         `json:"console_url,omitempty"`
	Init    *setup.Result  `json:"init,omitempty"`
	Trigger *setup.Result  `json:"trigger,omitempty"`
	Play    *play.Result   `json:"play,omitempty"`
	Ports   map[string]int `json:"ports,omitempty"`
	Next    []string       `json:"next"`
}

func runStart(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass start takes no arguments (got %q)", args[0]), "Run deedglass start.")
	}
	dir, err := filepath.Abs(startValues.dir)
	if err != nil {
		return usageErr(err, "Check the path given to --dir.")
	}
	r := &setup.ProcRunner{Dir: dir}
	sp := newSpinner(e)
	if !e.Globals.JSON {
		sp.Start("Looking for Docker", "")
	}
	ok, why := startDocker(ctx, r, dir)
	sp.Stop()
	if !ok {
		return startReplay(ctx, e, why)
	}
	return startLive(ctx, e, r, dir)
}

// startNext is the closing list: what to do after the first success.
func startNext(e *Env, live bool) [][2]string {
	inv := invocation(e)
	var rows [][2]string
	if !live {
		rows = append(rows, [2]string{inv + " start", "again with Docker running: the same scene, live"})
	}
	return append(rows,
		[2]string{inv + " protect", "guard Claude Code, Cursor and other coding agents"},
		[2]string{inv + " health pilot", "the same checks on synthetic patient records"},
	)
}

// startReplay is the no-Docker path: the scene from internal/play, paced on a terminal.
func startReplay(ctx context.Context, e *Env, why string) error {
	next := startNext(e, false)
	o := play.Options{Out: e.Stdout, Style: e.Style, Width: e.Width, Verbose: e.Globals.Verbose > 0, Brief: e.Globals.Verbose == 0}
	st := e.Style
	if e.Globals.JSON {
		o.Out = discard{}
	} else {
		ui.Lines(e.Stdout, statusLines(e, ui.Unknown, upperFirst(why)+", so this is the offline version: the rules run live here, and the Postgres lines are replayed from a recorded run.")...)
		fmt.Fprintln(e.Stdout)
		o.Tail = append([]string{""}, statusLines(e, ui.OK, st.Bold("Caught.")+" The agent reported 1 row; Postgres changed 301. The undo puts them back after a person approves.")...)
		o.Tail = append(append(o.Tail, "", st.Section("Next")), st.Commands("  ", e.Width, next...)...)
	}
	if e.IsTTY && !startValues.fast && !e.Globals.JSON {
		o.Pace = pace
	}
	res, err := play.Run(ctx, o)
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, startResult{Object: "start_result", Mode: "replay", Reason: why, Play: res, Next: firsts(next)})
	}
	return nil
}

// startLive is the Docker path: init with every default (no prompt, free ports), then the trigger.
func startLive(ctx context.Context, e *Env, r *setup.ProcRunner, dir string) error {
	listen, pg := "127.0.0.1:8080", 55432
	var notes []string
	if stack, _ := setup.ReadStack(dir); stack == nil {
		listen, pg, notes = pickPorts(listen, pg, portFree)
	}
	if e.Globals.JSON {
		o := setup.Options{Dir: dir, Yes: true, Listen: listen, PGPort: pg, TTY: e.IsTTY, Runner: r, NoOpen: true,
			Cmd: invocation(e), Home: e.Getenv("HOME")}
		out := startResult{Object: "start_result", Mode: "live", Ports: map[string]int{}}
		res, err := setup.Init(ctx, o)
		out.Init = res
		if err == nil {
			out.Trigger, err = setup.Trigger(ctx, setup.Options{Dir: dir, Runner: r, Yes: true, NoOpen: true, Cmd: invocation(e)})
		}
		if stack, _ := setup.ReadStack(dir); stack != nil {
			out.Console = stack.Server() + "/app"
			if _, p, perr := net.SplitHostPort(stack.Listen); perr == nil {
				out.Ports["server"], _ = strconv.Atoi(p)
			}
			out.Ports["postgres"], out.Ports["demo_postgres"] = stack.PGPort, stack.DemoPort
		}
		out.Next = firsts(startNext(e, true))
		if out.Trigger != nil && len(out.Trigger.Next) > 0 {
			out.Next = append([]string{out.Trigger.Next[0]}, out.Next...)
		}
		_ = writeJSON(e, out)
		if err != nil {
			return &ExitError{Code: ExitFailure, ErrCode: "start_failed", printed: true, Err: err}
		}
		return nil
	}
	for _, n := range notes {
		ui.Lines(e.Stdout, statusLines(e, ui.Unknown, n)...)
	}
	saved := initValues
	defer func() { initValues = saved }()
	initFlags(flag.NewFlagSet("init", flag.ContinueOnError)) // init's defaults, as if no flag was given
	initValues.yes, initValues.dir, initValues.listen, initValues.pgPort, initValues.quiet = true, dir, listen, pg, true
	initFromStart = true
	defer func() { initFromStart = false }()
	if err := runInit(ctx, e, nil); err != nil {
		return err
	}
	fmt.Fprintln(e.Stdout)
	tres, err := runInitTriggerResult(ctx, e, r, dir)
	if err != nil {
		return err
	}
	if stack, _ := setup.ReadStack(dir); stack != nil && r.Healthy(ctx, stack.Server()) {
		fmt.Fprintln(e.Stdout)
		fmt.Fprintln(e.Stdout, "Open the live view: "+e.Style.Accent(stack.Server()+"/app"))
	}
	fmt.Fprintln(e.Stdout)
	fmt.Fprintln(e.Stdout, e.Style.Section("Next"))
	if tres != nil && len(tres.Next) > 0 {
		fmt.Fprintln(e.Stdout, "  "+tres.Next[0]) // a command a person copies is never broken
	}
	ui.Lines(e.Stdout, e.Style.Commands("  ", e.Width, startNext(e, true)...)...)
	return nil
}

// pickPorts keeps the default ports when they're free and otherwise picks the next free ones: the
// server from listen's port up, Postgres from pg up with its demo database 10 above. free is
// portFree (tests pass their own). Each change comes back as one line to print.
func pickPorts(listen string, pg int, free func(string) bool) (string, int, []string) {
	var notes []string
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen, pg, nil
	}
	p, _ := strconv.Atoi(port)
	if !free(listen) {
		for q := p + 1; q < p+50; q++ {
			if a := net.JoinHostPort(host, strconv.Itoa(q)); free(a) {
				notes = append(notes, fmt.Sprintf("Port %d is busy, so the server uses %d.", p, q))
				listen = a
				break
			}
		}
	}
	pgOK := func(n int) bool {
		return free("127.0.0.1:"+strconv.Itoa(n)) && free("127.0.0.1:"+strconv.Itoa(n+10))
	}
	if !pgOK(pg) {
		for q := pg + 1; q < pg+50; q++ {
			if pgOK(q) {
				notes = append(notes, fmt.Sprintf("Port %d or %d is busy, so Postgres uses %d and %d.", pg, pg+10, q, q+10))
				pg = q
				break
			}
		}
	}
	return listen, pg, notes
}

func portFree(addr string) bool {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// pace waits d between the replayed scene's lines on a terminal (tests swap it for a counter).
var pace = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// statusLines is a status line wrapped to the layout width, later lines under the text.
func statusLines(e *Env, glyph, text string) []string {
	ls := ui.Wrap(text, ui.LayoutWidth(e.Width)-2, 0)
	out := []string{e.Style.Status(glyph, ls[0])}
	for _, l := range ls[1:] {
		out = append(out, "  "+l)
	}
	return out
}

func firsts(rows [][2]string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r[0])
	}
	return out
}

// --- the welcome: bare deedglass ---

// welcome is bare deedglass: what Deedglass is and the commands to start with, in 5 lines (the
// first-run-v1 audit: a 73-line help is not a first step). A directory with a local stack gets
// its status line first.
func welcome(e *Env) string {
	inv := invocation(e)
	st := e.Style
	var b strings.Builder
	mo := e.Mark
	mo.Command = ""
	if il := mark.Inline(mo); il != "" {
		b.WriteString(il + "\n")
	}
	head := "Deedglass checks what your AI agents actually changed."
	if wd, err := os.Getwd(); err == nil {
		if stack, _ := setup.ReadStack(wd); stack != nil {
			head = "Deedglass is set up in this directory: " + stack.Server() + "/app when it's running."
		}
	}
	b.WriteString(st.Bold(head) + "\n\n")
	for _, l := range st.Commands("  ", e.Width,
		[2]string{inv + " app", "open it in your browser, no setup"},
		[2]string{inv + " start", "see it catch an agent's 1-row claim that changed 301"},
		[2]string{inv + " protect", "guard Claude Code, Cursor and other coding agents"},
	) {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n" + st.Dim("More: "+inv+" health pilot (synthetic patient records) "+ui.Sep+" "+inv+" help") + "\n")
	return b.String()
}
