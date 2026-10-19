# healthcare-fhir

Reads run. Clinical records are never deleted. Orders, allergies, problems and final notes wait for a clinician. Claims need two people.

For: the FHIR connector (`docs/connectors/fhir.md`). Try it on synthetic data first.

## Install

```sh
deedglass policy install healthcare-fhir
```

This checks the pack against its signed index, compiles it, runs its test cases and shows the files and the diff before asking `Write 2 files? [y/N]`. It writes `policies/healthcare-fhir.yaml` and `policies/healthcare-fhir.cases.yaml`, and nothing else: it never pushes or activates a policy.

## Before you push it

1. Replace `user:clinician-1@example.org` and `user:clinician-2@example.org` with the clinicians who review agent changes (three rules use them). Approver groups replace this list in a later release.
2. Turn PHI mode on for the org (`docs/PHI_MODE.md`), so stored actions keep ids and digests instead of clinical content.
3. Run the cases again after any change: `deedglass policy test policies/healthcare-fhir.yaml`.

## Then

```sh
deedglass policy push policies/healthcare-fhir.yaml
```

Activate it in shadow mode first (`deedglass policy activate healthcare-fhir --version <n> --mode shadow --reason "…"`), compare with `deedglass policy replay`, then enforce.

## Break-glass

Keep a separate agent for emergencies with the label `break-glass=on`, stopped until it is needed. Starting and stopping it are admin actions with their own sealed records. While it runs, its creates and updates go ahead without waiting for a clinician, and each record names the rule `break-glass`; review them afterwards with `deedglass actions list --agent <name>`. It still can't delete a clinical record, and claims still need two people.

## What it doesn't do

- No rule judges whether a clinical change is right. The pack decides who may change what, and when a person looks first.
- ABDM consent artefacts aren't checked yet: that needs a consent input the connector doesn't send.
- A pack is a starting point, not a review of your setup. It decides only the calls it names; everything else falls to its default, which is deny.
