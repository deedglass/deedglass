# The Postgres connector

The Postgres connector runs agent SQL in a transaction Deedglass controls. Every statement runs as
the least-privilege role `deedglass_agent`, one statement per action, and every write commits
together with a ledger row and a logical marker that the witness reads (S20 and S21: L2, [WITNESS.md](WITNESS.md)). Setting up a
database takes two commands: `init` plans and applies the roles, schema, grants and publication;
`check` tells you whether the database is ready for L2.

```
$ deedglass connector postgres check --connector postgres
PostgreSQL 18.6 · acme · 127.0.0.1 · local
✓ wal_level          logical
✓ slot capacity      0 of 10 slots used · 0 of 10 senders
✓ schema deedglass   ledger · RLS forced · replica identity full
✓ begin_action       security definer · search_path pinned · body sha256:2f3f…7bd4
✓ agent role         least privilege · 2 writable tables · no ledger access
✓ witness role       replication · pinned settings · read-only
✓ watched tables     3 of 3 · primary keys · replica identity full
✓ publication        deedglass_pub · 4 tables · insert, update, delete, truncate
○ slot               not created yet · the witness creates deedglass_01k67c04kxevyex3pjyqe0t45d on first start
Ready for L2 · 14 passed · 3 notes · 0 failed
```

## Set it up

1. **Create the connector** from a config file (the Acme demo's is
   `examples/demo/connectors/postgres.yaml`):
   ```sh
   deedglass connector create --config examples/demo/connectors/postgres.yaml
   ```
2. **Plan.** Put an admin DSN in your shell (a superuser, or a role with `CREATEROLE`, such as the
   RDS master user). Deedglass reads it once and never stores it.
   ```sh
   export DEEDGLASS_INIT_DSN='postgres://postgres:…@127.0.0.1:5432/acme?sslmode=disable'
   deedglass connector postgres init --connector postgres
   ```
   ```
   Deedglass · Postgres setup plan · acme on 127.0.0.1:5432 · PostgreSQL 18.6 · local
     + role deedglass_owner · NOLOGIN
     + role deedglass_agent · password generated at apply
     + role deedglass_witness · REPLICATION · password generated at apply
     + schema deedglass · ledger · begin_action (sha256:2f3f…7bd4)
     + grants · read public (5 tables) · write public.orders, public.refunds
     + replica identity full · 3 watched tables · brief lock on each
     + publication deedglass_pub · 4 tables
   7 changes · SQL for review: .deedglass/plans/postgres-20261006T1112Z.sql
   Next: deedglass connector postgres init --connector postgres --apply
   ```
3. **Review** the SQL file. It is the exact transaction `--apply` runs, without the passwords.
4. **Apply.**
   ```
   $ deedglass connector postgres init --connector postgres --apply
   ◐ Applying 7 changes in one transaction · lock_timeout 5s
   ✓ Applied · 7 changes · 32 ms
   ✓ Credentials stored · postgres · deedglass_agent@127.0.0.1:5432/acme · deedglass_witness@127.0.0.1:5432/acme
     Passwords were generated here and sent to Postgres as SCRAM verifiers. They weren't printed or logged.
     Sealed connector.postgres_initialized · deed_01m3wnr8cet3fcp8t4r23jfdwt
   Next: deedglass connector postgres check --connector postgres
   ```
5. **Check:** `deedglass connector postgres check --connector postgres`.

Run `init` again whenever the config changes (a table becomes writable or watched). A second run on
an unchanged database prints `Nothing to change.`

## Tools

A connector named `postgres` serves these tools. Every argument schema is closed
(`additionalProperties: false`).

| Tool | Side effect | Arguments | Offered |
|---|---|---|---|
| `postgres.query` | read | `sql` (≤ 64 KiB), `params` (≤ 100 values), `limit` (≤ `row_limit`) | always |
| `postgres.execute` | write; destructive for a `DELETE` | `sql`, `params` | `mode: sql` |
| `postgres.call` | the function's own, from the config | `function` (allow-listed), `args` (named) | when `functions` is set |
| `postgres.describe` | read | `schema`, `table` (both optional) | always |

