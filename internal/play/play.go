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
	"strings"
	"time"

	demopolicies "github.com/deedglass/deedglass/examples/demo/policies"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

//go:embed hero.cast.json
var castJSON []byte

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

// Style colours the output; the zero value prints plain text.
type Style struct {
	Teal, Red, Amber, Dim, Bold func(string) string
}

func (s Style) f(fn func(string) string, t string) string {
	if fn == nil {
		return t
	}
	return fn(t)
}

// Options tune a run.
type Options struct {
	Out   io.Writer
	Style Style
	// Pace is called before each line with the pause the line asks for; nil means no pauses
	// (tests, --fast, and output that isn't a terminal).
	Pace func(ctx context.Context, d time.Duration) error
	Now  func() time.Time // nil: time.Now
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
	ruleLen  = 62
	tagLive  = "live"
	tagRerun = "replay"
)

type run struct {
	ctx      context.Context
	o        Options
	st       Style
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

func (r *run) tag(t string) string {
	if t == tagLive {
		return r.st.f(r.st.Teal, "["+t+"]")
	}
	return r.st.f(r.st.Amber, "["+t+"]")
}

func (r *run) scene(n, of int, title string) error {
	head := fmt.Sprintf("── %d/%d %s ", n, of, title)
	if pad := ruleLen - len([]rune(head)); pad > 0 {
		head += strings.Repeat("─", pad)
	}
	if err := r.line(700*time.Millisecond, ""); err != nil {
		return err
	}
	return r.line(0, r.st.f(r.st.Bold, head))
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
	header := []string{
		st.f(st.Bold, "Deedglass play") + " · offline · no Docker · nothing leaves this machine",
		"",
		"  " + r.tag(tagLive) + "    Rules: the policy engine deedglass serve runs, deciding each call now (acme-demo).",
		"  " + r.tag(tagLive) + "    Records: sealed and checked here, with a key made for this run.",
		"  " + r.tag(tagRerun) + "  Postgres's change log: a recorded real run of " + r.res.Recording + ".",
		st.f(st.Dim, "          Postgres can't run inside this binary, so its lines are replayed as they printed."),
	}
	for _, l := range header {
		if err := r.line(60*time.Millisecond, l); err != nil {
			return r.res, err
		}
	}
	const scenes = 6

	// 1. A safe read.
	if err := r.scene(1, scenes, "A safe read"); err != nil {
		return r.res, err
	}
	dRead, err := r.decide(pol, "read", "postgres.query", sqlRead)
	if err != nil {
		return r.res, err
	}
	_ = dRead

	// 2. A destructive request.
	if err := r.scene(2, scenes, "A destructive request"); err != nil {
		return r.res, err
	}
	if _, err := r.decide(pol, "drop", "postgres.execute", sqlDrop); err != nil {
		return r.res, err
	}
	if err := r.line(300*time.Millisecond, st.f(st.Dim, "  Nothing reached the database.")); err != nil {
		return r.res, err
	}

	// 3. A normal write: the rule decides live, Postgres's answer is replayed.
	if err := r.scene(3, scenes, "A normal write"); err != nil {
		return r.res, err
	}
	if _, err := r.decide(pol, "ship", "postgres.execute", sqlShip); err != nil {
		return r.res, err
	}
	if err := r.replay(cast, "1. ", "2. ", 0); err != nil {
		return r.res, err
	}

	// 4. The trigger.
	if err := r.scene(4, scenes, "The trigger"); err != nil {
		return r.res, err
	}
	if err := r.line(200*time.Millisecond, "  Same request. A legacy trigger on public.orders is on."); err != nil {
		return r.res, err
	}
	if _, err := r.decide(pol, "ship", "postgres.execute", sqlShip); err != nil {
		return r.res, err
	}
	if err := r.replay(cast, "2. ", "3. ", 6); err != nil {
		return r.res, err
	}

	// 5. Put it back.
	if err := r.scene(5, scenes, "Put it back"); err != nil {
		return r.res, err
	}
	if _, err := r.decideUndo(pol); err != nil {
		return r.res, err
	}
	if err := r.line(400*time.Millisecond, "  "+r.tag(tagRerun)+" "+st.f(st.Dim, "the undo in the recorded launch demo (make demo, examples/demo/testdata/launch.golden):")); err != nil {
		return r.res, err
	}
	for _, l := range UndoReplay {
		if err := r.line(500*time.Millisecond, "  │ "+colourMark(st, l)); err != nil {
			return r.res, err
		}
	}

	// 6. Records, live.
	if err := r.scene(6, scenes, "Check every record"); err != nil {
		return r.res, err
	}
	if err := r.records(); err != nil {
		return r.res, err
	}

	tail := []string{
		"",
		st.f(st.Bold, "Play complete.") + " What was live ran here; what was replayed is labelled.",
		"",
		"Try it on your own setup:",
		"  npx deedglass scan            grade what your agent setup can change (read-only, sends nothing)",
		"  npx deedglass init --yes      the real stack in Docker: Postgres, the server, the rules",
		"  npx deedglass init --trigger  then this 301 scene, live, on your machine",
		"  https://deedglass.com/play    the same scene in your browser",
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
	st := r.st
	if err := r.line(250*time.Millisecond, "  agent  "+st.f(st.Dim, tool)+"  "+sql); err != nil {
		return Decision{}, err
	}
	args, _ := json.Marshal(map[string]string{"sql": sql})
	side := "write"
	if tool == "postgres.query" {
		side = "read"
	}
	d := r.evaluate(c, key, baseInput(tool, strings.TrimPrefix(tool, "postgres."), side, args, r.o.Now()))
	return d, r.line(350*time.Millisecond, r.decisionLine(d))
}

func (r *run) decideUndo(c *policy.Compiled) (Decision, error) {
	st := r.st
	if err := r.line(250*time.Millisecond, "  person "+st.f(st.Dim, "postgres.undo")+"  put back the rows that action changed"); err != nil {
		return Decision{}, err
	}
	d := r.evaluate(c, "undo", baseInput("postgres.undo", "undo", "write", json.RawMessage(`{}`), r.o.Now()))
	return d, r.line(350*time.Millisecond, r.decisionLine(d))
}

func (r *run) decisionLine(d Decision) string {
	st := r.st
	var s string
	switch d.Outcome {
	case string(policy.Allow):
		s = st.f(st.Teal, "✓") + " allowed · rule " + d.Rule
		if d.Verify != "" {
			s += " · check " + levelName(d.Verify)
		}
	case string(policy.Deny):
		s = st.f(st.Red, "✕") + " denied · " + d.Message
	case string(policy.RequireApproval):
		s = st.f(st.Amber, "◐") + " needs a person · " + d.Message
	default:
		s = "· " + d.Outcome + " · " + d.Message
	}
	return s + "  " + r.tag(tagLive)
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

// replay prints the recorded lines from the one starting with `from` up to the one starting with
// `until` (excluded), at their recorded pace (each gap capped at 1.2 s). keepRows > 0 keeps that
// many "✕ unexpected" rows of the diff and says how many recorded lines it skipped.
func (r *run) replay(c *Cast, from, until string, keepRows int) error {
	st := r.st
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
		return fmt.Errorf("play: the recording has no section %q", from)
	}
	if err := r.line(300*time.Millisecond, "  "+r.tag(tagRerun)+" "+st.f(st.Dim, "what the recorded run printed ("+r.res.Recording+"):")); err != nil {
		return err
	}
	prev := c.Lines[start].T
	rows, skipped := 0, 0
	for _, l := range c.Lines[start+1 : end] {
		gap := time.Duration(l.T-prev) * time.Millisecond
		prev = l.T
		if gap > 1200*time.Millisecond {
			gap = 1200 * time.Millisecond
		}
		if keepRows > 0 && strings.Contains(l.Text, "✕ unexpected") {
			rows++
			if rows > keepRows {
				skipped++
				continue
			}
		}
		if skipped > 0 {
			if err := r.line(0, st.f(st.Dim, fmt.Sprintf("  ⋮                  (%d more recorded rows not shown here)", skipped))); err != nil {
				return err
			}
			skipped = 0
		}
		if strings.TrimSpace(l.Text) == "" {
			continue
		}
		if reVerdict.MatchString(l.Text) && strings.Contains(l.Text, " rows · act_") {
			r.lastShip = l.Text
		}
		if err := r.line(gap, "  │ "+colourMark(st, l.Text)); err != nil {
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

// colourMark colours a recorded line's leading status mark.
func colourMark(st Style, s string) string {
	for mark, fn := range map[string]func(string) string{"✓": st.Teal, "✕": st.Red, "◐": st.Amber} {
		if strings.HasPrefix(s, mark) {
			return st.f(fn, mark) + s[len(mark):]
		}
	}
	return s
}

// --- records ---

type sealedLine struct {
	seq  int64
	line []byte
}

func (r *run) records() error {
	st := r.st
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
	if err := r.line(250*time.Millisecond, fmt.Sprintf("  Sealed %d records, one per action above, with %s (made for this run).  %s",
		len(lines), keyID[:11]+"…", r.tag(tagLive))); err != nil {
		return err
	}
	if err := r.line(0, st.f(st.Dim, "  The checks inside the two Postgres records are the ones the replayed run printed.")); err != nil {
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
		err = r.line(400*time.Millisecond, fmt.Sprintf("%s %d deeds · chain intact · 1 seal key  %s", st.f(st.Teal, "✓"), rep.Deeds, r.tag(tagLive)))
	} else {
		err = r.line(400*time.Millisecond, fmt.Sprintf("%s the chain didn't check out (%s)", st.f(st.Red, "✕"), rep.Result))
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
	if err := r.line(500*time.Millisecond, st.f(st.Dim, fmt.Sprintf("· one byte changed in deed %d", seq))); err != nil {
		return err
	}
	rep2, err := deed.VerifyStream(r.ctx, bytes.NewReader(changed), deed.StreamOptions{Keys: ks, KeysPinned: true, Org: org})
	if err != nil {
		return fmt.Errorf("play: the check didn't run: %w", err)
	}
	if rep2.Result == deed.ResultInvalid && len(rep2.Failures) > 0 {
		r.res.TamperSeen = true
		return r.line(400*time.Millisecond, fmt.Sprintf("%s %s  %s", st.f(st.Red, "✕"), rep2.Failures[0].Message, r.tag(tagLive)))
	}
	return r.line(400*time.Millisecond, st.f(st.Red, "✕")+" the changed record was not caught; please file a bug")
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
