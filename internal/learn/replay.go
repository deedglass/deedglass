package learn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/sqlguard"
)

// Replay answers one question (S27 §3.4): had candidate C been enforced over window W, which
// recorded decisions would differ? It re-evaluates each stored policy_input with
// policy.Evaluate in shadow mode (no reservations). It never executes, never touches usage, never
// seals and never emits action events. The same inputs, candidate and sqlguard version give a
// byte-identical result (ResultDigest), whatever the worker count or batch size.

// Replay limits (S27 §3.4).
const (
	MaxReplayWindow  = 90 * 24 * time.Hour
	MaxReplayActions = 1_000_000
	DefaultBatch     = 1000
	MaxSamples       = 20
)

// replayDeadline is the per-evaluation backstop. The Gate uses 50 ms; replay runs many evaluations
// in parallel on a busy machine, so a wall-clock limit there would make the result depend on load.
// The CEL cost limit stays the real bound; a trip is counted unknown. A variable for the test that
// trips it.
var replayDeadline = 2 * time.Second

// Candidate is the policy replayed: a stored version, or a draft source (S28's suggestions).
type Candidate struct {
	Compiled   *policy.Compiled
	PolicyID   string // pol_…, when known
	VersionID  string // pv_… for a stored version; "" for a draft
	Version    int    // the version number, when stored
	SourceHash string // a draft's source hash
	Label      string // "acme-sim v3", for messages
}

// Baseline is what the candidate is compared with: the decisions recorded at the time (the
// default), or another version re-evaluated over the same inputs, which isolates your change from
// everything else that differed at the time (another enforced version, a newer sqlguard).
type Baseline struct {
	Type      string           `json:"type"` // recorded | version
	Compiled  *policy.Compiled `json:"-"`
	VersionID string           `json:"version_id,omitempty"`
	Version   int              `json:"version,omitempty"`
}

// Baseline types.
const (
	BaselineRecorded = "recorded"
	BaselineVersion  = "version"
)

// Unknown reasons: an action counted but not evaluated, never guessed.
const (
	UnknownInputPruned        = "input_pruned"              // retention removed the stored input
	UnknownInputMissing       = "input_missing"             // recorded before inputs were stored (before S13)
	UnknownInputUnsupported   = "input_version_unsupported" // a stored input of an unknown v
	UnknownInputUnreadable    = "input_unreadable"          // a stored input that doesn't decode
	UnknownEvaluationTimedOut = "evaluation_time_limit"     // the evaluation hit its backstop
)

// ReplayResult is a finished replay (the replay object's result part).
type ReplayResult struct {
	Candidate    ReplayCandidate `json:"candidate"`
	Baseline     ReplayBaseline  `json:"baseline"`
	Window       Window          `json:"window"`
	Summary      ReplaySummary   `json:"summary"`
	Transitions  []Transition    `json:"transitions"`
	RuleChanges  []RuleChange    `json:"rule_changes"`
	Unknown      UnknownCounts   `json:"unknown"`
	SQLGuard     SQLGuardDrift   `json:"sqlguard"`
	ResultDigest string          `json:"result_digest"`
	ElapsedMS    int64           `json:"elapsed_ms"` // not in the digest
}

// ReplayCandidate names the candidate in a result.
type ReplayCandidate struct {
	PolicyID    string `json:"policy,omitempty"`
	Name        string `json:"name"`
	Version     int    `json:"version,omitempty"`
	VersionID   string `json:"version_id,omitempty"`
	VersionHash string `json:"version_hash"`
	SourceHash  string `json:"source_hash,omitempty"`
}

// ReplayBaseline names the baseline in a result.
type ReplayBaseline struct {
	Type        string `json:"type"`
	VersionID   string `json:"version_id,omitempty"`
	Version     int    `json:"version,omitempty"`
	VersionHash string `json:"version_hash,omitempty"`
}