Params are strings, integers up to 2^53−1, booleans or null. Send decimals and larger numbers as
strings (`"0.1"`), so the digest is exact.

**`postgres.query`**

```json
{"tool": "postgres.query", "args": {"sql": "SELECT id, status FROM orders WHERE id = $1", "params": [42]}}
```
```json
{"columns": [{"name": "id", "type": "int4"}, {"name": "status", "type": "text"}], "rows": [[42, "paid"]], "row_count": 1, "truncated": false}
```

It runs in `BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY` and always rolls back. At most
`row_limit` rows (500 by default) and `max_result_bytes` come back; past either, Deedglass stops
reading, closes the connection so Postgres stops sending, and sets `truncated`.

**`postgres.execute`**

```json
{"tool": "postgres.execute", "args": {"sql": "UPDATE orders SET status = 'shipped' WHERE id = $1", "params": [42]}}
```
```json
{"command": "UPDATE", "rows_affected": 1, "returning": null}
```

With `RETURNING`, `returning` holds `columns`, `rows`, `row_count` and `truncated`. A statement that
changes more than `max_rows_affected` rows (1,000 by default) is rolled back:
`row_limit_exceeded`, and nothing is committed.

**`postgres.call`** runs an allow-listed function with named, typed arguments. The agent sends
values only; the statement comes from the config:

```json
{"tool": "postgres.call", "args": {"function": "public.ship_order", "args": {"p_order_id": 42}}}
```
runs `SELECT * FROM "public"."ship_order"("p_order_id" => $1::bigint)` in the same transaction
recipe as `execute`.

**`postgres.describe`** lists the tables the agent can read: columns (name, type, nullable),
primary keys, row estimates (`reltuples`), and whether each table is writable and watched. Table
comments are included, cut at 200 characters, and the output says they are data written by
people, not instructions. Results are cached for 60 seconds per connector.

## Modes

- `sql` (default): all four tools.
- `controlled_functions`: no `postgres.execute`. Writes happen only through the functions you
  allow-list, which is the strongest setup: the agent never writes SQL that changes data.

## Config

| Key | Default | Rules |
|---|---|---|
| `mode` | `sql` | `sql` or `controlled_functions` |
| `allowed_schemas` | required | 1–16 lowercase schema names; the read scope and the `search_path`, in order. Never `deedglass`, `pg_catalog` or `information_schema` |
| `writable_tables` | `[]` | `schema.table` in an allowed schema; `deedglass_agent` gets `INSERT`, `UPDATE`, `DELETE` on these and nothing else |
| `row_limit` | 500 | 1–10,000 rows returned by `query` (and `RETURNING`) |
| `max_result_bytes` | 4 MiB | 1 KiB–16 MiB of JSON rows |
| `max_rows_affected` | 1,000 | over this, the write rolls back (`row_limit_exceeded`) |
| `statement_timeout_ms` | 5,000 | 100–60,000 |
| `lock_timeout_ms` | 2,000 | 100 up to `statement_timeout_ms` |
| `max_connections` | 4 | 1–16 per connector |
| `witness.enabled` | false | S20 creates a slot only for connectors with the witness on |
| `witness.watched_tables` | `[]` | must include every writable table when the witness is on |
| `witness.sensitive_columns` | `[]` | `schema.table.column` in a watched table; stored as digests and masked in the Console, diffs and approvals (S20–S22) |
| `witness.auto_columns` | none | S21: `{schema.table: [column, …]}`, columns a trigger maintains on purpose (`updated_at`); L2 doesn't count them as `unexpected_column` |
| `witness.max_rows` | none | S21: `{schema.table: n}`, the most rows one action may change in a watched table (`row_limit_exceeded`) |
| `witness.strict_triggers` | `false` | S21: triggers on a footprint table are the gap `triggers_unwatched` instead of a note |
| `witness.l2_timeout` | `10s` | S21: how long a write waits for its frame (1s to 60s); past it the deed is sealed with `l2_timeout`, and a late frame amends it |
| `witness.diff_rows` | `20` | S21: changed rows kept in the L2 diff (1 to 100) |
| `functions[].writes` | none | S21: `[{table, ops, max_rows, columns}]`, what a controlled function writes, so `postgres.call` gets an L2 footprint ([WITNESS.md](WITNESS.md)) |
| `functions` | `[]` | `{name: schema.function, args: [{name, type}], side_effect: read\|write\|destructive, description}`; at most 64, no overloads |
| `throwaway` | false | `true` only for disposable databases: `init --apply` then needs no `--confirm` |

