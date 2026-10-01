package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deedglass/deedglass/internal/policy"
)

type policyFmtResult struct {
	Object  string `json:"object"` // "policy_fmt_result"
	File    string `json:"file"`
	Valid   bool   `json:"valid"`
	Changed bool   `json:"changed"`
	Written bool   `json:"written"`
}

func runPolicyFmt(_ context.Context, e *Env, args []string) error {
	var write, check, asJSON bool
	paths, err := parseInterleaved("deedglass policy fmt", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&write, "w", false, "write the result to the file")
		fs.BoolVar(&check, "check", false, "report files that would change")
		fs.BoolVar(&asJSON, "json", false, "print JSON")
	})
	if err != nil {
		return err
	}
	asJSON = asJSON || e.Globals.JSON
	if len(paths) == 0 {
		return usageErr(errors.New("deedglass policy fmt needs a file, a directory, or - for stdin"),
			"For example: deedglass policy fmt -w policies/refunds.yaml")
	}
	if write && check {
		return usageErr(errors.New("use either -w or --check, not both"), "--check only reports; -w writes the files.")
	}
	ins, err := policyInputs("deedglass policy fmt", paths)
	if err != nil {
		return err
	}
	if !write && !check && len(ins) > 1 {
		return usageErr(errors.New("formatting several files needs -w or --check"),
			"Run deedglass policy fmt -w "+shellQuote(paths[0])+" to write them, or --check to list the ones that would change.")
	}
	for _, in := range ins {
		if in.path == "-" && write {
			return usageErr(errors.New("-w can't write to stdin"), "Run deedglass policy fmt - to print the formatted policy instead.")
		}
	}
	var results []policyFmtResult
	bad, changed := 0, 0
	firstBad, firstChanged := "", ""
	for _, in := range ins {
		src, err := readPolicy(e, in)
		if err != nil {
			return err
		}
		res := policyFmtResult{Object: "policy_fmt_result", File: in.display}
		out, ferr := policy.Format(src)
		if ferr != nil {
			bad++
			if firstBad == "" {
				firstBad = in.display
			}
			results = append(results, res)
			if !asJSON {
				fmt.Fprintf(e.Stdout, "%s %s has errors, so it was not formatted · run deedglass policy lint %s\n",
					e.Style.Red("✕"), in.display, shellQuote(in.display))
			}
			continue
		}
		res.Valid = true
		res.Changed = !bytes.Equal(out, src)
		if res.Changed {
			changed++
			if firstChanged == "" {
				firstChanged = in.display
			}
		}
		switch {
		case !write && !check:
			if !asJSON {
				_, _ = e.Stdout.Write(out)
			}
		case check && res.Changed && !asJSON:
			fmt.Fprintf(e.Stdout, "%s %s would change · run deedglass policy fmt -w %s\n", e.Style.Amber("○"), in.display, shellQuote(in.display))
		case write && res.Changed:
			if err := writeAtomic(in.path, out); err != nil {
				return &ExitError{Code: ExitFailure, ErrCode: "write_failed", Err: fmt.Errorf("can't write %s: %s", in.display, plainErr(err)),
					Next: "Check the file's permissions and try again."}
			}
			res.Written = true
			if !asJSON {
				fmt.Fprintf(e.Stdout, "%s %s formatted\n", e.Style.Teal("✓"), in.display)
			}
		}
		results = append(results, res)
	}
	if asJSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if len(results) == 1 {
			_ = enc.Encode(results[0])
		} else {
			_ = enc.Encode(results)
		}
	} else if write || check {
		n := len(ins)
		switch {
		case check && changed > 0:
			verb := "needs"
			if changed > 1 {
				verb = "need"
			}
			fmt.Fprintf(e.Stdout, "%s %d of %d %s %s formatting\n", e.Style.Red("✕"), changed, n, pluralWord(n, "file", "files"), verb)
		case bad > 0:
		case write && changed > 0:
		default:
			fmt.Fprintf(e.Stdout, "%s %d of %d %s already formatted\n", e.Style.Teal("✓"), n, n, filesWord(n))
		}
	}
	if bad > 0 || (check && changed > 0) {
		return &ExitError{Code: ExitFailure, ErrCode: "policy_unformatted", Err: errors.New("not formatted"), printed: true}
	}
	return nil
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func filesWord(n int) string {
	if n == 1 {
		return "file is"
	}
	return "files are"
}

// writeAtomic replaces path with b through a temporary file in the same directory, keeping the mode.
func writeAtomic(path string, b []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".deedglass-fmt-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
