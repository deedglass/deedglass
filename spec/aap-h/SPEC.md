# Agent Action Provenance for Health (AAP-H)

**Version:** 0.1.0-draft · **Date:** 2026-10-26 · **Status:** draft for comment; it will change.
**Licence:** this text is CC-BY-4.0; the schemas, StructureDefinitions and examples are Apache-2.0
(see [`README.md`](README.md)). **Editors:** Arjun Prabhakaran and the Deedglass contributors.
**Comments:** an issue or pull request on this repository, or the thread on chat.fhir.org.

The key words MUST, MUST NOT, SHOULD, SHOULD NOT and MAY are used as in RFC 2119 and RFC 8174, and
only in sections 3 to 9.

## 0. In one paragraph

When an AI agent proposes or makes a write to a health system (an EHR, a FHIR server, a payer API,
a claims database), AAP-H records five things about that one action: **which AI agent** did it and
with which model and tool; **what the policy decided**; **who approved it**, step by step; **what the
agent or tool claimed** happened; and **what was checked afterwards** by reading the system of record:
match, doesn't match, or can't check. The record is either a pair of FHIR R4 resources (a Provenance
and an AuditEvent, built on HL7 AI Transparency and IHE BALP) or a small JSON envelope for systems
that don't speak FHIR. At the top conformance level the record is signed and chained, so anyone with
the issuer's public keys can check it offline, without asking the vendor who wrote it.

## 1. Why

AI agents now prepare orders, write notes and submit prior authorisations through FHIR APIs and
payer gateways. Existing standards say where data came from (FHIR Provenance), that an access
happened (FHIR AuditEvent, IHE BALP), and that data was influenced by AI (HL7 AI Transparency, in
ballot). None of them records, for one agent action, the gap between what the agent said happened
and what the system of record shows, together with the rule and the people that let it through, in a
form a third party can check. [`RESEARCH.md`](RESEARCH.md) lists the sources and the gap.

A hospital, a payer or a regulator reviewing an AI agent's work asks four questions. AAP-H gives each
one a field:

| Question | AAP-H field (envelope · FHIR) |
|---|---|
| Was it an AI agent, and which one? | `agent` · the AI agent participant, a Device (AI Transparency AI-Device) |
| Was it allowed, and by whom? | `policy`, `approvals`, `authorization` · extensions `aaph-policy-decision`, `aaph-approval`, `aaph-authorization` |
| Did it do what it said? | `claimed` and `verified` · extensions `aaph-claimed-outcome`, `aaph-verified-outcome` |
| Can I check this record without trusting whoever handed it to me? | `chain`, `seal` · extension `aaph-record-seal`, `Provenance.signature` |

## 2. Scope

**In scope:** one tool call by an AI agent against a system that holds health or claims data: reads,
searches, creates, updates, deletes, operations (`$submit`), and calls refused before they ran.

**Out of scope:** whether the AI's output is clinically right (AAP-H never judges content); model
evaluation and model cards (AI Transparency and its model card cover those); the agent's prompts and
reasoning; transport security; how a policy engine decides. AAP-H records digests of requests and
results, never the clinical content itself.

## 3. Terms

| Term | Meaning |
|---|---|
| **Action** | One tool call an AI agent proposed or made. |
| **AI agent** | Software that chooses tool calls using a machine-learning model. A person using AI assistance who then makes the call themselves is not an AI agent for this spec. |
| **Gateway** | The component that sees the call, applies the policy, runs or forwards it, and writes the record. It may be part of the agent platform, the EHR or a separate service. |
| **System of record** | The system the action changed or read: a FHIR server, a database, a payer API. |
| **Claimed outcome** | What the tool's response, or the agent, said happened. |
| **Verified outcome** | What the gateway found when it checked the system of record after the action. |
| **Check depth** | How the verified outcome was obtained: `reported` (only the tool's own answer), `read-back` (a separate read of the system of record), `change-log` (the system's own change stream or history, bound to this action). |
| **Record** | The AAP-H data about one action: an envelope (section 6), or FHIR resources (section 5) that are views of an envelope or of a native signed record. |
| **Issuer** | The party that writes and signs records, named by an https URI. |
| **Stream** | An issuer's ordered sequence of records, numbered by `seq` and linked by `prev`. |
| **Native record** | A signed record in an implementation's own format (for example a Deedglass deed) that an envelope is derived from. |

## 4. The record model

Every record describes one action and carries these parts. The envelope (section 6) and the FHIR
profiles (section 5) are two encodings of the same model.

