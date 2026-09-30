> V0 reference. Superseded by Deedglass; kept for the L2 design.

# Observation boundary (V0)

What the boundary covers, how it is committed and hashed, and how drift is detected and classified (`20_ARCHITECTURE_SPEC_V0.md` §9).

## What V0 specified

- **Explicit list.** The publication `deedglass_pub` names each watched table. `FOR ALL TABLES` needs superuser, so most managed services can't use it.
- **Boundary manifest.** A catalog snapshot, serialized as JCS and hashed as `boundary_hash`: the publication, member relations, columns, constraints, triggers, rules, row-level policies, the controlled function, the active bundle set and the privilege-audit result.
- **Drift detection.**
  - In-stream: each RELATION message must match the committed columns and replica identity.
  - Bracketing snapshots: `pg_current_wal_lsn()`, one catalog query, `pg_current_wal_lsn()` again, taken around each commit LSN.
  - A startup audit.
- **Classification.** A transaction is eligible for VALID only when snapshots on both sides of its commit LSN match the committed hash. Detected drift gives INCONCLUSIVE `BOUNDARY_DRIFT`. Effects with no WAL or catalog signal are OUTSIDE SCOPE.
- The replication slot was `deedglass_v0` and the logical message prefix `deedglass`.

## Where this lands in Deedglass

The boundary becomes part of the Witness at level L2 (Witnessed): S19 builds the Witness engine, S20 the Postgres decoder (`internal/decoder`), and S21 the L2 expected-versus-observed check.

## What this page does not do

It does not cover reads, effects outside the WAL (pg_net, FDW, NOTIFY), unlogged or temp tables, sequences, large objects or other databases. A privileged change that is added and removed between two snapshots is outside scope.