// ReplaySummary holds the invariants actions = evaluated + unknown and
// evaluated = unchanged + changed + rule_changed.
type ReplaySummary struct {
	Actions     int64 `json:"actions"`
	Evaluated   int64 `json:"evaluated"`
	Unchanged   int64 `json:"unchanged"`
	Changed     int64 `json:"changed"`
	RuleChanged int64 `json:"rule_changed"`
	Unknown     int64 `json:"unknown"`
}

// Transition is every action whose decision would change from From (the baseline) to To (the
// candidate).
type Transition struct {
	From         string        `json:"from"`
	To           string        `json:"to"`
	Count        int64         `json:"count"`
	HumanOutcome *HumanOutcome `json:"human_outcome,omitempty"` // transitions out of require_approval
	ByRule       []RuleCount   `json:"by_rule"`                 // the candidate's deciding rule
	ByTool       []ToolCount   `json:"by_tool"`
	Samples      []string      `json:"samples"` // the first 20 by decision time
}

// RuleChange is the same decision reached by a different rule.
type RuleChange struct {
	Decision string   `json:"decision"`
	FromRule string   `json:"from_rule"`
	ToRule   string   `json:"to_rule"`
	Count    int64    `json:"count"`
	Samples  []string `json:"samples"`
}

// HumanOutcome is what people did with the actions of a transition out of require_approval: what
// makes a loosening defensible ("humans approved 401 of 404").
type HumanOutcome struct {
	Approved     int64 `json:"approved"`
	Denied       int64 `json:"denied"`
	Expired      int64 `json:"expired"`
	Cancelled    int64 `json:"cancelled"`
	Pending      int64 `json:"pending"`
	NotRequested int64 `json:"not_requested,omitempty"` // baseline version: the recorded decision wasn't an approval
}

// RuleCount, ToolCount and AgentCount are one group of a breakdown.
type (
	RuleCount struct {
		RuleID string `json:"rule_id"`
		Count  int64  `json:"count"`
	}
	ToolCount struct {
		Tool  string `json:"tool"`
		Count int64  `json:"count"`
	}
	AgentCount struct {
		Agent string `json:"agent"`
		Name  string `json:"name,omitempty"`
		Count int64  `json:"count"`
	}
)

// keyCount is a breakdown entry before it gets its JSON name.
type keyCount struct {
	Key   string
	Count int64
}

// UnknownCounts counts actions replay couldn't evaluate, by reason.
type UnknownCounts struct {
	InputPruned        int64 `json:"input_pruned"`
	InputMissing       int64 `json:"input_missing"`
	InputUnsupported   int64 `json:"input_version_unsupported"`
	InputUnreadable    int64 `json:"input_unreadable"`
	EvaluationTimedOut int64 `json:"evaluation_time_limit"`
}

// SQLGuardDrift shows the classifier versions recorded with the decisions next to the current
// one: a difference is shown, never hidden (derived SQL facts are recomputed at replay).
type SQLGuardDrift struct {
	Recorded []string `json:"recorded"`
	Current  string   `json:"current"`
}

// Engine runs replays over an InputSource.
type Engine struct {
	Src     InputSource
	Batch   int // rows per scan (default 1,000)
	Workers int // parallel evaluations (default GOMAXPROCS)
	// Now is the clock for ElapsedMS only (nothing else in a result depends on time).
	Now func() time.Time
}

// ErrReplayTooLarge and ErrWindowInvalid are the limits' refusals (the API maps them to
// replay_too_large and replay_window_invalid).
var (
	ErrReplayTooLarge = errors.New("learn: the replay covers more actions than the limit")
	ErrWindowInvalid  = errors.New("learn: the replay window is invalid")
)

// TooLargeError carries the count for the message.
type TooLargeError struct{ Actions int64 }

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("learn: %d actions; the limit is %d", e.Actions, MaxReplayActions)
}
func (e *TooLargeError) Unwrap() error { return ErrReplayTooLarge }