Unknown keys, `witness.*` included, are errors: a typo never loosens a limit.

## Credentials

The connector's credential has kind `dsn` with two fields: `dsn` (connects as `deedglass_agent`)
and `witness_dsn` (connects as `deedglass_witness`; optional until S20). `init --apply` stores both
in the vault. The admin DSN that `init` uses is never stored anywhere.

DSNs are libpq URLs or key=value strings. Every connection goes through Deedglass's egress door:
cloud metadata and reserved addresses never; private and loopback addresses only with
`DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1` (dev) or `DEEDGLASS_EGRESS_ALLOW_CIDRS`. In prod, a host that
isn't local needs `sslmode=verify-full` or `verify-ca`; `require` is accepted with a warning;
`disable`, `allow` and `prefer` are refused. Unix sockets are dev only. A DSN may not set
`search_path`, `role` or `options`: Deedglass sets the session itself.

## The least-privilege model

```
customer database (for example acme)
├─ role deedglass_owner    NOLOGIN · owns schema deedglass, the ledger and begin_action
├─ role deedglass_agent    LOGIN · reads the allowed schemas · writes only writable_tables
│                          · EXECUTE begin_action · no ledger privileges · no DDL · member of nothing
├─ role deedglass_witness  LOGIN REPLICATION (RDS: rds_replication) · SELECT on the ledger and the
│                          watched tables · pinned session settings
├─ schema deedglass
│  ├─ table ledger         RLS forced · REPLICA IDENTITY FULL · primary key action_id
│  └─ function begin_action(…)  SECURITY DEFINER · search_path pinned · body sha256 pinned in Deedglass
├─ publication deedglass_pub  FOR TABLE deedglass.ledger, the watched tables · insert, update, delete, truncate
└─ slot deedglass_<connector id without con_>   (created by the witness in S20, never by init)
```

`deedglass_agent` never gets `TRUNCATE`, `REFERENCES`, `TRIGGER` or `CREATE`, and nothing on schema
`deedglass` but `USAGE` and `EXECUTE` on `begin_action`. Its sessions default to
`statement_timeout = 5s`, `lock_timeout = 2s` and `idle_in_transaction_session_timeout = 10s`.

Every new connection runs a role safety probe: it reads the role's attributes and walks its
memberships for `pg_write_all_data`, the server-file roles, `rds_superuser`, `azure_pg_admin`,
`cloudsqlsuperuser`, `neon_superuser` and `supabase_admin`. In prod, a hit (or any role other than
`deedglass_agent`) refuses the connection with `database_role_unsafe`, and nothing runs. In dev it
logs a warning and `check` fails `agent_role`.

## The ledger and the marker

Each write runs `deedglass.begin_action` first, in the same transaction as the agent's statement:

```sql
BEGIN ISOLATION LEVEL READ COMMITTED READ WRITE;
SELECT set_config('statement_timeout', '5000ms', true), …;        -- one batch,
SELECT xid8, marker_lsn FROM deedglass.begin_action($1, …, $5);    -- one round trip
UPDATE orders SET status = 'shipped' WHERE id = $1;                -- the agent's statement
COMMIT;
```

