# postgres-check

Reads and writes run. Every write is checked at Proven. DROP and TRUNCATE never run.

For: the Postgres connector (postgres.query, postgres.execute), on a local or throwaway database.
`deedglass init` enforces it by default so an agent's writes don't wait for you while you try
Deedglass out. In production, use `postgres-safe`, where writes wait for a human.

## Install

```sh
deedglass policy install postgres-check
```

This checks the pack against its signed index, compiles it, runs its test cases and shows the files and the diff before asking `Write 2 files? [y/N]`. It writes `policies/postgres-check.yaml` and `policies/postgres-check.cases.yaml`, and nothing else: it never pushes or activates a policy.

## Then

```sh
deedglass policy test policies/postgres-check.yaml
deedglass policy push policies/postgres-check.yaml
```

## What it doesn't do

A pack is a starting point, not a review of your setup. Writes run before anyone looks at them: the Proven check tells you afterwards what Postgres really changed, and an undo (which waits for a person) can put the rows back. It decides only the tools it names; everything else falls to its default, which is deny.
