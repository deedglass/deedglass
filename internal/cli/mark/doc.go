// Package mark draws the Deedglass mark in a terminal (brand §5 and §13.3; S05X): the Split Orb
// in half-block pixels beside a short welcome (Banner), the one-line inline mark ◖│◗ that heads
// init, doctor and help (Inline), and the optional "Sealed" closing moment (Sealed).
//
// Invariants:
//   - Pure: no global state, no writes to stdout. Banner and Inline return strings; Sealed writes
//     only to the writer it is given. Callers decide whether to print.
//   - Machines never see decoration: Detect sets Quiet off a TTY, in CI and with
//     DEEDGLASS_NO_BANNER; callers add --json and -q. Quiet options render nothing.
//   - Pixels outside the orb are never painted: the terminal's own background shows through.
//   - Output is deterministic for given Options. The pixel grid (orb_gen.go) is generated from
//     the brand geometry by go generate and checked in; TestGeneratedFresh fails on drift.
//   - Stdlib only; no cgo.
//
// Filled by: S05X
package mark

//go:generate go run ./gen
