// Package feed is the Witness's interface over a foreign change feed (TY-05, ADR-114): change
// events that another system carried out of another database (Debezium today; a vendor's change
// feed or audit log later). A source adapter turns its events into Tx values; Evaluate binds the
// transactions to one action, runs the same L2 comparison the Postgres decoder's frames get
// (witness/l2), and labels the level by its source: "Proven · via Debezium (MySQL)".
//
// The rules, beyond witness/l2's own:
//
//   - Binding. A transaction that holds exactly one insert into the ledger table naming the action
//     is bound to it (attribution "action"): the feed carries the action id. Otherwise the
//     transactions that touch the action's tables are compared as its own, and the note any_writer
//     says so (attribution "any_writer"): any writer to those tables in the feed counts.
//   - Completeness. A transaction the feed holds only part of (no BEGIN or END, a gap in the event
//     order, counts that disagree with END) is the gap feed_incomplete and is never compared, so a
//     replayed or cut feed can't become a violation or a pass.
//   - Words. witness/l2's sentences name Postgres; Relabel says the source instead.
//
// It is pure: no I/O. Source adapters live in subpackages (feed/debezium).
package feed
