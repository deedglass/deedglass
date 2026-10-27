package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/cli/mark"
	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/scan"
)

// deedglass scan (S32 §3.13): flags and rendering only; internal/scan does the work.

var scanValues struct {
	dir, dsnEnv, outDir, format, failUnder        string
	userConfigs, noWrite, score, badge, share, ci bool
	full, noSeal, noStar, badgeShare              bool
	maxFiles                                      int
}

func scanFlags(fs *flag.FlagSet) {
	v := &scanValues
	*v = struct {
		dir, dsnEnv, outDir, format, failUnder        string
		userConfigs, noWrite, score, badge, share, ci bool
		full, noSeal, noStar, badgeShare              bool
		maxFiles                                      int
	}{}
	fs.StringVar(&v.dir, "dir", ".", "the directory to scan")
	fs.StringVar(&v.dsnEnv, "postgres-dsn-env", "", "the environment variable holding a Postgres URL to check read-only (never a URL itself)")
	fs.BoolVar(&v.userConfigs, "user-configs", true, "also read your user-level MCP client configs (Claude Desktop, Cursor, Windsurf) and Claude Code settings")
	fs.StringVar(&v.outDir, "out-dir", ".", "where the report files go")
	fs.StringVar(&v.format, "format", "md,html", "report files to write: md, html, json, sarif (comma-separated)")
	fs.IntVar(&v.maxFiles, "max-files", 20000, "stop the file walk after this many files")
	fs.BoolVar(&v.noWrite, "no-write", false, "print only; write no report files")
	fs.BoolVar(&v.score, "score", false, "print the score line, the small print and the deductions only")
	fs.BoolVar(&v.badge, "badge", false, "write .deedglass/score.json (a shields.io endpoint) and print the README line; the badge links to deedglass.com/score")
	fs.BoolVar(&v.badgeShare, "badge-share", false, "--badge, but the badge links to your /s share page (grade, points and counts only; anyone who reads the README can open it)")
	fs.BoolVar(&v.share, "share", false, "print a share link that holds the grade, counts and finding ids only")
	fs.BoolVar(&v.ci, "ci", false, "for CI: no user configs, no colour, no mark, never a prompt")
	fs.StringVar(&v.failUnder, "fail-under", "", "exit 1 when the grade is below this one: A, B, C or D")
	fs.BoolVar(&v.full, "full", false, "also write deedglass-full-report.html, the full scan report (Pro or the $29 report; checks the licence)")
	fs.BoolVar(&v.noSeal, "no-seal", false, "with --full: don't seal the report on your Deedglass server")
	fs.BoolVar(&v.noStar, "no-star", false, "don't show the one-line GitHub star note (it shows at most once per machine; DEEDGLASS_NO_STAR=1 hides it for good)")
}

// scanFormats are the values --format accepts (help --json lists them for completions).
var scanFormats = []string{"md", "html", "json", "sarif"}

