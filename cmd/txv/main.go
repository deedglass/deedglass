// Command txv is the single V0 binary (spec 20_ §2.1, §3.3). Each component
// runs as a separate process with its own credentials: the signer
// (txv sign), the executor (txv executor serve), the verifier
// (txv verifier run) and the offline checker (txv statement verify,
// txv evidence verify). Only `version` is implemented today; every other
// subcommand prints "not implemented" and exits with status 3.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
)

// version is set at release time with -ldflags "-X main.version=v0.1.0".
var version = "0.0.0-dev"

type command struct {
	name    string // one or two words, e.g. "executor serve"
	summary string
}

// commands lists the subcommands named in spec §3.3 (plus `evidence report`
// from spec §14.1) in the order they appear there.
var commands = []command{
	{"keygen", "generate an Ed25519 key pair (--principal | --verifier)"},
	{"sign", "sign a txv.intent/1 intent with a principal key (separate from agent and app)"},
	{"submit", "POST a signed intent to the executor"},
	{"executor serve", "run the executor HTTP service (prevention; holds no signing key)"},
	{"verifier run", "run the verifier: decode, classify, check, issue statements"},
	{"statement verify", "check a verification statement offline with verifier public keys"},
	{"evidence export", "write the evidence record for an intent_hash"},
	{"evidence verify", "recompute the expected delta and digests from an evidence record, offline"},
	{"evidence report", "render a static HTML evidence report (no JavaScript)"},
	{"install", "install roles, schema, function, publication and slot (--harden for self-hosted)"},
	{"audit", "run the privilege and hardening audit (spec §4.5)"},
	{"boundary", "print the observation boundary manifest and its committed hash"},
	{"status", "look up an intent: STATEMENT | PENDING | NOT_COMMITTED"},
	{"chain", "show or check the statement hash chain and its head"},
	{"version", "print version and build information"},
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches args and returns the process exit status:
// 0 ok, 2 usage error, 3 not implemented.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	case "version", "--version":
		printVersion(stdout)
		return 0
	}
	cmd, ok := lookup(args)
	if !ok {
		fmt.Fprintf(stderr, "txv: unknown command %q\n\n", strings.Join(head(args, 2), " "))
		usage(stderr)
		return 2
	}
	fmt.Fprintf(stderr, "txv %s: not implemented\n", cmd.name)
	return 3
}

func head(args []string, n int) []string {
	if len(args) < n {
		return args
	}
	return args[:n]
}

// lookup matches a two-word command first, then a one-word command.
func lookup(args []string) (command, bool) {
	if len(args) >= 2 {
		for _, c := range commands {
			if c.name == args[0]+" "+args[1] {
				return c, true
			}
		}
	}
	for _, c := range commands {
		if c.name == args[0] {
			return c, true
		}
	}
	return command{}, false
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: txv <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-18s %s\n", c.name, c.summary)
	}
}

func printVersion(w io.Writer) {
	rev, dirty := "unknown", false
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if dirty {
		rev += "-dirty"
	}
	fmt.Fprintf(w, "txv %s (commit %s, %s, %s/%s)\n", version, rev, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
