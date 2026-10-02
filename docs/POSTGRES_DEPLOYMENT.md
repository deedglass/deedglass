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
[POSTGRES.md](POSTGRES.md#providers); the notes below say what we have and haven't run.

## Sizing

The decoder ([DECODER.md](DECODER.md)) reads the slot every second and advances it once the
evidence is stored, and it advances past WAL that has nothing to do with the watched tables
(ADR-045). So in normal running the slot holds about one poll's worth of WAL. What
`max_slot_wal_keep_size` sizes is how long the decoder may be down (a stopped `deedglass serve`, an
unreachable database, a network partition) before Postgres gives up the slot.

Start at **2 to 10 GB** and size it from your WAL rate:

1. Measure the rate over a busy hour:
   `SELECT pg_current_wal_lsn();`, wait an hour, run it again, then
   `SELECT pg_wal_lsn_diff('<second>', '<first>') / 3600 AS bytes_per_second;`
2. Pick how long the decoder may be down before the slot is invalidated, say 30 minutes.
3. `max_slot_wal_keep_size` = rate × that time, plus headroom. At 2 MB/s: 2 MB × 1,800 s = 3.6 GB,
   so `4GB`. At 5 MB/s, `10GB` covers about 34 minutes.
4. Make sure the disk holding `pg_wal` has that much free space on top of `max_wal_size`.

The decoder alerts (`lag_high`) at min(`DEEDGLASS_SLOT_LAG_ALERT_MB`, a quarter of
`max_slot_wal_keep_size`), so with `4GB` you hear about a stall at 512 MB, long before the slot is
lost. A slot that is lost anyway halts the decoder (`slot_invalidated`); `deedglass decoder drop`
starts a new one, and reconcile records the actions that fell in the gap as missing.

## The decoder's slot

- The slot `deedglass_<connector id>` is created by the decoder, as `deedglass_witness`, after it has
  checked that `deedglass_pub` exists and contains `deedglass.ledger` and every watched table. The
  creation is sealed as the admin deed `decoder.slot_created`; dropping it (`deedglass decoder
  drop`, or deleting the connector) seals `decoder.slot_dropped`.
- One slot and one ordinary connection per watched connector. The decoder reads with SQL
  (`pg_logical_slot_peek_binary_changes`), so it uses no walsender today; keep the headroom for the
  streaming transport.
- The decoder writes a heartbeat every 10 s with `pg_logical_emit_message`. If you revoke that
  function from PUBLIC (the optional hardening `check` suggests), grant it to `deedglass_witness` as
  well as `deedglass_owner`; without it the decoder still reads, but a stall shows only as lag.
- Its session pins `TimeZone`, `DateStyle`, `IntervalStyle`, `bytea_output` and
  `extra_float_digits` in the connection's startup packet, which outranks `ALTER ROLE … SET`, and
  checks them before every read.

## Providers

The provider steps (parameter groups, flags, which role can grant REPLICATION) are in
[POSTGRES.md](POSTGRES.md#providers), re-checked against each provider's documentation on
2026-10-02. What we have run the decoder on:

| Where | Status |
|---|---|
| Local Postgres 16.15, 17.11, 18.6 (Homebrew) | tested: the integration suite on all three (`make matrix`) |
| Docker (`postgres:16.15`, `17.11`, `18.6`) | tested: CI's integration job and the Acme demo |
| Amazon RDS, Aurora, Supabase, Neon, Google Cloud SQL, Azure | untested; guidance only ([CLOUD_COMPATIBILITY.md](CLOUD_COMPATIBILITY.md)) |

## Roles

Roles are cluster-wide: two databases on one cluster share them, and their passwords. Rotating the
passwords for one connector (`init --rotate-passwords`) rotates them for every connector on that
cluster; see [shared roles](POSTGRES.md#shared-roles-on-one-cluster).

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
- **Decoded row images are stored in Deedglass** for `decoder.retention` (7 days by default; frames
  with no Deedglass action, 30 days), under row-level security. Columns in
  `witness.sensitive_columns` are stored only as a sha256 of their text.
- **PG 18's `idle_replication_slot_timeout`** invalidates a slot that nobody reads for that long. The
  decoder reads every second, so only a decoder that is down longer than the timeout loses its slot;
  keep it at 0 or at least a day.
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