`begin_action` refuses to run unless it is the transaction's first write (`DG101`), with
`session_replication_role = origin` (`DG102`) in a read-write transaction (`DG103`). It inserts the
ledger row (`action_id`, `org_id`, `agent_id`, `intent_hash`, `sql_digest`, `xid8`, `created_at`)
and emits one transactional logical message with prefix `deedglass` and the action id as its
content. A logical decoder then shows, under one transaction id:

```
BEGIN 821
table deedglass.ledger: INSERT: action_id[text]:'act_01m3wnvac0eyah18j0rfsp6nf9' …
message: transactional: 1 prefix: deedglass, sz: 30 content:act_01m3wnvac0eyah18j0rfsp6nf9
table public.orders: UPDATE: …
COMMIT 821
```

The ledger row, the marker and the change commit together or not at all. The function's body has
a pinned sha256 (`setup.BeginActionBodySHA256`); `check` fails when the live body differs.

`sql_digest` is `"sha256:" + hex(sha256(JCS({"params": […], "sql": "…"})))`; for `postgres.call`
it is the digest of `{"args": {…}, "function": "public.ship_order"}`. The witness recomputes it.
The marker's LSN is evidence only and is never compared with the commit LSN.

**What the marker can't do on its own.** Any role that can call `pg_logical_emit_message` (PUBLIC,
by default) can write a message that looks like a marker. The ledger row in the same transaction is
what authenticates it, and only `begin_action` can write the ledger. Anyone holding
`deedglass_agent`'s credentials can call `begin_action`, so Deedglass witnesses what Postgres
committed, not whether a compromised Deedglass told the truth. `check` suggests revoking
`pg_logical_emit_message` from PUBLIC as optional hardening.

## What the connector refuses, even when policy allows it

Policy decides what an agent may do. The connector also refuses some SQL outright, with
`statement_refused`, before anything reaches the database:

| Refused | Tools |
|---|---|
| More than one statement | all |
| A kind that doesn't match the tool: `query` runs only `SELECT` (no data-changing CTE, no `SELECT INTO`); `execute` runs only `INSERT`, `UPDATE`, `DELETE` or `MERGE` | query, execute |
| Transaction control, `SET`, `RESET`, `DISCARD`, `LISTEN`, `NOTIFY`, `COPY`, `DO`, `CALL`, `GRANT`, `REVOKE`, any DDL (including `TRUNCATE`), `EXPLAIN ANALYZE`, and SQL the classifier can't read | all |
| Any reference to schema `deedglass` | all |
| Denylisted functions (unquoted, `pg_catalog.` stripped, case folded): `pg_logical_emit_message`, `set_config`, `pg_read_file`, `pg_read_binary_file`, `pg_ls_dir`, `pg_stat_file`, `lo_import`, `lo_export`, `dblink*`, `pg_terminate_backend`, `pg_cancel_backend`, `pg_reload_conf`, `pg_advisory_lock`, `pg_advisory_lock_shared`, `query_to_xml*`, `table_to_xml*`, `cursor_to_xml`, `pg_notify` | all |
| `sql` over 64 KiB or more than 100 params | `validation_failed` |

The denylist is defence in depth. The real boundary is the role, the extended query protocol (one
statement per execution, enforced by Postgres), and `DISCARD ALL` between actions. One pgx detail
matters here: `Conn.Exec` with no arguments uses the simple protocol, which runs stacked
statements, so the connector sends every agent statement through `Conn.Query` with an explicit
exec mode. A test bypasses admission and confirms that `UPDATE …; DROP TABLE …` is refused by
Postgres itself.

## Values

Rows come back as JSON arrays, converted by type:

