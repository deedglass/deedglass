// Package deed builds, seals and verifies deeds: the signed record of one action (contract §7).
// A deed body is JCS canonical JSON (RFC 8785); its seal is Ed25519 over a domain-separated hash;
// deeds are hash-chained per org. An optional COSE_Sign1 (RFC 9052) encoding follows the SCITT
// signed-statement shape; it is not an RFC 9942 transparency-log entry.
//
// Invariants:
//   - Pure: no database, no network, no clock except what the caller passes in.
//   - A sealed deed is never modified. A later verdict or correction is an amendment deed
//     (kind: amendment, amends: deed_…).
//   - Payloads never enter a deed, only digests: "sha256:" + lowercase hex(sha256(JCS(x))).
//   - The signature input is "deedglass/deed/v1\n" + body_hash. Bundle manifests use
//     "deedglass/bundle/v1\n"; signed intents use "deedglass/intent/v1\n".
//   - Bodies pass the strict profile before JCS: valid UTF-8, no duplicate keys, integers only in
//     0..2^53-1, no -0, no lone surrogates (internal/intent has the parser; ADR-013 defers the
//     internal/canon extraction to S04).
//   - seq starts at 1 per org with no gaps; prev_hash of seq 1 is sha256("deedglass/genesis\n" + org).
//
// Planned API (finalized by S04–S05):
//
//	type Body struct{ V int; Kind Kind; DeedID ids.DeedID; Org ids.OrgID; Seq uint64; PrevHash string; … }
//	func Seal(b Body, key SealKey) (Envelope, error)
//	func Verify(env []byte, keys KeySet) (Result, error)
//	func VerifyChain(r io.Reader, keys KeySet) (ChainResult, error) // deedglass verify
//
// V0 note (docs/v0/STATEMENT_SPEC.md): the V0 verification statement becomes the Deed. COSE stays
// an optional encoding (ADR-008 used alg -8, EdDSA with Ed25519).
//
// Filled by: S04 (body, seal, chain), S05 (verify, export bundle, COSE)
package deed
