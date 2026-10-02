// Package undo plans the undo of a Postgres write (S23): it reads what Postgres committed for the
// original action (the decoder's row images, never what the agent said), collapses the transaction
// to its net effect per row, and turns that into a plan of single-row steps, the guards that must
// hold before any step runs, and a digest that pins both.
//
// It is pure, like its parent (internal/archtest): no I/O, no storage types, no Postgres driver. The
// catalog and the current rows arrive as plain data (Catalog, Current) read by the Postgres
// connector (internal/connector/postgres/undo.go); the HTTP routes, the Gate executor and the
// stored plan live in internal/undoapi.
//
// The rules, in short:
//
//   - The plan is a pure function of the evidence and a catalog snapshot: the same inputs give the
//     same digest (Plan.Digest, sha256 over JCS of the plan without its digest and notes).
//   - Every value a step writes comes from Postgres's own old images. A request carries only ids, a
//     digest, a flag and a reason.
//   - Identifiers come from the catalog by relid, then are checked against the evidence's names;
//     types come from format_type and must match TypePattern before they are used in a cast.
//   - Guards compare text byte for byte under the decoder's pinned settings. A value equal in
//     meaning but printed differently counts as changed: fail closed.
//   - Any failing check makes the undo unavailable, and every failing check is listed with its fix.
//
// Files: evidence.go (the original transaction as row changes), net.go (net effect per row),
// plan.go (steps, guards, order, digest), catalog.go (the catalog snapshot types), checks.go (the
// twelve checks), sqlgen.go (the statements), footprint.go (the exact L2 footprint), present.go (every
// string a person reads: CLI, API, approval context and the Console strings S25 renders).
package undo
