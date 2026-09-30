// Package ledger is the searchable history of actions and their events, plus the deed chain
// check, export and retention (contract §4, §5).
//
// Invariants:
//   - action_events and deeds are append-only; the database refuses UPDATE and DELETE on them.
//   - The chain check reports the first break with its seq and reason, and never "repairs" it.
//   - Queries are org-scoped through store.OrgTx; list endpoints use cursor pagination.
//   - Retention (ledger prune) removes events past the window, never deeds.
//
// Planned API (finalized by S06):
//
//	func (l *Ledger) Actions(ctx context.Context, org ids.OrgID, q Query) (Page[Action], error)
//	func (l *Ledger) Check(ctx context.Context, org ids.OrgID) (ChainReport, error) // deedglass ledger check
//	func (l *Ledger) Export(ctx context.Context, org ids.OrgID, w io.Writer, q Query) error
//
// Filled by: S06
package ledger
