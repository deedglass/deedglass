package main

//deedglass:allow-exec exec.LookPath finds the deedglass on PATH so the hook protect writes names a stable path; nothing is started here

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/protect"
)

// deedglass protect (growth pick 1, docs/protect.md): one command that adds the shell guard to every
// coding agent it finds on this machine (Claude Code, Cursor, Codex CLI, Gemini CLI, GitHub Copilot
// CLI) from the hook configs in internal/shellguard. It prints one plan, writes only after [y/N] or
// --yes, backs up every file it changes, and --undo puts them back. No server, no network.

// protectBadge is the README badge a successful run offers (a shields.io static badge linking to
// the check page). It is printed, never written anywhere.
const protectBadge = "[![Agents protected by Deedglass](https://img.shields.io/badge/agents-protected%20by%20Deedglass-0f766e)](https://deedglass.com/check)"

type protectOpts struct {
	dryRun, yes, undo, project bool
	client, hookCommand        string
}

var protectFlagValues protectOpts

func protectFlags(fs *flag.FlagSet) {
	protectFlagValues = protectOpts{}
	fs.BoolVar(&protectFlagValues.dryRun, "dry-run", false, "print what would change and write nothing")
	fs.BoolVar(&protectFlagValues.yes, "yes", false, "write without asking")
	fs.BoolVar(&protectFlagValues.undo, "undo", false, "put back every file protect changed (with --dry-run: say what it would do)")
	fs.BoolVar(&protectFlagValues.project, "project", false, "also add the guard to this directory's agent folders (.claude, .cursor, .codex, .gemini, .github)")
	fs.StringVar(&protectFlagValues.client, "client", "", "only these agents, comma-separated: "+protect.ClientNames())
	fs.StringVar(&protectFlagValues.hookCommand, "hook-command", "", "how the hook starts deedglass (default: this binary's path, or npx -y deedglass@<version> after an npx run)")
}

func runProtect(_ context.Context, e *Env, args []string) error {
	o := protectFlagValues
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass protect takes no arguments (got %q)", args[0]), "To pick agents: deedglass protect --client claude-code,cursor.")
	}
	home := e.Getenv("HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return failNext(e, ExitFailure, "validation_failed", "Can't find your home directory.", "Set HOME and run deedglass protect again.")
		}
		home = h
	}
	po := protect.Options{Home: home, Project: o.project}
	if wd, err := os.Getwd(); err == nil {
		po.Dir = wd
	}
	if o.client != "" {
		known := map[string]bool{}
		for _, a := range protect.Agents {
			known[a.Client] = true
		}
		for _, c := range strings.Split(o.client, ",") {
			c = strings.TrimSpace(c)
			if !known[c] {
				return usageErr(fmt.Errorf("--client takes %s (got %q)", protect.ClientNames(), c), "For example: deedglass protect --client claude-code,cursor.")
			}
			po.Clients = append(po.Clients, c)
		}
	}
	if o.undo {
		return protectUndo(e, po, o.dryRun)
	}
	po.Command = o.hookCommand
	if po.Command == "" {
		exe, _ := os.Executable()
		onPath, _ := exec.LookPath("deedglass")
		if onPath != "" {
			onPath, _ = filepath.Abs(onPath)
		}
		po.Command = protect.HookCommand(exe, onPath, buildInfo().Version)
	}
	cs, err := protect.Plan(po)
	if err != nil {
		return failNext(e, ExitFailure, "validation_failed", "Couldn't plan the changes: "+err.Error()+".", "Nothing was written. Run deedglass protect --dry-run to see what it found.")
	}
	adds, found := 0, 0
	for _, c := range cs {
		if c.Status == protect.Add {
			adds++
		}
		if c.Status != protect.Absent {
			found++
		}
	}
	tilde := func(p string) string {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
		return p
	}
	jsonOut := func(applied bool, run string) error {
		if cs == nil {
			cs = []protect.Change{}
		}
		badge := ""
		if applied {
			badge = protectBadge
		}
		return writeJSON(e, map[string]any{"object": "protect", "dry_run": o.dryRun, "applied": applied, "command": po.Command + " guard shell",
			"backup": run, "changes": adds, "agents": cs, "badge": badge})
	}
	if !e.Globals.JSON {
		meta := []string{fmt.Sprintf("%d of %d agents found", found, len(cs))}
		if o.dryRun {
			meta = append(meta, "dry run")
		}
		lines := []string{e.Style.Header("deedglass protect", meta...), ""}
		var rows []ui.Row
		for _, c := range cs {
			r := ui.Row{Key: c.Title}
			if c.Scope == "project" {
				r.Key += " (project)"
			}
			switch c.Status {
			case protect.Add:
				r.Glyph, r.Value = ui.Arrow, "adds the guard ("+c.Event+")\n"+tilde(c.File)
			case protect.Present:
				r.Glyph, r.Value = ui.OK, "already has the guard\n"+tilde(c.File)
			case protect.Skip:
				r.Glyph, r.Value = ui.Unknown, "left alone: "+c.Reason+"\n"+tilde(c.File)
			case protect.Absent:
				r.Glyph, r.Value = ui.Bullet, "not found"
			}
			rows = append(rows, r)
		}
		lines = append(lines, e.Style.KV(ui.Indent, e.Width, rows...)...)
		lines = append(lines, "")
		lines = append(lines, e.Style.KV(ui.Indent, e.Width, ui.Row{Key: "hook", Value: po.Command + " guard shell"})...)
		ui.Lines(e.Stdout, lines...)
	}
	if found == 0 {
		if e.Globals.JSON {
			return jsonOut(false, "")
		}
		fmt.Fprintln(e.Stdout, "")
		fmt.Fprintln(e.Stdout, "No coding agent found: none of ~/.claude, ~/.cursor, ~/.codex, ~/.gemini or ~/.copilot exists.")
		fmt.Fprintln(e.Stdout, e.Style.Next("Install one, or print a hook config by hand: deedglass guard shell --print-config --client <name>"))
		return nil
	}
	if adds == 0 {
		if e.Globals.JSON {
			return jsonOut(false, "")
		}
		fmt.Fprintln(e.Stdout, "")
		fmt.Fprintln(e.Stdout, e.Style.OKLine("Nothing to change."))
		return nil
	}
	if o.dryRun {
		if e.Globals.JSON {
			return jsonOut(false, "")
		}
		fmt.Fprintln(e.Stdout, "")
		fmt.Fprintf(e.Stdout, "Dry run: nothing was written. %d %s would change.\n", adds, plural(adds, "file", "files"))
		fmt.Fprintln(e.Stdout, e.Style.Next("deedglass protect"))
		return nil
	}
	if !o.yes {
		if e.Globals.JSON {
			return jsonOut(false, "")
		}
		fmt.Fprintln(e.Stdout, "")
		fmt.Fprintf(e.Stderr, "Change %d %s? Each is backed up first, and deedglass protect --undo puts it back. [y/N] ", adds, plural(adds, "file", "files"))
		ans, _ := bufio.NewReader(e.stdin()).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			fmt.Fprintln(e.Stderr, "Nothing was written.")
			return nil
		}
	}
	run, err := protect.Apply(po, cs)
	if err != nil {
		next := "Nothing was written."
		if run != "" {
			next = "deedglass protect --undo puts back what was written."
		}
		return failNext(e, ExitFailure, "validation_failed", "Couldn't write the hooks: "+err.Error()+".", next)
	}
	if e.Globals.JSON {
		return jsonOut(true, run)
	}
	fmt.Fprintln(e.Stdout, "")
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Added the guard to %d %s. Backups are in %s.", adds, plural(adds, "file", "files"), tilde(run))))
	for _, c := range cs {
		if c.Status != protect.Add {
			continue
		}
		switch c.Client {
		case "codex":
			fmt.Fprintln(e.Stdout, ui.Indent+e.Style.Dim("•")+" Codex runs the hook after you trust it with /hooks. It can't ask, so the guard denies.")
		case "gemini":
			fmt.Fprintln(e.Stdout, ui.Indent+e.Style.Dim("•")+" Gemini CLI can't ask, so the guard denies and shows why.")
		}
	}
	// Optional, two lines, no prompt: the badge stays whole on one line so it copies cleanly.
	fmt.Fprintln(e.Stdout, ui.Indent+e.Style.Dim("badge")+"  "+protectBadge)
	fmt.Fprintln(e.Stdout, ui.Indent+e.Style.Dim("share")+"  "+"Optional: paste it in your README, or send a teammate npx deedglass protect.")
	fmt.Fprintln(e.Stdout, e.Style.Next("Restart your agents. deedglass protect --undo puts the files back."))
	return nil
}

