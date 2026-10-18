# Deedglass · Supabase integrations directory kit

The founder submits this through Supabase's partner or integration form. Facts checked 2026-10-11
(sources at the end); re-check them on the day of submission.

## 50 words

Deedglass sits between your AI agents and your Supabase Postgres. Policies decide every tool call,
risky writes wait for a person, and each action is checked against Postgres's own change log and
sealed as a record anyone can verify offline. Self-hosted, public repo, Apache-2.0 core.

## 150 words

AI agents now write to production databases, and their reports are not always what happened. An
agent can say it changed one row while a trigger changed three hundred. Deedglass is a gateway
between your agents and the tools they call. Each call passes a policy you write in plain YAML:
reads can run, destructive SQL never runs, writes can wait for a person to approve. After a write,
Deedglass reads Postgres's own change log through logical replication, compares what the agent said
with what the database committed, and seals a signed record. When they differ, it shows the rows
and can undo them, with a person's countersign. It works with Claude, Cursor and any MCP client,
and agents never hold your database credentials. Deedglass is self-hosted, keeps no telemetry and
runs anywhere Postgres runs. Free to self-host; Pro adds the full report and the scan Action's
pull request comments on private repos.

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

## Assets

- Logo: `../cursor/icon-512.png` (the approved icon, copied, never redrawn).
- Screenshots: `../cursor/screenshot-1-play.png`; two more are the founder's captures.
- Docs: https://docs.deedglass.com/guides/supabase/ · Support: hello@deedglass.com

## Sources (checked 2026-10-11)

- https://supabase.com/docs/guides/database/postgres/setup-replication-external (direct connection, IPv4 add-on)
- https://neon.com/docs/guides/logical-replication-supabase-to-neon (logical replication on by default in Supabase; slot limits by instance size)
- https://supabase.com/docs/guides/database/replication (max_replication_slots setting)
