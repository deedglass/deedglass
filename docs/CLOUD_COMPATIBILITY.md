# Cloud compatibility

Which Postgres services work with the Postgres connector, and how well we know it. A label is only
as good as its evidence: **Tested** means a dated run of the connector's integration suite or
`deedglass connector postgres check` on file; **Guidance only** means the steps come from the
provider's documentation (re-read on 2026-10-02) and nothing has run there yet.

| Service | Label | Evidence | Notes |
|---|---|---|---|
| PostgreSQL 16, 17, 18 (self-hosted, local) | **Tested** | `make integration` and `make matrix` (16.15, 17.11, 18.6), S17 | the whole suite: init, check, every tool, the decode test |
| Docker (`postgres:16.15`, `17.11`, `18.6`) | **Tested** | `make demo-up && make demo-check` (18.6), S17 | the Acme demo: `Ready for Proven checks · 14 passed · 3 notes · 0 failed` |
| Amazon RDS for PostgreSQL | Guidance only | AWS RDS User Guide | `rds.logical_replication = 1` (static, reboot); witness via `rds_replication` |
| Amazon Aurora PostgreSQL | Guidance only | AWS Aurora User Guide | the same parameter in a DB cluster parameter group; reboot the writer |
| Supabase | Guidance only | Supabase docs | direct connection, not the pooler; witness role on PG 17+, `postgres` before |
| Neon | Guidance only | Neon docs | logical replication on in the Console (restarts computes, can't be reverted); REPLICATION only through `neon_superuser`; inactive slots removed after about 40 hours |
| Google Cloud SQL | Guidance only | Cloud SQL docs | `cloudsql.logical_decoding = on` |
| Azure Database for PostgreSQL (Flexible) | Guidance only | Microsoft Learn | `wal_level = logical`, `max_worker_processes` ≥ 16 |

[POSTGRES.md](POSTGRES.md#providers) has each provider's steps, and `check` prints the provider's
fix when `wal_level` isn't `logical`.

## What L2 needs from a service

- Postgres 16, 17 or 18.
- `wal_level = logical`, with room for one replication slot and one WAL sender per watched connector.
- A role with REPLICATION (or the provider's equivalent), and permission to create a publication on
  the watched tables.
- `REPLICA IDENTITY FULL` on each watched table.
- A direct connection for the witness: logical replication never works through a transaction pooler.

## What this page does not do

It doesn't label a service Tested until a smoke run exists. If you have a throwaway project on a
guidance-only service, `deedglass connector postgres check --dsn-env …` is read-only and safe to
run against it.
