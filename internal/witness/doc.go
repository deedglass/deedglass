// Package witness decides what actually happened: it compares what the agent reported with what
// the system of record shows, and returns a verdict (contract §6). L0 Reported is the tool's own
// response, L1 Confirmed is a read-back from the target, and L2 Witnessed is the system of record
// itself (the Postgres change stream).
//
// Invariants:
//   - Precedence: any violation gives MISMATCH; otherwise any gap gives UNVERIFIABLE; otherwise at
//     least one pass gives VERIFIED; no checks gives UNVERIFIABLE.
//   - No evidence gap ever becomes MISMATCH, and nothing short of a clean pass becomes VERIFIED.
//     The precedence function is tested over every combination of results.
//   - A later verdict never changes an existing deed; it produces an amendment deed.
//   - Pure core: the verdict logic imports no net/http, database/sql or store (internal/archtest).
//
// Planned API (finalized by S19, S21, S23):
//
//	func Decide(checks []Check) Verdict // VERIFIED | MISMATCH | UNVERIFIABLE
//	func (w *Witness) Observe(ctx context.Context, a gate.Action) (Result, error)
//	func PlanUndo(obs Observation) (UndoPlan, error)
//
// V0 note (the former verify and boundary packages; docs/v0/VERIFICATION_MODEL.md and
// OBSERVATION_BOUNDARY.md): V0 classified each decoded frame, ran ordered checks, and compared the
// expected delta computed from the signed intent with the observed delta, writing a diff on
// mismatch. The observation boundary (publication members, columns, triggers, policies, the
// controlled function) was hashed and re-checked around each commit; drift gave an UNVERIFIABLE
// result. marker_lsn is evidence only and is never compared with commit_lsn.
//
// Filled by: S19 (L0/L1, precedence), S21 (L2 expected vs observed, diffs), S23 (undo planner)
package witness