// CheckWindow validates a replay window: from before to, at most 90 days.
func CheckWindow(w Window) error {
	if w.From.IsZero() || w.To.IsZero() || !w.From.Before(w.To) || w.To.Sub(w.From) > MaxReplayWindow {
		return ErrWindowInvalid
	}
	return nil
}

// Run replays candidate c against baseline b over window w for one org. progress, when set, is
// called after every batch with the actions processed so far and the total.
func (e *Engine) Run(ctx context.Context, org ids.OrgID, c Candidate, b Baseline, w Window,
	progress func(processed, total int64)) (*ReplayResult, error) {
	if c.Compiled == nil {
		return nil, errors.New("learn: the candidate has no compiled policy")
	}
	if b.Type == "" {
		b.Type = BaselineRecorded
	}
	if b.Type == BaselineVersion && b.Compiled == nil {
		return nil, errors.New("learn: a version baseline needs its compiled policy")
	}
	if err := CheckWindow(w); err != nil {
		return nil, err
	}
	now := e.Now
	if now == nil {
		now = time.Now
	}
	t0 := now()
	total, err := e.Src.Count(ctx, org, w)
	if err != nil {
		return nil, err
	}
	if total > MaxReplayActions {
		return nil, &TooLargeError{Actions: total}
	}
	batch := e.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}
	workers := e.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	f := newFold(c, b, w)
	var (
		after     Cursor
		processed int64
		outs      = make([]outcome, batch)
	)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, next, err := e.Src.Scan(ctx, org, w, after, batch)
		if err != nil {
			return nil, err
		}
		if len(rows) > len(outs) {
			outs = make([]outcome, len(rows))
		}
		evaluateAll(ctx, c, b, rows, outs[:len(rows)], workers)
		for i := range rows {
			f.add(&rows[i], &outs[i]) // fold in scan order: deterministic whatever the worker count
		}
		processed += int64(len(rows))
		if progress != nil {
			progress(processed, max(total, processed))
		}
		if len(rows) < batch {
			break
		}
		after = next
	}
	res, err := f.result()
	if err != nil {
		return nil, err
	}
	res.ElapsedMS = now().Sub(t0).Milliseconds()
	return res, nil
}

// outcome is one row's evaluation.
type outcome struct {
	unknown  string
	from     string
	fromRule string
	to       string
	toRule   string
}

// evaluateAll evaluates rows into outs (same index) with up to `workers` goroutines.
func evaluateAll(ctx context.Context, c Candidate, b Baseline, rows []InputRow, outs []outcome, workers int) {
	if workers <= 1 || len(rows) < 2*workers {
		for i := range rows {
			outs[i] = evaluateRow(ctx, c, b, &rows[i])
		}
		return
	}
	var wg sync.WaitGroup
	chunk := (len(rows) + workers - 1) / workers
	for start := 0; start < len(rows); start += chunk {
		end := min(start+chunk, len(rows))
		wg.Go(func() {
			for i := start; i < end; i++ {
				outs[i] = evaluateRow(ctx, c, b, &rows[i])
			}
		})
	}
	wg.Wait()
}

// evaluateRow decodes one stored input strictly and decides it under the candidate (and the
// baseline version, when there is one).
func evaluateRow(ctx context.Context, c Candidate, b Baseline, r *InputRow) outcome {
	if len(r.Input) == 0 || string(r.Input) == "null" {
		if r.Pruned {
			return outcome{unknown: UnknownInputPruned}
		}
		return outcome{unknown: UnknownInputMissing}
	}
	var in policy.Input
	if err := json.Unmarshal(r.Input, &in); err != nil {
		return outcome{unknown: UnknownInputUnreadable}
	}
	if in.V != policy.InputVersion {
		return outcome{unknown: UnknownInputUnsupported}
	}
	o := outcome{from: r.Recorded.Decision, fromRule: r.Recorded.RuleID}
	// An action whose tool no connector served was denied before any policy ran (S13's builtin
	// tool_unknown); the tool didn't exist then, so no candidate could have decided otherwise.
	builtin := r.Recorded.Source == policy.SourceBuiltin && r.Recorded.Code == "tool_unknown"
	if b.Type == BaselineVersion && !builtin {
		bd := policy.Evaluate(ctx, b.Compiled, in, policy.EvalOptions{Mode: policy.ModeShadow, Deadline: replayDeadline})
		if policy.TimeLimitTripped(bd) {
			return outcome{unknown: UnknownEvaluationTimedOut}
		}
		o.from, o.fromRule = string(bd.Outcome), bd.RuleID
	}
	if builtin {
		o.to, o.toRule = o.from, o.fromRule
		return o
	}
	d := policy.Evaluate(ctx, c.Compiled, in, policy.EvalOptions{Mode: policy.ModeShadow, Deadline: replayDeadline})
	if policy.TimeLimitTripped(d) {
		return outcome{unknown: UnknownEvaluationTimedOut}
	}
	o.to, o.toRule = string(d.Outcome), d.RuleID
	return o
}

