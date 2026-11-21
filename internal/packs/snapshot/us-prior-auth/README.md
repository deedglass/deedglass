# us-prior-auth

A Deedglass policy pack for US health systems and payers operating Da Vinci CRD, DTR, and PAS prior authorization workflows under CMS-0057-F.

Clinical records and prior authorization claims are never deleted: they are corrected with a new version. Orders, problem list changes, and completed documentation templates wait for a clinician. Da Vinci PAS prior authorization requests (`use: preauthorization`) wait for utilization review before transmission, with dual-admin review on requests of $5,000 USD or more, or where the request total is unverified. Coverage is read-only.

## Install

Run in your project root:

```sh
deedglass policy install us-prior-auth
```

This checks the pack against its signed index, compiles it, runs its test cases and displays the files and diff before asking `Write 2 files? [y/N]`. It writes `policies/us-prior-auth.yaml` and `policies/us-prior-auth.cases.yaml`, and nothing else: it never pushes or activates a policy.

## Before you activate it

1. Replace the clinician approvers (`user:clinician-1@example.org` and `user:clinician-2@example.org`) with your licensed utilization review and clinical staff.
2. Turn PHI mode on for the organization (`docs/PHI_MODE.md`), so stored actions record identifiers and digests rather than clinical text.
3. Check your change: `deedglass policy test policies/us-prior-auth.yaml`.

## Deploying

Push the policy to your Deedglass cluster:

```sh
deedglass policy push policies/us-prior-auth.yaml
```

Activate it in shadow mode first (`deedglass policy activate us-prior-auth --version <n> --mode shadow --reason "..."`), compare with `deedglass policy replay`, then switch to enforce mode.
