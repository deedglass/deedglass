# Postgres deployment

What a Postgres database needs before the Postgres connector can run agent SQL in it and the
witness can watch it (L2). `deedglass connector postgres init` plans and applies all of it, and
`deedglass connector postgres check` reads it back; [POSTGRES.md](POSTGRES.md) walks through both.
This page is the reference for whoever runs the database.

## Versions

PostgreSQL 16, 17 and 18 are supported and tested (16.15, 17.11 and 18.6 in CI's matrix). 14 and
15 may work; `check` marks them untested. Earlier versions are refused.

## Server settings

| Setting | Needed | Why |
|---|---|---|
| `wal_level` | `logical` | the witness decodes the change stream (S20) |
| `max_replication_slots`, `max_wal_senders` | 2 or more free each | one slot and one sender per watched connector, plus headroom |
| `max_slot_wal_keep_size` | a cap, such as `4GB` | without one, a stalled witness keeps WAL until the disk is full; with one, the slot is invalidated and `check` fails `slot` |
| `idle_replication_slot_timeout` (18) | `0`, or at least 1 day | shorter, and a witness that is down overnight loses its slot |

`wal_level` needs a restart; the other settings need a reload. Provider steps are in
[POSTGRES.md](POSTGRES.md#providers).

## Roles

Roles are cluster-wide: two databases on one cluster share them.

| Role | Attributes | Grants |
|---|---|---|
| `deedglass_owner` | `NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS` | owns schema `deedglass`, `deedglass.ledger` and `deedglass.begin_action` |
| `deedglass_agent` | `LOGIN NOINHERIT`, nothing else; member of nothing | `USAGE` on the allowed schemas, `SELECT` on their tables, `INSERT`, `UPDATE`, `DELETE` on `writable_tables` only, `USAGE` on their serial sequences, `EXECUTE` on allow-listed functions and on `begin_action`. Session defaults: `statement_timeout 5s`, `lock_timeout 2s`, `idle_in_transaction_session_timeout 10s` |
| `deedglass_witness` | `LOGIN NOINHERIT REPLICATION` (RDS and Aurora: no attribute, `rds_replication` instead) | `USAGE` on schema `deedglass` and the watched tables' schemas, `SELECT` on the ledger and the watched tables. Pinned session settings: `TimeZone UTC`, `DateStyle 'ISO, YMD'`, `IntervalStyle postgres`, `bytea_output hex`, `extra_float_digits 3` |

The installer needs `CREATEROLE` (or superuser) to create the roles, and must own the watched
tables (or be a superuser) to set their replica identity and add them to the publication. On PG 16
and later, a non-superuser installer is granted `deedglass_owner` (`GRANT deedglass_owner TO
CURRENT_USER`) so it can create objects as that role; `init` plans that line when it's needed.

## Objects

- Schema `deedglass`, owned by `deedglass_owner`, `USAGE` for the agent and the witness only.
- `deedglass.ledger`: one row per action (`action_id` primary key, `org_id`, `agent_id`,
  `intent_hash`, `sql_digest`, `xid8`, `created_at`), row level security forced, replica identity
  full, no privileges for `deedglass_agent` or PUBLIC.
- `deedglass.begin_action(text, text, text, text, text)`: `SECURITY DEFINER`, `search_path`
  pinned to `pg_catalog, pg_temp`, `EXECUTE` for `deedglass_agent` only. Its body's sha256 is
  pinned in Deedglass (`sha256:2f3f…7bd4`).
- Publication `deedglass_pub` `FOR TABLE deedglass.ledger, <watched tables>` publishing insert,
  update, delete and truncate, with no row filters or column lists. It is created before any slot
  exists: on PG 18 a slot whose publication is missing only warns and silently skips changes.
- Each watched table gets `REPLICA IDENTITY FULL` (a brief `ACCESS EXCLUSIVE` lock, bounded by
  `lock_timeout 5s`).
- The slot `deedglass_` plus the connector id without `con_`, lowercased (36 characters), is created
  by the witness in S20, never by `init`.
- The control-plane database and a watched database are never the same database: `init` refuses a
  database with `deedglass.schema_migrations`, and `deedglass migrate` refuses one with
  `deedglass.ledger`.

## Disclosures

- **A REPLICATION role can decode every row change in its database,** not only the watched tables.
  Treat `witness_dsn` like a read-everything credential.
- **An inactive slot keeps WAL on disk** until it advances or is dropped (`deedglass decoder drop`,
  S20). `max_slot_wal_keep_size` turns a full disk into an invalidated slot instead.
- **WAL cost.** `REPLICA IDENTITY FULL` writes the old row into WAL on update and delete. On narrow
  rows the spike measured about 5% more WAL per transaction (956 to 1,005 bytes for two updates and
  two inserts), and the full V0 path with a ledger row and a marker 2,517 bytes. This was a laptop
  micro-measurement of 1,000 sequential transactions on PG 18.6, not a benchmark; wide rows cost
  more. Source: `ops/spikes/evidence/wal_pg18.txt`.
- **Statement overhead.** On localhost the connector added 0.14 ms at p50 over raw pgx for a one-row
  UPDATE (`bench/results/2026-10-02-darwin-arm64-postgres.json`, `make bench-postgres`).

## What this page does not do

It doesn't cover the control-plane database Deedglass itself runs on ([DATABASE_SETUP.md](DATABASE_SETUP.md)),
or databases other than Postgres.
