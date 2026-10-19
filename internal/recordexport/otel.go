package recordexport

import (
	"io"
	"strconv"
)

// OTLP/JSON (opentelemetry-proto trace/v1, JSON encoding): one ExportTraceServiceRequest per line,
// as the OpenTelemetry file exporter writes them, so a collector's otlpjsonfile receiver can read
// the file. Each action record is one `execute_tool` span (GenAI semantic conventions v1.41.0);
// each amendment is a child span of the action's span carrying the later check. Admin records have
// no span. docs/exports/otel-genai.md has the mapping and what it loses.

// OTLP enum values (encoded as integers in OTLP/JSON).
const (
	spanKindInternal = 1
	statusError      = 2
)

type otelWriter struct {
	w   io.Writer
	opt Options
	st  Stats
}

func (x *otelWriter) Stats() Stats { return x.st }
func (x *otelWriter) Close() error { return nil }

func (x *otelWriter) Write(raw []byte) error {
	x.st.Records++
	e, err := parse(raw)
	if err != nil {
		return err
	}
	span, ok := otelSpan(&e)
	if !ok {
		x.st.Skipped++
		return nil
	}
	req := map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{
			kv("service.name", "deedglass"),
			kv("deedglass.org.id", e.Body.Org),
		}},
		"scopeSpans": []any{map[string]any{
			"scope":     map[string]any{"name": "deedglass.export", "version": x.opt.Producer},
			"spans":     []any{span},
			"schemaUrl": OTelSchemaURL,
		}},
		"schemaUrl": OTelSchemaURL,
	}}}
	if err := writeLine(x.w, req); err != nil {
		return err
	}
	x.st.Written++
	return nil
}

// TraceID is the OTel trace id of an action: every record about it (the action and its
// amendments) lands in one trace.
func TraceID(actionID string) string { return derivedID("otel-trace", actionID, 16) }

// SpanID is the OTel span id of a record.
func SpanID(deedID string) string { return derivedID("otel-span", deedID, 8) }

func otelSpan(e *envelope) (map[string]any, bool) {
	b := &e.Body
	if b.Kind == "admin" {
		return nil, false
	}
	start, end := mustTime(b.IssuedAt), mustTime(b.IssuedAt)
	if b.Execution != nil {
		start, end = mustTime(b.Execution.StartedAt), mustTime(b.Execution.FinishedAt)
	}
	var attrs []any
	add := func(k string, v any) { attrs = append(attrs, kv(k, v)) }
	span := map[string]any{
		"traceId":           TraceID(b.Action.ID),
		"spanId":            SpanID(b.DeedID),
		"kind":              spanKindInternal,
		"startTimeUnixNano": strconv.FormatInt(start.UnixNano(), 10),
		"endTimeUnixNano":   strconv.FormatInt(end.UnixNano(), 10),
	}
	if b.Kind == "action" {
		span["name"] = "execute_tool " + b.Action.Tool
		add("gen_ai.operation.name", "execute_tool")
	} else {
		span["name"] = "deedglass.check " + b.Action.Tool
		span["parentSpanId"] = SpanID(*b.Amends)
	}
	add("gen_ai.tool.name", b.Action.Tool)
	add("gen_ai.tool.call.id", b.Action.ID)
	add("gen_ai.tool.type", toolType(b.Action.Mode))
	add("gen_ai.agent.id", b.Agent.ID)
	add("gen_ai.agent.name", b.Agent.Name)
	if b.Session != nil {
		add("gen_ai.conversation.id", *b.Session)
	}
	if b.Kind == "action" {
		if et, msg := otelError(b); et != "" {
			add("error.type", et)
			span["status"] = map[string]any{"code": statusError, "message": msg}
		}
	}
	add("deedglass.record.id", b.DeedID)
	add("deedglass.record.kind", b.Kind)
	add("deedglass.record.seq", b.Seq)
	add("deedglass.record.issued_at", b.IssuedAt)
	add("deedglass.record.body_hash", e.BodyHash)
	add("deedglass.record.seal", e.Seal)
	add("deedglass.record.seal_key_id", b.SealKeyID)
	if b.Amends != nil {
		add("deedglass.record.amends", *b.Amends)
	}
	if b.OnBehalfOf != nil {
		add("deedglass.on_behalf_of", *b.OnBehalfOf)
	}
	add("deedglass.action.mode", b.Action.Mode)
	if b.Action.Connector != nil {
		add("deedglass.action.connector_id", *b.Action.Connector)
	}
	add("deedglass.action.request_digest", b.Action.RequestDigest)
	if b.Action.IntentHash != nil {
		add("deedglass.action.intent_hash", *b.Action.IntentHash)
	}
	add("deedglass.action.intent_signed", b.Action.IntentSigned)
	if p := b.Policy; p != nil {
		add("deedglass.policy.decision", p.Decision)
		add("deedglass.policy.mode", p.Mode)
		if p.RuleID != nil {
			add("deedglass.policy.rule_id", *p.RuleID)
		}
		if p.ID != nil {
			add("deedglass.policy.id", *p.ID)
		}
		if p.VersionID != nil {
			add("deedglass.policy.version_id", *p.VersionID)
		}
		if p.VersionHash != nil {
			add("deedglass.policy.version_hash", *p.VersionHash)
		}
	}
	if a := b.Approval; a != nil {
		add("deedglass.approval.id", a.ID)
		add("deedglass.approval.outcome", a.Outcome)
		add("deedglass.approval.decided_by", a.DecidedBy)
		if a.DecidedAt != nil {
			add("deedglass.approval.decided_at", *a.DecidedAt)
		}
	}
	if x := b.Execution; x != nil {
		add("deedglass.execution.status", x.Status)
		add("deedglass.execution.latency_ms", x.LatencyMS)
		if x.ResultDigest != nil {
			add("deedglass.execution.result_digest", *x.ResultDigest)
		}
		if x.ErrorCode != nil {
			add("deedglass.execution.error_code", *x.ErrorCode)
		}
	}
	if w := b.Witness; w != nil {
		add("deedglass.check.level", w.Level)
		add("deedglass.check.verdict", w.Verdict)
		add("deedglass.check.method", w.Method)
		if w.DetailDigest != nil {
			add("deedglass.check.detail_digest", *w.DetailDigest)
		}
	}
	if c := b.Cost; c != nil {
		add("deedglass.cost.amount", c.Amount)
		add("deedglass.cost.currency", c.Currency)
	}
	span["attributes"] = attrs
	return span, true
}

