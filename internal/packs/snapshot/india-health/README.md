# india-health

PHI mode first. Clinical records are never deleted. ABDM records and orders wait for a clinician. NHCX claims need one or two people by use and amount.

For: the FHIR connector (`docs/connectors/fhir.md`) on ABDM and NHCX flows. It reads the profiles of the NRCeS FHIR Implementation Guide for ABDM 6.5.0, which also carries the NHCX profiles. What it checks and what it doesn't: `docs/healthcare/INDIA.md`. Try it on synthetic data first.

## Install

```sh
deedglass policy install india-health
```

This checks the pack against its signed index, compiles it, runs its test cases and shows the files and the diff before asking `Write 2 files? [y/N]`. It writes `policies/india-health.yaml` and `policies/india-health.cases.yaml`, and nothing else: it never pushes or activates a policy.

## Before you push it

1. Turn PHI mode on for the org (`docs/PHI_MODE.md`). Until it is on, the first rule refuses every FHIR call.
2. Replace `user:clinician-1@example.org` and `user:clinician-2@example.org` with the clinicians who review agent changes, and `role:admin` on the claims rules with your billing desk if it differs.
3. Set the pre-authorisation threshold (INR 1,00,000 in `preauth-large-two-people`) to your own.
4. Run the cases again after any change: `deedglass policy test policies/india-health.yaml`.

## Claims by NHCX use and amount

| The request carries | Who approves |
|---|---|
| A claim response, payment notice or other payer-side record | Two people |
| A claim whose use, total or currency can't be read | Two people |
| A final claim (`use: claim`), any amount | Two people |
| A pre-authorisation of INR 1,00,000 or more | Two people, within 45 minutes |
| A smaller pre-authorisation, or a pre-determination | One person, within 45 minutes |
| A patch to a Claim, or any other billing write | Two people |

The rules read the same facts from a plain FHIR Claim and from Claims inside an NHCX bundle; the largest total in the request counts.

## Then

```sh
deedglass policy push policies/india-health.yaml
```

Activate it in shadow mode first, compare with `deedglass policy replay`, then enforce.

## What it doesn't do

- No rule judges whether a clinical record, an order or a claim is right. The pack decides who may change what, and when a person looks first.
- It doesn't validate a resource against the IG's profiles; it reads `meta.profile` and what a bundle holds.
- ABDM consent artefacts aren't checked: that needs a consent input the connector doesn't send.
- It doesn't send anything to the ABDM gateway or NHCX; it governs writes to your FHIR server.
- A pack is a starting point, not a review of your setup. It decides only the calls it names; everything else falls to its default, which is deny.
