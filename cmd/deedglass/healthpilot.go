package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	"github.com/deedglass/deedglass/internal/healthpilot"
)

// deedglass health pilot: the hospital IT demo on synthetic data (internal/healthpilot does the
// work and docs/healthcare/PILOT.md describes it).

var healthPilotValues struct {
	region, fhirURL, consoleURL, out string
	fast, printReviewPack, noFiles   bool
}

func healthPilotFlags(fs *flag.FlagSet) {
	v := &healthPilotValues
	*v = struct {
		region, fhirURL, consoleURL, out string
		fast, printReviewPack, noFiles   bool
	}{}
	fs.StringVar(&v.region, "region", healthpilot.RegionDefault, "global (the healthcare-fhir pack) or india (india-health, for ABDM and NHCX)")
	fs.StringVar(&v.fhirURL, "fhir-url", "", "the base URL of your FHIR R4 sandbox (auth none, synthetic data only); scene 1 writes there")
	fs.StringVar(&v.consoleURL, "console-url", healthpilot.DefaultConsoleURL, "the Console base URL the approve link names")
	fs.StringVar(&v.out, "out", "deedglass-pilot", "the folder for REPORT.md, IT_REVIEW_PACK.md, deeds.jsonl and keys.json")
	fs.BoolVar(&v.noFiles, "no-files", false, "write no files")
	fs.BoolVar(&v.fast, "fast", false, "print everything at once, with no pauses")
	fs.BoolVar(&v.printReviewPack, "print-review-pack", false, "print the hospital IT review pack (Markdown) and exit")
}

func runHealthPilot(ctx context.Context, e *Env, args []string) error {
	v := healthPilotValues
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass health pilot takes no arguments (got %q)", args[0]), "Run deedglass health pilot, or add --fhir-url <your FHIR sandbox>.")
	}
	if v.printReviewPack {
		if e.Globals.JSON {
			return writeJSON(e, map[string]any{"object": "review_pack", "format": "markdown", "markdown": string(healthpilot.ReviewPack())})
		}
		_, err := e.Stdout.Write(healthpilot.ReviewPack())
		return err
	}
	if v.region != healthpilot.RegionDefault && v.region != healthpilot.RegionIndia {
		return usageErr(fmt.Errorf("unknown region %q", v.region), "Use --region global or --region india.")
	}
	if v.fhirURL != "" {
		if u, err := url.Parse(v.fhirURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return usageErr(fmt.Errorf("--fhir-url %q isn't an http or https URL", v.fhirURL), "Pass your FHIR sandbox's base URL, like https://hapi.example.org/fhir.")
		}
	}
	o := healthpilot.Options{Out: e.Stdout, Style: e.Style, Width: e.Width, Region: v.region, FHIRURL: v.fhirURL, ConsoleURL: v.consoleURL, Cmd: invocation(e), Brief: e.Globals.Verbose == 0}
	if !v.noFiles {
		o.Dir, o.DisplayDir = v.out, v.out
		if abs, err := filepath.Abs(v.out); err == nil {
			o.Dir = abs
		}
	}
	if e.Globals.JSON {
		o.Out = discard{}
	}
	if e.IsTTY && !v.fast && !e.Globals.JSON {
		o.Pace = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	r, err := healthpilot.Run(ctx, o)
	if err != nil {
		return &ExitError{Code: 1, ErrCode: "internal_error", Err: err,
			Next: "Run it again with --fast; if it fails the same way, file an issue with this output."}
	}
	if e.Globals.JSON {
		return writeJSON(e, struct {
			Object string `json:"object"`
			*healthpilot.Result
		}{"health_pilot_result", r})
	}
	return nil
}