| Postgres | JSON |
|---|---|
| `int2`, `int4` | number |
| `int8` | number up to 2^53−1, else a string |
| `numeric` | string (`"0.1"`) |
| `float4`, `float8` | number; `NaN` and `±Infinity` as strings |
| `bool` | `true` / `false` |
| text types, `uuid`, enums, `inet` | string |
| `bytea` | `"\\x…"` |
| `date` | `"2026-10-06"` |
| `timestamptz` | RFC 3339 UTC with microseconds: `"2026-10-06T11:12:00.123456Z"` |
| `timestamp` | the same without a zone |
| `interval` | Postgres text: `"1 day 02:00:00"` |
| `json`, `jsonb` | embedded JSON |
| arrays | arrays, element by element |
| `NULL` | `null` |

`columns[].type` is the type's name (`int8`, `text`, `int4[]`).

## `init`

```
deedglass connector postgres init --connector <name> [--admin-dsn-env DEEDGLASS_INIT_DSN] [--config <file>]
  [--apply] [--confirm <database>] [--rotate-passwords] [--dsn-host host:port] [--no-store] [--json]
```

- **Plan.** `init` reads the connector's config (from the server, or offline with `--config`),
  connects with the admin DSN, reads the catalogs with parameterized queries, and diffs them with
  the config. The plan's order is roles, membership, schema, grants, revokes, replica identity,
  publication. The SQL goes to `.deedglass/plans/<connector>-<UTC>.sql` (mode 0600). It never holds
  a password or a verifier.
- **Apply gate.** `--apply` runs right away when the host is local (`localhost`, `127.0.0.1`, `::1`
  or a socket) or the config says `throwaway: true`. Otherwise it needs `--confirm <database>`,
  typed exactly and checked against `current_database()`; on a terminal it asks, and without one
  it refuses.
- **Apply.** One transaction with `lock_timeout = 5s`. It reads the catalogs again first, and
  refuses if the database changed since the plan (`Run init again to see the new plan.`).
- **Passwords.** For each login role created in this run (or both, with `--rotate-passwords`),
  `init` generates a 32-character base62 password from `crypto/rand` and sends Postgres a
  SCRAM-SHA-256 verifier (RFC 5802 and 7677: a 16-byte salt, 4,096 iterations), as psql's
  `\password` does. Postgres never sees the password, so it never reaches the server log.
- **Store.** `dsn` and `witness_dsn` go to the vault (`PUT /v1/connectors/{id}/credentials`), on
  the admin DSN's host, port, database and `sslmode`, or `--dsn-host` when the server reaches the
  database another way (the demo stores `orders-db:5432`). With `--no-store`, or when the server
  can't be reached, they go to `.deedglass/pg-<connector>.env` (mode 0600), and only the path is
  printed.
- **Record.** `init` seals the admin deed `connector.postgres_initialized` with the plan's sha256
  and change count (`POST /v1/connectors/{id}/initialized`).

When the roles already exist (another database on the same cluster, or an earlier run), `init`
leaves their passwords alone: pass `--rotate-passwords` to set new ones and store new DSNs. Roles
are cluster-wide in Postgres, so two databases on one cluster share them.

### Shared roles on one cluster

`deedglass_owner`, `deedglass_agent` and `deedglass_witness` belong to the Postgres **cluster**,
not to one database. Every database on that cluster that `init` prepares uses the same three roles
and the same two passwords. Three things follow:

- `init --rotate-passwords` for one connector changes the password for **every** connector on that
  cluster. Only the connector you ran it for gets the new DSNs in the vault; the others fail with
  `upstream_auth_failed` until you store the new DSNs for them too (run `init --rotate-passwords
  --apply` once, then `deedglass connector credentials set` for each other connector on that
  cluster with the same DSNs, changing only the database name).
- Grants stay per database: `deedglass_agent` can write only the `writable_tables` of each database
  `init` prepared, and `deedglass_witness` reads only those databases' watched tables.
- To keep connectors fully independent, put their databases on separate clusters. Separate
  role names per database are not supported in v1.

## `check`

```
deedglass connector postgres check --connector <name> [--json]
deedglass connector postgres check --dsn-env VAR [--witness-dsn-env VAR] [--config <file>] [--probe] [--json]
```

