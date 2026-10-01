// Command deedglass is the Deedglass CLI and server binary (contract §10). Every contract command
// is in the registry (registry.go); commands that aren't built yet say so and name no dates.
//
// Exit codes: 0 ok · 1 failure or invalid · 2 usage, unknown, not built, or cannot determine ·
// 3 not authenticated.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/cli/mark"
	"github.com/deedglass/deedglass/internal/config"
)

// Exit codes.
const (
	ExitOK         = 0
	ExitFailure    = 1
	ExitUsage      = 2
	ExitNotAuthed  = 3
	docsURL        = "https://docs.deedglass.com"
	errorDocPrefix = docsURL + "/errors/"
)

// Globals are accepted anywhere before "--".
type Globals struct {
	JSON    bool
	NoColor bool
	Server  string
	APIKey  string
	Org     string
	Verbose int
}

// Env is everything a command touches, injected so tests run without process state.
type Env struct {
	Stdin          io.Reader // nil means os.Stdin
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	IsTTY          bool
	Width          int // terminal columns; 0 when unknown
	Globals        Globals
	Style          ui.Style
	Mark           mark.Options // the terminal mark (banner, inline); Quiet off a TTY, in CI, with --json
	LoadOptions    func() config.LoadOptions
}

// ExitError carries an exit code and, for --json, the API error shape.
type ExitError struct {
	Code    int
	Err     error
	ErrCode string   // snake_case, as in the API error envelope
	Next    string   // the next step, printed on its own line
	Detail  []string // text mode only: indented lines between the message and the next step
	Plain   bool     // print without the ✕ symbol (calm, not an error)
	Extra   map[string]any
	printed bool
}

// stdin returns the input stream (deedglass verify -).
func (e *Env) stdin() io.Reader {
	if e.Stdin == nil {
		return os.Stdin
	}
	return e.Stdin
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	e := &Env{
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Getenv:      os.Getenv,
		IsTTY:       ui.IsTerminal(os.Stdout),
		Width:       termWidth(os.Stdout, os.Getenv),
		LoadOptions: config.OSOptions,
	}
	os.Exit(run(ctx, e, os.Args[1:]))
}

// run parses args, dispatches, prints any error in the right format, and returns the exit code.
func run(ctx context.Context, e *Env, args []string) int {
	g, rest, err := parseGlobals(args)
	e.Globals = g
	e.Style = ui.Style{Color: ui.ColorAllowed(e.IsTTY, e.Getenv, g.NoColor)}
	e.Mark = markOptions(e)
	if err == nil {
		err = dispatch(ctx, e, rest)
	}
	return report(e, err)
}

// parseGlobals pulls the global flags out of args (anywhere before "--") and returns the rest.
func parseGlobals(args []string) (Globals, []string, error) {
	var g Globals
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || a == "-" {
			rest = append(rest, a)
			continue
		}
		takeValue := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", usageErr(fmt.Errorf("flag --%s needs a value", name), "Run deedglass help to see every flag.")
			}
			i++
			return args[i], nil
		}
		var err error
		switch {
		case name == "json" && !hasVal:
			g.JSON = true
		case name == "no-color" && !hasVal:
			g.NoColor = true
		case name == "server":
			g.Server, err = takeValue()
		case name == "api-key":
			g.APIKey, err = takeValue()
		case name == "org":
			g.Org, err = takeValue()
		case strings.HasPrefix(a, "-v") && !strings.HasPrefix(a, "--") && strings.Trim(a[1:], "v") == "":
			g.Verbose += len(a) - 1
		case name == "verbose" && !hasVal:
			g.Verbose++
		default:
			rest = append(rest, a)
		}
		if err != nil {
			return g, nil, err
		}
	}
	return g, rest, nil
}

// dispatch finds the longest matching command path and runs it.
func dispatch(ctx context.Context, e *Env, args []string) error {
	words, flags := splitPath(args)
	if len(words) == 0 {
		if hasHelpFlag(flags) {
			return printHelp(e, nil)
		}
		if bareOnly(flags) {
			return runBare(ctx, e, flags)
		}
		return usageErr(fmt.Errorf("flag %s needs a command before it", flags[0]), "Run deedglass help to see every command.")
	}
	cmd, n := lookup(words)
	if cmd == nil {
		return notFound(e, words)
	}
	extra := words[n:]
	cmdArgs := append(append([]string{}, extra...), flags...)
	if hasHelpFlag(flags) {
		return printHelp(e, cmd.Path)
	}
	if !cmd.Built() {
		return notBuilt(cmd)
	}
	if cmd.RawArgs {
		return cmd.Run(ctx, e, cmdArgs)
	}
	fs := flag.NewFlagSet("deedglass "+strings.Join(cmd.Path, " "), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if cmd.Flags != nil {
		cmd.Flags(fs)
	}
	if err := fs.Parse(cmdArgs); err != nil {
		return usageErr(fmt.Errorf("%s: %v", "deedglass "+strings.Join(cmd.Path, " "), err),
			"Run deedglass help "+strings.Join(cmd.Path, " ")+" for its flags.")
	}
	return cmd.Run(ctx, e, fs.Args())
}

// splitPath separates leading command words from everything after the first flag.
func splitPath(args []string) (words, rest []string) {
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			return words, args[i:]
		}
		words = append(words, a)
	}
	return words, nil
}

func hasHelpFlag(flags []string) bool {
	for _, f := range flags {
		if f == "--" {
			return false
		}
		if f == "-h" || f == "--help" || f == "-help" {
			return true
		}
	}
	return false
}

