// Package store owns the control-plane Postgres: the pgx pool, the embedded forward-only
// migrations, org-scoped transactions under row-level security, and the repositories every other
// package uses (contract §4, ADR-019).
//
// Invariants:
//   - Org-scoped queries run inside OrgTx, which sets the transaction-local deedglass.org_id that
//     RLS keys on. There is no other way to read an org's rows.
//   - Cross-org access happens only through SystemTx with a named reason, or through the narrow
//     SECURITY DEFINER functions (preauth_*, claim_*, maint_*, sync_*); a test lists every call site.
//   - The runtime role deedglass_app is NOBYPASSRLS, owns nothing, and cannot UPDATE or DELETE
//     history (deeds, action_events, admin_events, policy_versions).
//   - Migrations are embedded, forward-only, checksummed, named internal/store/migrations/NNNN_name.sql,
//     and an applied file is never edited.
//   - SQL is schema-qualified with a pinned search_path; no SQL is built from user input.
//
// API (S02):
//
//	func Open(ctx context.Context, url config.Secret, o Options) (*DB, error)
//	func (db *DB) OrgTx(ctx context.Context, org ids.OrgID, fn TxFunc) error      // RLS scoped to org
//	func (db *DB) OrgReadTx(ctx context.Context, org ids.OrgID, fn TxFunc) error  // the same, READ ONLY
//	func (db *DB) SystemTx(ctx context.Context, r SystemReason, fn TxFunc) error  // org_create, maintenance, health
//	func (db *DB) CheckRuntimeRole(ctx context.Context) (RoleReport, error)
//	func (db *DB) CreateOrg(ctx context.Context, in NewOrg) (Org, User, Membership, error)
//	GetOrg, GetOrgBySlug, UpsertUserByEmail, GetUser, CreateAgent, GetAgent, GetAgentByName, ListAgents
//
// Migrations: internal/store/migrate (the runner) and internal/store/migrations (the files).
// The schema, and what the database enforces: docs/DATA_MODEL.md.
//
// V0 note: the V0 verifier kept its checkpoint, statement index and nonce memory in SQLite. The
// Deedglass control plane is Postgres; the decoder's checkpoint moves to decoder_slots (S20).
//
// Filled by: S02 (S07 and later add repositories and SECURITY DEFINER functions)
package store