| Part | Required | Content |
|---|---|---|
| Identity | always | `record_id`, `issuer`, `recorded_at` |
| Agent | always | `kind` (`ai-agent`), agent `id` and `name`, optional model (`provider`, `name`, `version`), opaque `on_behalf_of` and `session` references |
| Action | always | action `id`, `tool`, `interaction`, `agent_role` (`proposed` or `made`), the system (`kind`, `base`), targets, `request_digest`, optional `intent_digest` |
| Authorization | when known | what the agent was allowed to do: SMART scopes, a consent artefact reference, a grant |
| Policy | always, except in a record that only supersedes another's verified outcome (then `null`) | `decision` (`allow`, `deny`, `require-approval`), `mode` (`enforce`, `shadow`), the policy, its version digest and the rule |
| Approvals | when the decision is `require-approval` | one entry per step: `outcome`, `approvers`, `role`, `decided_at` |
| Claimed outcome | when the action ran, or its outcome is unknown | `status` (`succeeded`, `failed`, `unknown`), times, `result_digest`, `error_code`, optional `assertion` |
| Verified outcome | when a check ran | `result` (`match`, `mismatch`, `cannot-check`), `depth`, `method`, `detail_digest`, `checked_at` |
| Supersedes | for a later verdict | the `record_id` of the record whose verified outcome this one replaces |
| Chain | L2 and up | `stream`, `seq`, `prev` |
| Seal | L3 | a signature over the record, or the native signed record it is derived from |

Rules that apply to every encoding:

1. A record MUST be written for every action the gateway decides, including refused ones
   (`decision: deny`) and ones that waited for approval and never ran.
2. `claimed` MUST come from the tool's response or the agent, never from the check. `verified` MUST
   come from the gateway's own check, never be copied from the tool's response. A check that could
   not run, or could not reach a conclusion, is `cannot-check`, never `match`.
3. `result: match` MUST NOT be written at `depth: reported` for a write. A tool's own success answer
   is a claim, not a check; record it in `claimed` and, if nothing else was checked, write
   `verified.result: cannot-check` at `depth: reported`.
4. A later check (a change-log entry that arrives after the record was written) is a new record with
   `supersedes` naming the earlier one. Records are never edited. A reader takes the latest record
   in the stream that supersedes a given record as its current verified outcome.
5. Digests are `sha256:` followed by 64 lowercase hex digits, of the RFC 8785 (JCS) form of the JSON
   value. Records hold digests of requests, results and check details, never the payloads.
6. `on_behalf_of`, `session` and approver references SHOULD be opaque (`usr_ref:4821`), never names,
   emails or patient identifiers. A patient reference appears only where the deployment allows it
   (section 10).
7. `agent_role: proposed` means the agent drafted the action and a person or another system made the
   call; `made` means the agent's call was executed (by the gateway or the agent itself).

## 5. FHIR R4 profiles

**Canonical base (draft):** `https://deedglass.com/fhir/aap-h`. Files: [`fhir/`](fhir/). FHIR
version 4.0.1. If an HL7 work group adopts this work, the canonicals move to an HL7 namespace and this
draft's URLs stay as aliases (see `README.md`).

### 5.1 Profiles

| Profile | Base | Use |
|---|---|---|
| `aaph-provenance` (`…/StructureDefinition/aaph-provenance`) | Provenance | One per action that **created, updated or deleted** a resource (`agent_role: made`, claimed status `succeeded` or `unknown`). Targets the resource version written. SHOULD also claim HL7 AI Transparency `AI-Provenance` in `meta.profile`. |
| `aaph-auditevent` (`…/StructureDefinition/aaph-auditevent`) | AuditEvent | One per action, **whatever the outcome**, including refused calls and reads. For a successful RESTful interaction it SHOULD also claim the matching IHE BALP profile (section 7). |

A Provenance alone can't record a refused call (there is no target); an AuditEvent alone can't be
the provenance of the written resource. An implementation that writes FHIR at L1 MUST write the
AuditEvent for every action and the Provenance for every write that ran.

### 5.2 Participants

| Who | Provenance.agent | AuditEvent.agent |
|---|---|---|
| The AI agent | slice `aiAgent`, 1..1: `type` = provenance-participant-type `author` (as AI Transparency does); `role` includes `aaph-participant#ai-agent`; `who` = a Device (contained, AI-Device profile: `type` = `AIdeviceTypeCS#Artificial-Intelligence`, `identifier` = agent id, `deviceName` = agent name, `version` = model version) | slice `aiAgent`, 1..1: `role` includes `aaph-participant#ai-agent`; `requestor` = true; `who` = the same Device |
| The gateway | slice `gateway`, 0..1: `type` = `assembler`; `role` = `aaph-participant#gateway` | the BALP client agent (DCM 110153) or `source.observer` |
| The checker | slice `checker`, 0..*: `type` = `verifier`; only when `verified.result` is `match` at depth `read-back` or `change-log` | not repeated; the verified outcome extension carries it |
| An approver | slice `approver`, 0..*: `type` = `aaph-participant#approver`; `who` = a Practitioner, PractitionerRole or an identifier-only reference | slice `approver`, 0..*: `role` = `aaph-participant#approver`; `requestor` = false |
| The person the agent acted for | `aiAgent.onBehalfOf` | not repeated; `on_behalf_of` |

