# Export: W3C PROV-JSON

`deedglass export --format prov-json` writes the records as one W3C PROV document in the PROV-JSON
serialization, so a provenance store or a research tool can follow who did what, for whom, under
which plan, and what each record says about it. Overview and the other formats:
[EXPORTS.md](../EXPORTS.md).

## Pinned version

| What | Version | Date |
|---|---|---|
| PROV-DM (the data model) | W3C Recommendation | 2013-04-30 |
| PROV-JSON (the serialization) | W3C Member Submission | 2013-04-24 |

PROV-JSON is a Member Submission, not a Recommendation; it is the JSON form PROV tools read
(ProvToolbox, the Python `prov` package). Unlike the other formats, the export is one JSON object,
written when every record has been read.

## Mapping

Every id is a qualified name under the prefix `dg` (`urn:deedglass:`), with characters a PROV
local name can't hold percent-escaped (`usr_ref:priya` → `dg:obo-usr_ref%3Apriya`).

| PROV | From the record | Notes |
|---|---|---|
| `agent` `dg:agt_…`, `prov:SoftwareAgent` | `agent.id`, `agent.name` (as `prov:label`) | |
| `agent` `dg:deedglass`, `prov:SoftwareAgent` | fixed | the issuer of the records |
| `agent` `dg:obo-…`, `prov:Person` | `on_behalf_of` | the opaque reference as recorded (`dg:reference`) |
| `agent` `dg:usr_…`, `prov:Person` | `approval.decided_by` | the people who decided an approval |
| `activity` `dg:act_…`, type `dg:ToolCall` | `action` | `prov:startTime`/`prov:endTime` from the execution; the tool, mode, connector, policy decision, rule and mode, approval, execution status and error code, cost and session as `dg:` attributes |
| `wasAssociatedWith` | the agent and the activity | with `prov:plan` the policy version |
| `entity` `dg:pv_…`, `prov:Plan` | `policy.version_id` | `dg:versionHash`, `dg:policy` |
| `actedOnBehalfOf` | `on_behalf_of` | `prov:delegate` the agent, `prov:responsible` the person, `prov:activity` the call |
| `wasInfluencedBy` (`dg:role` approver) | `approval.decided_by` | the approver influenced the call |
| `entity` `dg:sha256-…`, type `dg:Request`; `used` with `prov:role` `dg:request` | `action.request_digest` | the request, named by its digest |
| `entity` `dg:sha256-…`, type `dg:Result`; `wasGeneratedBy` at `finished_at` | `execution.result_digest` | the result, named by its digest |
| `entity` `dg:deed_…`, type `dg:Record` | the record | `dg:kind`, `dg:seq`, `dg:org`, `dg:issuedAt`, `dg:prevHash`, `dg:bodyHash`, `dg:seal`, `dg:sealKeyId`, `dg:specVersion`, and the check (`dg:checkLevel`, `dg:checkVerdict`, `dg:checkMethod`, `dg:checkDetailDigest`) |
| `wasAttributedTo` | the record and `dg:deedglass` | |
| `wasInfluencedBy` | the record and the activity it is about | |
| `wasDerivedFrom`, `prov:type` `prov:Revision` | `amends` | an amendment is a revision of the record it amends |
| `activity` `dg:adm_…`, type `dg:AdminChange` | `admin` | the change (`dg:action`), its target and data digest; `wasAssociatedWith` its actor (`dg:<type>-<id>`: a person for a user, a software agent for an agent, system or key) |

CI validates the document of a real release's bundle against a transcription of the PROV-JSON
schema (sections, required members, literals, the prefix pattern), and checks that every id is a
qualified name with a declared prefix, every relation names something the document defines, and
every time is an `xsd:dateTime`.

## What is lost or approximated

- **The verdict is an attribute.** PROV has no notion of "checked by a second source"; the check is
  `dg:checkVerdict` and friends on the record entity, and the record `wasInfluencedBy` the call.
- **What changed is not modelled.** The rows or resources the call changed aren't entities: the
  record holds only digests of the request and result, so those are the entities.
- **`on_behalf_of` may not be a person.** It is an opaque reference the agent passed along; the export
  types it `prov:Person` because that is what Deedglass asks callers to put there.
- **Policy and approval are attributes on the activity**, not separate activities: when the decision
  was taken is part of the record's `issued_at`, not a PROV time.
- **Order and chain.** PROV has no sequence; `dg:seq` and `dg:prevHash` carry the chain, but a PROV
  reader won't check it. `deedglass verify` does.
- **One document, in memory.** The export holds the whole document until the end (about 3 KB per
  record).
- **The seal can't be checked from the document.** Check the records with `deedglass verify`.
