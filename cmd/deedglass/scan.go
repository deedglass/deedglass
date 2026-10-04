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
	"github.com/deedglass/deedglass/internal/scan"
)

// deedglass scan (S32 §3.13): flags and rendering only; internal/scan does the work.

var scanValues struct {
	dir, dsnEnv, outDir, format, failUnder        string
	userConfigs, noWrite, score, badge, share, ci bool
	maxFiles                                      int
}

func scanFlags(fs *flag.FlagSet) {
	v := &scanValues
	*v = struct {
		dir, dsnEnv, outDir, format, failUnder        string
		userConfigs, noWrite, score, badge, share, ci bool
		maxFiles                                      int
	}{}
	fs.StringVar(&v.dir, "dir", ".", "the directory to scan")
	fs.StringVar(&v.dsnEnv, "postgres-dsn-env", "", "the environment variable holding a Postgres URL to check read-only (never a URL itself)")
	fs.BoolVar(&v.userConfigs, "user-configs", true, "also read your user-level MCP client configs (Claude Desktop, Cursor, Windsurf)")
	fs.StringVar(&v.outDir, "out-dir", ".", "where the report files go")
	fs.StringVar(&v.format, "format", "md,html", "report files to write: md, html, json, sarif (comma-separated)")
	fs.IntVar(&v.maxFiles, "max-files", 20000, "stop the file walk after this many files")
	fs.BoolVar(&v.noWrite, "no-write", false, "print only; write no report files")
	fs.BoolVar(&v.score, "score", false, "print the score line, the small print and the deductions only")
	fs.BoolVar(&v.badge, "badge", false, "write .deedglass/score.json (a shields.io endpoint) and print the README line")
	fs.BoolVar(&v.share, "share", false, "print a share link that holds the grade, counts and finding ids only")
	fs.BoolVar(&v.ci, "ci", false, "for CI: no user configs, no colour, no mark, never a prompt")
	fs.StringVar(&v.failUnder, "fail-under", "", "exit 1 when the grade is below this one: A, B, C or D")
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
	if v.badge {
		if _, err := scan.WriteBadge(abs, r.Score.Grade); err != nil {
			return fmt.Errorf("couldn't write .deedglass/score.json: %w", err)
		}
		rem := scan.ReadRemote(abs)
		if rem.OK {
			badgeLines = append(badgeLines, e.Style.OKLine("Wrote .deedglass/score.json. Add it with git add -f .deedglass/score.json, then paste this line into your README:"))
		} else {
			badgeLines = append(badgeLines, e.Style.UnknownLine("No GitHub remote found, so the line below has placeholders. Replace <owner>, <repo> and <branch>."))
		}
		badgeLines = append(badgeLines, scan.BadgeMarkdown(rem))
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
		if il := mark.Inline(mo); il != "" {
			fmt.Fprintln(e.Stdout, il)
		}
		header := fmt.Sprintf("deedglass scan · %s · %s s · read-only", label, secs(time.Duration(r.TookMS)*time.Millisecond))
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
			fmt.Fprint(e.Stdout, scan.RenderText(r, st, e.Width, header, wroteLine))
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
	}
	if below {
		return &ExitError{Code: ExitFailure, ErrCode: "below_fail_under",
			Err: fmt.Errorf("Blast radius %s is below %s (--fail-under %s). The report was written", r.Score.Grade, v.failUnder, v.failUnder)}
	}
	if pgFailed {
		return &ExitError{Code: ExitFailure, ErrCode: "postgres_unreachable", printed: !e.Globals.JSON,
			Err: errors.New("the Postgres part failed; the file part was written")}
	}
	return nil
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
