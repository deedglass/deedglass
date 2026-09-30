# Cloud compatibility

Which managed Postgres services work with the Postgres connector, and at which level, with the evidence for each. **Owners: S17** (connector setup and `connector postgres check`) **and S20** (the L2 decoder). This page is a stub.

## What L2 needs from a service

- Postgres 16, 17 or 18.
- `wal_level=logical`, with room for one replication slot and one WAL sender per watched connector.
- A role with REPLICATION, and permission to create a publication on the watched tables.
- `REPLICA IDENTITY FULL` on each watched table.
- `pg_logical_emit_message` available to the controlled function.

## Planned labels

Each service gets one label, backed by a dated `connector postgres check` run: **L2 supported**, **L2 with a setting change**, **L1 only**, or **not tested**. Services to assess include Amazon RDS and Aurora, Google Cloud SQL, Azure Database for PostgreSQL, Supabase, Neon, Crunchy Bridge and self-hosted Postgres.

## What this page does not do

It labels nothing yet. Until a service has a check run on file, treat it as not tested.
