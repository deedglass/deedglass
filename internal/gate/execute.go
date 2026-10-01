package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/deed"
)

// MaxErrorMessage is the cap on execution.error_message (redacted before it is stored).
const MaxErrorMessage = 1024

// classify is the one table that turns a connector's answer into an execution status (S13 §3.5).
// failed means the upstream certainly did not apply the call; anything in doubt is unknown. S16
// adds kinds to the failed row (auth, not_found, rate_limited, destination_blocked,
// credentials_missing, vault) and too_large to the unknown row.
var classify = map[connector.Kind]ExecStatus{
	connector.KindInvalidRequest: ExecFailed,
	connector.KindConfig:         ExecFailed,
	connector.KindToolError:      ExecFailed,
	connector.KindUpstream:       ExecUnknown,
	connector.KindTimeout:        ExecUnknown,
}

// errPanic marks a connector that panicked; the pipeline recovers it and records unknown.
var errPanic = errors.New("gate: the connector panicked")

// execute runs one call with the deadline, recovers a panic, and classifies the answer. No
// transaction is open while it runs (invariant 5: the pipeline commits before calling it).
func (p *Pipeline) execute(ctx context.Context, ex Executor, call connector.Call) Execution {
	ctx, cancel := context.WithDeadline(ctx, call.Deadline)
	defer cancel()
	if p.hooks.beforeExecute != nil {
		p.hooks.beforeExecute(call)
	}
	start := p.now()
	res, err := safeExecute(ctx, ex, call)
	end := p.now()
	if res.Latency == 0 {
		res.Latency = end.Sub(start)
	}
	e := Execution{StartedAt: start, FinishedAt: end, Result: res, Sent: true}
	switch {
	case err == nil:
		e.Status = ExecSucceeded
		if len(res.Output) > 0 && !json.Valid(res.Output) {
			// A connector bug: the tool ran, and its answer can't be recorded.
			e.Status, e.ErrorCode, e.Reason = ExecUnknown, "internal_error", ReasonOutcomeUnknown
			e.ErrorMsg = "The tool answered with something that isn't JSON, so Deedglass can't record what it returned. It may have run."
			e.Result.Output = nil
		}
	case errors.Is(err, errPanic):
		e.Status, e.ErrorCode, e.Reason = ExecUnknown, "internal_error", ReasonOutcomeUnknown
		e.ErrorMsg = "The connector stopped unexpectedly while it ran this tool. It may have run."
	default:
		ce := connector.AsError(err)
		if ce == nil {
			// An untyped error: in doubt, so unknown.
			e.Status, e.ErrorCode, e.Reason = ExecUnknown, "upstream_error", ReasonOutcomeUnknown
			if errors.Is(err, context.DeadlineExceeded) {
				e.ErrorCode, e.Reason = "timeout", ReasonTimeout
			}
			e.ErrorMsg = "The tool's answer was lost. It may have run."
			break
		}
		st, ok := classify[ce.Kind]
		if !ok {
			st = ExecUnknown
		}
		e.Status, e.ErrorMsg = st, ce.AgentMessage
		switch st {
		case ExecFailed:
			e.ErrorCode = safeErrorCode(ce.Code, "tool_error")
			e.Sent = ce.Kind == connector.KindToolError // invalid_request and config never reached the upstream
		default:
			e.ErrorCode, e.Reason = safeErrorCode(ce.Code, "upstream_error"), ReasonOutcomeUnknown
			if ce.Kind == connector.KindTimeout || errors.Is(err, context.DeadlineExceeded) {
				e.ErrorCode, e.Reason = "timeout", ReasonTimeout
			}
		}
	}
	e.ErrorMsg = clipMessage(e.ErrorMsg)
	return e
}

func safeExecute(ctx context.Context, ex Executor, call connector.Call) (res connector.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = connector.Result{}, fmt.Errorf("%w: %v", errPanic, r)
		}
	}()
	return ex.Execute(ctx, call)
}

// clipMessage keeps an error message at most 1 KiB, on a rune boundary.
func clipMessage(s string) string {
	if len(s) <= MaxErrorMessage {
		return s
	}
	s = s[:MaxErrorMessage-3]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// resultColumns is what the action row stores of a result: the output when it fits
// gate.max_result_bytes, and always its digest.
func resultColumns(out json.RawMessage, max int) (stored json.RawMessage, digest *string, err error) {
	if len(out) == 0 {
		return nil, nil, nil
	}
	d, err := deed.DigestJSON(out)
	if err != nil {
		return nil, nil, err
	}
	if len(out) > max {
		return nil, &d, nil
	}
	return out, &d, nil
}

// latencyMS is the execution's latency in whole milliseconds.
func latencyMS(e Execution) int64 {
	if e.FinishedAt.Before(e.StartedAt) {
		return 0
	}
	return e.FinishedAt.Sub(e.StartedAt).Milliseconds()
}

// truncateMS is t at millisecond precision, as deeds carry it.
func truncateMS(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }
