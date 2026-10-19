# Pinned schemas for the record exports (TY-06)

The tests in `internal/recordexport` validate every export against these files. Each pins one
version of a standard. They are transcriptions written for these tests (JSON Schema 2020-12 plus a
few rules files), not copies of the upstream files: the upstream sources were read on 2026-10-19
and the attribute names, types, requirements, enums and constraints the export can touch were
written down here. Nothing is fetched at test time.

| File | Standard and version | Source read | What it holds |
|---|---|---|---|
| `ocsf-1.9.0-api_activity.schema.json` | OCSF schema 1.9.0 (released 2026-08-03) | `https://schema.ocsf.io/api/1.9.0/classes/api_activity` and `/objects/{metadata,product,actor,user,api,service,request,response,network_endpoint,ai_agent,delegation,policy}`, the `ai_operation` profile | API Activity's 65 attributes, and those objects' full attribute lists (`additionalProperties: false`), required members, `at_least_one` constraints, enums. Objects the export never writes are plain objects |
| `ocsf-1.9.0-api_activity.rules.json` | same | same | the class's computed fields, enum captions, sibling strings and allowed profiles, which the Go checker applies |
| `prov-json-2013-04-24.schema.json` | W3C PROV-JSON, Member Submission of 2013-04-24 | `https://www.w3.org/Submission/prov-json/schema`, linked from `https://www.w3.org/submissions/prov-json/` | sections, required members of each relation, literals and typed literals, the prefix pattern. The original spells one key `wasEndedby`; the submission's text and this file use `wasEndedBy`. `format` keywords are checked by the Go test |
| `otel-semconv-1.41.0-genai.json` | OpenTelemetry semantic conventions v1.41.0 (2026-04-28), GenAI attributes, status Development | the `gen_ai.*` names exported by `@opentelemetry/semantic-conventions` 1.43.0 (npm, already a dev dependency of `web/`; its GenAI docs link v1.41.0), with their deprecations; the `execute_tool` span section of `docs/gen-ai/gen-ai-spans.md` at tag v1.41.0 | the attribute registry, the deprecated names with their replacement, the well-known `gen_ai.operation.name` values, the `gen_ai.tool.type` values, and the `execute_tool` span's required, conditionally required and recommended attributes |
| `otlp-1.11.1-trace.schema.json` | opentelemetry-proto v1.11.1 (2026-09-29), `trace/v1`, OTLP/JSON encoding | `trace.proto`, `common.proto`, `resource.proto` and the OTLP/JSON rules | `ExportTraceServiceRequest` as JSON: lowerCamelCase names, hex trace and span ids, integer enums, 64-bit integers as decimal strings |

## Why the GenAI conventions are pinned at v1.41.0

Releases after v1.41.0 mark every `gen_ai.*` attribute as moved to
`open-telemetry/semantic-conventions-genai`. That repository had no release on 2026-10-19, so v1.41.0
is the newest released definition. When it publishes one, update `otel-semconv-*.json`, the
constants in `recordexport.go`, the goldens and `docs/exports/otel-genai.md` together.

## Updating a pin

1. Read the new release's definitions and write the new file next to the old one (a new name: the
   version is in the file name).
2. Change the version constant in `recordexport.go`; the tests refuse a mismatch between the two.
3. `go test ./internal/recordexport -update`, review the golden diff, and update the format's
   mapping doc under `docs/exports/` (pinned version, date, what is lost).
4. Delete the old file in the same change.
