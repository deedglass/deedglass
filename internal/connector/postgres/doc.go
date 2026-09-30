// Package postgres is the Postgres connector: it prepares a customer database (deedglass connector
// postgres init|check), runs SQL through the controlled function deedglass.begin_action, and gives
// the Witness what it needs for L2 (contract §4).
//
// Invariants:
//   - The control-plane database and a watched customer database are never the same database.
//   - Customer-side roles: deedglass_owner (NOLOGIN, owns the controlled functions),
//     deedglass_agent (least privilege), deedglass_witness (REPLICATION, pinned session settings).
//   - The publication deedglass_pub exists before any slot; the slot is deedglass_ plus the
//     connector id without con_, lowercased.
//   - Each action emits one transactional logical message with prefix deedglass and the action id.
//   - SQL rules: no dynamic SQL, no EXCEPTION blocks in controlled functions, schema-qualified
//     objects, search_path pinned to pg_catalog, pg_temp, REVOKE and GRANT with CREATE.
//   - Customer-side SQLSTATEs are DG101–DG103; the V0 transfer demo keeps TX001–TX031.
//
// Planned API (finalized by S17):
//
//	func Init(ctx context.Context, dsn config.Secret, opts InitOptions) (Plan, error)
//	func Check(ctx context.Context, dsn config.Secret) (Report, error)
//
// V0 note (the former executor and pgsql packages; sql/001–004 and bundles/transfer_v1): V0 ran a
// transfer through deedglass.execute_transfer_v1, SECURITY DEFINER, owned by deedglass_owner,
// called once per transaction as the first write (TX001 otherwise), with retries in a new
// transaction. The installer refused minors below 16.15 / 17.11 / 18.6, created the publication
// before the slot, and checked the live prosrc hash against the bundle's fn_src_sha256. The
// privilege audit checked: no DML grants for application roles, no user triggers or rules on
// boundary tables, forced RLS and REPLICA IDENTITY FULL. S17 absorbs these checks.
//
// Filled by: S17
package postgres
