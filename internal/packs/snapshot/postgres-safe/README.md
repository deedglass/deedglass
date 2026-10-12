# postgres-safe

Reads run freely. Writes wait for a human. Schema changes never run.

For: the Postgres connector (postgres.query, postgres.execute).

## Install

```sh
deedglass policy install postgres-safe
```

This checks the pack against its signed index, compiles it, runs its test cases and shows the files and the diff before asking `Write 2 files? [y/N]`. It writes `policies/postgres-safe.yaml` and `policies/postgres-safe.cases.yaml`, and nothing else: it never pushes or activates a policy.

## Then

```sh
deedglass policy test policies/postgres-safe.yaml
deedglass policy push policies/postgres-safe.yaml
```

Activate it in shadow mode first (`deedglass policy activate postgres-safe --version <n> --mode shadow --reason "…"`), compare with `deedglass policy replay`, then enforce.

## What it doesn't do

A pack is a starting point, not a review of your setup. It decides only the tools it names; everything else falls to its default, which is deny.
