// Package play is `deedglass play`: the 301 scene in a terminal, offline, with no Docker.
//
// It never pretends. Each line says where it comes from:
//
//   - live: computed now, on this machine, by the product's own code. The policy decisions come
//     from policy.Evaluate on the Acme demo policy (examples/demo/policies/acme-demo.yaml), the same
//     engine `deedglass serve` runs. The records are sealed with a key made for this run and then
//     checked by deed.VerifyStream, the code behind `deedglass verify`; one byte is changed and the
//     check fails.
//   - replay: lines a real run printed. Postgres's change log can't run without Postgres, so the
//     witness lines are replayed from hero.cast.json, a byte-for-byte copy of
//     docs/assets/demo-hero.cast.json (a recorded `make demo-hero`, with its commit and time), and
//     the undo lines from examples/demo/testdata/launch.golden (`make demo`'s compared output).
//     Tests keep both copies equal to their sources.
package play

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	demopolicies "github.com/deedglass/deedglass/examples/demo/policies"
	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/embedgz"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// hero.cast.json is embedded gzip-compressed (cast.tar.gz, make embed-gz; the image budget).
//
//go:embed cast.tar.gz
var castGz []byte

var castJSON = []byte(embedgz.MustString(embedgz.NewFiles(castGz), "hero.cast.json")())

// Cast is a recorded terminal run (the deedglass_cast object the growth capture tool writes).
type Cast struct {
	Object     string     `json:"object"`
	Command    string     `json:"command"`
	Commit     string     `json:"commit"`
	RecordedAt string     `json:"recorded_at"`
	LogSHA256  string     `json:"log_sha256"`
	Lines      []CastLine `json:"lines"`
}

// CastLine is one printed line and the millisecond it appeared.
type CastLine struct {
	T    int64  `json:"t"`
	Text string `json:"text"`
}

// LoadCast parses the embedded recording.
func LoadCast() (*Cast, error) {
	var c Cast
	if err := json.Unmarshal(castJSON, &c); err != nil {
		return nil, fmt.Errorf("play: the recording doesn't parse: %w", err)
	}
	if c.Object != "deedglass_cast" || len(c.Lines) == 0 {
		return nil, errors.New("play: the recording is empty")
	}
	return &c, nil
}

// UndoReplay is scene 5 of examples/demo/testdata/launch.golden, verbatim (TestUndoReplayInGolden).
var UndoReplay = []string{
	"✓ Undo available · 301 rows in public.orders · 301 updates",
	"◐ pending approval · postgres.undo · a person countersigns first",
	"✓ countersigned by ops@example.test",
	"✓ executed · postgres.undo · L2 · VERIFIED · Restored 301 rows in public.orders exactly as planned.",
}

// Options tune a run.
type Options struct {
	Out   io.Writer
	Style ui.Style // the zero value prints plain text
	// Width is the terminal's columns (0: unknown); the scenes are laid out at most ui.MaxWidth wide.
	Width int
	// Verbose also prints, under each replayed scene, the recorded lines exactly as they printed.
	Verbose bool
	// Pace is called before each line with the pause the line asks for; nil means no pauses
	// (tests, --fast, and output that isn't a terminal).
	Pace func(ctx context.Context, d time.Duration) error
	Now  func() time.Time // nil: time.Now
	// Cmd is how the closing commands name the CLI: "deedglass" (the default) or "npx deedglass".
	Cmd string
	// Tail, when set, replaces the closing block (deedglass start prints its own).
	Tail []string
}

// Result is what a run found, for tests and --json.
type Result struct {
	Decisions  map[string]Decision `json:"decisions"`
	Records    int                 `json:"records"`
	ChainOK    bool                `json:"chain_intact"`
	TamperSeen bool                `json:"tamper_detected"`
	TamperSeq  int64               `json:"tamper_seq"`
	Checks     map[string]string   `json:"record_checks"` // the level and result in each sealed record, from the replay
	Recording  string              `json:"recording"`
}

// Decision is one live policy answer.
type Decision struct {
	Outcome string `json:"decision"`
	Rule    string `json:"rule_id"`
	Message string `json:"message,omitempty"`
	Verify  string `json:"verify,omitempty"`
}

