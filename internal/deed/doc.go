// Package deed builds, seals and verifies deeds: the signed record of one action, one admin
// change, or a later witness verdict (contract §7, docs/DEED_SPEC.md).
//
// A deed body is RFC 8785 canonical JSON under a strict profile. body_hash is
// "sha256:" + hex(SHA-256(JCS(body))). The seal is pure Ed25519 over the bytes
// "deedglass/deed/v1\n" + body_hash. Deeds are hash-chained per org: prev_hash of seq 1 is
// sha256("deedglass/genesis\n" + org), and of seq n the body_hash of seq n−1. An optional
// COSE_Sign1 encoding (RFC 9052) follows the RFC 9943 Signed Statement shape; it is not an
// RFC 9942 transparency-service object.
//
// Invariants (each has a test):
//  1. Deeds hold digests, never payloads. The only free text is agent.name (≤ 64), action.tool
//     (≤ 200 bytes), on_behalf_of and session (≤ 256), witness.method (≤ 64),
//     execution.error_code (≤ 64) and admin.reason (≤ 280). The schema is closed.
//  2. Every body integer is 0 ≤ n ≤ 2^53−1; no fractions, no -0, anywhere in a body.
//  3. Per org: seq is 1, 2, 3… with no gap, duplicate or fork; prev_hash links; issued_at never
//     decreases.
//  4. body.seal_key_id == envelope.seal_key_id, envelope.id == body.deed_id, object == "deed".
//  5. A deed is sealed in the same transaction as the state change it records (Sealer.Seal runs
//     inside the caller's ChainStore). Rollback means no deed and no consumed seq.
//  6. Stored envelope bytes equal JCS(envelope); they are served and exported verbatim.
//  7. Deeds are append-only (the database enforces it). A later verdict is an amendment deed.
//  8. Small-order or non-canonical public keys never enter a KeySet or a key file
//     (internal/keys; attack case A13).
//  9. The test-vector keys never seal outside tests and never load in prod.
//  10. Pure: no database, no network, no clock. Times come from the caller (the Sealer uses the
//     database clock it reads through ChainStore). internal/archtest enforces the imports.
//
// API for later sessions:
//
//	func ForAction(a ActionSnapshot) (Draft, error)                    // S06 SealAction, S13 Gate
//	func ForAdmin(e AdminSnapshot) (Draft, error)                      // S06 RecordAdmin
//	func ForAmendment(original *Body, w WitnessSection) (Draft, error) // S19, S21
//	func NewSealer(s Signer, opts ...SealerOption) *Sealer
//	func (s *Sealer) Seal(ctx context.Context, cs ChainStore, d Draft) (*Envelope, error)
//	func VerifyEnvelope(raw []byte, ks *KeySet, opt VerifyOptions) (*Result, error) // S05 verify
//	func NewChainChecker(opt ChainOptions) *ChainChecker
//	func (c *ChainChecker) Next(r *Result) error
//	func ParseKeySet(raw []byte) (*KeySet, error)
//	func Message(e *VerifyError) (sentence, next string)
//	func EncodeCOSE(env *Envelope, s Signer, issuer string, now time.Time) ([]byte, error)
//	func VerifyCOSE(b []byte, ks *KeySet) (*COSEResult, error)
//
// internal/store.DeedTx is the Postgres ChainStore; internal/deed/deedtest holds the test keys,
// an in-memory ChainStore and the vector generator (testdata/deeds).
package deed