// fold accumulates outcomes in scan order.
type fold struct {
	c        Candidate
	b        Baseline
	w        Window
	sum      ReplaySummary
	unknown  UnknownCounts
	trans    map[[2]string]*transAcc
	rules    map[[3]string]*ruleAcc
	sqlguard map[string]bool
}

type transAcc struct {
	t      Transition
	byRule map[string]int64
	byTool map[string]int64
	human  HumanOutcome
}

type ruleAcc struct{ r RuleChange }

func newFold(c Candidate, b Baseline, w Window) *fold {
	return &fold{c: c, b: b, w: w, trans: map[[2]string]*transAcc{}, rules: map[[3]string]*ruleAcc{}, sqlguard: map[string]bool{}}
}

func (f *fold) add(r *InputRow, o *outcome) {
	f.sum.Actions++
	if r.SQLGuard != "" {
		f.sqlguard[r.SQLGuard] = true
	}
	switch o.unknown {
	case "":
	case UnknownInputPruned:
		f.unknown.InputPruned++
	case UnknownInputMissing:
		f.unknown.InputMissing++
	case UnknownInputUnsupported:
		f.unknown.InputUnsupported++
	case UnknownInputUnreadable:
		f.unknown.InputUnreadable++
	case UnknownEvaluationTimedOut:
		f.unknown.EvaluationTimedOut++
	}
	if o.unknown != "" {
		f.sum.Unknown++
		return
	}
	f.sum.Evaluated++
	switch {
	case o.from == o.to && o.fromRule == o.toRule:
		f.sum.Unchanged++
	case o.from == o.to:
		f.sum.RuleChanged++
		k := [3]string{o.to, o.fromRule, o.toRule}
		a := f.rules[k]
		if a == nil {
			a = &ruleAcc{r: RuleChange{Decision: o.to, FromRule: o.fromRule, ToRule: o.toRule, Samples: []string{}}}
			f.rules[k] = a
		}
		a.r.Count++
		if len(a.r.Samples) < MaxSamples {
			a.r.Samples = append(a.r.Samples, r.ActionID)
		}
	default:
		f.sum.Changed++
		k := [2]string{o.from, o.to}
		a := f.trans[k]
		if a == nil {
			a = &transAcc{t: Transition{From: o.from, To: o.to, Samples: []string{}}, byRule: map[string]int64{}, byTool: map[string]int64{}}
			f.trans[k] = a
		}
		a.t.Count++
		a.byRule[o.toRule]++
		a.byTool[r.Tool]++
		if len(a.t.Samples) < MaxSamples {
			a.t.Samples = append(a.t.Samples, r.ActionID)
		}
		if o.from == string(policy.RequireApproval) {
			switch {
			case r.Recorded.Decision != string(policy.RequireApproval):
				a.human.NotRequested++
			case r.Approval == ApprovalApproved:
				a.human.Approved++
			case r.Approval == ApprovalDenied:
				a.human.Denied++
			case r.Approval == ApprovalExpired:
				a.human.Expired++
			case r.Approval == ApprovalCancelled:
				a.human.Cancelled++
			default:
				a.human.Pending++
			}
		}
	}
}

