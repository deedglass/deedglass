package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/deedglass/deedglass/internal/shellguard"
)

// deedglass guard shell (FEAT-07, ADR-104): a Claude Code PreToolUse hook. It reads the hook's JSON
// on stdin, checks the Bash command in it against internal/shellguard's catalog of commands that
// destroy database data, and answers "ask" (or "deny" with --deny) with one sentence naming
// Deedglass and the reason. It never answers "allow": when nothing matches it writes nothing and
// Claude Code's own permission rules decide. No server, no network, no config: a second check, not
// a sandbox.
//
// In hook mode every error exits 1, never 2: Claude Code treats exit 2 from a PreToolUse hook as a
// block, and a guard that can't read its input must not block every command.

type guardShellOpts struct {
	deny, list, noFiles bool
	command             string
}

var guardShellFlagValues guardShellOpts

func guardShellFlags(fs *flag.FlagSet) {
	guardShellFlagValues = guardShellOpts{}
	fs.BoolVar(&guardShellFlagValues.deny, "deny", false, "deny instead of ask: the command doesn't run and Claude reads the reason")
	fs.StringVar(&guardShellFlagValues.command, "command", "", "check this command and print what was found, instead of reading a hook input on stdin")
	fs.BoolVar(&guardShellFlagValues.list, "list", false, "print the catalog: every command shape it asks about, with its source and the date it was checked")
	fs.BoolVar(&guardShellFlagValues.noFiles, "no-files", false, "read only the command: no package.json scripts, shell scripts or SQL files from the working directory")
}

func runGuardShell(_ context.Context, e *Env, args []string) error {
	o := guardShellFlagValues
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass guard shell takes no arguments (got %q)", args[0]), `To check one command: deedglass guard shell --command "npx prisma migrate reset".`)
	}
	if o.list {
		return guardShellList(e)
	}
	opts := shellguard.Options{ReadFile: readGuardFile}
	if o.noFiles {
		opts.ReadFile = nil
	}
	if o.command != "" {
		if wd, err := os.Getwd(); err == nil {
			opts.Dir = wd
		}
		return guardShellCommand(e, o, shellguard.Check(o.command, opts))
	}
	if _, err := shellguard.Hook(e.stdin(), e.Stdout, o.deny, opts); err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "validation_failed", Err: fmt.Errorf("Deedglass couldn't read the hook input: %w", err),
			Next: "Claude Code runs this command as a PreToolUse hook and sends one JSON object on stdin; to check a command by hand, use --command."}
	}
	return nil
}

// guardShellCommand prints what --command found: the sentence a hook would show, then each part.
// Exit 1 when the hook would ask or deny, so scripts can use it.
func guardShellCommand(e *Env, o guardShellOpts, fs []shellguard.Finding) error {
	decision := "none"
	if len(fs) > 0 {
		decision = "ask"
		if o.deny {
			decision = "deny"
		}
	}
	if e.Globals.JSON {
		if fs == nil {
			fs = []shellguard.Finding{}
		}
		if err := writeJSON(e, map[string]any{"decision": decision, "reason": shellguard.Reason(fs, o.deny), "findings": fs}); err != nil {
			return err
		}
	} else if len(fs) == 0 {
		fmt.Fprintln(e.Stdout, "Nothing to ask about: no part of this command matches the catalog (deedglass guard shell --list).")
	} else {
		fmt.Fprintln(e.Stdout, shellguard.Reason(fs, o.deny))
		w := 0
		for _, f := range fs {
			w = max(w, len(f.ID))
		}
		for _, f := range fs {
			fmt.Fprintf(e.Stdout, "  %s  %s\n", e.Style.Dim(fmt.Sprintf("%-*s", w, f.ID)), f.What)
		}
	}
	if len(fs) > 0 {
		return &ExitError{Code: ExitFailure, Err: errors.New("the shell guard would " + decision), printed: true}
	}
	return nil
}

func guardShellList(e *Env) error {
	if e.Globals.JSON {
		return writeJSON(e, shellguard.Catalog)
	}
	for _, c := range shellguard.Catalog {
		fmt.Fprintf(e.Stdout, "%s\n  %s %s\n  %s (checked %s)\n", c.Shape, e.Style.Dim(c.ID+":"), c.Why, e.Style.Dim(c.Source), c.Checked)
	}
	return nil
}

// errNotRegular: the guard reads only regular files, so a FIFO or a device never blocks it.
var errNotRegular = errors.New("not a regular file of at most 1 MiB")

// readGuardFile reads a package.json, script or SQL file the checked command would read, if it is a
// regular file of at most shellguard.MaxBytes.
func readGuardFile(name string) ([]byte, error) {
	st, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > shellguard.MaxBytes {
		return nil, errNotRegular
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, shellguard.MaxBytes+1))
}
