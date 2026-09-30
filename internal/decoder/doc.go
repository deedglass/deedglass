// Package decoder reads Postgres logical replication (pgoutput) for the Witness: slots, the
// peek-then-advance loop, transaction assembly and correlation of each transaction to its action
// through the deedglass logical message (contract §4).
//
// Invariants:
//   - The confirmed-flush LSN advances only after the decoded transaction is persisted
//     (decoded_txs), so delivery is at-least-once and verdicts exactly-once.
//   - Every pgoutput message goes through safeParse (recover, per-type minimum lengths, declared
//     lengths); a parse failure halts the connector's verdicts rather than guessing.
//   - Every decode session pins TimeZone, DateStyle, IntervalStyle, bytea_output and
//     extra_float_digits, and asserts them on each poll.
//   - Transactions are classified by frame position and the transactional flag of each message,
//     never by the peek xid or by assuming peek LSNs are monotonic.
//   - Slot lag above DEEDGLASS_SLOT_LAG_ALERT_MB raises decoder.alert; max_slot_wal_keep_size is
//     finite.
//
// Planned API (finalized by S20):
//
//	func (d *Decoder) Run(ctx context.Context, con ids.ConnectorID) error
//	func (d *Decoder) Status(ctx context.Context) ([]SlotStatus, error) // deedglass decoder status
//
// Filled by: S20 (the spikes in ~/txv/ops/spikes hold proven code to port)
package decoder
