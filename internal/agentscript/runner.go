package agentscript

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Styler colours the symbols (the CLI's ui.Style). Colour only repeats what the symbol says.
type Styler interface {
	Teal(string) string
	Red(string) string
	Violet(string) string
	Amber(string) string
	Dim(string) string
	Bold(string) string
}

// Result is an action as a step sees it.
type Result struct {
	ID, Tool, Status, Decision, Rule, Message string
	Level, Verdict, Summary, Error            string
	Deed, DeedSeq, Approval                   string
	Diff                                      []string // the expected and observed lines of an L2 MISMATCH
	Raw                                       string   // the action object as JSON
}

// RunOutput is a deedglass subcommand's combined output and exit code.
type RunOutput struct {
	Text string
	Exit int
}

// Backend does the real work: the CLI wires it to the server, the binary and make.
type Backend interface {
	Call(ctx context.Context, as, tool string, args json.RawMessage) (Result, error)
	Run(ctx context.Context, as string, argv []string) (RunOutput, error)
	Make(ctx context.Context, target string) error
	// Countersign approves offline, as the named approver (dev only).
	Countersign(ctx context.Context, approval, as, reason string) error
	// WaitDecided waits for a person to decide an approval (live).
	WaitDecided(ctx context.Context, approval string, timeout time.Duration) (string, error)
	// Await waits for an action to finish.
	Await(ctx context.Context, id string) (Result, error)
}

// Options configure a run.
type Options struct {
	Live  bool
	Step  bool
	Only  string
	Root  string    // the working directory .tmp/demo/ is relative to
	Out   io.Writer // nil discards
	In    io.Reader // Enter between scenes in --step mode
	Style Styler
	Now   func() time.Time
}

// SceneResult is one scene in the summary.
type SceneResult struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Status  string   `json:"status"` // passed, skipped, failed
	Reason  string   `json:"reason,omitempty"`
	Lines   []string `json:"lines"`
	Elapsed int64    `json:"elapsed_ms"`
}

// Summary is the whole run (--json prints it).
type Summary struct {
	Object   string            `json:"object"` // agent_script_run
	Script   string            `json:"script"`
	Org      string            `json:"org"`
	Mode     string            `json:"mode"` // offline or live
	Scenes   []SceneResult     `json:"scenes"`
	Passed   int               `json:"passed"`
	Skipped  int               `json:"skipped"`
	Failed   int               `json:"failed"`
	Captured map[string]string `json:"captured"`
	Elapsed  int64             `json:"elapsed_ms"`
}

// StepError is the first unmet expectation: the run stops with exit 1.
type StepError struct {
	Scene, Step      string
	Field            string
	Expected, Actual string
	Detail           string
}

func (e *StepError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("scene %s, step %s: %s", e.Scene, e.Step, e.Detail)
	}
	return fmt.Sprintf("scene %s, step %s: expected %s %s, got %s", e.Scene, e.Step, e.Field, e.Expected, orNothing(e.Actual))
}

func orNothing(s string) string {
	if s == "" {
		return "nothing"
	}
	return s
}

type plain struct{}

func (plain) Teal(s string) string   { return s }
func (plain) Red(s string) string    { return s }
func (plain) Violet(s string) string { return s }
func (plain) Amber(s string) string  { return s }
func (plain) Dim(s string) string    { return s }
func (plain) Bold(s string) string   { return s }

// RuleWidth is the scene header's width in columns.
const RuleWidth = 62

type runner struct {
	o    Options
	b    Backend
	st   Styler
	vars map[string]string
	cur  *SceneResult
	in   *bufio.Reader
}

// Run runs the script. The error is a *StepError for an unmet expectation.
func Run(ctx context.Context, s *Script, b Backend, o Options) (*Summary, error) {
	return RunWith(ctx, s, b, o, nil)
}

