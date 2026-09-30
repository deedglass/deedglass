// Package gate is the action pipeline: every tool call passes through the Gate, which authenticates
// it, evaluates policy, waits for approval when needed, runs it through a connector, hands the
// result to the Witness and seals a deed (contract §5).
//
// Invariants:
//   - Each transition (state, timestamp, event) is persisted before the next side effect.
//   - Execution never runs inside a database transaction.
//   - Unknown tools are denied.
//   - Only the edges in state.go exist; a database trigger mirrors them.
//   - When Deedglass can't know whether a tool ran, the action goes executed with
//     execution.status = unknown, then witnessing → unverifiable. No other edge is invented.
//   - POST /v1/actions owns its idempotency: a unique index on (org, agent, key).
//
// Planned API (finalized by S13):
//
//	func (g *Gate) Submit(ctx context.Context, p identity.Principal, req ActionRequest) (Action, error)
//	var Edges = map[State][]State{ … } // state.go, created in S02
//
// Filled by: S13 (S02 creates state.go)
package gate
