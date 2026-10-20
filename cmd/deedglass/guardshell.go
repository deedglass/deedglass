package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

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
	deny, list, noFiles, printConfig bool
	command, client                  string
}

var guardShellFlagValues guardShellOpts

func guardShellFlags(fs *flag.FlagSet) {
	guardShellFlagValues = guardShellOpts{}
	fs.BoolVar(&guardShellFlagValues.deny, "deny", false, "deny instead of ask: the command doesn't run and Claude reads the reason")
	fs.StringVar(&guardShellFlagValues.command, "command", "", "check this command and print what was found, instead of reading a hook input on stdin")
	fs.BoolVar(&guardShellFlagValues.list, "list", false, "print the catalog: every command shape it asks about, with its source and the date it was checked")
	fs.BoolVar(&guardShellFlagValues.noFiles, "no-files", false, "read only the command: no package.json scripts, shell scripts or SQL files from the working directory")
	fs.StringVar(&guardShellFlagValues.client, "client", "claude-code", "the coding agent running the hook, which sets the input it reads and the answer it writes: "+shellguard.ClientNames())
	fs.BoolVar(&guardShellFlagValues.printConfig, "print-config", false, "print the hook config for --client and where it goes, instead of reading a hook input (it writes no file)")
}

func runGuardShell(_ context.Context, e *Env, args []string) error {
	o := guardShellFlagValues
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass guard shell takes no arguments (got %q)", args[0]), `To check one command: deedglass guard shell --command "npx prisma migrate reset".`)
	}
	if o.list {
		return guardShellList(e)
	}
	client, ok := shellguard.LookupClient(o.client)
	if !ok {
		return usageErr(fmt.Errorf("--client takes %s (got %q)", shellguard.ClientNames(), o.client), "For example: deedglass guard shell --print-config --client codex.")
	}
	if o.printConfig {
		return guardShellPrintConfig(e, client)
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
	if client.Unsupported != "" {
		return usageErr(fmt.Errorf("the shell guard has no hook for %s", client.Title), client.Unsupported)
	}
	if _, err := client.Hook(e.stdin(), e.Stdout, o.deny, opts); err != nil {
		if client.ErrorBlocks {
			// This client denies the tool call when the hook exits non-zero; never block on our own failure.
			fmt.Fprintf(e.Stderr, "Deedglass couldn't read the hook input (%v), so it checked nothing.\n", err)
			return nil
		}
		return &ExitError{Code: ExitFailure, ErrCode: "validation_failed", Err: fmt.Errorf("Deedglass couldn't read the hook input: %w", err),
			Next: client.Title + " runs this command as a hook and sends one JSON object on stdin; to check a command by hand, use --command."}
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

// guardShellPrintConfig prints one client's hook config from the table in internal/shellguard on
// stdout, and where it goes and what the client can't do on stderr. It writes no file.
func guardShellPrintConfig(e *Env, c shellguard.ClientHook) error {
	if e.Globals.JSON {
		limits := c.Limits
		if limits == nil {
			limits = []string{}
		}
		return writeJSON(e, map[string]any{"client": c.Name, "title": c.Title, "supported": c.Unsupported == "", "file": c.File,
			"config": c.Config, "limits": limits, "unsupported": c.Unsupported, "docs": c.Docs, "checked": c.Checked})
	}
	if c.Unsupported != "" {
		fmt.Fprintf(e.Stdout, "No shell guard hook for %s yet. %s\n", c.Title, c.Unsupported)
		return nil
	}
	// The config alone on stdout, so it can go straight into a new file; the rest on stderr.
	fmt.Fprint(e.Stdout, c.Config)
	fmt.Fprintf(e.Stderr, "%s goes in %s. Merge it with any hooks already there.\n", c.Title, c.File)
	for _, l := range c.Limits {
		fmt.Fprintf(e.Stderr, "Note: %s\n", l)
	}
	fmt.Fprintf(e.Stderr, "Format checked %s against %s\n", c.Checked, strings.Join(c.Docs, ", "))
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
