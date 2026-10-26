package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"github.com/deedglass/deedglass/internal/play"
)

// deedglass play: the 301 scene offline (internal/play does the work and labels every line).

var playValues struct{ fast bool }

func playFlags(fs *flag.FlagSet) {
	playValues.fast = false
	fs.BoolVar(&playValues.fast, "fast", false, "print everything at once, with no pauses")
}

func runPlay(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass play takes no arguments (got %q)", args[0]), "Run deedglass play, or deedglass play --fast.")
	}
	o := play.Options{Out: e.Stdout, Style: e.Style, Width: e.Width, Verbose: e.Globals.Verbose > 0}
	if e.Globals.JSON {
		o.Out = discard{}
	}
	if e.IsTTY && !playValues.fast && !e.Globals.JSON {
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
	r, err := play.Run(ctx, o)
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Object string `json:"object"`
			*play.Result
		}{"play_result", r})
	}
	return nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