The first form runs on the server (`POST /v1/connectors/{id}/test`) with the stored DSNs. The
second runs the same code on your machine. Both are read-only, except `--probe`, which runs
`begin_action` with throwaway ids and then tries a direct ledger insert as `deedglass_agent`
(which must get `42501`), all in a transaction it rolls back. `check` exits 1 when any line fails.

| id | pass | note (○) | fail |
|---|---|---|---|
| `connection` | connects as `deedglass_agent` | | can't connect, or the role is superuser-class or another role |
| `version` | 16 or later | 14–15: untested | before 14 |
| `wal_level` | `logical` | | anything else, with the provider's fix |
| `slot_capacity` | 2 or more free slots and senders | exactly 1 free | none free |
| `wal_keep_cap` | `max_slot_wal_keep_size` set | `-1`: a stalled witness can fill the disk; set `4GB` | |
| `idle_slot_timeout` (18) | `0` or at least 1 day | under 1 day: the slot can be invalidated | |
| `schema` | the ledger's shape, RLS forced, replica identity full | | missing or different |
| `begin_action` | owner, security definer, pinned `search_path`, body sha256 equals the pin | | any difference: `body changed: expected sha256:2f3f…7bd4, found …` |
| `agent_role` | attributes, memberships and grants match the config; no ledger access | extra read grants outside the allowed schemas | superuser-class, writes outside `writable_tables`, ledger access, missing grants |
| `witness_role` | REPLICATION (or `rds_replication`), pinned settings, read-only | a provider-wide role (`neon_superuser`, or `postgres` on Supabase before 17) | no replication, settings not pinned |
| `witness.watched_tables` | exist, primary key, replica identity full, logged | no primary key; triggers (named); partitioned | missing, not full, `UNLOGGED` |
| `writable_watched` | every writable table is watched | | a writable table isn't watched |
| `publication` | the ledger plus the watched tables; all four operations; no row filters or column lists | extra tables | missing, `FOR ALL TABLES`, missing tables, filters |
| `slot` | exists, active, `wal_status` reserved | not created yet (the witness creates it); inactive; `extended` | `lost`, or an invalidation reason |
| `tls` | on | off to a local host (or a remote host in dev) | off to a remote host in prod |
| `pooler` | direct | `dsn` goes through a transaction pooler | `witness_dsn` goes through a pooler |
| `emit_message_acl` | revoked from PUBLIC | PUBLIC may call it (the default) | |
| `probe` (`--probe`) | ledger and marker work; the agent's direct insert gets 42501 | | otherwise |

A failing line prints one sentence to fix it, and SQL where it helps (`fix_sql` in `--json`). The
summary is `Ready for L2 · 14 passed · 3 notes · 0 failed`, or `Not ready for L2 · 2 items to fix · …`.

**Through a transaction pooler** (port 6543, or `pgbouncer=true`): statements use pgx's
`QueryExecModeExec` (the extended protocol, one round trip, parameter types inferred by the
server) and skip `DISCARD ALL`, because the pooler resets sessions. Logical replication never works
through a pooler, so `witness_dsn` must be a direct connection.

## Providers

Re-checked against each provider's documentation on 2026-10-02. Only local Postgres 16, 17 and 18
and Docker are tested; the rest is guidance until a smoke run exists
([CLOUD_COMPATIBILITY.md](CLOUD_COMPATIBILITY.md)).

- **Local.** `ALTER SYSTEM SET wal_level = logical;`, then restart (`pg_ctl -D "$PGDATA" restart`).
  Set `max_slot_wal_keep_size = '4GB'`.
- **Docker.** Can't be told apart from local. In compose:
  `command: ["postgres", "-c", "wal_level=logical", "-c", "max_slot_wal_keep_size=4GB"]`, then
  `docker compose up -d --force-recreate`. The Acme demo already does this.
