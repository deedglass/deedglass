# money-movement

Small refunds run. Larger refunds need one or two admins. Payouts and transfers always need two admins and have a hard cap.

For: Stripe refunds, payouts and transfers.

This pack is part of Pro: `deedglass policy install` asks for a Pro licence before it writes anything. Once installed, it keeps enforcing if the licence lapses, because rules never read the licence.

## Install

```sh
deedglass policy install money-movement
```

This checks the pack against its signed index, compiles it, runs its test cases and shows the files and the diff before asking `Write 2 files? [y/N]`. It writes `policies/money-movement.yaml` and `policies/money-movement.cases.yaml`, and nothing else: it never pushes or activates a policy.

## Then

```sh
deedglass policy test policies/money-movement.yaml
deedglass policy push policies/money-movement.yaml
```

Activate it in shadow mode first (`deedglass policy activate money-movement --version <n> --mode shadow --reason "…"`), compare with `deedglass policy replay`, then enforce.

## What it doesn't do

A pack is a starting point, not a review of your setup. It decides only the tools it names; everything else falls to its default, which is deny.
