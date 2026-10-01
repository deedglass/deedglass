// Package policystore keeps the policy engine's state in the control-plane Postgres. Usage is the
// authoritative policy.UsageStore (S11 §3.6): usage and budget counters in
// deedglass.usage_counters.
//
// Invariants:
//   - Every check-and-increment is one conditional upsert, so budgets are exact across any number
//     of instances: Postgres decides, never a cache.
//   - Reserve sends every row of an action in one pgx.Batch inside one OrgTx (one round trip, then
//     the commit), and is all or nothing: a row that would pass its limit rolls the batch back.
//   - Snapshot is one SELECT in an OrgReadTx; Release, RecordCost and Reserve are org-scoped writes
//     under row-level security; only Prune crosses orgs, through SystemTx(ReasonMaintenance).
//   - SQL is fixed text, schema-qualified, with pg_catalog.now(); input travels only as parameters.
//
// It passes internal/policy/usagetest, the same suite as policy.MemUsage.
package policystore