Which HL7 code best names "a person approved an AI-proposed action" is an open question (section 13);
this draft uses its own code with an extensible binding.

### 5.3 Extensions

All are complex extensions on both profiles' root (Provenance or AuditEvent). Full definitions:
`fhir/StructureDefinition-aaph-*.json`.

| Extension | Cardinality | Sub-extensions (type) | Envelope member |
|---|---|---|---|
| `aaph-action` | 1..1 | `actionId` (string) 1..1, `tool` (string) 1..1, `interaction` (code, `aaph-interaction`) 1..1, `agentRole` (code, `aaph-agent-role`) 1..1, `systemKind` (code, `aaph-system-kind`) 1..1, `systemBase` (uri) 0..1, `requestDigest` (string) 1..1, `intentDigest` (string) 0..1 | `action` |
| `aaph-policy-decision` | 1..1 | `decision` (code, `aaph-policy-decision`) 1..1, `mode` (code: enforce, shadow) 1..1, `policy` (string) 0..1, `versionDigest` (string) 0..1, `rule` (string) 0..1 | `policy` |
| `aaph-approval` | 0..* | `step` (positiveInt) 1..1, `outcome` (code, `aaph-approval-outcome`) 1..1, `approver` (Reference or Identifier) 0..*, `role` (string) 0..1, `decidedAt` (dateTime) 0..1 | `approvals[]` |
| `aaph-authorization` | 0..* | `kind` (code, `aaph-authorization-kind`) 1..1, `ref` (string) 1..1, `scope` (string) 0..1, `digest` (string) 0..1 | `authorization[]` |
| `aaph-claimed-outcome` | 0..1 | `status` (code, `aaph-claimed-status`) 1..1, `assertion` (code) 0..1, `resultDigest` (string) 0..1, `errorCode` (string) 0..1, `started` (dateTime) 0..1, `finished` (dateTime) 0..1 | `claimed` |
| `aaph-verified-outcome` | 0..* | `result` (code, `aaph-verification-result`) 1..1, `depth` (code, `aaph-check-depth`) 1..1, `method` (string) 1..1, `detailDigest` (string) 0..1, `checkedAt` (dateTime) 0..1, `supersedes` (string) 0..1 | `verified` (and `supersedes`) |
| `aaph-record-seal` | 0..1 (1..1 at L3) | `recordId` (string) 1..1, `issuer` (uri) 1..1, `recordDigest` (string) 1..1, `format` (code, `aaph-seal-format`) 1..1, `keyId` (string) 0..1, `keySet` (uri) 0..1, `stream` (string) 0..1, `seq` (positiveInt) 0..1 | `seal`, `chain` |

### 5.4 Code systems

Each has a ValueSet of the same name that includes the whole system (`fhir/CodeSystem-*.json`,
`fhir/ValueSet-*.json`).

