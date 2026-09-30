// Package policy compiles policies written in YAML into CEL programs, evaluates actions against
// them, tracks usage and budgets, and provides replay inputs (contract §6, ADR-017).
//
// Invariants:
//   - Deterministic and side-effect free: the same input and policy version give the same decision.
//   - Evaluation order: Stop, then budgets and rates, then rules (first match wins), then the default.
//   - Decisions are allow, deny or require_approval; a shadow decision is recorded alongside.
//   - Unknown tools are denied. A policy that doesn't compile never activates.
//   - CEL runs in one declared environment with cost limits.
//   - Pure: no net/http, no database/sql, no store or httpx imports (internal/archtest).
//
// Planned API (finalized by S10–S12):
//
//	func Compile(src []byte) (*Policy, []Diagnostic, error)            // deedglass policy lint
//	func (p *Policy) Evaluate(in Input, usage Usage) Decision
//
// Filled by: S10 (compiler), S11 (evaluator, usage), S12 (activation, versions)
package policy