// RunWith is Run with values a scene reads as ${name} before anything is captured (--var).
func RunWith(ctx context.Context, s *Script, b Backend, o Options, vars map[string]string) (*Summary, error) {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Style == nil {
		o.Style = plain{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	r := &runner{o: o, b: b, st: o.Style, vars: map[string]string{}}
	for k, v := range vars {
		r.vars[k] = v
	}
	if o.In != nil {
		r.in = bufio.NewReader(o.In)
	}
	scenes := s.Scenes
	if o.Only != "" {
		scenes = nil
		for _, sc := range s.Scenes {
			if sc.ID == o.Only {
				scenes = append(scenes, sc)
			}
		}
		if len(scenes) == 0 {
			var ids []string
			for _, sc := range s.Scenes {
				ids = append(ids, sc.ID)
			}
			return nil, fmt.Errorf("no scene %q; the scenes are %s", o.Only, strings.Join(ids, ", "))
		}
	}
	mode := "offline"
	if o.Live {
		mode = "live"
	}
	sum := &Summary{Object: "agent_script_run", Script: s.Title, Org: s.Org, Mode: mode, Captured: r.vars}
	start := o.Now()
	title := s.Title
	if title == "" {
		title = "Agent script"
	}
	r.line(fmt.Sprintf("%s · %s · %s · %d scenes", title, s.Org, mode, len(scenes)))
	var runErr error
	for i, sc := range scenes {
		if o.Step && i > 0 {
			fmt.Fprint(o.Out, r.st.Dim(fmt.Sprintf("Press Enter for scene %d · %s", indexOf(s, sc.ID), sc.Title)))
			if r.in != nil {
				_, _ = r.in.ReadString('\n')
			}
			fmt.Fprintln(o.Out)
		}
		res := SceneResult{ID: sc.ID, Title: sc.Title, Lines: []string{}}
		r.cur = &res
		t0 := o.Now()
		head := fmt.Sprintf("── %d/%d %s ", indexOf(s, sc.ID), len(s.Scenes), sc.Title)
		if n := RuleWidth - utf8.RuneCountInString(head); n > 0 {
			head += strings.Repeat("─", n)
		}
		fmt.Fprintln(o.Out, head)
		if o.Step {
			for _, l := range sc.Say {
				fmt.Fprintln(o.Out, r.st.Dim(l))
			}
		}
		if why := r.unmet(sc.Needs); why != "" {
			res.Status, res.Reason = "skipped", why
			r.line(r.st.Amber("○") + " skipped · " + why)
			sum.Skipped++
		} else {
			res.Status = "passed"
			for j, stp := range sc.Steps {
				if err := r.step(ctx, sc, j, stp); err != nil {
					res.Status = "failed"
					var se *StepError
					if errors.As(err, &se) {
						r.line(r.st.Red("✕") + " " + se.Error())
					} else {
						r.line(r.st.Red("✕") + " " + fmt.Sprintf("scene %s, step %d: %v", sc.ID, j+1, err))
					}
					runErr = err
					break
				}
			}
			if res.Status == "passed" {
				sum.Passed++
			} else {
				sum.Failed++
			}
		}
		res.Elapsed = o.Now().Sub(t0).Milliseconds()
		sum.Scenes = append(sum.Scenes, res)
		if runErr != nil {
			break
		}
	}
	sum.Elapsed = o.Now().Sub(start).Milliseconds()
	if runErr != nil {
		r.line(fmt.Sprintf("Stopped · %d passed · %d failed · %s", sum.Passed, sum.Failed, Duration(o.Now().Sub(start))))
		return sum, runErr
	}
	done := fmt.Sprintf("%s complete · %d scenes", firstWord(title), sum.Passed)
	if sum.Skipped > 0 {
		done += fmt.Sprintf(" · %d skipped", sum.Skipped)
	}
	r.line(done + " · " + Duration(o.Now().Sub(start)))
	return sum, nil
}

func firstWord(title string) string {
	f := strings.Fields(title)
	if len(f) >= 2 && f[0] == "Deedglass" {
		return strings.ToUpper(f[1][:1]) + f[1][1:]
	}
	if len(f) > 0 {
		return f[0]
	}
	return "Script"
}

func indexOf(s *Script, id string) int {
	for i, sc := range s.Scenes {
		if sc.ID == id {
			return i + 1
		}
	}
	return 0
}

// Duration is "3 m 12 s", or "41 s" under a minute.
func Duration(d time.Duration) string {
	sec := int(d.Round(time.Second) / time.Second)
	if sec < 60 {
		return fmt.Sprintf("%d s", sec)
	}
	return fmt.Sprintf("%d m %02d s", sec/60, sec%60)
}

func (r *runner) line(s string) {
	fmt.Fprintln(r.o.Out, s)
	if r.cur != nil {
		r.cur.Lines = append(r.cur.Lines, s)
	}
}

func (r *runner) unmet(needs []string) string {
	if r.o.Live {
		return ""
	}
	for _, n := range needs {
		switch n {
		case "stripe":
			return "needs Stripe test mode · run make demo-live"
		case "slack":
			return "needs Slack · run make demo-live"
		case "live":
			return "needs a person on the phone · run make demo-live"
		}
	}
	return ""
}

func (r *runner) step(ctx context.Context, sc Scene, j int, st Step) error {
	fail := func(field, want, got string) error {
		return &StepError{Scene: sc.ID, Step: fmt.Sprint(j + 1), Field: field, Expected: want, Actual: got}
	}
	bad := func(format string, a ...any) error {
		return &StepError{Scene: sc.ID, Step: fmt.Sprint(j + 1), Detail: fmt.Sprintf(format, a...)}
	}
	as := st.As
	if as == "" {
		as = "agent"
	}
	switch st.Kind() {
	case "make":
		if err := r.b.Make(ctx, st.Make); err != nil {
			return bad("make %s failed: %v", st.Make, err)
		}
	case "call":
		args, err := r.expandArgs(st.Call.Args)
		if err != nil {
			return bad("%v", err)
		}
		res, err := r.b.Call(ctx, as, st.Call.Tool, args)
		if err != nil {
			return bad("the call didn't reach the Gate: %v", err)
		}
		r.callLines(res)
		if err := checkResult(st.Expect, res, fail); err != nil {
			return err
		}
		for k, v := range st.Capture {
			got := map[string]string{"id": res.ID, "deed": res.Deed, "approval": res.Approval}[v]
			if got == "" {
				return bad("capture %s: the action has no %s", k, v)
			}
			r.vars[k] = got
		}
	case "run":
		argv := make([]string, len(st.Run))
		for i, a := range st.Run {
			x, err := expand(a, r.vars)
			if err != nil {
				return bad("%v", err)
			}
			argv[i] = x
		}
		out, err := r.b.Run(ctx, as, argv)
		if err != nil {
			return bad("deedglass %s didn't run: %v", strings.Join(argv[:min(2, len(argv))], " "), err)
		}
		lines := nonEmpty(out.Text)
		switch st.Show {
		case "", "first":
			if len(lines) > 0 {
				r.line(lines[0])
			}
		case "all":
			for _, l := range lines {
				r.line(l)
			}
		}
		want := 0
		if st.Expect.Exit != nil {
			want = *st.Expect.Exit
		}
		if out.Exit != want {
			first := ""
			if len(lines) > 0 {
				first = " (" + lines[0] + ")"
			}
			return fail("exit code", fmt.Sprint(want), fmt.Sprint(out.Exit)+first)
		}
		if st.Expect.Output != "" && !strings.Contains(out.Text, st.Expect.Output) {
			return fail("output containing", fmt.Sprintf("%q", st.Expect.Output), firstLine(lines))
		}
		for k, pat := range st.Capture {
			re, err := regexp.Compile(pat)
			if err != nil {
				return bad("capture %s: %v", k, err)
			}
			m := re.FindStringSubmatch(out.Text)
			if m == nil {
				return bad("capture %s: %q isn't in the output", k, pat)
			}
			r.vars[k] = m[len(m)-1]
		}
	case "approve":
		a := st.Approve
		apr, err := expand(a.Approval, r.vars)
		if err != nil {
			return bad("%v", err)
		}
		if r.o.Live {
			r.line(r.st.Violet("◐") + " waiting for a person · countersign on your phone or in Slack (2 minutes)")
			who, err := r.b.WaitDecided(ctx, apr, 2*time.Minute)
			if err != nil {
				return bad("no one countersigned: %v", err)
			}
			r.line(r.st.Teal("✓") + " countersigned by " + orNothing(who))
		} else {
			if err := r.b.Countersign(ctx, apr, a.As, a.Reason); err != nil {
				return bad("deedglass approve --as %s failed: %v", a.As, err)
			}
			r.line(r.st.Teal("✓") + " countersigned by " + a.As)
		}
		if a.Action != "" {
			id, err := expand(a.Action, r.vars)
			if err != nil {
				return bad("%v", err)
			}
			res, err := r.b.Await(ctx, id)
			if err != nil {
				return bad("the approved action didn't finish: %v", err)
			}
			r.executedLine(res)
			if err := checkResult(st.Expect, res, fail); err != nil {
				return err
			}
		}
	case "tamper":
		deed, err := expand(st.Tamper.Deed, r.vars)
		if err != nil {
			return bad("%v", err)
		}
		seq, err := TamperFile(r.o.Root, st.Tamper.From, st.Tamper.To, deed)
		if err != nil {
			return bad("%v", err)
		}
		r.line(r.st.Dim(fmt.Sprintf("· one byte changed in deed %s · %s", seq, st.Tamper.To)))
	}
	if st.Print != "" {
		p, err := expand(st.Print, r.vars)
		if err != nil {
			return bad("%v", err)
		}
		r.line(r.symbolize(p))
	}
	return nil
}

// symbolize colours a printed line's leading brand symbol.
func (r *runner) symbolize(s string) string {
	for sym, f := range map[string]func(string) string{"✓": r.st.Teal, "✕": r.st.Red, "◐": r.st.Violet, "○": r.st.Amber} {
		if strings.HasPrefix(s, sym) {
			return f(sym) + s[len(sym):]
		}
	}
	return s
}

func (r *runner) expandArgs(args map[string]any) (json.RawMessage, error) {
	if args == nil {
		return json.RawMessage(`{}`), nil
	}
	var walk func(v any) (any, error)
	walk = func(v any) (any, error) {
		switch x := v.(type) {
		case string:
			return expand(x, r.vars)
		case map[string]any:
			out := map[string]any{}
			for k, e := range x {
				y, err := walk(e)
				if err != nil {
					return nil, err
				}
				out[k] = y
			}
			return out, nil
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				y, err := walk(e)
				if err != nil {
					return nil, err
				}
				out[i] = y
			}
			return out, nil
		}
		return v, nil
	}
	v, err := walk(map[string]any(args))
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func (r *runner) callLines(res Result) {
	switch res.Status {
	case "executed":
		if res.Verdict == "MISMATCH" {
			r.line(r.st.Red("✕") + " " + res.Level + " · " + r.st.Red("MISMATCH") + " · " + res.Summary)
			for _, d := range res.Diff {
				r.line("    " + d)
			}
			return
		}
		line := r.verdictSym(res.Verdict) + " allowed · " + res.Tool + " · " + res.Level + " · " + res.Verdict
		if res.DeedSeq != "" {
			line += " · deed " + res.DeedSeq
		}
		r.line(line)
	case "denied":
		r.line(r.st.Red("✕") + " denied · " + res.Tool + " · " + res.Message)
	case "pending_approval":
		r.line(r.st.Violet("◐") + " pending approval · " + res.Tool + " · " + res.Message)
	case "failed":
		r.line(r.st.Red("✕") + " failed · " + res.Tool + " · " + res.Error)
	default:
		r.line(r.st.Violet("◐") + " " + strings.ReplaceAll(res.Status, "_", " ") + " · " + res.Tool)
	}
}

func (r *runner) executedLine(res Result) {
	if res.Status != "executed" {
		r.callLines(res)
		return
	}
	line := r.verdictSym(res.Verdict) + " executed · " + res.Tool + " · " + res.Level + " · " + res.Verdict
	if res.Summary != "" {
		line += " · " + res.Summary
	}
	r.line(line)
}

func (r *runner) verdictSym(v string) string {
	switch v {
	case "VERIFIED":
		return r.st.Teal("✓")
	case "MISMATCH":
		return r.st.Red("✕")
	}
	return r.st.Amber("○")
}

func checkResult(x Expect, res Result, fail func(field, want, got string) error) error {
	for _, c := range []struct{ field, want, got string }{
		{"status", x.Status, res.Status}, {"decision", x.Decision, res.Decision}, {"rule", x.Rule, res.Rule},
		{"level", x.Level, res.Level}, {"verdict", x.Verdict, res.Verdict}, {"error", x.Error, res.Error},
	} {
		if c.want != "" && c.want != c.got {
			return fail(c.field, c.want, c.got)
		}
	}
	if x.Output != "" {
		hay := res.Raw + "\n" + res.Summary + "\n" + res.Message + "\n" + strings.Join(res.Diff, "\n")
		if !strings.Contains(hay, x.Output) {
			return fail("output containing", fmt.Sprintf("%q", x.Output), res.Summary)
		}
	}
	return nil
}

func nonEmpty(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, " "))
		}
	}
	return out
}

func firstLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

var (
	reLatency = regexp.MustCompile(`"latency_ms":(\d+)`)
	reSeq     = regexp.MustCompile(`"seq":(\d+)`)
)

// TamperFile copies from to to (both under root/.tmp/demo/) with exactly one byte changed: the
// last digit of execution.latency_ms in the deed whose deed_id is deed. It returns that deed's seq.
// The source file is only read.
func TamperFile(root, from, to, deed string) (string, error) {
	for _, p := range []string{from, to} {
		if err := checkDemoPath(p); err != nil {
			return "", err
		}
	}
	src := filepath.Join(root, filepath.FromSlash(from))
	b, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("tamper: %w", err)
	}
	lines := strings.SplitAfter(string(b), "\n")
	key := `"deed_id":"` + deed + `"`
	for i, l := range lines {
		if !strings.Contains(l, key) {
			continue
		}
		loc := reLatency.FindStringSubmatchIndex(l)
		if loc == nil {
			return "", fmt.Errorf("tamper: deed %s records no execution.latency_ms", deed)
		}
		end := loc[3] - 1 // the number's last digit
		d := l[end]
		nd := byte('0' + (int(d-'0')+1)%10)
		lines[i] = l[:end] + string(nd) + l[end+1:]
		seq := ""
		if m := reSeq.FindStringSubmatch(l); m != nil {
			seq = m[1]
		}
		dst := filepath.Join(root, filepath.FromSlash(to))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, []byte(strings.Join(lines, "")), 0o644); err != nil {
			return "", fmt.Errorf("tamper: %w", err)
		}
		return seq, nil
	}
	return "", fmt.Errorf("tamper: no deed %s in %s", deed, from)
}
