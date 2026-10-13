# Observe mode: watched sessions

Most teams don't rewire their agents on day one. Observe mode asks nothing of the agent: connect
the Postgres database Deedglass already reads, open a session, let the agent work, close the
session. Deedglass shows every row Postgres committed on the watched tables during that window,
seals the session in the record, and lets a person undo all of it.

It works beside any gate you already use (an MCP proxy, a sandbox, nothing at all), because it reads
Postgres's own change log and nothing else.

## What it records

A watched session holds every transaction that:

- Postgres committed while the session was open (`opened_at` to `closed_at`, both included);
- touched at least one of the connector's `witness.watched_tables`;
- no Deedglass action claimed (the decoder's `unattributed` transactions: no ledger row, no marker).
  A transaction that came through the Gate already has its own record and is never attached.

For each transaction it keeps the decoder's row images (the first `decoder.max_images_per_tx`,
1,000 by default), exact counts per table and a digest over every change.

When the session closes, Deedglass waits for the decoder to pass the close time (a transaction
committed after it, or a heartbeat two periods after it), at most 30 seconds. If the decoder is
behind, the session is flagged `decoder_behind` and late transactions are still attached for up to
10 minutes. Then the session is sealed as the admin record `observe.session_sealed`: the session
and connector ids, the window, the transaction ids (up to 100 by name; a digest pins all of them),
the transactions left out, counts per table, a digest of the changes and the flags. Ids, counts and
digests only, never a row value.

## What it can't know

- **Who made each change.** Postgres's change log doesn't carry the database role or the program
  that wrote a row. If another writer committed during the window, its transactions appear too,
  and the session page says so: `Changes from any writer during this window appear here. Leave out
  anything that wasn't the agent before you undo.`
- **What the agent said.** There is no claim to compare, so a watched session never gets a level or
  a Matches or Doesn't match verdict. It reads `Observed: what Postgres committed. Nothing was
  compared with what the agent said.`
- **Unwatched tables.** Only the connector's watched tables are in the change log.

## Turning it on

Observe mode needs only what `deedglass connector postgres init` already set up (the publication,
the slot and `deedglass_witness`). In the connector's config:

```yaml
witness:
  enabled: true
  watched_tables: [public.orders, public.order_events]
  observe:
    enabled: true          # group unattributed transactions into watched sessions
    max_session: 12h       # an open session closes itself after this, with a note (1m to 168h)
    caught_rows: 100       # one transaction changing this many rows in one table is "caught"
```

Unknown keys are errors, as everywhere in the connector's config.

## Sessions

```sh
deedglass observe run --connector pg-demo -- ./agent.sh      # open, run, close, print
deedglass observe start --connector pg-demo --label "nightly agent"
deedglass observe stop obs_01k…
deedglass observe list [--connector pg-demo] [--state sealed] [--caught]
deedglass observe show obs_01k…
```

`observe run` runs the command with your terminal, passes its exit code through (130 after Ctrl-C)
and never hands it a `DEEDGLASS_*` variable: the admin key that opened the session must not be the
agent's. Agent keys get 403 on every observe route; an agent can't open, close, edit or undo its own
recording.

In the Console, **Sessions** lists them; a session's page shows the transactions with their diffs
(20 rows each), the record line and the undo area. Overview shows up to three caught sessions.

## Deedglass caught this

A session is flagged `caught` when one of its transactions truncated a watched table, or changed (or
deleted) at least `caught_rows` rows of one table. The line is built from the record:
`Deedglass caught this: one transaction changed 301 rows in public.orders.` Every place that shows
it also shows the free way back.

## Undo

Two ways back, side by side everywhere:

- **Free, always:** `deedglass observe show obs_01k… --undo-sql` prints the undo as SQL you review
  and run yourself. Deedglass never runs it and never connects with write rights to make it. See
  [UNDO.md](UNDO.md#undoing-a-watched-session).
- **Pro:** `deedglass observe undo obs_01k… --reason "…"` sends one undo of every included
  transaction through the Gate: one countersign, one Postgres transaction, conflict checks, and its
  own record checked against the plan's exact footprint.

Before either, leave out what wasn't the agent: `deedglass observe exclude obs_01k… dtx_01k…`
(only while the session is sealed and before its undo starts; each change is sealed too).

## Limits

- Retention: unattributed transactions are kept 30 days (`decoder.retention` for the rest). A
  session whose transactions have aged out shows `Frames expired: undo no longer possible` and keeps
  its sealed summary.
- `undo.max_age` (7 days by default) and `undo.max_rows` (1,000) apply to a session's undo as to an
  action's.
- A TRUNCATE, a transaction past the image cap, DDL in the window, or a value the plan would write
  that was stored only as a digest (`witness.sensitive_columns`) can't be undone from the record.
- Two sessions open on one connector at the same time both hold the shared transactions, flagged
  `overlap`.
- MySQL and other databases: not yet (no witness for them).