| CodeSystem | Codes |
|---|---|
| `aaph-verification-result` | `match` (the system of record shows what was claimed), `mismatch` (it shows something else, or nothing), `cannot-check` (no conclusion: no read access, the read failed, the system can't be read back, or the check found a gap) |
| `aaph-check-depth` | `reported` (the tool's own answer only), `read-back` (a separate read of the system of record after the action), `change-log` (the system's own change stream or history, bound to this action) |
| `aaph-policy-decision` | `allow`, `deny`, `require-approval` |
| `aaph-approval-outcome` | `approved`, `denied`, `expired`, `cancelled` |
| `aaph-claimed-status` | `succeeded`, `failed`, `unknown` |
| `aaph-agent-role` | `proposed`, `made` |
| `aaph-interaction` | `create`, `read`, `update`, `patch`, `delete`, `search`, `operation`, `execute`, `other` |
| `aaph-system-kind` | `fhir`, `postgres`, `saas`, `other` |
| `aaph-authorization-kind` | `smart-scope`, `consent`, `grant`, `other` |
| `aaph-participant` | `ai-agent`, `gateway`, `approver`, `checker` |
| `aaph-seal-format` | `aaph-jcs` (section 8.2), `deedglass-deed` (section 8.3), `other` |
| `aaph-policy-mode` | `enforce`, `shadow` |

### 5.5 Invariants

| Key | Severity | On | Rule (FHIRPath, in the StructureDefinitions) |
|---|---|---|---|
| `aaph-1` | error | both | a decision of `require-approval` has at least one `aaph-approval` |
| `aaph-2` | error | both | a verified outcome of `match` is never at depth `reported` when the interaction is a write (`create`, `update`, `patch`, `delete`, `operation`, `execute`) |
| `aaph-3` | warning | both | a claimed status of `succeeded` for a write has a verified outcome |
| `aaph-4` | error | Provenance | exactly one agent has role `aaph-participant#ai-agent` and its `who` is a Device |

### 5.6 What goes where in the base resources

**Provenance:** `target` = the resource version the server reported (`Observation/123/_history/2`);
`occurredPeriod` = claimed `started`/`finished`; `recorded` = `recorded_at`; `policy` = the policy
reference and, for a consent, the consent artefact reference as a URI; `reason` SHOULD include
AI Transparency `AddedProvenanceCS#AIRPT` (or `CLINAST_AIRPT` when a clinician approved the content
before it was written); `activity` = `v3-DataOperation` CREATE, UPDATE or DELETE; `entity` (role
`source`) = the request digest as an identifier; `signature` = section 8.4.

**AuditEvent:** `type` = `audit-event-type#rest` for FHIR RESTful calls, otherwise DCM `110100`
(Application Activity); `subtype` = the FHIR `restful-interaction` code when FHIR; `action` = C, R, U,
D or E; `recorded` = `recorded_at`; `outcome` = `0` when the action ran and the claimed status is
`succeeded`, `4` when it was refused or failed, `8` when the outcome is unknown; `outcomeDesc` = one
plain sentence (decision, approval, claimed status, verified result); `agent.policy` = consent or token
references; `entity` = the target, and for searches the request digest in `query` (never the search
values).

## 6. The JSON envelope

For systems that don't write FHIR (a Postgres-backed product, a SaaS API, a claims engine), the same
record is a JSON object. Schema: [`schema/aaph-envelope.schema.json`](schema/aaph-envelope.schema.json)
(JSON Schema 2020-12). The schema is closed: unknown members are refused, except inside `extensions`.

```json
{
  "aaph": "0.1",
  "record_id": "rec-01",
  "issuer": "https://agent-gateway.example",
  "recorded_at": "2026-10-20T09:15:02.311Z",
  "agent": { "kind": "ai-agent", "id": "agt-claims-assist", "name": "claims-assist",
             "model": { "provider": "example-ai", "name": "example-model", "version": "2026-09" },
             "on_behalf_of": "usr_ref:billing-desk-07", "session": "ses-41c2" },
  "action": { "id": "act-01", "tool": "fhir.create", "interaction": "create", "agent_role": "made",
              "system": { "kind": "fhir", "base": "https://fhir.hospital.example/r4" },
              "target": [ { "ref": "Bundle/preauth-1001", "version": "1" } ],
              "request_digest": "sha256:…", "intent_digest": null },
  "authorization": [ { "kind": "smart-scope", "ref": "client:claims-assist", "scope": "system/Bundle.c", "digest": null } ],
  "policy": { "decision": "require-approval", "mode": "enforce", "policy": "pol-claims",
              "version_digest": "sha256:…", "rule": "preauth-two-person" },
  "approvals": [ { "step": 1, "outcome": "approved", "approvers": ["usr_ref:billing-lead-01"], "role": "billing", "decided_at": "…" } ],
  "claimed": { "status": "succeeded", "assertion": null, "result_digest": "sha256:…", "error_code": null,
               "started_at": "…", "finished_at": "…" },
  "verified": { "result": "match", "depth": "read-back", "method": "readback.fhir.create",
                "detail_digest": "sha256:…", "checked_at": "…" },
  "supersedes": null,
  "chain": { "stream": "demo-india-hospital", "seq": 1, "prev": "sha256:…" },
  "seal": { "format": "aaph-jcs", "alg": "Ed25519", "key_id": "aaph-test-1",
            "key_set": "https://agent-gateway.example/.well-known/aaph/keys.json",
            "record_digest": "sha256:…", "signature": "ed25519:…" },
  "extensions": {}
}
```

Member rules beyond the schema:

- Every member in the example is present in every envelope, with `null` (or `[]` for arrays) where a
  part doesn't apply. Presence is what lets a reader tell "not recorded" from "not applicable".
- `recorded_at` and every time are RFC 3339 in UTC with `Z`, at most 9 fractional digits.
- `record_id`, `agent.id`, `action.id` and `chain.stream` match `^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`.
- `claimed.assertion` is a short code (`^[a-z][a-z0-9-]{0,63}$`) for what the agent said the effect
  was, when that differs from a plain success: `payer-approved`, `order-placed`, `record-shared`.
  Free text never goes in a record.
- `extensions` is an object for implementation-specific members. It is covered by the seal. A
  verifier MUST NOT treat anything in `extensions` as changing the meaning of the standard members.
- Numbers are integers (`chain.seq`, `approvals[].step`) between 1 and 2^53−1.

## 7. Mapping to IHE BALP

An `aaph-auditevent` for a successful FHIR RESTful interaction is also a BALP AuditEvent. The table
lists what BALP 1.1.4 asks for and where AAP-H puts it.

| BALP profile (canonical `https://profiles.ihe.net/ITI/BALP/StructureDefinition/…`) | When AAP-H claims it | BALP element | AAP-H source |
|---|---|---|---|
| `IHE.BasicAudit.Create` / `PatientCreate` | interaction `create`, claimed `succeeded`, decision allowed or approved | `type` rest, `subtype` create, `action` C, `outcome` 0 | `action.interaction`, `claimed.status` |
| `IHE.BasicAudit.Read` / `PatientRead` | `read`, succeeded | `subtype` read/vread, `action` R | same |
| `IHE.BasicAudit.Update` / `PatientUpdate` | `update` or `patch`, succeeded | `subtype` update/patch, `action` U | same |
| `IHE.BasicAudit.Delete` / `PatientDelete` | `delete`, succeeded | `subtype` delete, `action` D | same |
| `IHE.BasicAudit.Query` / `PatientQuery` | `search`, succeeded | `subtype` search, `action` E, `entity.query` | the request digest, never the search values |
| (Patient variants) | only when a patient reference may be recorded in the clear (section 10) | `entity` type 1 role 1 | the patient the target names |
| client agent (DCM 110153) | always | `agent.type`, `agent.who`, `agent.network` | the gateway; its SMART `client_id` when it uses SMART Backend Services |
| server agent (DCM 110152) | always | `agent.who` | `action.system.base` |
| user agent | always | `agent.who`, `requestor` = true | the AI agent's Device (slice `aiAgent`) |
| `source.observer` | always | `source` | the gateway |
| `IHE.BasicAudit.AuthZconsent` | when a consent or grant decided access | `subtype`, `entity` | `authorization[]` of kind `consent` or `grant` |

**Not covered by BALP**, so no BALP profile is claimed: a refused call (`deny`), a call that waited
and never ran, a failed or unknown outcome (BALP fixes `outcome` to success), and FHIR `history`
interactions. These are still `aaph-auditevent` resources.

## 8. Signing and the chain

### 8.1 What is signed

The signed object is always a canonical JSON record: the envelope (8.2), or a native record the
envelope is derived from (8.3). FHIR resources are never the signed object; they point to it (8.4).
This avoids depending on a canonical form for FHIR JSON, which FHIR doesn't fix.

### 8.2 `aaph-jcs`: the envelope signs itself

1. **Strict JSON.** The envelope MUST be valid UTF-8, with no duplicate member names, integers only
   (no fraction, exponent or minus sign, at most 2^53−1), no lone surrogate escapes, nesting at most
   16 levels. A verifier MUST refuse, never repair, input that fails this.
2. **Record digest.** `record_digest` = `"sha256:" + lowercase_hex(SHA-256(JCS(E)))`, where `E` is
   the envelope with the `seal` member removed and JCS is RFC 8785.
3. **Signature input.** The ASCII bytes `"aap-h/record/v0.1/" + tag + "\n" + record_digest`, where
   `tag` is `ed25519` for `alg: Ed25519` and `ml-dsa-65` for `alg: ML-DSA-65`.
4. **Signature.** `signature` = `tag + ":" + base64url_nopad(sig)`. Ed25519 is pure Ed25519 (RFC
   8032 §5.1); ML-DSA-65 is FIPS 204 ML-DSA.Sign, pure, with the empty context.
5. **Key set.** The issuer publishes its keys at `key_set` (an https URL), and a verifier MAY hold a
   copy obtained earlier. A key set is a JSON object `{ "issuer": <uri>, "keys": [ … ] }`; each key
   has `id`, `alg`, `public_key` (base64url of the raw key: 32 bytes for Ed25519, 1,952 for
   ML-DSA-65), `status` (`active`, `retired`, `revoked`), `activated_at`, `retired_at`, `revoked_at`.
   Other members are allowed and ignored. The Deedglass `keys.json` (record spec §10) is a valid key
   set.

### 8.3 `deedglass-deed` (and other native formats)

`seal.native` holds the native signed record as a JSON value and `seal.record_digest` its own digest
(for a Deedglass deed, its `body_hash`). The envelope's other members are derived from the native
record by a published mapping (section 11 for Deedglass). Verification checks the native record by
its own spec, then checks that the envelope says what the native record says. Members the mapping
doesn't derive (for Deedglass: `issuer`, `agent.model`, `action.system.base`, `action.target`,
`authorization`, `verified.checked_at`, `claimed.assertion`, `extensions`) are **not covered** by the
seal, and a verifier MUST say so when it reports the result. Another native format registers its own
`format` code and mapping.

