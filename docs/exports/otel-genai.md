# Export: OpenTelemetry GenAI spans

`deedglass export --format otel-genai` writes each sealed record as an OpenTelemetry span, using the
GenAI semantic conventions' `execute_tool` span, so a trace backend can show what an agent's tool
calls did next to the rest of its traces. The output is OTLP/JSON, one `ExportTraceServiceRequest`
per line (the format the OpenTelemetry file exporter writes and the Collector's `otlpjsonfile`
receiver reads). Overview and the other formats: [EXPORTS.md](../EXPORTS.md).

## Pinned versions

| What | Version | Date | Status |
|---|---|---|---|
| Semantic conventions, `gen_ai.*` attributes and the `execute_tool` span | v1.41.0 (`schemaUrl` `https://opentelemetry.io/schemas/1.41.0`) | 2026-04-28 | Development |
| OTLP/JSON encoding | opentelemetry-proto v1.11.1, `trace/v1` | 2026-09-29 | Stable |

v1.41.0 is the last semantic-conventions release that defines the GenAI attributes in the main
repository. Later releases mark them as moved to `open-telemetry/semantic-conventions-genai`,
which had published no release on 2026-10-19. When it does, the export moves to it in one change:
the attribute registry file, the goldens and this page. The GenAI conventions are at Development
status, so names can still change.

## Mapping

| Span field or attribute | From the record | Notes |
|---|---|---|
| `traceId` | `action.id` | the first 16 bytes of SHA-256 over a fixed tag and the action id: every record about one action lands in one trace |
| `spanId` | `deed_id` | the first 8 bytes, the same way |
| `parentSpanId` | `amends` (amendments only) | the amended record's span |
| `name` | `action.tool` | `execute_tool {tool}` for an action; `deedglass.check {tool}` for an amendment |
| `kind` | fixed | `INTERNAL` (1) |
| `startTimeUnixNano`, `endTimeUnixNano` | `execution.started_at`, `finished_at` | `issued_at` for both when the call never ran, and for amendments |
| `status` | `execution`, `policy`, `approval` | unset when the call ran and succeeded; `ERROR` (2) when it was denied, never ran, failed or its outcome is unknown |
| `gen_ai.operation.name` | fixed | `execute_tool` (actions only) |
| `gen_ai.tool.name` | `action.tool` | |
| `gen_ai.tool.call.id` | `action.id` | |
| `gen_ai.tool.type` | `action.mode` | `extension` when Deedglass ran the tool (`gateway`); `function` when the agent ran it and reported back (`proxy`) |
| `gen_ai.agent.id`, `gen_ai.agent.name` | `agent.id`, `agent.name` | |
| `gen_ai.conversation.id` | `session` | when present |
| `error.type` | the reason it didn't run cleanly | `policy_denied`, `approval_denied`/`approval_expired`/`approval_cancelled`, `not_run`, the record's `error_code`, `outcome_unknown`, or `_OTHER` |
| `deedglass.record.*` | `deed_id`, `kind`, `seq`, `issued_at`, `body_hash`, `seal`, `seal_key_id`, `amends` | to find the sealed record the span came from |
| `deedglass.on_behalf_of` | `on_behalf_of` | the opaque reference, as recorded |
| `deedglass.action.*` | `mode`, `connector`, `request_digest`, `intent_hash`, `intent_signed` | |
| `deedglass.policy.*` | `decision`, `mode`, `rule_id`, `id`, `version_id`, `version_hash` | |
| `deedglass.approval.*` | `id`, `outcome`, `decided_by` (an array), `decided_at` | |
| `deedglass.execution.*` | `status`, `latency_ms`, `result_digest`, `error_code` | |
| `deedglass.check.*` | `witness.level`, `verdict`, `method`, `detail_digest` | the independent check: `VERIFIED`, `MISMATCH` or `UNVERIFIABLE` |
| `deedglass.cost.*` | `cost.amount`, `currency` | |
| resource `service.name` | fixed | `deedglass` |
| resource `deedglass.org.id` | `org` | |
| scope | fixed | `deedglass.export`, with the Deedglass version that wrote the file |

Every attribute is either in the pinned semantic-conventions registry (and not deprecated there) or
under the `deedglass.` namespace. CI checks that, the `execute_tool` requirements (the required
attributes, the span name, `error.type` exactly when the span ended in an error) and the OTLP/JSON
shape (hex ids, integer enums, 64-bit integers as decimal strings) on every span of a real release's
bundle.

## What is lost or approximated

- **The verdict is not the span status.** OpenTelemetry's status describes the call. A call that
  ran and succeeded but whose effect didn't match what the agent reported has an unset status and
  `deedglass.check.verdict` = `MISMATCH`. Alert on that attribute, not on the status.
- **A later check is a second span.** An amendment can't change a span already exported, so it
  becomes a child span of the action's span with the new verdict. A reader takes the latest one.
- **Trace context.** The ids are derived from Deedglass ids, not taken from the agent's own trace:
  a record doesn't carry the caller's W3C `traceparent`, so these spans don't join the agent's trace
  by themselves. Correlate on `gen_ai.tool.call.id` (the action id).
- **Admin records** (agent stopped, member invited, ledger pruned, and so on) have no GenAI span and
  are skipped; the count is printed.
- **No arguments or results.** `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` are never
  written: the record holds only their digests, which are in `deedglass.action.request_digest` and
  `deedglass.execution.result_digest`.
- **`gen_ai.tool.type` is approximate.** The conventions' three kinds don't include "a gateway ran it
  for the agent"; `extension` is the closest.
- **`gen_ai.conversation.id`** is the agent's own session reference, which may not be a
  conversation in the model provider's sense.
- **No model, provider or token counts.** Deedglass sees the tool call, not the model call.
- **The seal can't be checked from the span.** The attributes don't reproduce the record's
  canonical body, so `deedglass.record.seal` is there to find the record, not to check it. Check the
  record with `deedglass verify`.
- **Development status.** Attribute names may change in the GenAI conventions; a new pin regenerates
  the export from the records.
