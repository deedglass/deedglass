package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/deedglass/deedglass/internal/buildinfo"
	"github.com/deedglass/deedglass/internal/cli/mark"
)

// The terminal mark (brand §13.3, S05X). It shows only for bare deedglass, deedglass version
// and --version on an interactive TTY, and as the inline header of help. Never under --json,
// -q, when stdout is piped, in CI or with DEEDGLASS_NO_BANNER; mark.Detect and markOptions
// decide, and nothing here can fail a command.

// markOptions resolves the mark for this run. --json makes it quiet; --no-color drops colour.
func markOptions(e *Env) mark.Options {
	o := mark.Detect(e.Getenv, e.IsTTY, e.Width)
	if e.Globals.JSON {
		o.Quiet = true
	}
	if e.Globals.NoColor && o.Depth > mark.DepthASCII {
		o.Depth, o.Anim = mark.DepthNone, false
	}
	// The terminal colour depth must agree with the Style rules (TERM=dumb has no colour).
	if !e.Style.Color && o.Depth > mark.DepthASCII {
		o.Depth, o.Anim = mark.DepthNone, false
	}
	in := buildInfo()
	o.Version = in.Version
	if n := len(in.APIVersions); n > 0 {
		o.API = in.APIVersions[n-1]
	}
	return o
}

// bareOnly reports whether flags are ones bare deedglass accepts: -q / --quiet and --version.
func bareOnly(flags []string) bool {
	for _, f := range flags {
		switch f {
		case "-q", "--quiet", "-quiet", "--version", "-version":
		default:
			return false
		}
	}
	return true
}

// runBare is deedglass with no command: the welcome (what it is and the three commands to start
// with; first-run-v1), plain when piped. -q prints the plain help, --version is deedglass version.
func runBare(ctx context.Context, e *Env, flags []string) error {
	quiet, version := false, false
	for _, f := range flags {
		if strings.TrimLeft(f, "-") == "version" {
			version = true
		} else {
			quiet = true
		}
	}
	if quiet {
		e.Mark.Quiet = true
	}
	if version {
		versionFlagValues = versionOpts{}
		return runVersion(ctx, e, nil)
	}
	if quiet {
		return printHelp(e, nil)
	}
	fmt.Fprint(e.Stdout, welcome(e))
	return nil
}

// versionBanner is the banner in version mode: commit, build date and platform as its last lines.
func versionBanner(e *Env, in buildinfo.Info) string {
	o := e.Mark
	commit := in.Commit
	if len(commit) > 7 {
		commit = commit[:7]
	}
	if commit == "" {
		commit = "unknown"
	}
	if in.Modified {
		commit += " (modified)"
	}
	first := "commit " + commit
	if len(in.Date) >= 10 {
		first += " · built " + in.Date[:10]
	}
	o.Next = first + "\n" + in.Go + " · " + in.OS + "/" + in.Arch
	return mark.Banner(o)
}
