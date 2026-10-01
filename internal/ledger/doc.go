// Package ledger is Deedglass's own history: every action, every state change, every deed and
// every admin decision in an org, plus the deed chain check, exports and retention (contract §4,
// §5; S06).
//
// Naming: this package has nothing to do with deedglass.ledger, the table S17 creates in a
// customer's database for L2 witnessing. That one records what an agent is about to do inside the
// customer's Postgres; this one is the control plane's record of everything that happened.
//
// One write path: nothing in Deedglass writes action_events, admin_events or deeds except this
// package. Write methods never open a transaction: callers pass their org transaction
// (store.DB.OrgTx), so a state change, its event, its deed and its NOTIFY commit together or not at
// all. Read methods open their own read-only org transactions.
//
// Invariants (each has a test):
//   - Transitions follow internal/gate/state.go exactly; the database trigger mirrors it.
//   - action_events, admin_events and deeds are append-only; retention (Prune) only ever nulls the
//     payload columns of sealed actions and seals a ledger.pruned deed saying so.
//   - Events (pg_notify on deedglass_events) carry ids and states, never payloads, and are
//     delivered at commit, so a rolled-back seal never announces itself.
//   - Every read is org-scoped by row-level security; an id from another org is not found, exactly
//     like an id that doesn't exist. Cursors carry no org.
//   - CheckChain reports the first break with its seq and reason, and never repairs anything.
//   - Export streams one REPEATABLE READ snapshot through archive.Writer with flat memory; the same
//     range and created_at give the same bytes.
//
// API for later sessions (S07, S08, S12, S13, S16, S22, S28):
//
//	func New(db *store.DB, sealer *deed.Sealer, signer deed.Signer, opts ...Option) *Ledger
//	func (l *Ledger) CreateAction(ctx context.Context, tx pgx.Tx, org ids.OrgID, a NewAction) (ids.ActionID, error)
//	func (l *Ledger) Transition(ctx context.Context, tx pgx.Tx, actionID string, from, to gate.State, d EventData) error
//	func (l *Ledger) AppendEvent(ctx context.Context, tx pgx.Tx, actionID string, d EventData) error
//	func (l *Ledger) SealAction(ctx context.Context, tx pgx.Tx, actionID string) (*deed.Envelope, error)
//	func (l *Ledger) SealAmendment(ctx context.Context, tx pgx.Tx, actionID string, w deed.WitnessSection, detail any) (*deed.Envelope, error)
//	func (l *Ledger) RecordAdmin(ctx context.Context, tx pgx.Tx, e AdminEvent) (*deed.Envelope, error)
//	func (l *Ledger) Publish(ctx context.Context, tx pgx.Tx, ev Event) error
package ledger
