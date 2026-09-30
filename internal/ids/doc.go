// Package ids makes and parses Deedglass ids: a short prefix, an underscore and a lowercase ULID,
// for example agt_01j9zr3m5k8q2w7x4t6v0b1c9d (contract §3, ADR-014).
//
// Invariants:
//   - Ids are not secrets and never authorize anything. They reveal their creation time to the
//     millisecond.
//   - Parsing is strict and never normalizes: uppercase, i l o u, a wrong length, a wrong prefix or
//     a first ULID character above 7 is ErrInvalid. Normalize is for CLI input only.
//   - One generator per process: ids from it are strictly increasing. Ids from different processes
//     are only roughly time-ordered.
//   - Randomness comes from crypto/rand only; a read failure panics.
//   - Pattern(prefix) is the one regular expression; S02 copies it into SQL domains.
//   - Stdlib only (internal/archtest enforces it).
//
// Filled by: S01
package ids
