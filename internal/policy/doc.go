// Package policy compiles policies written in YAML into CEL programs (S10), and will evaluate
// actions against them (S11) and back stored versions (S12). Contract §6, ADR-017, and the spec in
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
//   - Evaluation order (S11): Stop, then budgets, then rules (first match wins), then the default;
//     an evaluation error never widens access.
//   - One CEL environment per language version, with pinned extension versions; facts.go declares
//     every field a condition can read.
//   - Pure: no net/http, no database/sql, no store or httpx imports (internal/archtest).
//
// API:
//
//	func Compile(src []byte, opts Options) (*Compiled, []Diagnostic) // deedglass policy lint
//	func Lint(src []byte, opts Options) *LintResult                  // the policy_lint_result object
//	func Format(src []byte) ([]byte, error)                           // deedglass policy fmt
//
// Filled by: S10 (language, compiler, lint, fmt), S11 (evaluator, usage), S12 (versions, activation).
package policy
