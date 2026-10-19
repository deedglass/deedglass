package witness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// L1 methods: readback.<connector type>.<read tool>, readback.<connector type>.row (the row
// read-back: readback.postgres.row), postcondition.<type>.

// ReadbackDetail is witness_detail.levels.L1.readback: what the read-back did, never what it read
// (the body is kept only as a digest).
type ReadbackDetail struct {
	Tool           string `json:"tool"`
	Target         string `json:"target,omitempty"`
	RequestDigest  string `json:"request_digest,omitempty"`
	Status         int    `json:"status,omitempty"`
	ResponseDigest string `json:"response_digest,omitempty"`
	Attempts       int    `json:"attempts"`
	LatencyMS      int64  `json:"latency_ms"`
}

// L1Result is one L1 evaluation.
type L1Result struct {
	Reasons  []Reason
	Method   string
	Readback *ReadbackDetail
	Deferred bool // inline only: the budget ran out before a final answer; the worker finishes it
}

// EvaluateL1 reads the write back through obs and checks it (S19 §3.5). The budget is ctx's
// deadline. With final false (the request path), running out of budget defers the decision to
// the worker; with final true (the worker), it is the gap readback_timeout.
func EvaluateL1(ctx context.Context, s Subject, obs Observer, final bool) L1Result {
	p := PlanFor(s)
	if !p.L1 {
		if p.Why != "" {
			return L1Result{Reasons: []Reason{R(L1, CodeReadbackSkipped, "readback", map[string]any{"why": p.Why})}}
		}
		return L1Result{}
	}
	if obs == nil {
		return L1Result{Reasons: []Reason{R(L1, CodeInternalError, "readback", nil)}}
	}
	switch p.Kind {
	case KindRow:
		return rowReadBack(ctx, s, obs, final)
	case KindNative:
		return nativeReadBack(ctx, s, obs, final)
	}
	return readBack(ctx, s, obs, final)
}

func readBack(ctx context.Context, s Subject, obs Observer, final bool) L1Result {
	spec := s.Spec.ReadBack
	env := newTemplateEnv(s)
	readTool := s.Spec.Connector + "." + spec.Tool
	method := "readback." + nonEmptyOr(s.ConnectorType, "connector") + "." + spec.Tool
	if len(spec.Expect) == 0 && s.Spec.Postcondition != "" {
		method = "postcondition." + nonEmptyOr(s.ConnectorType, "connector")
	}
	method = clipMethod(method)
	args, target := readArgs(spec, env)
	req := ReadRequest{Tool: readTool, Args: args, Target: target}
	res := L1Result{Method: method, Readback: &ReadbackDetail{Tool: readTool, Target: target}}

	var pc *Postcondition
	var pcErr error
	if s.Spec.Postcondition != "" {
		pc, pcErr = cachedPostcondition(s.Spec.ConfigHash, s.Spec.Connector+"."+s.Spec.Tool, s.Spec.Postcondition)
	}
	n := spec.attempts()
	var last []Reason
	for attempt := 1; attempt <= n; attempt++ {
		res.Readback.Attempts = attempt
		o, err := obs.ReadBack(ctx, s, req, attempt)
		res.Readback.RequestDigest = nonEmptyOr(o.RequestDigest, res.Readback.RequestDigest)
		res.Readback.ResponseDigest = o.ResponseDigest // this attempt's answer only
		if o.Status != 0 {
			res.Readback.Status = o.Status
		}
		res.Readback.LatencyMS += o.LatencyMS
		retry := false
		var se *SkipError
		var re *ReadError
		switch {
		case err == nil:
			last, retry = checkObservation(s, spec, env, pc, pcErr, readTool, o)
		case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
			if !final {
				return L1Result{Deferred: true, Method: method, Readback: res.Readback}
			}
			last = []Reason{R(L1, CodeReadbackTimeout, "readback", map[string]any{"readback": readTool, "budget": budgetText(ctx)})}
		case errors.As(err, &se):
			res.Reasons = []Reason{R(L1, CodeReadbackSkipped, "readback", map[string]any{"why": se.Why})}
			return res
		case errors.Is(err, ErrNotFound):
			retry = true
			if spec.OnNotFound == OnNotFoundViolation {
				last = []Reason{R(L1, CodeReadbackAbsent, "readback", map[string]any{"tool": s.Tool, "readback": readTool, "target": target})}
			} else {
				last = []Reason{R(L1, CodeReadbackNotFound, "readback", map[string]any{"readback": readTool, "target": target})}
			}
		case errors.As(err, &re):
			last = []Reason{R(L1, CodeReadbackFailed, "readback", map[string]any{"readback": readTool, "error_code": nonEmptyOr(re.Code, "upstream_error")})}
		default:
			last = []Reason{R(L1, CodeReadbackFailed, "readback", map[string]any{"readback": readTool, "error_code": "upstream_error"})}
		}
		if !retry || attempt == n || !wait(ctx, spec.interval()) {
			break
		}
	}
	res.Reasons = last
	return res
}