func runScan(ctx context.Context, e *Env, args []string) error {
	v := scanValues
	v.failUnder = strings.ToUpper(v.failUnder)
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass scan takes no arguments (got %q)", args[0]), "Pass the directory with --dir, for example deedglass scan --dir ~/code/my-agent.")
	}
	if v.failUnder != "" && !strings.Contains("ABCD", v.failUnder) || len(v.failUnder) > 1 {
		return usageErr(fmt.Errorf("--fail-under takes A, B, C or D (got %q)", v.failUnder), "Nothing is below F, so F isn't a value. Try --fail-under C.")
	}
	formats := map[string]bool{}
	for _, f := range strings.Split(v.format, ",") {
		f = strings.TrimSpace(strings.ToLower(f))
		if f == "" {
			continue
		}
		ok := false
		for _, k := range scanFormats {
			ok = ok || f == k
		}
		if !ok {
			return usageErr(fmt.Errorf("--format takes md, html, json and sarif (got %q)", f), "For example: deedglass scan --format md,sarif.")
		}
		formats[f] = true
	}
	if v.ci {
		v.userConfigs = false
		e.Style.Color = false // errors on stderr too
	}
	dsn := ""
	if v.dsnEnv != "" {
		dsn = e.Getenv(v.dsnEnv)
		if dsn == "" {
			return &ExitError{Code: ExitUsage, ErrCode: "dsn_unset",
				Err: fmt.Errorf("%s isn't set. Export the database URL in that variable, then run deedglass scan again", v.dsnEnv)}
		}
	}
	abs, err := filepath.Abs(v.dir)
	if err != nil {
		return usageErr(err, "Check the path given to --dir.")
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return usageErr(fmt.Errorf("%s isn't a directory", v.dir), "Pass a directory with --dir.")
	}
	home := e.Getenv("HOME")
	label := abs
	if home != "" && (abs == home || strings.HasPrefix(abs, home+string(filepath.Separator))) {
		label = "~" + strings.TrimPrefix(abs, home)
	}
	in := buildInfo()
	r, err := scan.Scan(ctx, scan.Options{Dir: abs, DirLabel: label, Home: home, UserConfigs: v.userConfigs,
		MaxFiles: v.maxFiles, GOOS: runtime.GOOS, DSN: dsn, DSNVar: v.dsnEnv, Version: in.Version})
	dsn = ""
	if err != nil {
		return fmt.Errorf("the scan couldn't run: %w", err)
	}
	pgFailed := r.PostgresError != nil

	// Files first: everything is written before any exit code.
	var wrote []string
	if !v.noWrite && !e.Globals.JSON {
		for _, f := range []string{"md", "html", "json", "sarif"} {
			if !formats[f] {
				continue
			}
			name, err := writeScanFile(v.outDir, f, r)
			if err != nil {
				return fmt.Errorf("couldn't write %s: %w", name, err)
			}
			wrote = append(wrote, name)
		}
	}
	var badgeLines, shareLines []string
	if v.badgeShare {
		v.badge = true
	}
	if v.badge {
		if _, err := scan.WriteBadge(abs, r.Score.Grade); err != nil {
			return fmt.Errorf("couldn't write .deedglass/score.json: %w", err)
		}
		rem := scan.ReadRemote(abs)
		if rem.OK {
			badgeLines = append(badgeLines, e.Style.OKLine("Wrote .deedglass/score.json (agent blast radius: "+r.Score.Grade+"). Add this line to your README:"))
		} else {
			badgeLines = append(badgeLines, e.Style.UnknownLine("No GitHub remote found, so the line below has placeholders. Replace <owner>, <repo> and <branch>."))
		}
		link := ""
		if v.badgeShare {
			if link, err = scan.ShareLink(scan.BadgeShare(r)); err != nil {
				return err
			}
		}
		badgeLines = append(badgeLines, scan.BadgeMarkdown(rem, link))
		if v.badgeShare {
			badgeLines = append(badgeLines,
				e.Style.UnknownLine("This badge links to your share page, not deedglass.com/score. Anyone who reads your README can open it."),
				"  It shows the grade, the points and how many findings of each severity: no finding names, paths or tables.",
				"  The link keeps today's result. Run deedglass scan --badge-share again when the grade changes, or --badge to link deedglass.com/score.")
		}
		if scan.GitignoreHidesBadge(abs) {
			badgeLines = append(badgeLines, "Your .gitignore ignores all of .deedglass/. To commit only the badge file, replace that line with these two:",
				"  .deedglass/*", "  !.deedglass/score.json")
		}
		badgeLines = append(badgeLines, "Commit it if you want it in your README. Nothing was committed.")
	}
	if v.share {
		link, err := scan.ShareLink(scan.NewShare(r))
		if err != nil {
			return err
		}
		shareLines = []string{"Share link (grade, counts and finding ids only; no paths or table names):", link,
			"The part after # stays in your browser. Nothing was sent."}
	}
	below := v.failUnder != "" && scan.GradeRank(r.Score.Grade) > scan.GradeRank(v.failUnder)

	if e.Globals.JSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			return err
		}
	} else {
		st := scan.Style{}
		if e.Style.Color && !v.ci {
			st = scan.Style{Red: e.Style.Red, Teal: e.Style.Teal, Amber: e.Style.Amber, Violet: e.Style.Violet, Dim: e.Style.Dim, Bold: e.Style.Bold}
		}
		mo := e.Mark
		mo.Command = "scan"
		if v.ci {
			mo.Quiet = true
		}
		// One header line: the inline mark (on a terminal) or the command, then dim meta.
		header := e.Style.Header("deedglass scan", label, secs(time.Duration(r.TookMS)*time.Millisecond)+" s", "read-only")
		if il := mark.Inline(mo); il != "" {
			header = il + "  " + e.Style.Dim(label+" "+ui.Sep+" "+secs(time.Duration(r.TookMS)*time.Millisecond)+" s "+ui.Sep+" read-only")
		}
		if v.score {
			fmt.Fprintln(e.Stdout, header)
			fmt.Fprintln(e.Stdout)
			for _, l := range scan.ScoreBlock(st, r.Score) {
				fmt.Fprintln(e.Stdout, l)
			}
		} else {
			wroteLine := ""
			if len(wrote) > 0 {
				wroteLine = "Wrote " + andList(wrote) + " (nothing left this machine)."
			}
			fmt.Fprint(e.Stdout, scan.RenderText(r, st, ui.LayoutWidth(e.Width), header, wroteLine))
		}
		for _, w := range wrote {
			if strings.HasSuffix(w, ".sarif") {
				_, n, _ := scan.RenderSARIF(r)
				fmt.Fprintf(e.Stdout, "Wrote %s (SARIF 2.1.0, %d %s).\n", w, n, map[bool]string{true: "rule", false: "rules"}[n == 1])
			}
		}
		for _, l := range append(badgeLines, shareLines...) {
			fmt.Fprintln(e.Stdout, l)
		}
		if !v.ci && !v.score {
			// The Next block (CLI_STYLE.md › Next steps; first-run-v1 F11).
			inv := invocation(e)
			fmt.Fprintln(e.Stdout)
			fmt.Fprintln(e.Stdout, e.Style.Section("Next"))
			ui.Lines(e.Stdout, e.Style.Commands("  ", e.Width,
				[2]string{inv + " protect", "guard Claude Code, Cursor and other coding agents"},
				[2]string{inv + " start", "see a check catch an agent's wrong row count"})...)
		}
	}
	if v.full {
		if v.noWrite {
			return usageErr(errors.New("--full writes a report file, so it can't be used with --no-write"), "Drop --no-write.")
		}
		if err := scanFull(ctx, e, r, v.outDir, v.noSeal); err != nil {
			return err
		}
	}
	if below {
		return &ExitError{Code: ExitFailure, ErrCode: "below_fail_under",
			Err: fmt.Errorf("Blast radius %s is below %s (--fail-under %s). The report was written", r.Score.Grade, v.failUnder, v.failUnder)}
	}
	if pgFailed {
		return &ExitError{Code: ExitFailure, ErrCode: "postgres_unreachable", printed: !e.Globals.JSON,
			Err: errors.New("the Postgres part failed; the file part was written")}
	}
	if !e.Globals.JSON && !v.ci && !v.noStar {
		starNote(e)
	}
	return nil
}