// toolType maps the action's mode to gen_ai.tool.type: Deedglass ran the tool as a gateway to an
// outside system ("extension"), or the agent ran it at the edge and reported back ("function").
func toolType(mode string) string {
	if mode == "proxy" {
		return "function"
	}
	return "extension"
}

// otelError is error.type and the status message for an action that didn't run cleanly, or "" for
// one that ran and succeeded. A Doesn't-match check isn't an error of the call: it is the
// deedglass.check.verdict attribute.
func otelError(b *body) (string, string) {
	x := b.Execution
	switch {
	case x == nil && b.Policy.Decision == "deny":
		return "policy_denied", "Not run: the policy denied it."
	case x == nil && b.Approval != nil:
		return "approval_" + b.Approval.Outcome, "Not run: the approval was " + b.Approval.Outcome + "."
	case x == nil:
		return "not_run", "Not run."
	case x.Status == "failed":
		if x.ErrorCode != nil {
			return *x.ErrorCode, "The tool call failed."
		}
		return "_OTHER", "The tool call failed."
	case x.Status == "unknown":
		return "outcome_unknown", "Deedglass can't know whether the tool ran."
	}
	return "", ""
}

// kv is an OTLP KeyValue. Integers are strings in OTLP/JSON (proto3 int64).
func kv(k string, v any) map[string]any {
	return map[string]any{"key": k, "value": anyValue(v)}
}

func anyValue(v any) map[string]any {
	switch t := v.(type) {
	case string:
		return map[string]any{"stringValue": t}
	case bool:
		return map[string]any{"boolValue": t}
	case int64:
		return map[string]any{"intValue": strconv.FormatInt(t, 10)}
	case int:
		return map[string]any{"intValue": strconv.Itoa(t)}
	case []string:
		vals := make([]any, 0, len(t))
		for _, s := range t {
			vals = append(vals, anyValue(s))
		}
		return map[string]any{"arrayValue": map[string]any{"values": vals}}
	}
	panic("recordexport: unsupported OTel attribute type")
}
