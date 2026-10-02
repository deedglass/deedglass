# What the witness watches

L2 ("Witnessed by Postgres") can only speak about what the decoder can see. This page says exactly
what that is for a Postgres connector, what it isn't, and how to change it safely. The decoder is
[DECODER.md](DECODER.md); the V0 design this replaces is [v0/OBSERVATION_BOUNDARY.md](v0/OBSERVATION_BOUNDARY.md).

## What is watched

The **publication** `deedglass_pub` is the boundary. It contains:

- `deedglass.ledger`, the row `begin_action` writes in every agent transaction;
- every table in the connector's `witness.watched_tables`, which must include every table in
  `writable_tables` (config validation refuses a writable table that isn't watched, and
  `deedglass connector postgres check` fails `writable_watched`).

It publishes inserts, updates, deletes and truncates, with no row filters and no column lists; the
decoder halts (`publication_invalid`) at start when that isn't so, and treats any change to the
publication during a read as an evidence gap (`publication_changed`, `publication_gap`). Each
watched table has `REPLICA IDENTITY FULL`, so updates and deletes carry the whole old row.

For each committed transaction that touches a watched table or the ledger, the decoder keeps the
tables, exact counts, the first 1,000 changes with old and new images, and a digest over all of
them ([DECODER.md](DECODER.md#frames-and-classes)).

## What isn't

- **Tables outside the publication.** Their changes are skipped by Postgres before Deedglass sees
  them. A trigger on a watched table that writes to an unwatched one is invisible (`check` warns
  about triggers on watched tables).
- **Unlogged and temporary tables, sequences and large objects.** Logical decoding doesn't carry
  them.
- **Schema changes.** DDL shows only as a changed relation shape (`relation_changed:<table>`) on the
  next change to that table.
- **Effects outside the WAL**: `NOTIFY`, `pg_net`, foreign tables, files, other databases.
- **Reads.** The decoder sees writes only.
- **What a REPLICATION role could read anyway.** `deedglass_witness` can decode every table in its
  database (spike S1(h)); Deedglass asks only for the publication and stores only what it returns.
  Treat the `witness_dsn` as a read-everything credential.

## Changing what is watched

Add to the publication first, then to `watched_tables`:

1. Add the table to the connector's config file under `witness.watched_tables` (and, if agents may
   write it, `writable_tables`).
2. Run `deedglass connector postgres init --connector <name>`: it plans `REPLICA IDENTITY FULL` and
   `ALTER PUBLICATION deedglass_pub ADD TABLE …`. Review the plan and apply it.
3. Update the connector (`PATCH /v1/connectors/{id}` with the new config). The decoder picks the
   new table up on its next read; a change committed before step 2 isn't in the stream.

Removing a table is the reverse: drop it from `writable_tables` and `watched_tables` first, then from
the publication. Changing the publication while the decoder reads is safe but leaves a mark: on
Postgres 18, a change in the window can be skipped (`publication_gap`); on 16 and 17, dropping the
publication altogether halts the decoder (`publication_missing`) until the slot is dropped.

## What this page doesn't do

It doesn't cover the Gate's own boundary (which tools an agent may call: [GATE.md](GATE.md) and the
policy pages) or databases other than Postgres.
