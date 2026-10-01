// Package sqlguard tokenizes and classifies SQL in pure Go, so policies can read what a statement
// would do (read, write, DDL, server code, several statements) before the Gate runs it.
//
// It is a conservative pre-execution filter, not a Postgres parser (ADR-033). Its one rule: when it
// isn't sure, it reports Ambiguous with a named reason, and the starter policies deny ambiguous SQL.
//
// Invariants:
//   - Pure: it classifies and never executes, and imports no database driver (internal/archtest).
//   - Lossless: concatenating Tokenize's tokens gives back the input, byte for byte.
//   - Fail closed: anything it can't read with confidence is Ambiguous, with category "unknown".
//   - Linear: the tokenizer is one pass; inputs over Options.MaxBytes are refused as too_large.
//
// What it can't see (triggers, rules, function bodies) is what the L2 witness covers (S20–S21).
// docs/SQLGUARD.md is the user-facing description; testdata/corpus.yaml is the executable one.
package sqlguard
