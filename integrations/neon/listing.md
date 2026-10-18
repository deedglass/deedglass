# Deedglass · Neon integrations kit

The founder submits this through Neon's partner form. Facts checked 2026-10-11 (sources at the
end); re-check them on the day of submission.

## 50 words

Deedglass sits between your AI agents and your Neon Postgres. Policies decide every tool call,
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

## Assets

- Logo: `../cursor/icon-512.png` (the approved icon, copied, never redrawn).
- Screenshots: `../cursor/screenshot-1-play.png`; two more are the founder's captures.
- Docs: https://docs.deedglass.com/guides/neon/ · Support: hello@deedglass.com

## Sources (checked 2026-10-11)

- https://neon.com/docs/guides/logical-replication-neon (enable, irreversible, compute restart, REPLICATION, 40-hour slot removal, scale to zero)
- https://neon.com/docs/guides/logical-replication-guide (available to all Neon users)