### 8.4 FHIR resources point to the signed record

An `aaph-provenance` or `aaph-auditevent` at L3 carries `aaph-record-seal` with the record id, the
issuer, the record digest, the format, the key id and the key set. `Provenance.signature` MAY also
carry the signature: `type` = `1.2.840.10065.1.12.1.14` (Source Signature), `when` = `recorded_at`,
`who` = the gateway, `targetFormat` = `application/json`, no `sigFormat`, and `data` = the raw
signature bytes (base64). Those bytes sign the record digest of section 8.2 or 8.3, not the
Provenance resource, so a verifier checks them against the sealed record, never against the
Provenance's own JSON.

### 8.5 The chain

Each issuer keeps one or more streams. In a stream, `seq` runs 1, 2, 3… with no gaps. `prev` of
seq 1 is `"sha256:" + hex(SHA-256("aap-h/genesis\n" + stream))`; `prev` of seq n is the
`record_digest` of seq n−1. `recorded_at` MUST NOT decrease along `seq`. For `deedglass-deed`,
`stream` is the org, `seq` and `prev` are the deed's `seq` and `prev_hash`, and the genesis value is
the deed spec's.

A chain shows a changed, inserted, reordered or removed record anywhere before the last one a
reader holds. It can't show that records were removed from the end. Readers keep the last `seq` and
`record_digest` of every set they receive.