const (
	sqlRead  = "SELECT id, status FROM public.orders WHERE id = 42"
	sqlDrop  = "DROP TABLE public.refunds"
	sqlShip  = "UPDATE public.orders SET status = 'shipped' WHERE id = 42"
	agent    = "claude-code"
	tagLive  = "live"
	tagRerun = "replay"
	// A scene's body sits under its title: "4/6  The trigger" (CLI_STYLE.md › play).
	bodyIndent = "     "
	keyWidth   = 7
	diffRows   = 3 // diff rows a scene shows; the rest are counted
)

type run struct {
	ctx      context.Context
	o        Options
	st       ui.Style
	res      *Result
	lastShip string // the replayed run's verdict line for the trigger scene
}

func (r *run) pause(d time.Duration) error {
	if r.o.Pace == nil || d <= 0 {
		return r.ctx.Err()
	}
	return r.o.Pace(r.ctx, d)
}

func (r *run) line(d time.Duration, s string) error {
	if err := r.pause(d); err != nil {
		return err
	}
	_, err := fmt.Fprintln(r.o.Out, s)
	return err
}

// row prints one key/value row of a scene (and its wrapped lines), after a pause of d.
func (r *run) row(d time.Duration, key, glyph, value, tag string) error {
	for _, l := range r.st.KV(bodyIndent, r.o.Width, ui.Row{Key: ui.Pad(key, keyWidth), Glyph: glyph, Value: value, Tag: tag}) {
		if err := r.line(d, l); err != nil {
			return err
		}
		d = 0
	}
	return nil
}

func (r *run) scene(n, of int, title, intro string) error {
	if err := r.line(700*time.Millisecond, ""); err != nil {
		return err
	}
	if err := r.line(0, r.st.Dim(fmt.Sprintf("%d/%d", n, of))+"  "+r.st.Bold(title)); err != nil {
		return err
	}
	if intro == "" {
		return nil
	}
	for _, l := range ui.Wrap(bodyIndent+intro, ui.LayoutWidth(r.o.Width), 0) {
		if err := r.line(200*time.Millisecond, l); err != nil {
			return err
		}
	}
	return nil
}

