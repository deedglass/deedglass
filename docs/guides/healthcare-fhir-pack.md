# The healthcare-fhir policy pack

`healthcare-fhir` is a free policy pack for the [FHIR connector](../connectors/fhir.md): a policy
and its test cases that you install as files, read, change and push like any other policy. It
denies deletes of clinical records, holds orders and medication changes, allergies, the problem
list and final notes for a clinician, needs two people for claims and other billing records, keeps
coverage read-only and has every other write read back (Checked). It never judges whether a
clinical change is right: it decides who may change what, and when a person has to look first.

Try it on synthetic data first.

## Install it

```sh
deedglass policy install healthcare-fhir
```

`install` checks the pack against its index, compiles it, runs its 36 cases and shows the two files
before writing `policies/healthcare-fhir.yaml` and `policies/healthcare-fhir.cases.yaml`. It never
pushes or activates anything.

Before you push it:

1. **Name your clinicians.** Three rules send approvals to `user:clinician-1@example.org` and
   `user:clinician-2@example.org`. Replace them with the email addresses your clinicians sign in to
   Deedglass with. Approver groups (`group:clinicians`) replace this list in a later release.
2. **Turn PHI mode on** for the org ([PHI mode](../PHI_MODE.md)), so stored actions keep ids and
   digests instead of clinical content once they are sealed.
3. Run the cases again after any change: `deedglass policy test policies/healthcare-fhir.yaml`.

Then push it, activate it in shadow mode, compare with `deedglass policy replay`, and enforce.

## The rules

The first rule that matches decides. Every rule matches on the connector type `fhir`, so it applies
to every FHIR connector in the org whatever you named it, except `no-history-rewrite`, which is for
a FHIR server behind the REST connector.

| Rule | What it decides |
|---|---|
| `fhir-needs-a-request` | A call that didn't build a request (a resource type outside the connector's `allowed_resource_types`, or arguments that don't fit) is denied. |
| `no-delete-clinical` | A delete of a clinical resource, the Patient or the audit trail (AuditEvent, Provenance) is denied: "Clinical records are corrected with a new version, not deleted." 37 types are listed in the rule. |
| `no-history-rewrite` | Through the REST connector, a path with `$expunge` or `$meta-delete` is denied. |
| `coverage-read-only` | Creates, updates, patches and deletes of Coverage and CoverageEligibilityResponse are denied. A CoverageEligibilityRequest can be created. |
| `phi-minimum-read` | A search with no parameters, a search asking for more than 100 results (`_count`), and a type's whole history are denied. |
| `claims-four-eyes` | Any write to Account, ChargeItem, Claim, ClaimResponse, ExplanationOfBenefit, Invoice, PaymentNotice or PaymentReconciliation waits for **two different admins**. |
| `break-glass` | The emergency agent's creates and updates run without waiting (below). |
| `orders-need-clinician` | Creates, updates and patches of MedicationRequest, MedicationAdministration, ServiceRequest, DeviceRequest, NutritionOrder and ImmunizationRecommendation wait for one clinician on the list. Status changes to `active` or `completed` are updates, so they wait too. |
| `allergy-problem-list-review` | Writes to AllergyIntolerance and Condition wait for a clinician. |
| `notes-draft-only` | A DocumentReference with `docStatus: preliminary`, or a Composition with `status: preliminary`, runs. |
| `final-notes-need-clinician` | Any other note write, and every patch to a note, waits for a clinician. |
| `fhir-reads` | `read`, `vread`, `search` and a resource's own `history` run (L0). |
| `other-deletes-need-approval` | A delete of anything not listed above (a Task, an Appointment) waits for an admin. |
| `verify-levels` | Every other create, update and patch runs and is read back at the version the server reported (Checked, L1). |

Anything else, including tools on other connectors, falls to the default: deny.

## Break-glass

Keep a separate agent for emergencies with the label `break-glass=on`, stopped until it is needed:

```sh
deedglass agent create er-bot --label break-glass=on
deedglass agent stop er-bot
```

Starting and stopping it (`deedglass agent start er-bot`, `deedglass agent stop er-bot`) are admin
actions with their own sealed records. While it runs, its creates and updates go ahead without
waiting for a clinician, and each action's record names the rule `break-glass`. Review them
afterwards with `deedglass actions list --agent er-bot`. It still can't delete a clinical record,
and claims still need two people, because those rules come first.

A call that carries its own emergency reason, and a review queue in the Console, are planned
(HC-P08); this pack doesn't depend on them.

## What it doesn't do

- **No clinical judgement.** No rule reads a value, a dose or a diagnosis to decide; the coded
  fields in `request.fhir` aren't used by any rule.
- **ABDM consent artefacts aren't checked.** That needs a consent input the connector doesn't send
  yet.
- **Approvers are people, not groups,** until approver groups ship.
- **`_count` is checked by length.** A value of up to two characters, or exactly `100`, passes; the
  server decides what to do with one that isn't a number.
- **It decides only the calls it names.** It is a starting point, not a review of your setup.
