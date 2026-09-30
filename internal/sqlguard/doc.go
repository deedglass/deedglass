// Package sqlguard tokenizes and classifies SQL in pure Go, so policies can reason about what a
// statement would do (read, write, DDL, destructive) before the Gate runs it.
//
// Invariants:
//   - Pure: it classifies and never executes, and imports no database driver (internal/archtest).
//   - Anything it can't classify is treated as destructive.
//   - Multiple statements, comments and dollar quotes are handled, never stripped silently.
//
// Planned API (finalized by S11):
//
//	func Classify(sql string) (Result, error) // Result{Kind, Tables, Destructive, Statements}
//
// Filled by: S11
package sqlguard
