// Package statement builds and checks verification statements (spec §11,
// amended by plan §3.0 #4): COSE_Sign1 (RFC 9052, tag 18) objects shaped as
// RFC 9943 Signed Statements, produced with veraison/go-cose using alg -8
// (EdDSA, crv Ed25519) per ADR-008 because go-cose 1.3.0 has no built-in -19.
// The payload is JCS JSON of type txv.stmt/1 carrying the verdict, reasons,
// subject (sysid, timeline, commit_lsn, xid8), intent, bundle, policy,
// boundary status and salted transition digests, with no balances or amounts.
// Statements, alerts included, are hash-chained (seq, prev) and the chain head
// is published daily. A statement is a signed statement from this verifier,
// not an RFC 9942 transparency-log inclusion proof, and is never named as one.
package statement