// result builds the final, ordered result and its digest.
func (f *fold) result() (*ReplayResult, error) {
	res := &ReplayResult{Window: Window{From: f.w.From.UTC(), To: f.w.To.UTC()}, Summary: f.sum, Unknown: f.unknown,
		Transitions: []Transition{}, RuleChanges: []RuleChange{},
		Candidate: ReplayCandidate{PolicyID: f.c.PolicyID, Name: f.c.Compiled.Name, Version: f.c.Version, VersionID: f.c.VersionID,
			VersionHash: f.c.Compiled.VersionHash, SourceHash: f.c.SourceHash},
		Baseline: ReplayBaseline{Type: f.b.Type},
		SQLGuard: SQLGuardDrift{Recorded: []string{}, Current: sqlguard.Version}}
	if f.b.Type == BaselineVersion {
		res.Baseline.VersionID, res.Baseline.Version, res.Baseline.VersionHash = f.b.VersionID, f.b.Version, f.b.Compiled.VersionHash
	}
	for v := range f.sqlguard {
		res.SQLGuard.Recorded = append(res.SQLGuard.Recorded, v)
	}
	sort.Strings(res.SQLGuard.Recorded)
	for _, a := range f.trans {
		t := a.t
		for _, kc := range sortedCounts(a.byRule) {
			t.ByRule = append(t.ByRule, RuleCount{RuleID: kc.Key, Count: kc.Count})
		}
		for _, kc := range sortedCounts(a.byTool) {
			t.ByTool = append(t.ByTool, ToolCount{Tool: kc.Key, Count: kc.Count})
		}
		if t.From == string(policy.RequireApproval) {
			h := a.human
			t.HumanOutcome = &h
		}
		res.Transitions = append(res.Transitions, t)
	}
	slices.SortFunc(res.Transitions, func(x, y Transition) int {
		if x.Count != y.Count {
			return int(y.Count - x.Count)
		}
		if x.From != y.From {
			return compareStrings(x.From, y.From)
		}
		return compareStrings(x.To, y.To)
	})
	for _, a := range f.rules {
		res.RuleChanges = append(res.RuleChanges, a.r)
	}
	slices.SortFunc(res.RuleChanges, func(x, y RuleChange) int {
		if x.Count != y.Count {
			return int(y.Count - x.Count)
		}
		if x.Decision != y.Decision {
			return compareStrings(x.Decision, y.Decision)
		}
		if x.FromRule != y.FromRule {
			return compareStrings(x.FromRule, y.FromRule)
		}
		return compareStrings(x.ToRule, y.ToRule)
	})
	d, err := ResultDigest(res)
	if err != nil {
		return nil, err
	}
	res.ResultDigest = d
	return res, nil
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// sortedCounts orders a breakdown by count (descending), then key.
func sortedCounts(m map[string]int64) []keyCount {
	out := make([]keyCount, 0, len(m))
	for k, v := range m {
		out = append(out, keyCount{Key: k, Count: v})
	}
	slices.SortFunc(out, func(a, b keyCount) int {
		if a.Count != b.Count {
			return int(b.Count - a.Count)
		}
		return compareStrings(a.Key, b.Key)
	})
	return out
}

// ResultDigest is "sha256:" + hex(sha256(JCS(result))) with the timing field and the digest
// itself removed: the same inputs, candidate and sqlguard version always give this digest. Anyone
// can re-derive it from a result object with those two fields deleted.
func ResultDigest(r *ReplayResult) (string, error) {
	c := *r
	c.ResultDigest, c.ElapsedMS = "", 0
	b, err := json.Marshal(digestView(&c))
	if err != nil {
		return "", err
	}
	return deed.DigestJSON(b)
}

// digestView is the result as JSON without elapsed_ms and result_digest.
func digestView(r *ReplayResult) map[string]any {
	b, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	delete(m, "elapsed_ms")
	delete(m, "result_digest")
	return m
}