- **Amazon RDS.** Set `rds.logical_replication = 1` in a custom DB parameter group and reboot the
  instance; the parameter is static, and turning it on needs `rds_superuser`. There is no
  REPLICATION attribute: `init` creates the witness without it and runs
  `GRANT rds_replication TO deedglass_witness`. Run `init` as the master user.
- **Amazon Aurora.** The same parameter in a custom DB cluster parameter group, then reboot the
  writer instance.
- **Supabase.** Use the direct connection, not the pooler (IPv4-only clients need the IPv4 add-on).
  On Postgres 17 and later, `postgres` can create a REPLICATION role, so `init` creates
  `deedglass_witness`; before 17 the witness must be `postgres` itself, which can read and change
  everything, and `check` says so.
- **Neon.** Turn on logical replication in the Console (Settings › Postgres › Logical replication ›
  Enable); it restarts the project's computes and can't be reverted. Only roles made in the
  Console, CLI or API get REPLICATION (through `neon_superuser`, which is broad; `check` says so).
  Neon removes inactive slots after about 40 hours, allows 10 slots and senders, and a connected
  witness keeps the compute awake, which costs money.
- **Google Cloud SQL** (guidance only): set the flag `cloudsql.logical_decoding = on` (the instance
  restarts), then `ALTER USER deedglass_witness WITH REPLICATION`.
- **Azure Flexible Server** (guidance only): set `wal_level = logical` and `max_worker_processes` to
  at least 16, restart, then `ALTER ROLE deedglass_witness WITH REPLICATION`.

## Errors

| Code | When |
|---|---|
| `statement_refused` | admission refused the SQL (nothing sent), or Postgres refused a second command or a write in a read-only transaction |
| `row_limit_exceeded` | a write changed more than `max_rows_affected` rows; rolled back |
| `database_role_unsafe` | the DSN's role isn't least privilege; prod refuses the connection |
| `database_not_ready` | `begin_action` is missing or `deedglass_agent` can't run it: run `init` |
| `timeout` | `statement_timeout` or `lock_timeout` stopped the statement; nothing was committed |
| `upstream_error` | Postgres refused the statement (a constraint, by name only; a permission, with SQLSTATE 42501); nothing was committed. A lost answer to `COMMIT` is the one case that may have applied |

Every refusal before `COMMIT` leaves nothing committed, so the action is `failed`, never
`unknown`. Writes are never retried. See [api/errors.md](api/errors.md).

## What it doesn't do

- **The marker alone can be forged;** the same-transaction ledger row authenticates it, and anyone
  holding `deedglass_agent`'s credentials can write ledger rows through `begin_action`.
- **A REPLICATION role can decode every row change in the database,** not only the watched tables.
- **`row_limit` caps what comes back, not what Postgres reads;** `statement_timeout` bounds the work.
- **Triggers can write unwatched tables.** Those writes are invisible to L2 (S21 reports them as
  UNVERIFIABLE, never MISMATCH).
- **DDL is blocked, not witnessed.**
- **New tables aren't granted automatically:** run `init` again after you add a table (no
  `ALTER DEFAULT PRIVILEGES` in v1).
- **Overloaded functions** can't be allow-listed; give each controlled function its own name.
- **No slot, decoder or verdict yet:** S20 creates the slot and reads the change stream, S21 turns
  it into L2 verdicts. Until then, Postgres actions are L0.
- **MySQL** and other databases are v2.

## Troubleshooting

- `database_not_ready`: `init` hasn't run on this database, or ran on another one. Check the DSN's
  database name.
- `upstream_auth_failed` on every call: the stored password no longer matches (someone changed it,
  or `init` ran on another database of the same cluster with `--rotate-passwords`). Run `init
  --rotate-passwords --apply`.
- `check` fails `begin_action` with `body changed`: someone replaced the function. Find out who
  before you run `init`, which restores it.
- `init --apply` fails with a lock timeout: a long transaction holds a watched table. Wait for it,
  or run `init` in a quieter moment; `REPLICA IDENTITY FULL` needs a brief exclusive lock.