// lookup returns the command with the longest path that prefixes words, and its length.
func lookup(words []string) (*Command, int) {
	var best *Command
	for _, c := range commands {
		if len(c.Path) <= len(words) && equal(c.Path, words[:len(c.Path)]) && (best == nil || len(c.Path) > len(best.Path)) {
			best = c
		}
	}
	if best == nil {
		return nil, 0
	}
	return best, len(best.Path)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// notFound explains an unknown path: a family without its subcommand, a retired V0 command, or a
// typo with did-you-mean.
func notFound(e *Env, words []string) error {
	for n := min(2, len(words)); n >= 1; n-- {
		if succ, ok := retired[strings.Join(words[:n], " ")]; ok {
			return usageErr(fmt.Errorf("%q was a V0 command. Its successor is deedglass %s", strings.Join(words[:n], " "), succ),
				"See docs/NAMING.md for every renamed command.")
		}
	}
	// A family: "agent", "agent keys", or a family plus an unknown word.
	for n := len(words); n >= 1; n-- {
		if fam := family(words[:n]); len(fam) > 0 {
			name := strings.Join(words[:n], " ")
			if n == len(words) {
				return usageErr(fmt.Errorf("deedglass %s needs a subcommand", name), "Run deedglass help "+name+" to see them.")
			}
			bad := strings.Join(words[:n+1], " ")
			msg := fmt.Sprintf("Unknown command %q", bad)
			if s := suggestPath(words[:n+1], fam); s != "" {
				msg += fmt.Sprintf(". Did you mean %q?", s)
				return usageErrCode("unknown_command", errors.New(msg), "Run deedglass help "+name+" to see its subcommands.")
			}
			return usageErrCode("unknown_command", errors.New(msg+"."), "Run deedglass help "+name+" to see its subcommands.")
		}
	}
	msg := fmt.Sprintf("Unknown command %q", words[0])
	if s := suggestPath(words[:1], nil); s != "" {
		msg += fmt.Sprintf(". Did you mean %q?", s)
	} else {
		msg += "."
	}
	return usageErrCode("unknown_command", errors.New(msg), "Run deedglass help to see every command.")
}

// family returns the commands under prefix, excluding hidden ones.
func family(prefix []string) []*Command {
	var out []*Command
	for _, c := range commands {
		if len(c.Path) > len(prefix) && equal(c.Path[:len(prefix)], prefix) && !c.Hidden {
			out = append(out, c)
		}
	}
	return out
}

// suggestPath returns the closest visible command (by its first len(words) words) within edit
// distance 2.
func suggestPath(words []string, within []*Command) string {
	pool := within
	if pool == nil {
		pool = commands
	}
	target := strings.Join(words, " ")
	best, bestD := "", 3
	seen := map[string]bool{}
	for _, c := range pool {
		if c.Hidden || len(c.Path) < len(words) {
			continue
		}
		cand := strings.Join(c.Path[:len(words)], " ")
		if seen[cand] {
			continue
		}
		seen[cand] = true
		if d := levenshtein(target, cand); d < bestD {
			best, bestD = cand, d
		}
	}
	return best
}

func notBuilt(c *Command) error {
	name := "deedglass " + strings.Join(c.Path, " ")
	return &ExitError{Code: ExitUsage, ErrCode: "not_built", Plain: true,
		Err:  fmt.Errorf("%s isn't in this build yet", name),
		Next: "Run deedglass help to see what this build can do."}
}

func usageErr(err error, next string) error { return usageErrCode("usage_error", err, next) }

func usageErrCode(code string, err error, next string) error {
	return &ExitError{Code: ExitUsage, ErrCode: code, Err: err, Next: next}
}

// report prints err (text or JSON) and returns the exit code.
func report(e *Env, err error) int {
	if err == nil {
		return ExitOK
	}
	var x *ExitError
	if !errors.As(err, &x) {
		x = &ExitError{Code: ExitFailure, ErrCode: "failed", Err: err}
	}
	if x.printed {
		return x.Code
	}
	if e.Globals.JSON {
		body := map[string]any{
			"type":    "invalid_request_error",
			"code":    x.ErrCode,
			"message": sentence(x.Err.Error()) + nextSuffix(x.Next),
			"doc_url": errorDocPrefix + x.ErrCode,
		}
		if x.Code == ExitNotAuthed {
			body["type"] = "authentication_error"
		}
		for k, v := range x.Extra {
			body[k] = v
		}
		enc := json.NewEncoder(e.Stderr)
		enc.SetEscapeHTML(false)
		enc.Encode(map[string]any{"error": body})
		return x.Code
	}
	msg := sentence(x.Err.Error())
	if x.Plain {
		fmt.Fprintln(e.Stderr, msg)
		if x.Next != "" {
			fmt.Fprintln(e.Stderr, x.Next)
		}
		return x.Code
	}
	fmt.Fprintln(e.Stderr, e.Style.FailLine(msg))
	for _, d := range x.Detail {
		fmt.Fprintln(e.Stderr, "  "+d)
	}
	if x.Next != "" {
		fmt.Fprintln(e.Stderr, "  "+x.Next)
	}
	return x.Code
}

// sentence ends s with a period unless it already ends with punctuation.
func sentence(s string) string {
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}

func nextSuffix(next string) string {
	if next == "" {
		return ""
	}
	return " " + next
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func splitWords(s string) []string { return strings.Fields(s) }
