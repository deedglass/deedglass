# Deedglass with Neon Postgres

Put Deedglass in front of agents that use a Neon database: policies decide each call, risky
writes wait for a person, and Proven checks read Neon Postgres's own change log through logical
replication. Facts about Neon below were checked on 2026-10-11 against Neon's own docs.

## Setup on Neon

1. Enable logical replication: Console › project › Settings › Postgres › Logical replication ›
   Enable (or `neon projects update --enable-logical-replication`). It can't be turned off again,
   and it restarts the project's computes.
2. Use a role created in the Console, CLI or API (a member of `neon_superuser`, which has
   `REPLICATION`). Roles created with SQL can't be given `REPLICATION` on Neon.
3. Run `deedglass connector postgres init --connector <name> --admin-dsn-env NEON_DATABASE_URL`
   with the direct (non-pooled) host, and review the plan before `--apply`.

## What doesn't work there yet

- Neon removes a replication slot that hasn't confirmed progress for about 40 hours. If Deedglass
  is stopped that long, the slot is dropped: actions in the gap are recorded as missing and a new
  slot starts.
- While Deedglass is connected to the slot, the compute stays active and doesn't scale to zero.
- Pooled connection strings can't carry logical replication.

After `init --apply`, run `deedglass connector postgres check --connector <name>` and then
`deedglass decoder status` to see the slot reading changes.

## Sources (checked 2026-10-11)

- https://neon.com/docs/guides/logical-replication-neon (enable, irreversible, compute restart, REPLICATION, 40-hour slot removal, scale to zero)
- https://neon.com/docs/guides/logical-replication-guide (available to all Neon users)
