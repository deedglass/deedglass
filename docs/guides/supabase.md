# Deedglass with Supabase Postgres

Put Deedglass in front of agents that use a Supabase database: policies decide each call, risky
writes wait for a person, and Proven checks read Supabase Postgres's own change log through logical
replication. One command does the setup: `deedglass connect`. Facts about Supabase below were
checked on 2026-10-21 against Supabase's own docs.

## Before you start

- A running Deedglass server and its admin key. On a laptop, `npx -y deedglass init` starts one;
  `connect` then finds it from any directory (through `~/.deedglass/current.json`). For your own
  server, set `DEEDGLASS_SERVER` and `DEEDGLASS_API_KEY`.
- The project's **direct connection** string (Project Settings › Database › Connection string ›
  Direct connection: host `db.<project-ref>.supabase.co`, port 5432, user `postgres`). Proven checks
  need logical replication, which the pooler (Supavisor, `*.pooler.supabase.com`) can't carry.
- The direct connection is IPv6. If the machine running Deedglass has no IPv6, enable Supabase's
  IPv4 add-on.

## One command

```sh
export DATABASE_URL='postgres://postgres:…@db.<project-ref>.supabase.co:5432/postgres'
npx -y deedglass connect          # read-only checks and the plan; changes nothing
```

`connect` detects Supabase from the host and the catalogs (the `supabase_admin` role). If the URL
is the pooler's, it says so and prints the direct host to use instead (`db.<project-ref>.supabase.co`
for user `postgres.<project-ref>`); it won't apply through the pooler. On a session that can't write
(`SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY`) it checks:

- the Postgres version;
- `wal_level` (Supabase projects run with `logical`);
- replication slots in use, against `max_replication_slots` (the limit depends on the compute size);
- whether the user can create roles;
- which tables in `public` (or `--schema`) lack `REPLICA IDENTITY FULL`.

Then it prints one plan: the connector (named `supabase` unless you pass `--name`) with the tables the
agent may read and write, the database changes `connector postgres init` makes (roles, grants,
replica identity, publication), the policy, and the agent. Review it, then:

```sh
npx -y deedglass connect --apply --confirm postgres
```

`--confirm` takes the database's name, typed exactly, because the database is remote. With
`--apply`, `connect`:

1. creates the Postgres connector;
2. runs `connector postgres init`: the least-privilege roles `deedglass_agent` and
   `deedglass_witness` get passwords generated on your machine and sent to Postgres only as SCRAM
   verifiers, and their connection strings go into the Deedglass vault;
3. pushes and enforces `postgres-safe-supabase`: the `postgres-safe` starter scoped to this
   connector's tools (reads run, writes wait for a person, `DROP` and `TRUNCATE` never run);
4. creates the agent `supabase-agent` and saves its key to a file (mode 0600);
5. sends one read through the Gate and prints the result;
6. prints the line that adds it to Claude Code, with the key file's path, not the key.

Your connection string's password is never printed or stored: it is read once, like
`DEEDGLASS_INIT_DSN`. Run the same command again and every step already done is skipped.

To let writes run and check each one at Proven instead (for a branch or a throwaway project), pass
`--policy postgres-check`. To limit the agent to some tables, pass
`--tables public.orders,public.refunds`.

## What doesn't work there yet

- Connections through Supavisor (the pooler) can't carry logical replication; use the direct host.
- A replication slot holds WAL while Deedglass is down; watch disk use on small compute sizes.
- Read replicas as the source are not tested.

Afterwards, `deedglass connector postgres check --connector supabase` re-runs the setup checks and
`deedglass decoder status` shows the slot reading changes.

## Sources (checked 2026-10-21)

- https://supabase.com/docs/guides/database/postgres/setup-replication-external (direct connection, IPv4 add-on)
- https://supabase.com/docs/guides/database/connecting-to-postgres (direct connection host and port; the Supavisor pooler host and its `postgres.<project-ref>` user)
- https://neon.com/docs/guides/logical-replication-supabase-to-neon (logical replication on by default in Supabase; slot limits by instance size)
- https://supabase.com/docs/guides/database/replication (max_replication_slots setting)
