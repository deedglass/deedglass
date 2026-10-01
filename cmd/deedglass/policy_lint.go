package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/policy"
)

// policyInput is one policy file to read: a path, or stdin.
type policyInput struct {
	path    string // as given or found; "-" for stdin
	display string // shown in messages
	fromDir bool
}

// maxPolicyRead caps what the CLI reads from one file; the compiler refuses anything over 256 KiB
// with file_too_large, and this only bounds memory for absurd inputs.
const maxPolicyRead = 64 << 20

// parseInterleaved parses flags that may come before, between or after paths.
func parseInterleaved(name string, args []string, bind func(*flag.FlagSet)) ([]string, error) {
	var paths []string
	for i, a := range args {
		if a == "--" {
			paths = append(paths, args[i+1:]...)
			args = args[:i]
			break
		}
	}
	var rest []string
	for len(args) > 0 {
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		bind(fs)
		if err := fs.Parse(args); err != nil {
			return nil, usageErr(fmt.Errorf("%s: %v", name, err), "Run deedglass help "+strings.TrimPrefix(name, "deedglass ")+" for its flags.")
		}
		args = fs.Args()
		if len(args) > 0 {
			rest = append(rest, args[0])
			args = args[1:]
		}
	}
	return append(rest, paths...), nil
}

// policyInputs expands paths: files as given, directories recursively (*.yaml and *.yml, but not
// *.cases.yaml test files), and "-" for stdin.
func policyInputs(cmd string, paths []string) ([]policyInput, error) {
	var out []policyInput
	for _, p := range paths {
		if p == "-" {
			out = append(out, policyInput{path: "-", display: "stdin"})
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			return nil, usageErr(fmt.Errorf("can't read %s: %s", p, plainErr(err)), "Check the path and try again.")
		}
		if !fi.IsDir() {
			out = append(out, policyInput{path: p, display: p})
			continue
		}
		var found []string
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			n := d.Name()
			if d.IsDir() || strings.HasSuffix(n, ".cases.yaml") || strings.HasSuffix(n, ".cases.yml") {
				return nil
			}
			if strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") {
				found = append(found, path)
			}
			return nil
		})
		if err != nil {
			return nil, usageErr(fmt.Errorf("can't read %s: %s", p, plainErr(err)), "Check the directory's permissions and try again.")
		}
		if len(found) == 0 {
			return nil, usageErr(fmt.Errorf("%s has no .yaml or .yml policy files", p), "Point "+cmd+" at a policy file or a directory of them.")
		}
		sort.Strings(found)
		for _, f := range found {
			out = append(out, policyInput{path: f, display: f, fromDir: true})
		}
	}
	return out, nil
}