## 9. Verification rules

A third party can check a record with the record, the issuer's key set, and nothing else: no
network call and no account with the issuer.

### 9.1 One envelope

Run these in order; the first failure is the result.

| # | Check | Code |
|---|---|---|
| 1 | Strict JSON (8.2 step 1); top level is an object; at most 64 KiB (`aaph-jcs`) or 128 KiB (native) | `malformed` |
| 2 | `aaph` is a version this verifier reads (`0.1`) | `unsupported_version` |
| 3 | The envelope schema | `schema_invalid` |
| 4 | `seal` is not null | `not_sealed` (a valid L1 or L2 record, reported as unsigned, not as a failure of the record) |
| 5 | `seal.format` is one this verifier implements | `unsupported_format` |
| **aaph-jcs** | | |
| 6 | The recomputed record digest equals `seal.record_digest` | `digest_mismatch` |
| 7 | `seal.alg` is one this verifier implements | `unsupported_algorithm` |
| 8 | The key set holds `seal.key_id` | `key_unknown` |
| 9 | That key's `alg` equals `seal.alg` | `signature_invalid` |
| 10 | The key is not revoked | `key_revoked` |
| 11 | `activated_at ≤ recorded_at < retired_at` (open when null) | `key_not_valid_at` |
| 12 | The signature verifies over the signature input (Ed25519 refusing non-canonical `S` and `R`) | `signature_invalid` |
| **native** | | |
| 6n | The native record verifies by its own spec (a Deedglass deed: record spec §11, with the issuer's `keys.json`) | `native_invalid` (with the native code) |
| 7n | `seal.record_digest` equals the native record's digest | `digest_mismatch` |
| 8n | Every member the mapping derives equals the value derived from the native record | `native_mismatch` (with the member path) |

A verifier MUST check every key in a key set it is given (public key length and encoding; for
Ed25519, refuse small-order and non-canonical points) and refuse the whole set on a bad key
(`key_set_rejected`).

### 9.2 A stream

For records that passed 9.1, in input order, grouped by `chain.stream`:

| # | Check | Code |
|---|---|---|
| 1 | All records in the group share the issuer | `chain_issuer_mismatch` |
| 2 | The first record's `seq` is 1 and `prev` is the genesis value (or the anchor the reader holds) | `chain_genesis_mismatch` / `chain_seq_gap` |
| 3 | Each later `seq` is the previous plus 1 | `chain_seq_gap` / `chain_seq_out_of_order` |
| 4 | Each `prev` equals the previous `record_digest` | `chain_prev_mismatch` |
| 5 | `recorded_at` doesn't decrease | `chain_time_regression` |

### 9.3 A FHIR resource

1. Read `aaph-record-seal`. Without it, the resource is an L1 or L2 view and is reported as unsigned.
2. Get the sealed record it names (from the same export, by `recordId`) and check it by 9.1 and 9.2.
3. Check that the resource's AAP-H extensions and the base elements listed in section 5.6 equal the
   sealed record's values. A difference is `view_mismatch` with the element path. Elements AAP-H
   doesn't map are not covered.

### 9.4 What a valid result means, and what it doesn't

A valid record means: the issuer holding that key wrote these values at that place in its stream,
and nothing covered by the seal changed since. It doesn't mean the AI's output was right, that the
issuer checked well, or that the issuer recorded every action: a gateway sees only the calls that go
through it. Whoever holds the signing key can sign false records; key custody, rotation and
revocation are the issuer's job and part of what a reviewer should ask about.

## 10. Privacy

- Records hold digests, ids, codes and times. They MUST NOT hold clinical values, notes, search
  values, prompts or model outputs.
- Digests of short or guessable values (a yes/no, a small amount, a patient identifier) can be
  reversed by trying values. Don't digest a bare patient identifier; when a patient must be linked,
  use a reference the deployment already treats as protected, or a keyed digest held by the
  deployment.
- In a deployment that keeps patient references out of audit data, the AuditEvent uses the
  non-Patient BALP profiles and the target is the resource reference only.
- Approver and person references SHOULD be opaque, resolved inside the deployment.

## 11. Mapping from a Deedglass deed

Deedglass is the reference implementation. A Deedglass action deed (record spec, edition 2,
`spec/v2/DEED_SPEC.md`) converts to an envelope with `seal.format: deedglass-deed`:

| Envelope | From the deed body | Covered by the seal |
|---|---|---|
| `record_id` | `deed_id` | yes |
| `issuer` | the issuer's base URL (from its `keys.json`) | no |
| `recorded_at` | `issued_at` | yes |
| `agent.kind` | `ai-agent` (Deedglass records only agent calls) | yes |
| `agent.id`, `agent.name` | `agent.id`, `agent.name` | yes |
| `agent.model` | not recorded: `null` | no |
| `agent.on_behalf_of`, `agent.session` | `on_behalf_of`, `session` | yes |
| `action.id`, `action.tool`, `action.request_digest` | `action.id`, `action.tool`, `action.request_digest` | yes |
| `action.intent_digest` | `action.intent_hash` | yes |
| `action.interaction` | from the tool's last segment: `create`, `read`, `update`, `patch`, `delete`, `search`, `execute`; anything else `other` | yes |
| `action.agent_role` | `made` when `execution` is present or the deed is an amendment, else `proposed` | yes |
| `action.system.kind` | `postgres` for `postgres.*` tools, `fhir` for `fhir.*` tools, else `other` | yes |
| `action.system.base`, `action.target` | not in the deed: `null`, `[]` (the Deedglass database export knows them) | no |
| `authorization` | not in the deed: `[]` | no |
| `policy.decision` | `policy.decision` (`require_approval` → `require-approval`) | yes |
| `policy.mode`, `policy.rule` | `policy.mode`, `policy.rule_id` | yes |
| `policy.policy`, `policy.version_digest` | `policy.id`, `policy.version_hash` | yes |
| `approvals` | `approval` → one step: `step` 1, `outcome`, `approvers` = `decided_by`, `role` null, `decided_at` | yes |
| `claimed` | `execution`: `status`, `result_digest`, `error_code`, `started_at`, `finished_at`; `assertion` null | yes (`assertion` no) |
| `verified.result` | `witness.verdict`: `VERIFIED` → `match`, `MISMATCH` → `mismatch`, `UNVERIFIABLE` → `cannot-check` | yes |
| `verified.depth` | `witness.level`: `L0` → `reported`, `L1` → `read-back`, `L2` → `change-log` | yes |
| `verified.method`, `verified.detail_digest` | `witness.method`, `witness.detail_digest` | yes |
| `verified.checked_at` | not recorded separately: `null` | no |
| `supersedes` | `amends` (amendment deeds) | yes |
| `chain` | `stream` = `org`, `seq`, `prev` = `prev_hash` | yes |
| `seal` | `format` `deedglass-deed`, `record_digest` = `body_hash`, `native` = the deed envelope | (the seal itself) |

Admin deeds (people changing settings, Stop) aren't agent actions and don't convert. A Deedglass
`L0 · VERIFIED` deed for a write converts to `verified.result: match` at `depth: reported`, which
rule 3 of section 4 refuses for a write; the converter writes `cannot-check` at `reported` instead and
the consistency check (9.1 step 8n) allows exactly this one rewrite. Deedglass's own L0 `VERIFIED`
means "the connector returned success", which is a claim in AAP-H terms.

`spec/aap-h/aaph_test.go` converts every action deed in `testdata/deeds/` (v1 and v2), validates the
result against the schema, checks the native deed with the Go verifier, and runs the consistency
check, including on tampered copies.

## 12. Conformance levels

A product claims a level for a named set of actions (for example "FHIR writes through our gateway").
Each level includes the one below.

### L1 Record

- A record (envelope, or AuditEvent plus Provenance) for every action in the set, including refused
  calls and calls that never ran.
- The AI agent is identified as an AI agent, with its id, and its model when the gateway knows it.
- The policy decision, every approval step, the claimed outcome, and a verified outcome for every
  write that ran (which may be `cannot-check` at `reported`).
- Valid against the schema or the profiles; rules 1 to 7 of section 4.

### L2 Verify

- For writes to a system the gateway can read, the verified outcome comes from a read-back or the
  system's change log (`depth` `read-back` or `change-log`), made by the gateway after the action and
  bound to it. Where it can't be, the record says `cannot-check` with a `detail_digest` of why.
- Records are chained (`chain` present, section 8.5) and the issuer can export a stream with its
  record digests, so a third party can detect a changed or missing record.

### L3 Signed and approved

- Every record is sealed (`aaph-jcs` or a registered native format), and passes section 9 offline
  with the issuer's published key set. The issuer publishes key rotation and revocation in the key
  set and never removes a key.
- The policy can require approval for named actions, and when it does, each approval step, its
  approvers (opaque references allowed) and its time are inside the sealed content. Two-person
  approval is recorded as one step with two approvers, or two steps.
- FHIR views carry `aaph-record-seal`.

A conformance claim states the level, the action set, the spec version, and how a reviewer gets a
sample export and the key set. This draft has no test suite beyond the examples and schema in this
folder; a claim is the implementer's own statement, not an approval by anyone.

## 13. Open questions for reviewers

1. **Approver participant code.** Is there an HL7 code for "approved an AI-proposed action" that fits
   better than `aaph-participant#approver` (v3-ParticipationType `AUTHEN`? `LA`?).
2. **AI Transparency alignment.** Should `aaph-provenance` derive from `AI-Provenance` once that guide
   publishes STU1, instead of recommending both profiles side by side?
3. **R5.** R5 Provenance has `authorization` (CodeableReference); `aaph-authorization` maps to it.
   R5 AuditEvent's `outcome.detail` could carry the verified outcome. A full R5 profile set is future
   work.
4. **BALP for refused calls.** Would IHE consider a BALP pattern for refused or pending-approval
   RESTful calls, so audit tooling sees them consistently?
5. **Signature format.** Is a JWS or COSE_Sign1 encoding of the envelope worth adding beside
   `aaph-jcs`? Deedglass already offers COSE_Sign1 for its native records.
6. **Consent artefacts.** For ABDM, should `authorization` carry the consent artefact id only, or also
   the digest of the signed artefact the consent manager issued?
7. **Transparency logs.** Should L3 add an option to register record digests with a transparency
   service (IETF SCITT architecture, RFC 9943), so tail removal becomes visible too?

## 14. Examples

Synthetic data only. No real patient, person, payer or hospital.

| File | Case | Level shown |
|---|---|---|
| `examples/nhcx-preauth.envelope.json` | India: an AI agent prepares an NHCX pre-authorisation ClaimBundle on the hospital FHIR server under an ABDM consent artefact and SMART scope; two people approve; the read-back matches | L3 (`aaph-jcs`) |
| `examples/fhir/Provenance-nhcx-preauth.json`, `examples/fhir/AuditEvent-nhcx-preauth.json` | The same action as FHIR | L3 views |
| `examples/pas-submit.envelope.json` | US: an AI agent submits a Da Vinci PAS `Claim/$submit`; a clinician approves; the agent reports `payer-approved`, but the ClaimResponse read back shows the request pended: `mismatch` | L3 (`aaph-jcs`) |
| `examples/fhir/Provenance-pas-submit.json`, `examples/fhir/AuditEvent-pas-submit.json` | The same action as FHIR | L3 views |
| `examples/abdm-denied.envelope.json` | India: an AI agent tries to write a final OPConsultRecord without a clinician; the policy refuses; nothing runs | L1 (unsigned) |
| `examples/fhir/AuditEvent-abdm-denied.json` | The refused call as an AuditEvent (no BALP profile) | L1 |
| `examples/deedglass-native.envelope.json` | A real Deedglass test-vector deed (an approved, executed action) converted to an envelope | L3 (`deedglass-deed`) |
| `examples/keys.json` | The test key set for the `aaph-jcs` examples. **Test key only**: its seed is public (SHA-256 of `aap-h/test-key/1`) | |
| `examples/invalid/*.json` | Envelopes the schema must refuse | |

## 15. References

- HL7 FHIR R4 (4.0.1): Provenance, AuditEvent, Signature. https://hl7.org/fhir/R4/
- HL7 FHIR R5 (5.0.0): Provenance, AuditEvent. https://hl7.org/fhir/R5/
- IHE ITI Basic Audit Log Patterns 1.1.4 (2025-10-31). https://profiles.ihe.net/ITI/BALP/
- HL7 AI Transparency on FHIR 1.0.0-ballot (2025-12-09). https://hl7.org/fhir/uv/aitransparency/2026Jan/
- HL7 SMART App Launch 2.2.0 (2024-04-30). https://hl7.org/fhir/smart-app-launch/
- HL7 Da Vinci Prior Authorization Support 2.2.1 (2026-03-27). https://hl7.org/fhir/us/davinci-pas/
- NRCeS FHIR Implementation Guide for ABDM 6.5.0 (2025-05-08). https://nrces.in/ndhm/fhir/r4/
- W3C Verifiable Credentials Data Model v2.0, Recommendation (2025-05-15). https://www.w3.org/TR/vc-data-model-2.0/
- C2PA Technical Specification 2.2 (May 2025). https://spec.c2pa.org/
- RFC 8785 (JCS), RFC 8032 (Ed25519), FIPS 204 (ML-DSA), RFC 9052 (COSE), RFC 2119 and RFC 8174.
- Deedglass record spec, edition 2. [`../v2/DEED_SPEC.md`](../v2/DEED_SPEC.md)
