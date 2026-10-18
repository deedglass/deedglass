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

## Watch a coding agent

Many reported incidents with coding agents came through the shell: an ORM's command such as
`prisma db push --force-reset` or `alembic downgrade base`, or SQL sent over SSH. A gate on tool
calls never sees those. Observe mode reads the row
changes Postgres committed on the watched tables, whichever program wrote them. Start the agent
inside a session, from the project's folder:

```sh
mkdir -p ~/deedglass-undo
deedglass observe run --connector dev --label "claude code" --undo-sql ~/deedglass-undo -- claude
```

`--undo-sql` saves the session's undo SQL (free) once the session is sealed, so the end of the run
has everything in one place. Given a folder, the file is `undo-<session id>.sql` in it; given a
file name, an earlier file there is replaced and the line says so. Keep it outside the project: it
holds row values. With `caught_rows: 50` (the default is 100), quitting the agent after an
unbounded 60-row `UPDATE` prints:

```text
✓ Session sealed · 1 transaction · 14:05:12–14:05:12
  public.orders  60 updated
Deedglass caught this: one transaction changed 60 rows in public.orders.
  Undo SQL saved to /home/you/deedglass-undo/undo-obs_01k7….sql (free). Deedglass doesn't run it: review it, then run it yourself:
    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f /home/you/deedglass-undo/undo-obs_01k7….sql
  It holds row values: keep it out of version control and out of the agent's folder.
  Changes from any writer during this window appear here. Leave out anything that wasn't the agent before you undo.
Observed: what Postgres committed. Nothing was compared with what the agent said.
```

- **Keep the admin key out of the agent's reach.** You open the session in your own terminal with
  your admin key; `observe run` removes every `DEEDGLASS_*` variable from the agent's environment,
  and agent keys can't open, close or edit a session. A file is another matter: the agent runs as
  you. `deedglass init` saves the admin key in `.deedglass/admin.key`, so run it outside the
  project, and keep the key out of a shell profile, `.env` or agent settings the agent can read.
  `observe run` names an `admin.key` it finds in the working folder or a parent before the agent
  starts. Start the agent from inside the run, not the other way round: `claude --continue` picks
  up your most recent conversation in that folder. If `type claude` shows an alias, put its full
  path after `--`.
- **Tools that need a Deedglass key go quiet.** The Claude Code plugin's MCP server finds no key in
  its environment inside the session, so its tools refuse calls until you quit, unless it finds the
  agent key `init` wrote in `.deedglass/` above the folder (next to `admin.key`).
- **Any writer shows up.** The change log doesn't say which program wrote a row. If a migration, a
  cron job or a teammate wrote to the same tables during the session, their rows are in it too. Leave
  them out with `observe exclude` and print the SQL again with `observe show <id> --undo-sql`.
- **A second check, not a sandbox.** Nothing is blocked or asked first; the session shows what
  committed and how to put it back.
- **Row changes only.** A `DROP TABLE`, `DROP SCHEMA` or an ORM's reset (`prisma migrate reset`,
  `alembic downgrade base`) writes no row changes, so it leaves nothing in the session, which can
  read 0 transactions right after one. Dropping a watched table also takes it out of the
  publication, which stops the decoder; the run says so and names `deedglass decoder status`. A
  TRUNCATE, rows past the image cap and sensitive columns can't be undone from the record: the run
  prints why instead of saving a file, and offers no one-step undo. Sequences don't move back.
- Without `--undo-sql`, the caught line names the command that prints the SQL instead.

The Claude Code plugin's `/deedglass:watch` prints this line for your project; you run it.

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