func protectUndo(e *Env, po protect.Options, dry bool) error {
	us, err := protect.Undo(po, dry)
	if err != nil {
		return failNext(e, ExitFailure, "validation_failed", "Couldn't read protect's backups: "+err.Error()+".", "The backups are in "+protect.StateDir(po.Home)+"; each run's manifest.json lists the files.")
	}
	failed := 0
	for _, u := range us {
		if u.Status == protect.Failed {
			failed++
		}
	}
	if e.Globals.JSON {
		if us == nil {
			us = []protect.Undone{}
		}
		if err := writeJSON(e, map[string]any{"object": "protect_undo", "dry_run": dry, "files": us}); err != nil {
			return err
		}
	} else if len(us) == 0 {
		fmt.Fprintln(e.Stdout, "Nothing to undo: deedglass protect hasn't changed a file here.")
		return nil
	} else {
		title := fmt.Sprintf("Put back %d %s.", len(us)-failed, plural(len(us)-failed, "file", "files"))
		if dry {
			title = fmt.Sprintf("Dry run: %d %s would be put back. Nothing was written.", len(us), plural(len(us), "file", "files"))
		}
		lines := []string{e.Style.OKLine(title), ""}
		var rows []ui.Row
		for _, u := range us {
			r := ui.Row{Key: u.Client, Value: u.File}
			switch u.Status {
			case protect.Restored:
				r.Glyph = ui.OK
				r.Value = "restored\n" + u.File
			case protect.Removed, protect.Left:
				r.Glyph = ui.Bullet
				r.Value = u.Reason + "\n" + u.File
			case protect.Failed:
				r.Glyph = ui.Fail
				r.Value = "failed: " + u.Reason + "\n" + u.File
			}
			rows = append(rows, r)
		}
		lines = append(lines, e.Style.KV(ui.Indent, e.Width, rows...)...)
		ui.Lines(e.Stdout, lines...)
		if !dry {
			fmt.Fprintln(e.Stdout, e.Style.Next("Restart your agents."))
		}
	}
	if failed > 0 {
		return &ExitError{Code: ExitFailure, Err: errors.New("some files couldn't be put back"), printed: true}
	}
	return nil
}
