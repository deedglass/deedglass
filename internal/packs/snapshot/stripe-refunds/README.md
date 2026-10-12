# stripe-refunds

Small refunds run. Large, foreign-currency, disputed and unchecked refunds and cancellations wait for a human. Deletes never run.

For: the Stripe connector (sandbox).

## Install

```sh
deedglass policy install stripe-refunds
```

This checks the pack against its signed index, compiles it, runs its test cases and shows the files and the diff before asking `Write 2 files? [y/N]`. It writes `policies/stripe-refunds.yaml` and `policies/stripe-refunds.cases.yaml`, and nothing else: it never pushes or activates a policy.

## Then

```sh
deedglass policy test policies/stripe-refunds.yaml
deedglass policy push policies/stripe-refunds.yaml
```

Activate it in shadow mode first (`deedglass policy activate stripe-refunds --version <n> --mode shadow --reason "…"`), compare with `deedglass policy replay`, then enforce.

## What it doesn't do

A pack is a starting point, not a review of your setup. It decides only the tools it names; everything else falls to its default, which is deny.
