// Package sim plans a believable, seeded stretch of agent traffic for `deedglass dev simulate`
// (S27). It decides what happens when; the runner pushes each planned action through the real
// Gate, so policies, budgets, approvals and sealing are real and only the tools and the Witness
// are scripted.
//
// A scenario (examples/sim/acme.yaml is the default) names the org, the enforce policy and an
// optional shadow policy, the approvers, the agents (hours, daily volume, tools by weight,
// outcome and level fractions, approval answers) and the incidents on top of the baseline:
// mismatch, first_use, spike, off_hours, new_user_refs and budget_pressure. Parse is strict:
// an unknown key anywhere is an error with its line and, when one is close, a suggestion.
//
// Invariants:
//   - Plan is pure and deterministic: the same scenario, seed and window give a byte-identical
//     plan (PlanDigest). Randomness is math/rand/v2's PCG, one sub-stream per agent and per
//     incident, so adding an incident never reshuffles the baseline.
//   - Every action has its own millisecond, in time order, inside [Start, End).
//   - A MISMATCH only ever comes from a mismatch incident; baseline outcomes are VERIFIED,
//     UNVERIFIABLE or failed.
//   - budget_pressure reads the limit from the compiled enforce policy and counts refunds the
//     way the Gate reserves them (policy.Evaluate's reservations), per org-local calendar day.
//
// Day d of a window is the d-th 24 hours after Start; an incident's hour is the org-local hour
// inside that span. In a time zone with daylight saving, a span that crosses a change is placed
// by its starting offset, so the local hours can be off by one on those days.
package sim
