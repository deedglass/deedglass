// Package witness decides what actually happened (contract §6): it compares what a tool reported
// with what the target shows, and returns a verdict with a level and a method. L0 Reported is the
// tool's own response, L1 Confirmed is a read-back from the target, and L2 Witnessed is the system
// of record itself (the Postgres change stream, S21).
//
// The package is pure: it never does I/O (internal/archtest). Read-backs go through an Observer
// the Gate implements with the connector's ReadBacker; this package resolves templates, compares
// fields, evaluates CEL postconditions and decides.
//
// Invariants:
//   - Only Decide (precedence.go) produces VERIFIED; verdict_guard_test.go fails the build if any
//     other file does.
//   - Precedence: any violation gives MISMATCH; otherwise any gap gives UNVERIFIABLE; otherwise at
//     least one pass gives VERIFIED; no checks gives UNVERIFIABLE. The claim rule (ADR-042) applies
//     it to the reasons of the claimed level; violations are global.
//   - No evidence gap ever becomes MISMATCH, and nothing short of a clean level at or above the
//     required one becomes VERIFIED. Decide is tested against an independent oracle over every
//     combination of reason classes at every level and required level.
//   - A later verdict never changes an existing deed; it produces an amendment deed (S21).
//   - Every reason's class and sentence come from one catalog (catalog.go); docs/witness/REASONS.md
//     is generated from it.
//
// V0 note (the former verify and boundary packages; docs/v0/VERIFICATION_MODEL.md and
// OBSERVATION_BOUNDARY.md): V0 classified each decoded frame, ran ordered checks, and compared the
// expected delta computed from the signed intent with the observed delta, writing a diff on
// mismatch. Its contradiction class is this package's violation, and its gap class is the same.
// marker_lsn is evidence only and is never compared with commit_lsn.
//
// Filled by: S19 (L0, L1, the claim rule, the catalog, witness_detail v1), S21 (L2 expected vs
// observed, amendments), S23 (witnessing undo).
package witness
