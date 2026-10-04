package witness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Native read-backs (S29): the connector reads its own write back and compares it in code (GitHub's
// blob SHA, the issue marker, Slack's message metadata). The Witness still decides: the findings
// become catalog reasons here, a read-back that found nothing is a gap and never a violation, and
// a disagreement is retried within the budget first (a service may not show a write at once).
const (
	NativeAttempts = 3
	NativeInterval = 700 * time.Millisecond
)

func nativeReadBack(ctx context.Context, s Subject, obs Observer, final bool) L1Result {
	method := clipMethod("readback." + nonEmptyOr(s.ConnectorType, "connector") + "." + s.Spec.Tool)
	res := L1Result{Method: method, Readback: &ReadbackDetail{Tool: s.Tool}}
	req := ReadRequest{Native: true, Target: "the write"}
	var last []Reason
	for attempt := 1; attempt <= NativeAttempts; attempt++ {
		res.Readback.Attempts = attempt
		o, err := obs.ReadBack(ctx, s, req, attempt)
		if o.Target != "" {
			res.Readback.Target = o.Target
		}
		if o.Status != 0 {
			res.Readback.Status = o.Status
		}
		res.Readback.LatencyMS += o.LatencyMS
		target := nonEmptyOr(res.Readback.Target, s.Tool+"'s target")
		retry := false
		var se *SkipError
		var re *ReadError
		switch {
		case err == nil:
			last, retry = nativeFindings(s, target, o.Findings)
		case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
			if !final {
				return L1Result{Deferred: true, Method: method, Readback: res.Readback}
			}
			last = []Reason{R(L1, CodeReadbackTimeout, "readback", map[string]any{"readback": "of " + target, "budget": budgetText(ctx)})}
		case errors.As(err, &se):
			res.Reasons = []Reason{R(L1, CodeReadbackSkipped, "readback", map[string]any{"why": se.Why})}
			return res
		case errors.Is(err, ErrNotFound):
			retry = true
			last = []Reason{R(L1, CodeReadbackNotFound, "readback", map[string]any{"readback": "The read-back", "target": target})}
		case errors.As(err, &re):
			last = []Reason{R(L1, CodeReadbackFailed, "readback", map[string]any{"readback": "of " + target, "error_code": nonEmptyOr(re.Code, "upstream_error")})}
		default:
			last = []Reason{R(L1, CodeReadbackFailed, "readback", map[string]any{"readback": "of " + target, "error_code": "upstream_error"})}
		}
		if !retry || attempt == NativeAttempts || !wait(ctx, NativeInterval) {
			break
		}
	}
	res.Reasons = last
	return res
}

// nativeFindings turns findings into one reason: a violation naming the first field that
// disagrees (with every diff), or a pass naming what matched. retry: a field disagreed.
func nativeFindings(s Subject, target string, fs []Finding) ([]Reason, bool) {
	var diffs []FieldDiff
	var matched []string
	for _, f := range fs {
		if f.Match {
			matched = append(matched, f.Field)
			continue
		}
		diffs = append(diffs, FieldDiff{Path: f.Field, Expected: f.Expected, Observed: f.Observed})
	}
	if len(diffs) > 0 {
		d := diffs[0]
		return []Reason{R(L1, CodePostconditionFailed, "readback.native", map[string]any{"tool": s.Tool, "readback": target,
			"path": d.Path, "observed": d.Observed, "expected": d.Expected}).WithDiff(diffs)}, true
	}
	fields := "the write is there"
	if len(matched) > 0 {
		fields = joinAnd(matched) + " match"
		if len(matched) == 1 {
			fields = matched[0] + " matches"
		}
	}
	return []Reason{R(L1, CodeReadbackMatched, "readback.native", map[string]any{"readback": target, "fields": fields})}, false
}

// joinAnd is "a", "a and b", "a, b and c".
func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return fmt.Sprintf("%s and %s", strings.Join(xs[:len(xs)-1], ", "), xs[len(xs)-1])
}
