// Package learn powers Insights: replaying past actions against a policy version, usage
// statistics, anomalies and suggested policy changes, with optional LLM phrasing.
//
// Invariants:
//   - Insights never activates enforce mode by itself. A person accepts every suggestion.
//   - Replay reads recorded inputs only; it never re-runs a tool.
//   - LLM phrasing is optional and sees counts and rule text, never payloads.
//
// Planned API (finalized by S27–S28):
//
//	func (l *Learner) Replay(ctx context.Context, v ids.PolicyVersionID, window Window) (ReplayReport, error)
//	func (l *Learner) Suggest(ctx context.Context, org ids.OrgID) ([]Suggestion, error)
//
// Filled by: S27 (replay, stats, anomalies), S28 (suggestions, digest)
package learn
