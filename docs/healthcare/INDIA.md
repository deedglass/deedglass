# India: ABDM and NHCX

This page is for Indian hospitals, clinics and health-tech teams whose AI agents write to a FHIR
server on ABDM or NHCX flows. It says, in plain words, what Deedglass checks on those writes and what
it doesn't.

Deedglass is not clinical software. It decides whether an agent's call runs, records what happened
in a sealed deed, and checks what the server stored. It never reads a diagnosis for meaning, never
recommends treatment, never changes clinical content and never approves anything on a clinician's
behalf.

## What it reads

When an agent creates or updates a resource through the [FHIR connector](../connectors/fhir.md),
Deedglass reads two things before the decision: the resource's `meta.profile`, and, for a Bundle,
what the bundle holds. It names the result in the terms of India's national profiles:

- **ABDM health records**: `OPConsultRecord`, `DischargeSummaryRecord`, `PrescriptionRecord`,
  `DiagnosticReportRecord`, `ImmunizationRecord`, `HealthDocumentRecord`, `WellnessRecord` and
  `InvoiceRecord`. The record kind comes from the Composition's profile, or from its SNOMED CT type
  when the profile is missing.
- **NHCX bundles**: `ClaimBundle`, `ClaimResponseBundle`, `CoverageEligibilityRequestBundle`,
  `CoverageEligibilityResponseBundle`, `TaskBundle` and `InsurancePlanBundle`.
- **Claims**: each Claim's `use` (`claim`, `preauthorization` or `predetermination`) and its
  `total`, whether the Claim is sent on its own or inside a bundle.

The profiles come from one pinned version of the NRCeS FHIR Implementation Guide for ABDM: version
6.5.0 (package `ndhm.in#6.5.0`, FHIR R4 4.0.1, dated 8 May 2025), which also carries the NHCX
profiles. Its published examples are part of Deedglass's tests, and each one is classified as the
guide names it. A new guide version is a table and test update.

What a bundle holds counts for more than what it says it is. A bundle labelled
`CoverageEligibilityRequestBundle` that holds a Claim first is treated as a claim, and the mismatch
is reported. Claims nested inside other bundles, or contained in another resource, are read too, and
the largest total in the request is the one that counts.

These facts are listed under `request.fhir` in the [policy language](../POLICY_LANGUAGE.md).

## The india-health pack

`india-health` is a free policy pack built on these facts:

```sh
deedglass policy install india-health
```

It keeps every rule of the healthcare-fhir pack (no deletes of clinical records, orders and
allergies wait for a clinician, coverage is read-only, searches name what they look for) and adds:

| What the agent sends | What happens |
|---|---|
| Any FHIR call while the org's PHI mode is off | Refused, until an owner turns PHI mode on |
| A bundle whose NHCX profile doesn't match what it holds | Refused |
| A final claim (`use: claim`), any amount | Two people approve |
| A pre-authorisation of INR 1,00,000 or more | Two people approve, within 45 minutes |
| A smaller pre-authorisation, or a pre-determination | One person approves, within 45 minutes |
| A claim whose use, total or currency can't be read | Two people approve |
| A claim response, payment notice or other payer-side record | Two people approve |
| A bundle that carries an order, such as a PrescriptionRecord | A clinician approves |
| An ABDM health record that isn't a draft | A clinician approves before it can be shared |
| An ABDM health record whose Composition is `preliminary` | It runs, and is read back |
| A CoverageEligibilityRequestBundle or a TaskBundle | It runs, and is read back |

Replace the example clinicians and the INR 1,00,000 threshold with your own before you push it. The
pack's README lists the steps.

## What it helps you show

- **Who did what, and who approved it.** Every agent action, including reads, has a sealed deed:
  which agent, which tool, which rule decided, who approved, and what the server stored. This helps
  you show the access record that the EHR Standards for India (2016) ask for, which includes viewing.
- **Log retention.** DPDP Rules 2025 ask data fiduciaries to keep logs of processing for at least
  one year. Deedglass keeps its records for as long as you set (1 to 3,650 days), and exports them
  for offline checking. This helps you show that agent actions were logged and kept; it is not your
  full processing log.
- **Two people on claims.** For cashless claims, the approvals and their times are in the record.
  This helps you show who looked at each pre-authorisation and claim, and when. It doesn't measure
  your end-to-end turnaround.
- **Digests, not clinical content.** With PHI mode on, stored actions keep resource types, ids,
  versions and digests, not clinical values.

## What it doesn't do

- **It doesn't judge clinical content.** No rule decides whether a record, an order or a claim is
  right. A clinician or your billing desk decides that when they approve.
- **It doesn't validate against the profiles.** It reads `meta.profile` and what a bundle holds; it
  doesn't check every element against the guide's StructureDefinitions. Use the guide's validator
  for that.
- **It doesn't check ABDM consent.** Consent artefacts aren't verified with the ABDM gateway, and
  the connector doesn't send a consent artefact id to the policy yet.
- **It doesn't talk to the ABDM gateway or NHCX.** It governs writes to your FHIR server. The NHCX
  gateway's response is outside Deedglass.
- **It doesn't cover systems the agent doesn't go through Deedglass to reach.** Only calls that pass
  through it are decided and recorded.
- **It doesn't make your setup meet any law or standard.** It is one part of the evidence you
  collect. Your own review, and your counsel, decide what that evidence is enough for.

## Sources

- FHIR Implementation Guide for ABDM 6.5.0, NRCeS, C-DAC Pune: <https://nrces.in/ndhm/fhir/r4/index.html>;
  NHCX profiles: <https://nrces.in/ndhm/fhir/r4/hcx-profile.html>. The guide's ImplementationGuide
  resource gives its licence as CC0-1.0; Deedglass's tests use its examples with large attachments
  trimmed (`internal/connector/fhir/igclass/testdata/SOURCES.md`).
- Digital Personal Data Protection Rules, 2025 (G.S.R. 846(E)), Rule 6 and Rule 8(3):
  <https://www.meity.gov.in/static/uploads/2025/11/53450e6e5dc0bfa85ebd78686cadad39.pdf>
- EHR Standards for India (2016): <https://nrces.in/standards/ehr-standards-for-india>
- IRDAI Master Circular on health insurance (29 May 2024):
  <https://irdai.gov.in/documents/37343/365525/>
