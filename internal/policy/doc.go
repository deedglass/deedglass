// Package policy compiles policies written in YAML into CEL programs (S10), evaluates actions
// against them with budgets and usage counters (S11), and will back stored versions (S12). Contract §6, ADR-017, and the spec in
// docs/POLICY_LANGUAGE.md.
//
// Invariants:
//   - Compile is deterministic and never panics: the same bytes give the same diagnostics and the
//     same version_hash; an internal failure becomes internal_compiler_error and nothing is accepted.
//   - version_hash = "sha256:" + hex(sha256(JCS(normalized))): formatting, comments and key order
//     never move it; any change in meaning does.
//   - Hostile input is refused before it costs anything: 256 KiB, no anchors, aliases, merge keys or
//     non-core tags, 50,000 nodes, depth 32, CEL parser and nesting limits, an estimated cost cap,
//     constant regex and glob patterns.
//   - Evaluation order (S11): Stop, then the agent's budgets, then the policy's budgets, then rules
//     (first match wins), then the default. An evaluation error never widens access; over the
//     runtime cost limit is a deny for every rule decision (DL-024); a nil policy, a panic or the
//     deadline is a deny with policy_unavailable. Evaluate is pure: the input carries the time and
//     the usage counters, so the same input always gives the same decision.
//   - One CEL environment per language version, with pinned extension versions; facts.go declares
//     every field a condition can read.
//   - Pure: no net/http, no database/sql, no store or httpx imports (internal/archtest).
//
// API:
//
//	func Compile(src []byte, opts Options) (*Compiled, []Diagnostic) // deedglass policy lint
//	func Lint(src []byte, opts Options) *LintResult                  // the policy_lint_result object
//	func Format(src []byte) ([]byte, error)                           // deedglass policy fmt
//	func Evaluate(ctx, c *Compiled, in Input, o EvalOptions) Decision  // S13's gate; policy evaluate
//	func EvaluatePair(ctx, enforce, shadow *Compiled, in Input, o EvalOptions) (Decision, *Decision)
//	func AlwaysDenies(c *Compiled, tool ToolFacts, agent AgentFacts) bool // S14's tools/list
//	type UsageStore interface{ Snapshot; Reserve; Release; RecordCost; Prune } // MemUsage, policystore.Usage
//
// Filled by: S10 (language, compiler, lint, fmt), S11 (evaluator, usage), S12 (versions, activation).
package policy
