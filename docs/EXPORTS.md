# Exports: records in OpenTelemetry, OCSF, W3C PROV and FHIR

Deedglass can write its sealed records in four open formats, so the tools people already use (a
trace backend, a SIEM, a provenance store, a hospital's audit tooling) can read them:

| `--format` | Standard, pinned version | What you get | Mapping and what is lost |
|---|---|---|---|
| `otel-genai` | OpenTelemetry semantic conventions **v1.41.0** (2026-04-28), GenAI attributes (status Development); OTLP/JSON from opentelemetry-proto **v1.11.1** (2026-09-29) | one `execute_tool` span per action, a child span per later check, as OTLP/JSON lines | [exports/otel-genai.md](exports/otel-genai.md) |
| `ocsf` | OCSF schema **1.9.0** (2026-08-03): API Activity (6003), profiles `ai_operation` and `security_control` | one event per action or later check, NDJSON, with `ai_agent` and `delegation` | [exports/ocsf.md](exports/ocsf.md) |
| `prov-json` | W3C PROV-JSON, Member Submission of **2013-04-24** (PROV-DM, Recommendation 2013-04-30) | one document: agents, tool calls, records, who acted for whom, revisions | [exports/prov-json.md](exports/prov-json.md) |
| `fhir` (and `fhir-auditevent`, `fhir-provenance`) | FHIR **R4** AuditEvent (IHE BALP **1.1.4**) and Provenance (HL7 AI Transparency **1.0.0-ballot**) | AuditEvent and Provenance for FHIR connector calls, FHIR NDJSON | below |

An export is a view. The sealed record stays the primary form; its format knows nothing about any
of these standards (DL-109, the record spec in [`spec/v1/`](../spec/v1/DEED_SPEC.md)), and
`deedglass verify` checks the record, not the export. Exports hold ids and digests, never payloads,
resources, search values or notes. The same records always give the same bytes, so an export can be
compared or regenerated at any time.

## Run it

```sh
deedglass export deeds.tar.gz --format otel-genai        # → deeds.otlp.jsonl
deedglass export deeds.tar.gz --format ocsf --out -      # NDJSON to stdout
deedglass export deeds.jsonl --format prov-json --out records.prov.json
deedglass export deeds.tar.gz --format fhir              # AuditEvent, then Provenance, per record
```

`deedglass export` works offline on an export bundle (`deedglass deeds export`) or a `.jsonl` file of
records: it reads no config, database, key or network. It doesn't check seals, so run
`deedglass verify` on the file first; a bundle whose files don't match its manifest is refused, and a
failed export writes nothing. Each format's checks run in CI against the pinned schema files in
`internal/recordexport/testdata/schemas/` (what they are and where they come from is in that
directory's README), with golden files for a sealed bundle from a real release.

Sending spans to a collector (OTLP push) isn't built: the export writes a file, which an
OpenTelemetry Collector's `otlpjsonfile` receiver can read. A push, when it comes, goes through the
same egress door as every other outbound call and only when configured.

### FHIR from the database

```sh
deedglass deeds export --format fhir-auditevent --out audit.ndjson
deedglass deeds export --format fhir-provenance --since 24h --out provenance.ndjson
deedglass deeds export --from deedglass-org_…-1-500.tar.gz --format fhir-auditevent   # offline
```

```http
GET /v1/deeds/export?format=fhir-auditevent&from_seq=1&to_seq=500
```

The FHIR output is FHIR NDJSON (`application/fhir+ndjson`): one resource per line, as FHIR Bulk
Data writes it. The range flags (`--from-seq`, `--to-seq`, `--since`) and limits are those of
`deeds export`. The API route needs a Console session or an org admin key, like every export.

From the database (the CLI) or the API, each FHIR connector call is joined to its action row and
its connector, so the export names the resource and version, the FHIR server and the SMART client
id. `--from` and `deedglass export --format fhir` read a bundle or a `.jsonl` file with no database:
each record alone, so those three are unknown and the action id stands in for the resource.

## AuditEvent

| AuditEvent | From | Notes |
|---|---|---|
| `id` | the record's id, `_` replaced by `-` | `deed_01…` becomes `deed-01…` |
| `meta.profile` | the interaction and outcome | `IHE.BasicAudit.Create`, `Read`, `Update`, `Delete`, `Query`, or the `Patient…` variant when the call names a patient in the clear. None for a denied, failed or unknown call (BALP fixes `outcome` to success) and none for `history` (no BALP profile covers it) |
| `type` | fixed | `audit-event-type#rest` |
| `subtype` | the tool (`<connector>.create`, …) | `restful-interaction`: `create`, `read`, `vread`, `update`, `patch`, `delete`, `search`, `history-instance`, `history-type` |
| `action` | the interaction | C, R, U, D, E |
| `recorded` | `execution.finished_at`, else `issued_at` | |
| `outcome` | `execution.status` | `0` when it ran and succeeded; `4` otherwise |
| `outcomeDesc` | policy decision and rule, approval, execution, the check | "Deedglass: Policy allow by rule verify-levels; executed; check VERIFIED at L1 (readback.fhir.create)." |
| `agent` client (DCM 110153) | the connector | its OAuth 2.0 `client_id` when it uses SMART Backend Services; the connector id otherwise. `network` is the Deedglass server's base URL |
| `agent` server (DCM 110152) | the connector's `base_url` | `unknown` offline |
| `agent` user (`AUT`, requestor) | `agent.id`, `agent.name` | the AI agent, as a Device reference |
| `source.observer` | the Deedglass server | `security-source-type#4` |
| `entity` data (type 2, role 4) | the resource and version the server reported | `Observation/123/_history/2`; the action id when unknown |
| `entity` query (type 2, role 24) | `request_digest` | searches only: the query is the digest, never the search values |
| `entity` patient (type 1, role 1) | the Patient itself, the resource's `subject` or `patient`, or a `patient`/`subject` search parameter | only when stored in the clear; never in a PHI-mode org |
| `entity` transaction (`XrequestId`) | the binding token the connector sent | the last 16 characters of the action id (ADR-105) |
| `entity` record (type 4) | `deed_id`, `action.id`, `request_digest`, `result_digest`, the check | as `detail` |

ISO 27789's minimum fields are always present: the user (the agent), the function (`subtype`,
`action`), the time (`recorded`) and the subject (the resource reference; the patient when known).

## Provenance

| Provenance | From | Notes |
|---|---|---|
| `meta.profile` | fixed | AI Provenance |
| `contained` Device `#ai-agent` | `agent.id`, `agent.name` | AI Device profile, `AIdeviceTypeCS#Artificial-Intelligence` |
| `target` | the resource version the server reported | as AuditEvent's data entity |
| `occurredPeriod` | `execution.started_at`, `finished_at` | |
| `recorded` | `issued_at` | when the record was sealed |
| `policy` | `policy.version_id` and `rule_id` | `urn:deedglass:policy-version:pv_…#rule` |
| `reason` | fixed | `AddedProvenanceCS#AIRPT` (AI Reported) |
| `activity` | the interaction | `v3-DataOperation` CREATE, UPDATE, DELETE |
| `agent` author | the AI agent | the contained Device |
| `agent` assembler | the Deedglass server | |
| `agent` verifier | the Deedglass server | only when the check passed at L1 or L2 |
| `entity` (role `source`) | `request_digest` | what the agent sent, as a digest |
| `signature` | the record's seal | `data` is the Ed25519 signature over the record's canonical body; `deedglass verify` checks it with the seal key the record names |

## What the FHIR export loses

- **Content.** No resource, field value, search value, note or narrative: digests only.
- **The patient in PHI mode.** Its reference is a digest in the stored request, so the export uses
  the non-Patient BALP profiles. A salted patient digest is planned (HC-P09's second step).
- **Later verdicts.** An amendment (a later check result) has no resource of its own; the
  AuditEvent shows the verdict sealed with the action.
- **Admin records and non-FHIR tools.** Only FHIR connector interactions are exported.
- **Offline.** From a bundle alone, the resource, the server and the client id are unknown.
- **Validation.** CI validates every resource against HAPI FHIR's R4 StructureDefinitions, and
  checks the BALP and AI Transparency constraints the export relies on with an offline checker
  (`internal/fhirexport`); the full BALP and AI Transparency packages aren't loaded into a
  validator yet.
- **Ballot status.** The AI Transparency profiles are a ballot and may change before STU1.

## FHIR R5

R5 renames AuditEvent elements; the export writes R4 only. A mapping, when an R5 consumer asks:
`type` → `category`, `subtype` → `code`, `outcome` (a code) → `outcome.code` (a Coding),
`outcomeDesc` → `outcome.detail`, `recorded` stays, `agent.who` becomes required (already always
set), `entity.what` stays, `entity.detail.valueString` stays. Provenance is the same in R4 and R5
for the elements used here, apart from `occurred[x]` and `policy` keeping their names.
