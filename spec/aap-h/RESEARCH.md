# What exists, and the gap

> Licence: CC-BY-4.0. Part of Agent Action Provenance for Health (AAP-H) v0.1 draft. Informative,
> not normative. Checked 2026-10-26.

This note lists the standards an AAP-H record builds on, what each one already covers for AI agents
that write to health systems, and what none of them covers. Every version and date below comes from
the publisher's own page or package list. **[E]** means a primary source (the standards body or
publisher). **[S]** means a secondary source. **UNCONFIRMED** means we could not check it against
the primary text.

## 1. Summary

The pieces exist separately. FHIR has a resource for "where did this data come from" (Provenance) and
one for "what happened" (AuditEvent). IHE has profiled AuditEvent for RESTful access (BALP). HL7's AI
Transparency IG, now in ballot, says how to mark data as influenced by AI and how to name the AI
system that produced it. SMART scopes and ABDM consent artefacts say what an app was allowed to do.
W3C Verifiable Credentials and C2PA show how to sign a statement so a third party can check it
offline.

**No standard records, for one AI agent action on a health system, all of these together:**

1. that an AI agent (not a person, not a plain app) proposed or made the write, and which model and
   tool it used;
2. what the agent or tool *claimed* happened ("created", "submitted", "approved by the payer");
3. what was *checked afterwards* by reading the system of record, and whether it matched, didn't
   match or couldn't be checked;
4. which policy rule decided the call, and who approved it, step by step;
5. a signature and chain that let a third party check the record offline, without asking the
   vendor that wrote it.

AI Transparency covers part of 1. BALP covers the access event without 1 to 4. Provenance has a
`signature` element, but no common rule says what is signed or how a third party checks it. That
gap is what AAP-H fills.

## 2. Sources, one by one