func plainErr(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "no such file or directory"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func readPolicy(e *Env, in policyInput) ([]byte, error) {
	var r io.Reader
	if in.path == "-" {
		r = e.stdin()
	} else {
		f, err := os.Open(in.path)
		if err != nil {
			return nil, usageErr(fmt.Errorf("can't read %s: %s", in.display, plainErr(err)), "Check the path and try again.")
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, maxPolicyRead+1))
	if err != nil {
		return nil, usageErr(fmt.Errorf("can't read %s: %s", in.display, plainErr(err)), "Check the file and try again.")
	}
	if len(b) > maxPolicyRead {
		return nil, usageErr(fmt.Errorf("%s is larger than 64 MiB, far over the 256 KiB policy limit", in.display), "Split it into smaller policies.")
	}
	return b, nil
}

type policyLintOpts struct {
	format string
	voice  bool
	json   bool
}

func runPolicyLint(_ context.Context, e *Env, args []string) error {
	var o policyLintOpts
	paths, err := parseInterleaved("deedglass policy lint", args, func(fs *flag.FlagSet) {
		fs.StringVar(&o.format, "format", "", "text, json or github")
		fs.BoolVar(&o.voice, "voice", false, "check reasons against the brand voice")
		fs.BoolVar(&o.json, "json", false, "print JSON")
	})
	if err != nil {
		return err
	}
	format := "text"
	if e.Globals.JSON || o.json {
		format = "json"
	}
	switch o.format {
	case "":
	case "text", "json", "github":
		format = o.format
	default:
		return usageErr(fmt.Errorf("--format must be text, json or github (got %q)", clean(o.format)), "For example: deedglass policy lint --format github policies/")
	}
	if len(paths) == 0 {
		return usageErr(errors.New("deedglass policy lint needs a file, a directory, or - for stdin"),
			"For example: deedglass policy lint policies/refunds.yaml")
	}
	ins, err := policyInputs("deedglass policy lint", paths)
	if err != nil {
		return err
	}
	type linted struct {
		in  policyInput
		src []byte
		r   *policy.LintResult
	}
	var all []linted
	failed := 0
	for _, in := range ins {
		src, err := readPolicy(e, in)
		if err != nil {
			return err
		}
		file := in.display
		if in.path == "-" {
			file = ""
		}
		r := policy.Lint(src, policy.Options{File: file, Voice: o.voice})
		if !r.Valid {
			failed++
		}
		all = append(all, linted{in, src, r})
	}
	w := e.Stdout
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if len(all) == 1 {
			_ = enc.Encode(all[0].r)
		} else {
			rs := make([]*policy.LintResult, len(all))
			for i, x := range all {
				rs[i] = x.r
			}
			_ = enc.Encode(rs)
		}
	case "github":
		for _, x := range all {
			policy.RenderGitHub(w, x.r, x.in.display)
		}
	default:
		if len(all) == 1 && !all[0].in.fromDir {
			var buf bytes.Buffer
			policy.RenderText(&buf, all[0].r, all[0].src, all[0].in.display, e.Style)
			_, _ = w.Write(buf.Bytes())
			break
		}
		firstBad := ""
		warns := 0
		for _, x := range all {
			warns += len(x.r.Warnings)
			fmt.Fprintln(w, fileLine(e, x.in.display, x.r))
			if !x.r.Valid && firstBad == "" {
				firstBad = x.in.display
			}
		}
		if failed > 0 {
			verb := "has"
			if failed > 1 {
				verb = "have"
			}
			fmt.Fprintf(w, "%s %d of %d files %s errors · run deedglass policy lint %s for details\n",
				e.Style.Red("✕"), failed, len(all), verb, shellQuote(firstBad))
		} else {
			line := fmt.Sprintf("%s %d of %d files are valid", e.Style.Teal("✓"), len(all), len(all))
			if warns > 0 {
				line += " · " + e.Style.Amber("○") + " " + pluralN(warns, "warning", "warnings")
			}
			fmt.Fprintln(w, line)
		}
	}
	if failed > 0 {
		return &ExitError{Code: ExitFailure, ErrCode: "policy_invalid", Err: errors.New("the policy was not accepted"), printed: true}
	}
	return nil
}

// fileLine is one row of a multi-file lint.
func fileLine(e *Env, display string, r *policy.LintResult) string {
	if !r.Valid {
		parts := []string{e.Style.Red("✕") + " " + display, pluralN(len(r.Errors), "error", "errors")}
		if len(r.Warnings) > 0 {
			parts = append(parts, pluralN(len(r.Warnings), "warning", "warnings"))
		}
		return strings.Join(parts, " · ")
	}
	parts := []string{e.Style.Teal("✓") + " " + display, pluralN(r.Rules, "rule", "rules"), pluralN(r.Budgets, "budget", "budgets")}
	if len(r.Warnings) > 0 {
		parts = append(parts, pluralN(len(r.Warnings), "warning", "warnings"))
	}
	return strings.Join(parts, " · ")
}

func pluralN(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// shellQuote quotes a path for a copy-pasteable command when it needs it.
func shellQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t'\"$`\\!*?[]{}()<>|&;#~") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}
