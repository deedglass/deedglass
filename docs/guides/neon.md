# Deedglass with Neon Postgres

Put Deedglass in front of agents that use a Neon database: policies decide each call, risky
writes wait for a person, and Proven checks read Neon Postgres's own change log through logical
replication. One command does the setup: `deedglass connect`. Facts about Neon below were checked
on 2026-10-21 against Neon's own docs.

## Before you start

- A running Deedglass server and its admin key. On a laptop, `npx -y deedglass init` starts one;
  `connect` then finds it from any directory (through `~/.deedglass/current.json`). For your own
  server, set `DEEDGLASS_SERVER` and `DEEDGLASS_API_KEY`.
- Logical replication turned on: Console › project › Settings › Postgres › Logical replication ›
  Enable (or `neon projects update --enable-logical-replication`). It can't be turned off again, and
  it restarts the project's computes. Without it `connect` still works, with Proven checks off; its
  `wal_level` line says how to turn them on.
- A connection string for a role created in the Console, CLI or API (a member of `neon_superuser`,
  which has `REPLICATION`). Roles created with SQL can't be given `REPLICATION` on Neon.
- The **direct** host. Neon's pooled host has `-pooler` in its name; the direct host is the same
  name without it.

## One command

```sh
export DATABASE_URL='postgres://<role>:…@ep-….neon.tech/<database>?sslmode=require'
npx -y deedglass connect          # read-only checks and the plan; changes nothing
```

`connect` detects Neon from the host and the catalogs. If the URL is the pooled one, it says so and
prints the direct host; it won't apply through the pooler. On a session that can't write it checks
the Postgres version, `wal_level`, replication slots in use, whether the role can create roles, and
which tables in `public` (or `--schema`) lack `REPLICA IDENTITY FULL`. Then it prints one plan: the
connector (named `neon` unless you pass `--name`), the database changes, the policy and the agent.
Review it, then:

```sh
npx -y deedglass connect --apply --confirm <database>
```

With `--apply` it creates the connector; runs `connector postgres init` (the least-privilege roles
`deedglass_agent` and `deedglass_witness`, with passwords generated on your machine and sent to
Postgres only as SCRAM verifiers, their connection strings stored in the Deedglass vault); pushes and
enforces `postgres-safe-neon`, the `postgres-safe` starter scoped to this connector's tools; creates
the agent `neon-agent` and saves its key to a 0600 file; sends one read through the Gate; and prints
the Claude Code line, with the key file's path, not the key. Your connection string's password is
never printed or stored. Run it again and every step already done is skipped.

On a Neon branch you use for trying things out, `--policy postgres-check` lets writes run and checks
each one at Proven instead of waiting for a person.

## What doesn't work there yet

- Neon removes a replication slot that hasn't confirmed progress for about 40 hours. If Deedglass
  is stopped that long, the slot is dropped: actions in the gap are recorded as missing and a new
  slot starts.
- While Deedglass is connected to the slot, the compute stays active and doesn't scale to zero.
- Pooled connection strings can't carry logical replication.

Afterwards, `deedglass connector postgres check --connector neon` re-runs the setup checks and
`deedglass decoder status` shows the slot reading changes.

## Sources (checked 2026-10-21)

- https://neon.com/docs/guides/logical-replication-neon (enable, irreversible, compute restart, REPLICATION, 40-hour slot removal, scale to zero)
- https://neon.com/docs/guides/logical-replication-guide (available to all Neon users)
- https://neon.com/docs/connect/connection-pooling (the pooled host carries `-pooler` in its name)