| Source | Version and status | What it gives an AI-agent record | What it doesn't | Ref |
|---|---|---|---|---|
| HL7 FHIR R4 Provenance | R4 4.0.1, normative parts and trial-use parts | `target`, `occurred[x]`, `recorded`, `policy` (uri), `reason`, `activity`, `agent` (type, role, who, onBehalfOf), `entity` (role, what), `signature` (Signature) | No element for a claimed or verified outcome, a policy decision, or an approval step. The Signature datatype doesn't say what bytes a signature covers for a JSON resource | [E] hl7.org/fhir/R4/provenance.html |
| HL7 FHIR R5 Provenance | R5 5.0.0 | Adds `authorization` (CodeableReference, replacing R4 `reason`), `basedOn`, `patient`, `encounter`; entity role gains `instantiates`, loses `derivation` | Same gaps as R4. R5 says Provenance is written by the app that starts the create or update, AuditEvent as events happen | [E] hl7.org/fhir/R5/provenance.html |
| HL7 FHIR R4/R5 AuditEvent | R4 4.0.1; R5 5.0.0 | Who did what, when, the outcome code, `agent.requestor`, `agent.policy`, `source`, `entity` with `query` and `detail`. R5 renames `type`/`subtype` to `category`/`code` and makes `outcome` a structure | No AI agent type, no claimed-versus-checked outcome, no approval chain, no signature rule | [E] hl7.org/fhir/R4/auditevent.html; hl7.org/fhir/R5/auditevent.html |
| IHE ITI Basic Audit Log Patterns (BALP) | 1.1.4, trial implementation, 2025-10-31 (package list) | AuditEvent profiles for successful RESTful Create, Read, Update, Delete and Query, with and without a known Patient; OAuth and SAML token-use patterns; an AuthZ consent decision pattern; Privacy Disclosure. Extensions `ihe-assuranceLevel` and `ihe-otherId` | The profiles fix `outcome` to success, so a refused or failed agent call has no BALP profile. No AI agent, no check after the write, no approvals | [E] profiles.ihe.net/ITI/BALP/; …/artifacts.html; …/package-list.json |
| HL7 AI Transparency on FHIR | `hl7.fhir.uv.aitransparency#1.0.0-ballot`, STU1 ballot, generated 2025-12-09, FHIR 4.0.1; a CI build dated 2026-10-09 exists. Publisher listed as HL7 International / Electronic Health Records | Profiles AI-Provenance, AI-Device, AI-ModelCard (DocumentReference), AI-data; extensions for model card and kind of AI; codes `AIRPT` (AI reported), `CLINAST_AIRPT` (clinician asserted from AI reported), `AIAST_CLINRPT`; Device type `Artificial-Intelligence` | Its scope says it doesn't endorse or validate the AI and doesn't provide governance. It marks AI influence on data; it doesn't record an agent's tool call, a policy decision, an approval, a read-back check or a signature rule | [E] hl7.org/fhir/uv/aitransparency/2026Jan/; …/artifacts.html; …/package-list.json; [S] NIST summary (nist.gov/node/1919261) |
| SMART App Launch scopes | 2.2.0, STU 2.2, 2024-04-30 | The v2 scope grammar (`patient/Observation.cr`, `system/Claim.c`) and SMART Backend Services, which is how most server-side agents get a token | Says what an app may do, not what it did, or whether a person approved it | [E] hl7.org/fhir/smart-app-launch/ (package-list.json) |
| Da Vinci Prior Authorization Support (PAS) | 2.2.1, STU 2, 2026-03-27 | The `Claim/$submit` bundle, ClaimResponse, and inquiry; what a provider-side system sends to a payer | Nothing on AI agents preparing or submitting the request, or on who approved it | [E] hl7.org/fhir/us/davinci-pas/ (package-list.json) |
| CMS Interoperability and Prior Authorization final rule (CMS-0057-F) | Final rule; payer Prior Authorization APIs from 1 Jan 2027 | The date by which US payers must run the APIs that agents will call | Not a record format | [E] cms.gov CMS-0057-F page (as cited in `ops/healthcare/ADOPTION_STRATEGY.md`) |
| NRCeS FHIR IG for ABDM (incl. NHCX profiles) | 6.5.0, package `ndhm.in#6.5.0`, FHIR 4.0.1, 2025-05-08 | Record bundles (OPConsultRecord, PrescriptionRecord, …) and NHCX bundles (ClaimBundle, ClaimResponseBundle, CoverageEligibilityRequestBundle, TaskBundle, …) | No agent provenance or audit profile for AI writes | [E] nrces.in/ndhm/fhir/r4/ |
| ABDM consent artefact | ABDM Health Information Exchange and Consent Manager specs | A consent artefact (patient, HIP, HIU, purpose, HI types, permission with access mode, date range, erase date and frequency) signed by the consent manager | Covers data sharing between HIP and HIU, not the agent's own action. Field list and signing algorithm **UNCONFIRMED** against a primary ABDM text; we saw them only in developer-forum posts and generated client models | [S] devforum.abdm.gov.in threads; docs.cdpi.dev eConsent note |
| W3C Verifiable Credentials Data Model v2.0 | W3C Recommendation, 15 May 2025 | Issuer, holder, verifier model; a credential must be secured by at least one mechanism (Data Integrity proofs, or JOSE/COSE enveloping) | A general data model; no health action vocabulary. Useful pattern for issuer keys and offline checks | [E] w3.org/TR/vc-data-model-2.0/ |
| C2PA Technical Specification | 2.2 (May 2025); versions to 2.5 listed | Signed manifests of assertions; the claim is signed as COSE_Sign1; earlier manifests carried forward as ingredients; `digitalSourceType` marks algorithmically made content; offline validation | Built for media assets, bound by hashing the asset's bytes; no FHIR binding | [E] spec.c2pa.org/specifications/specifications/2.2/specs/C2PA_Specification.html |
| JCS, Ed25519, COSE | RFC 8785, RFC 8032, RFC 9052 | One canonical byte form for JSON, a signature scheme, and a CBOR signing envelope | | [E] rfc-editor.org |

## 3. What we looked for and didn't find

- An HL7 project that records an AI agent's tool call as an action with a policy decision, an
  approval chain and a later check. We found the AI Transparency IG and HL7's AI Office (launched
  July 2025, per a secondary source, **UNCONFIRMED**), but no profile for agent actions.
- A BALP profile for a refused, failed or pending-approval call. BALP fixes success.
- A shared rule for what a FHIR `Provenance.signature` covers. FHIR's Signature datatype carries
  bytes and a format; JSON signatures over FHIR resources depend on a canonical form FHIR itself
  doesn't fix for JSON.
- An ABDM or NHCX artefact for "an AI agent prepared this claim or record". None found.

## 4. What this means for AAP-H

- **Reuse first.** AAP-H Provenance SHOULD also claim AI Transparency's AI-Provenance profile, and
  the AI agent is an AI-Device. AAP-H AuditEvents for successful RESTful calls SHOULD also claim the
  matching BALP profile. AAP-H adds only what is missing.
- **Keep the signature outside FHIR's JSON.** The signed object is a small canonical JSON record (the
  envelope, or a native record such as a Deedglass deed). FHIR resources are views of it that point
  back to it, so a verifier checks one canonical form, not a FHIR serialisation.
- **Write it so HL7 can take it.** Canonical URLs, profile names and code systems are written so they
  can move to an HL7 namespace if a work group adopts the work (see `README.md`, "Where this goes").
