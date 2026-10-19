# Exports: records as FHIR AuditEvent and Provenance

Deedglass can write its sealed records as FHIR R4 resources, so a hospital's audit tooling can read
them next to its EHR's own audit log:

- **AuditEvent**: one per FHIR interaction an agent asked for (allowed, denied, failed or unknown),
  shaped to the IHE Basic Audit Log Patterns (BALP) 1.1.4 profiles.
- **Provenance**: one per FHIR write that ran and succeeded, shaped to the AI Provenance profile of
  the HL7 AI Transparency on FHIR ballot (1.0.0-ballot, January 2026).

An export is a view. The sealed record stays the primary form; its format knows nothing about FHIR
(DL-109), and `deedglass verify` checks the record, not the export. Exports hold ids and digests,
never resources, search values or notes.

## Run it

```sh
deedglass deeds export --format fhir-auditevent --out audit.ndjson
deedglass deeds export --format fhir-provenance --since 24h --out provenance.ndjson
deedglass deeds export --from deedglass-org_…-1-500.tar.gz --format fhir-auditevent   # offline
```

```http
GET /v1/deeds/export?format=fhir-auditevent&from_seq=1&to_seq=500
```

The output is FHIR NDJSON (`application/fhir+ndjson`): one resource per line, as FHIR Bulk Data
writes it. The range flags (`--from-seq`, `--to-seq`, `--since`) and limits are those of
`deeds export`. The API route needs a Console session or an org admin key, like every export.

From the database (the CLI) or the API, each FHIR connector call is joined to its action row and
its connector, so the export names the resource and version, the FHIR server and the SMART client
id. `--from` reads a bundle or a `.jsonl` file with no database: each record alone, so those three
are unknown and the action id stands in for the resource. It doesn't check seals; run
`deedglass verify` on the file first.

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

## What is lost

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