// checkObservation runs expect and the postcondition over one observation. retry is true when a
// check disagreed (eventual consistency may still catch up).
func checkObservation(s Subject, spec *ReadBackSpec, env templateEnv, pc *Postcondition, pcErr error, readTool string, o Observation) ([]Reason, bool) {
	var out []Reason
	retry := false
	if len(spec.Expect) > 0 {
		r := runExpect(spec.Expect, env, o)
		if len(r.diffs) > 0 {
			retry = true
			d := r.diffs[0]
			out = append(out, R(L1, CodePostconditionFailed, "readback.expect", map[string]any{"tool": s.Tool, "readback": readTool,
				"path": displayPath(d.Path), "observed": d.Observed, "expected": d.Expected}).WithDiff(r.diffs))
		} else {
			out = append(out, R(L1, CodeReadbackMatched, "readback.expect", map[string]any{"readback": readTool, "fields": fieldsPhrase(r.matched)}))
		}
	}
	switch {
	case pcErr != nil:
		out = append(out, R(L1, CodePostconditionError, "postcondition", map[string]any{"error": errText(pcErr)}))
	case pc != nil:
		held, diffs, err := pc.Eval(s, o)
		switch {
		case err != nil:
			out = append(out, R(L1, CodePostconditionError, "postcondition", map[string]any{"error": errText(err)}))
		case held:
			out = append(out, R(L1, CodePostconditionHeld, "postcondition", map[string]any{"expr": pc.Source}))
		default:
			retry = true
			vars := map[string]any{"tool": s.Tool, "readback": readTool, "path": "the postcondition", "observed": false, "expected": true}
			if len(diffs) > 0 {
				vars["path"], vars["observed"], vars["expected"] = displayCEL(diffs[0].Path), diffs[0].Observed, diffs[0].Expected
			}
			out = append(out, R(L1, CodePostconditionFailed, "postcondition", vars).WithDiff(diffs))
		}
	}
	if len(out) == 0 {
		// A read-back with nothing to compare: it answered, which is all it can say.
		out = append(out, R(L1, CodeReadbackMatched, "readback", map[string]any{"readback": readTool, "fields": "the record is there"}))
	}
	return out, retry
}

// readArgs resolves the read tool's arguments, and a short name for what it reads.
func readArgs(spec *ReadBackSpec, env templateEnv) (json.RawMessage, string) {
	m := map[string]any{}
	keys := make([]string, 0, len(spec.Args))
	for k, v := range spec.Args {
		val, ok := env.resolve(v)
		if ok {
			m[k] = val
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := m[k]; ok {
			parts = append(parts, k+" "+ShowValue(v))
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		b = []byte("{}")
	}
	target := strings.Join(parts, ", ")
	if target == "" {
		target = "the record"
	}
	return b, target
}

// displayCEL drops "observation.body." from a CEL operand, so the sentence names the field.
func displayCEL(p string) string {
	for _, pre := range []string{"observation.body.", "observation."} {
		if s, ok := strings.CutPrefix(p, pre); ok {
			return s
		}
	}
	return p
}

func wait(ctx context.Context, d time.Duration) bool {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d {
		return false // never past the budget: the last attempt stands
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// budgetText is how long the budget was, for readback_timeout.
func budgetText(ctx context.Context) string {
	if b, ok := ctx.Value(budgetKey{}).(time.Duration); ok {
		return humanDuration(b)
	}
	return "its time limit"
}

type budgetKey struct{}

// WithBudget returns a context that ends after d, and remembers d for the readback_timeout message.
func WithBudget(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx = context.WithValue(ctx, budgetKey{}, d)
	return context.WithTimeout(ctx, d)
}

func humanDuration(d time.Duration) string {
	switch {
	case d%time.Second == 0:
		return fmt.Sprintf("%d s", int(d/time.Second))
	case d >= time.Second:
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}

func errText(err error) string {
	var ce *CompileError
	if errors.As(err, &ce) {
		return strings.TrimSuffix(ce.Message, ".")
	}
	s := strings.TrimPrefix(err.Error(), errPostcondition.Error()+": ")
	return strings.TrimSuffix(s, ".")
}

func nonEmptyOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// clipMethod keeps a method within the deed's 64 characters of a-z 0-9 . _ -.
func clipMethod(m string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(m) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