// starURL is the repository the star note points to.
const starURL = "https://github.com/deedglass/deedglass"

// starNote prints a dim two-line note asking for a GitHub star, at most once per machine: only in a
// terminal, never in CI, never with DEEDGLASS_NO_STAR set. It writes a marker file before printing,
// so a machine where the marker can't be written never sees the line at all (rather than every run).
func starNote(e *Env) {
	if !e.IsTTY || e.Getenv("CI") != "" || truthy(e.Getenv("DEEDGLASS_NO_STAR")) {
		return
	}
	marker := starMarker(e.Getenv)
	if marker == "" {
		return
	}
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return
	}
	if err := os.WriteFile(marker, []byte("The GitHub star note was shown once on this machine. Delete this file to see it again.\n"), 0o600); err != nil {
		return
	}
	fmt.Fprintln(e.Stdout)
	fmt.Fprintln(e.Stdout, e.Style.Dim("If this helped, a GitHub star helps others find it: "+starURL+" (shown once)"))
}

// starMarker is where the star note remembers it was shown: $XDG_CONFIG_HOME/deedglass, else
// ~/.config/deedglass (%USERPROFILE%\.config\deedglass on Windows). "" when there is no home.
func starMarker(getenv func(string) string) string {
	dir := getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home := getenv("HOME")
		if home == "" {
			home = getenv("USERPROFILE")
		}
		if home == "" {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "deedglass", "star-note-shown")
}

func writeScanFile(dir, format string, r *scan.Report) (string, error) {
	name := "deedglass-scan." + format
	var b []byte
	switch format {
	case "md":
		b = []byte(scan.RenderMarkdown(r))
	case "html":
		h, err := scan.RenderHTML(r)
		if err != nil {
			return name, err
		}
		b = []byte(h)
	case "json":
		j, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return name, err
		}
		b = append(j, '\n')
	case "sarif":
		s, _, err := scan.RenderSARIF(r)
		if err != nil {
			return name, err
		}
		b = s
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return name, err
	}
	return name, os.Chmod(p, 0o644)
}

func andList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func secs(d time.Duration) string { return fmt.Sprintf("%.1f", d.Seconds()) }
