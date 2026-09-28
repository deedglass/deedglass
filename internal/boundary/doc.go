// Package boundary commits and watches the observation boundary (spec §9): a
// catalog snapshot of the txv_pub publication, its member relations, columns,
// constraints, triggers, rules, policies, the controlled function, the active
// bundle set and the privilege-audit result, serialized as canonical JCS and
// hashed as boundary_hash. Drift is detected in-stream (RELATION messages must
// match the committed columns and replica identity), by bracketing snapshots
// taken as pg_current_wal_lsn / one-query catalog read / pg_current_wal_lsn
// around each commit LSN, and by a startup audit. A transaction is eligible for
// VALID only when snapshots on both sides of its commit LSN match the
// committed hash; detected drift yields INCONCLUSIVE BOUNDARY_DRIFT.
package boundary
