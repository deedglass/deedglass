# Deedglass with Supabase Postgres

Put Deedglass in front of agents that use a Supabase database: policies decide each call, risky
writes wait for a person, and Proven checks read Supabase Postgres's own change log through logical
replication. Facts about Supabase below were checked on 2026-10-11 against Supabase's own docs.

## Setup on Supabase

1. Use a **direct connection** from the project's Connect panel, not the connection pooler.
   Logical replication needs a direct connection.
2. Supabase projects run with logical replication on. Check with `SHOW wal_level;` (it should say
   `logical`) and `SHOW max_replication_slots;`. Slot limits depend on the compute size.
3. Run `deedglass connector postgres init --connector <name> --admin-dsn-env SUPABASE_DB_URL` and
   review the plan before `--apply`. It creates the least-privilege roles, the publication and the
   replication slot Deedglass reads.
4. If your Deedglass host has no IPv6, enable Supabase's IPv4 add-on for the direct connection.

## What doesn't work there yet

- Connections through Supavisor (the pooler) can't carry logical replication; use the direct host.
- A replication slot holds WAL while Deedglass is down; watch disk use on small compute sizes.
- Read replicas as the source are not tested.

After `init --apply`, run `deedglass connector postgres check --connector <name>` and then
`deedglass decoder status` to see the slot reading changes.

## Sources (checked 2026-10-11)

- https://supabase.com/docs/guides/database/postgres/setup-replication-external (direct connection, IPv4 add-on)
- https://neon.com/docs/guides/logical-replication-supabase-to-neon (logical replication on by default in Supabase; slot limits by instance size)
- https://supabase.com/docs/guides/database/replication (max_replication_slots setting)
