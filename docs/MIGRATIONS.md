# Migrations

The control-plane schema is a sequence of SQL files embedded in the `deedglass` binary (ADR-019). `deedglass migrate up` applies the ones a database doesn't have yet.

```sh
deedglass migrate up            # apply pending migrations
deedglass migrate up --dry-run  # show what would run, apply nothing
deedglass migrate status        # applied and pending, with checksums
deedglass migrate verify        # exit 1 if an applied migration changed
```

Every command takes `--database-url` and `--json`. Exit codes: 0 ok, 1 a problem with the schema, 2 can't connect.

## Forward-only, and why

There are no down migrations. A mistake is fixed by a new migration that corrects it.

- History tables (`deeds`, `action_events`, `admin_events`, `policy_versions`) only grow. A "down" that drops or rewrites them would destroy the record Deedglass exists to keep.
- A down path is rarely tested and usually runs at the worst moment. Rolling forward is the one path that is exercised every time.
- To undo a release, run the previous binary against the same schema only if the new migrations were additive; otherwise, ship a fix forward.

## Checksums

Each file's checksum is `"sha256:" + hex(sha256(file bytes))`. `deedglass.schema_migrations` stores it when the file is applied. `up`, `status` and `verify` compare it with the file this binary carries:

| Finding | What happens |
|---|---|
| An applied file changed | `up` and `verify` stop: "0003 policy changed after it was applied." Restore the original file, and put the change in a new migration. |
| The database has a version this binary doesn't know | `up` and `verify` stop: the database is ahead. Run a newer build, or check the URL. |

## Locking

`up` holds a session advisory lock (`0x646565646d696731`, ASCII "deedmig1") while it works. A second `deedglass migrate up` waits, polling every 250 ms for up to 60 s, then stops with "Another deedglass is migrating this database." Two deploys starting together is safe: one applies, the other finds nothing to do.

## Preflight

Before it writes anything, `up` checks:

| Check | If it fails |
|---|---|
| PostgreSQL 16 or newer | "Deedglass needs PostgreSQL 16 or newer. This server is 15.8." |
| It isn't a customer database (no `deedglass.ledger`, no `deedglass_pub`) | "This database has deedglass.ledger, so it's a watched customer database." |
| Role `deedglass_app` exists | "Role deedglass_app doesn't exist yet." Run `deploy/postgres/control-plane-roles.sql` first. |
| Not a superuser | Dev warns; prod refuses. Run migrations as `deedglass_migrator`. |

## How each migration runs

Each file runs in its own transaction, with:

```sql
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '120s';
```

then the file, then its `schema_migrations` row, then COMMIT. If a file fails, it rolls back alone; the ones before it stay applied.

## Writing a migration

1. Add `internal/store/migrations/NNNN_name.sql` with the next free number. Lowercase letters, digits and underscores; `.up.sql` and gaps are refused.
2. The first line is a header: `-- NNNN_name.sql: <what it does>`.
3. Schema-qualify every name (`deedglass.agents`, `pg_catalog.now()`). The search path is `pg_catalog, pg_temp`, so an unqualified table name fails at once.
4. For every new table, in the same file: `ALTER TABLE … ENABLE ROW LEVEL SECURITY`, the `org_isolation` policy (and `system_scope` only if S02's list says so), `REVOKE ALL … FROM PUBLIC`, and the grants to `deedglass_app`. The catalog test fails on an org table without RLS or with a foreign key that skips `org_id`.
5. A new column on `actions` that the server updates needs its own column-level `GRANT UPDATE (…)`.
6. Don't write `BEGIN`, `COMMIT` or `ROLLBACK`: the runner owns the transaction. Don't use `CONCURRENTLY` in v1: it can't run in a transaction. A large index build needs a documented manual step and an ADR.
7. Never edit a file that has been applied anywhere. Write the next one.

## In CI and in prod

- CI runs every migration on PostgreSQL 16, 17 and 18 (`make matrix`, `pg-matrix.yml`), then the integration suites: the RLS matrix over every org table, the privilege matrix, the chain, the 225 state pairs and the catalog checks.
- In prod, run `deedglass migrate up` as a release step, with `DEEDGLASS_MIGRATE_DATABASE_URL` set, before starting the new `deedglass serve`. Run `deedglass migrate verify` in your health checks if you like: it exits 1 when the schema and the binary disagree.
