// Package l2 is the Witness's L2 level (S21): it compares what a Postgres statement should have
// touched (the footprint) with what Postgres's change log shows the transaction committed (the
// frame the decoder bound to the action), and turns the difference into reasons and a diff.
//
// It is pure, like its parent (internal/archtest): no I/O, no storage types. The Gate converts a
// stored decoded_txs row into a Frame, asks the connector for its witness settings and triggers,
// and hands the reasons to witness.Decide with L0 and L1 unchanged.
//
// The rules (S21 §3.3 to §3.5), in short:
//
//   - Conservative expectations. Only what the SQL plainly says becomes an expectation: a single
//     table, its operation, the reported row count, the keys of a WHERE on the primary key, the
//     SET list and its plain literals. Anything Deedglass can't predict is the gap
//     footprint_unknown, never a guess.
//   - Exact numbers. Counts come from the frame's per-table counts (the WAL), never from EXPLAIN or
//     the command tag.
//   - Every rule runs and every finding is listed; witness.Decide and the summary order decide what
//     the one sentence says.
//   - A gap never becomes a violation, and nothing short of a clean comparison is a pass.
//   - A sensitive column's value is never shown: the decoder stored only its digest, and the diff
//     prints "•••".
//
// Files: frame.go (the frame as plain data), footprint.go (Derive), compare.go (Compare and the
// ledger cross-check), diff.go (the row diff), render.go (the CLI text), summary.go (Evaluate,
// which runs everything for one action).
package l2