// Run plays the scene and reports what it found. It reads no file, opens no socket and writes
// only to o.Out.
func Run(ctx context.Context, o Options) (*Result, error) {
	if o.Out == nil {
		return nil, errors.New("play: no output")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	cast, err := LoadCast()
	if err != nil {
		return nil, err
	}
	pol, err := compileDemo()
	if err != nil {
		return nil, err
	}
	r := &run{ctx: ctx, o: o, st: o.Style, res: &Result{Decisions: map[string]Decision{}, Checks: map[string]string{}}}
	day := cast.RecordedAt
	if len(day) >= 10 {
		day = day[:10]
	}
	r.res.Recording = fmt.Sprintf("%s · commit %s · %s", cast.Command, cast.Commit, day)
	st := r.st
	if err := r.line(60*time.Millisecond, st.Header("Deedglass play", "offline", "nothing leaves this machine")); err != nil {
		return r.res, err
	}
	if err := r.line(60*time.Millisecond, ""); err != nil {
		return r.res, err
	}
	legend := st.KV("  ", o.Width,
		ui.Row{Key: tagLive, Value: "computed here, by the product's own policy engine and verifier"},
		ui.Row{Key: tagRerun, Value: fmt.Sprintf("printed by a recorded run of %s (%s, %s)\nPostgres can't run inside this binary, so its lines are replayed", cast.Command, cast.Commit, day)},
	)
	for _, l := range legend {
		if err := r.line(60*time.Millisecond, l); err != nil {
			return r.res, err
		}
	}
	const scenes = 6

	// 1. A safe read.
	if err := r.scene(1, scenes, "A safe read", ""); err != nil {
		return r.res, err
	}
	if _, err := r.decide(pol, "read", "postgres.query", sqlRead); err != nil {
		return r.res, err
	}

	// 2. A destructive request.
	if err := r.scene(2, scenes, "A destructive request", ""); err != nil {
		return r.res, err
	}
	if _, err := r.decide(pol, "drop", "postgres.execute", sqlDrop); err != nil {
		return r.res, err
	}
	if err := r.row(300*time.Millisecond, "result", "", "Nothing reached the database.", ""); err != nil {
		return r.res, err
	}

	// 3. A normal write: the rule decides live, Postgres's answer is replayed.
	if err := r.scene(3, scenes, "A normal write", ""); err != nil {
		return r.res, err
	}
	sec3, err := section(cast, "1. ", "2. ")
	if err != nil {
		return r.res, err
	}
	if err := r.before(sec3); err != nil {
		return r.res, err
	}
	if _, err := r.decide(pol, "ship", "postgres.execute", sqlShip); err != nil {
		return r.res, err
	}
	if err := r.witness(sec3); err != nil {
		return r.res, err
	}

	// 4. The trigger.
	if err := r.scene(4, scenes, "The trigger", "A legacy trigger on public.orders is on. The agent sends the same request."); err != nil {
		return r.res, err
	}
	sec4, err := section(cast, "2. ", "3. ")
	if err != nil {
		return r.res, err
	}
	if err := r.before(sec4); err != nil {
		return r.res, err
	}
	if _, err := r.decide(pol, "ship", "postgres.execute", sqlShip); err != nil {
		return r.res, err
	}
	if err := r.witness(sec4); err != nil {
		return r.res, err
	}

	// 5. Put it back.
	if err := r.scene(5, scenes, "Put it back", ""); err != nil {
		return r.res, err
	}
	if _, err := r.decideUndo(pol); err != nil {
		return r.res, err
	}
	if err := r.undo(); err != nil {
		return r.res, err
	}

	// 6. Records, live.
	if err := r.scene(6, scenes, "Check every record", ""); err != nil {
		return r.res, err
	}
	if err := r.records(); err != nil {
		return r.res, err
	}

	tail := o.Tail
	if tail == nil {
		cmd := o.Cmd
		if cmd == "" {
			cmd = "deedglass"
		}
		tail = []string{"", st.Status(ui.OK, st.Bold("Play complete.")+" Each row said where it came from."), "", st.Section("Try it on your own setup")}
		tail = append(tail, st.Commands("  ", o.Width,
			[2]string{cmd + " start", "this scene, live, with Postgres in Docker"},
			[2]string{cmd + " protect", "guard Claude Code, Cursor and other coding agents"},
			[2]string{cmd + " scan", "grade what your agent setup can change"},
			[2]string{"https://deedglass.com/play", "the same scene in your browser"},
		)...)
	}
	for _, l := range tail {
		if err := r.line(80*time.Millisecond, l); err != nil {
			return r.res, err
		}
	}
	return r.res, nil
}

func compileDemo() (*policy.Compiled, error) {
	c, diags := policy.Compile(demopolicies.AcmeDemo, policy.Options{})
	for _, d := range diags {
		if d.Severity == policy.SevError {
			return nil, fmt.Errorf("play: the demo policy doesn't compile (line %d: %s)", d.Line, d.Message)
		}
	}
	if c == nil {
		return nil, errors.New("play: the demo policy doesn't compile")
	}
	return c, nil
}

func baseInput(tool, op, side string, args json.RawMessage, now time.Time) policy.Input {
	return policy.Input{V: policy.InputVersion, Org: policy.OrgInput{OrgFacts: policy.OrgFacts{ID: "org_play", Timezone: "UTC"}},
		Agent: policy.AgentInput{AgentFacts: policy.AgentFacts{Name: agent, Status: policy.StatusActive}},
		Tool: policy.ToolInput{ToolFacts: policy.ToolFacts{Name: tool, Connector: "postgres", ConnectorType: "postgres",
			SideEffect: side, Type: "postgres", Op: op}, DefaultSchema: "public"},
		Args: args, Time: now}
}

func (r *run) evaluate(c *policy.Compiled, key string, in policy.Input) Decision {
	d := policy.Evaluate(r.ctx, c, in, policy.EvalOptions{})
	out := Decision{Outcome: string(d.Outcome), Rule: d.RuleID, Message: d.Message, Verify: string(d.Verify)}
	r.res.Decisions[key] = out
	return out
}

func (r *run) decide(c *policy.Compiled, key, tool, sql string) (Decision, error) {
	if err := r.row(250*time.Millisecond, "request", "", sql, ""); err != nil {
		return Decision{}, err
	}
	args, _ := json.Marshal(map[string]string{"sql": sql})
	side := "write"
	if tool == "postgres.query" {
		side = "read"
	}
	d := r.evaluate(c, key, baseInput(tool, strings.TrimPrefix(tool, "postgres."), side, args, r.o.Now()))
	return d, r.decision(d)
}

func (r *run) decideUndo(c *policy.Compiled) (Decision, error) {
	if err := r.row(250*time.Millisecond, "request", "", "postgres.undo: put back the rows that action changed", ""); err != nil {
		return Decision{}, err
	}
	d := r.evaluate(c, "undo", baseInput("postgres.undo", "undo", "write", json.RawMessage(`{}`), r.o.Now()))
	return d, r.decision(d)
}

// decision prints the rule row: what the live policy engine said, and why.
func (r *run) decision(d Decision) error {
	glyph, text := ui.Bullet, d.Outcome
	switch d.Outcome {
	case string(policy.Allow):
		glyph, text = ui.OK, "allowed by "+d.Rule
		if d.Verify != "" {
			text += ", checked at " + strings.TrimSuffix(strings.SplitN(levelName(d.Verify), " (", 2)[0], " ")
		}
	case string(policy.Deny):
		glyph, text = ui.Fail, "denied by "+d.Rule+"\n"+reason(d.Message)
	case string(policy.RequireApproval):
		glyph, text = ui.Pending, "waits for a person: "+d.Rule+"\n"+reason(d.Message)
	default:
		text += "\n" + reason(d.Message)
	}
	return r.row(350*time.Millisecond, "rule", glyph, text, tagLive)
}

// reason is a policy message without the "Blocked by policy "x":" lead the rule row already says.
func reason(m string) string {
	if _, after, ok := strings.Cut(m, `": `); ok && strings.Contains(m[:strings.Index(m, `": `)], `"`) {
		return after
	}
	return m
}

func levelName(l string) string {
	switch l {
	case "L0":
		return "Claimed (L0)"
	case "L1":
		return "Checked (L1)"
	case "L2":
		return "Proven (L2)"
	}
	return l
}

// section is the recorded lines from the one starting with `from` (excluded) up to the one starting
// with `until` (excluded).
func section(c *Cast, from, until string) ([]CastLine, error) {
	start, end := -1, len(c.Lines)
	for i, l := range c.Lines {
		if start < 0 && strings.HasPrefix(l.Text, from) {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(l.Text, until) {
			end = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("play: the recording has no section %q", from)
	}
	return c.Lines[start+1 : end], nil
}

var (
	reOpen    = regexp.MustCompile(`(\d+) open(?: orders)? besides 42`)
	reOrder42 = regexp.MustCompile(`[Oo]rder 42 (?:· )?(\w+)`)
	reCheck   = regexp.MustCompile(`^\s*check\s+(Claimed|Checked|Proven) · ([^·]+?) · (.+)$`)
	reRow     = regexp.MustCompile(`^\s*([✓✕]) (expected|unexpected)\s+(\d+)\s+(.+?)\s*$`)
	reOfRows  = regexp.MustCompile(`first \d+ of (\d+) rows`)
	reAction  = regexp.MustCompile(`\b(act_[0-9a-z]{26})\b`)
	reRowsN   = regexp.MustCompile(`· (\d+) rows ·`)
)

// find returns the first recorded line matching re, and its submatches.
func find(lines []CastLine, re *regexp.Regexp) (string, []string) {
	for _, l := range lines {
		if m := re.FindStringSubmatch(l.Text); m != nil {
			return l.Text, m
		}
	}
	return "", nil
}

// before is the scene's starting state, read from the recorded run's setup lines.
func (r *run) before(lines []CastLine) error {
	_, open := find(lines, reOpen)
	_, order := find(lines, reOrder42)
	if open == nil || order == nil {
		return errors.New("play: the recording doesn't say where order 42 started")
	}
	trigger := "trigger off"
	for _, l := range lines {
		if strings.HasPrefix(l.Text, "✓ Trigger on") {
			trigger = "trigger on"
		}
	}
	return r.row(250*time.Millisecond, "before", "", fmt.Sprintf("order 42 %s, %s others open, %s", order[1], open[1], trigger), tagRerun)
}

// witness prints what Postgres's change log said, from the recorded run: the check, the diff rows
// when it didn't match, and the result. With Verbose, the recorded lines follow as they printed.
func (r *run) witness(lines []CastLine) error {
	_, chk := find(lines, reCheck)
	if chk == nil {
		return errors.New("play: the recording has no check line")
	}
	verdictLine := ""
	for _, l := range lines {
		if reVerdict.MatchString(l.Text) && reAction.MatchString(l.Text) && strings.HasPrefix(l.Text, "✓ L") {
			verdictLine = l.Text
		}
	}
	if verdictLine == "" {
		return errors.New("play: the recording has no verdict line")
	}
	if strings.Contains(verdictLine, " rows · act_") {
		r.lastShip = verdictLine
	}
	glyph := ui.OK
	if chk[2] != "Matches" {
		glyph = ui.Fail
	}
	if err := r.row(300*time.Millisecond, "check", glyph, chk[2]+": "+chk[3], tagRerun); err != nil {
		return err
	}
	shown, total := 0, 0
	if _, m := find(lines, reOfRows); m != nil {
		total, _ = strconv.Atoi(m[1])
	}
	for _, l := range lines {
		m := reRow.FindStringSubmatch(l.Text)
		if m == nil || shown >= diffRows {
			continue
		}
		shown++
		if err := r.row(150*time.Millisecond, "", m[1], fmt.Sprintf("id %-4s %s", m[3], m[4]), ""); err != nil {
			return err
		}
	}
	if total > shown && shown > 0 {
		if err := r.row(0, "", "", r.st.Dim(fmt.Sprintf("%s %d more rows", ui.More, total-shown)), ""); err != nil {
			return err
		}
	}
	vm := reVerdict.FindStringSubmatch(verdictLine)
	act := reAction.FindString(verdictLine)
	res := strings.ToUpper(vm[2][:1]) + strings.ToLower(vm[2][1:]) + " at " + levelName(vm[1])
	if n := reRowsN.FindStringSubmatch(verdictLine); n != nil {
		res += ", " + n[1] + " rows"
	}
	rglyph := ui.OK
	if vm[2] != "VERIFIED" {
		rglyph = ui.Fail
	}
	if err := r.row(300*time.Millisecond, "result", rglyph, res+" "+ui.Sep+" "+ui.ShortID(act), tagRerun); err != nil {
		return err
	}
	return r.verbatim(lines)
}

// undo prints the recorded launch demo's undo (UndoReplay): the plan, the person, the result.
func (r *run) undo() error {
	plan := strings.TrimPrefix(UndoReplay[0], "✓ Undo available · ")
	person := strings.TrimPrefix(UndoReplay[2], "✓ ")
	_, done, ok := strings.Cut(UndoReplay[3], "VERIFIED · ")
	if plan == UndoReplay[0] || person == UndoReplay[2] || !ok {
		return errors.New("play: the undo lines changed shape")
	}
	if err := r.row(400*time.Millisecond, "plan", ui.OK, strings.ReplaceAll(plan, " · ", ", "), tagRerun); err != nil {
		return err
	}
	if err := r.row(500*time.Millisecond, "person", ui.OK, person, tagRerun); err != nil {
		return err
	}
	if err := r.row(500*time.Millisecond, "result", ui.OK, done, tagRerun); err != nil {
		return err
	}
	lines := make([]CastLine, len(UndoReplay))
	for i, l := range UndoReplay {
		lines[i] = CastLine{Text: l}
	}
	return r.verbatim(lines)
}

// verbatim prints, with Verbose, the recorded lines exactly as they printed, under a dim "│".
func (r *run) verbatim(lines []CastLine) error {
	if !r.o.Verbose {
		return nil
	}
	if err := r.line(0, bodyIndent+r.st.Dim("recorded output ("+r.res.Recording+")")); err != nil {
		return err
	}
	for _, l := range lines {
		if strings.TrimSpace(l.Text) == "" {
			continue
		}
		if err := r.line(0, bodyIndent+r.st.Dim("│")+" "+l.Text); err != nil {
			return err
		}
	}
	return nil
}

// reVerdict finds "L2 · <VERDICT>" in a recorded line.
var reVerdict = regexp.MustCompile(`\b(L[012]) · ([A-Z]{6,12})\b`)

// recordedWitness reads the level and verdict a recorded line printed.
func recordedWitness(line string) (*deed.WitnessSection, error) {
	m := reVerdict.FindStringSubmatch(line)
	if m == nil {
		return nil, fmt.Errorf("play: no level and verdict in the recorded line %q", line)
	}
	return &deed.WitnessSection{Level: m[1], Verdict: m[2], Method: "postgres.pgoutput"}, nil
}

// --- records ---

type sealedLine struct {
	seq  int64
	line []byte
}

func (r *run) records() error {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return fmt.Errorf("play: couldn't make a key: %w", err)
	}
	keyID, err := ids.NewString("sk")
	if err != nil {
		return err
	}
	signer, err := deed.NewFileSigner(keyID, seed)
	if err != nil {
		return fmt.Errorf("play: couldn't make a key: %w", err)
	}
	org, err := ids.NewString("org")
	if err != nil {
		return err
	}
	agt, err := ids.NewString("agt")
	if err != nil {
		return err
	}
	now := r.o.Now().UTC().Truncate(time.Millisecond)
	type plan struct {
		key, tool, sql string
		witness        *deed.WitnessSection
	}
	// The Postgres records carry the level and verdict the replayed run printed, read from its
	// lines, never written here: play decides no verdict. The read and the DROP were not witnessed
	// in this run, so their records have none.
	shipW, err := recordedWitness(r.lastShip)
	if err != nil {
		return err
	}
	undoW, err := recordedWitness(UndoReplay[len(UndoReplay)-1])
	if err != nil {
		return err
	}
	plans := []plan{
		{"read", "postgres.query", sqlRead, nil},
		{"drop", "postgres.execute", sqlDrop, nil},
		{"ship", "postgres.execute", sqlShip, shipW},
		{"undo", "postgres.undo", "", undoW},
	}
	prev := deed.Genesis(org)
	var lines []sealedLine
	for i, p := range plans {
		d := r.res.Decisions[p.key]
		act, err := ids.NewString("act")
		if err != nil {
			return err
		}
		args := map[string]string{}
		if p.sql != "" {
			args["sql"] = p.sql
		}
		dig, err := deed.DigestValue(args)
		if err != nil {
			return err
		}
		snap := deed.ActionSnapshot{Org: org, Agent: deed.AgentRef{ID: agt, Name: agent}, ActionID: act, Tool: p.tool, Mode: "gateway",
			RequestDigest: dig, Policy: deed.PolicySection{Mode: "enforce", Decision: d.Outcome}}
		if d.Rule != "" {
			rule := d.Rule
			snap.Policy.RuleID = &rule
		}
		at := now.Add(time.Duration(i) * time.Second)
		if d.Outcome != string(policy.Deny) {
			snap.Execution = &deed.ExecutionSnapshot{Status: "succeeded", StartedAt: at, FinishedAt: at.Add(17 * time.Millisecond)}
			snap.Witness = p.witness
			if p.witness != nil {
				r.res.Checks[p.key] = p.witness.Level + " " + p.witness.Verdict
			}
		}
		if d.Outcome == string(policy.RequireApproval) {
			apr, err := ids.NewString("apr")
			if err != nil {
				return err
			}
			usr, err := ids.NewString("usr")
			if err != nil {
				return err
			}
			snap.Approval = &deed.ApprovalSnapshot{ID: apr, Outcome: "approved", DecidedBy: []string{usr}, DecidedAt: &at}
		}
		dr, err := deed.ForAction(snap)
		if err != nil {
			return fmt.Errorf("play: record %d: %w", i+1, err)
		}
		b := dr.Body
		b.DeedID, err = ids.NewString("deed")
		if err != nil {
			return err
		}
		b.Seq = int64(i + 1)
		b.PrevHash = prev
		b.IssuedAt = deed.FormatTime(at.Add(20 * time.Millisecond))
		b.SealKeyID = keyID
		env, err := deed.SealBody(signer, b)
		if err != nil {
			return fmt.Errorf("play: sealing record %d: %w", i+1, err)
		}
		line, err := env.MarshalCanonical()
		if err != nil {
			return err
		}
		lines = append(lines, sealedLine{seq: b.Seq, line: line})
		prev = env.BodyHash
	}
	ks, err := deed.NewKeySet("deedglass-play", []deed.KeyRecord{{ID: keyID, PublicKey: signer.PublicKey(), Status: deed.KeyActive, ActivatedAt: now.Add(-time.Minute)}})
	if err != nil {
		return err
	}
	r.res.Records = len(lines)
	if err := r.row(250*time.Millisecond, "records", "", fmt.Sprintf("%d sealed with %s, a key made for this run\nThe Postgres checks inside are the replayed ones.",
		len(lines), ui.ShortID(keyID)), tagLive); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l.line)
		buf.WriteByte('\n')
	}
	rep, err := deed.VerifyStream(r.ctx, bytes.NewReader(buf.Bytes()), deed.StreamOptions{Keys: ks, KeysPinned: true, Org: org})
	if err != nil {
		return fmt.Errorf("play: the check didn't run: %w", err)
	}
	r.res.ChainOK = rep.Result == deed.ResultValid && rep.Chain.Status == deed.ChainIntact
	if r.res.ChainOK {
		err = r.row(400*time.Millisecond, "verify", ui.OK, fmt.Sprintf("%d deeds, chain intact, 1 seal key", rep.Deeds), tagLive)
	} else {
		err = r.row(400*time.Millisecond, "verify", ui.Fail, fmt.Sprintf("the chain didn't check out (%s)", rep.Result), tagLive)
	}
	if err != nil {
		return err
	}

	// Change one byte in record 1 (the last digit of a latency or a digit of the time) and check again.
	changed, seq, ok := tamper(lines)
	if !ok {
		return errors.New("play: found no byte to change")
	}
	r.res.TamperSeq = seq
	if err := r.row(500*time.Millisecond, "tamper", "", fmt.Sprintf("one byte changed in deed %d", seq), ""); err != nil {
		return err
	}
	rep2, err := deed.VerifyStream(r.ctx, bytes.NewReader(changed), deed.StreamOptions{Keys: ks, KeysPinned: true, Org: org})
	if err != nil {
		return fmt.Errorf("play: the check didn't run: %w", err)
	}
	if rep2.Result == deed.ResultInvalid && len(rep2.Failures) > 0 {
		r.res.TamperSeen = true
		return r.row(400*time.Millisecond, "verify", ui.Fail, rep2.Failures[0].Message, tagLive)
	}
	return r.row(400*time.Millisecond, "verify", ui.Fail, "the changed record was not caught; please file a bug", tagLive)
}

// tamper returns the records with one byte changed in the first record's execution.latency_ms
// (the same change make demo makes), and that record's seq.
func tamper(lines []sealedLine) ([]byte, int64, bool) {
	var out bytes.Buffer
	done := false
	var seq int64
	for _, l := range lines {
		b := append([]byte(nil), l.line...)
		if !done {
			const key = `"latency_ms":`
			if i := bytes.Index(b, []byte(key)); i >= 0 {
				j := i + len(key)
				for j < len(b) && b[j] >= '0' && b[j] <= '9' {
					j++
				}
				if j > i+len(key) {
					b[j-1] = '0' + (b[j-1]-'0'+1)%10
					done, seq = true, l.seq
				}
			}
		}
		out.Write(b)
		out.WriteByte('\n')
	}
	return out.Bytes(), seq, done
}
