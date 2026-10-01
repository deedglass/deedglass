// Package postgres is the Postgres connector (S17, docs/POSTGRES.md): it runs agent SQL in a
// transaction Deedglass controls, as the least-privilege role deedglass_agent, and leaves the
// evidence the witness needs (S20–S21): a ledger row and a logical marker in the same transaction as
// the change. Its subpackage setup prepares a customer database (deedglass connector postgres
// init|check).
//
// Tools: postgres.query (read-only transaction, row and byte caps), postgres.execute (one INSERT,
// UPDATE, DELETE or MERGE after begin_action), postgres.call (allow-listed functions, statement built
// from config), postgres.describe (cached 60 s).
//
// Invariants (each is a test):
//   - The ledger row, the marker and the agent's change commit in one transaction or not at all.
//   - begin_action is the first write; the agent's statement always runs after it, as
//     deedglass_agent, through Conn.Query with an explicit exec mode (pgx's Exec with no arguments
//     uses the simple protocol, which runs stacked statements).
//   - postgres.query never commits.
//   - In prod, no connection whose role is superuser-class (or isn't deedglass_agent) runs anything:
//     the AfterConnect probe refuses it with database_role_unsafe.
//   - The admin DSN and generated passwords are never stored by Deedglass or printed; Postgres sees
//     only SCRAM verifiers.
//   - marker_lsn is evidence only and never feeds a comparison.
//   - Every refusal before COMMIT is a failed kind (nothing committed); only a lost COMMIT answer is
//     unknown. Writes are never retried.
//
// The control-plane database and a watched customer database are never the same database
// (init refuses deedglass.schema_migrations; migrate refuses deedglass.ledger). Customer-side
// SQLSTATEs are DG101–DG103; the V0 transfer demo keeps TX001–TX031.
//
// V0 note: V0 ran a transfer through deedglass.execute_transfer_v1 (sql/001–004, bundles/transfer_v1)
// with the same first-write guard, a NOLOGIN owner, a pinned search_path, and the publication before
// the slot. S17 ports those rules to begin_action; the V0 demo SQL stays for the attack suite.
package postgres
