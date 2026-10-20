// Package stamp anchors the head of a sealed-record chain to independent timestamps (TY-03,
// ADR-112), so a reader can tell that a chain was not cut short or rewritten after a given time.
//
// A head is the statement {object: chain_head, v: 1, org, seq, deed_id, body_hash, issued_at}
// about one sealed deed. Two kinds of evidence can be attached to it:
//
//   - RFC 3161 timestamp tokens from time-stamping authorities (TSAs) the operator configures. The
//     token's message imprint is SHA-256 of JCS(head).
//   - An optional registration with a transparency service, following the IETF SCITT architecture
//     (RFC 9943): the issuer signs the head as a COSE_Sign1 Signed Statement with its Ed25519 seal
//     key, and the service's signed inclusion proof (RFC 9942, RFC9162_SHA256) is kept beside
//     it.
//
// Both are stored in timestamps.json (inside an export bundle, or as a file of its own), and
// `deedglass verify` checks them offline. Nothing here is on unless the operator configures a TSA
// or a transparency service: no outbound call happens by default. Nothing secret is stored: the
// head is public, the tokens and inclusion proofs are public signatures.
//
// The RFC 3161 and CMS code uses only the standard library (encoding/asn1, crypto/x509); the COSE
// code uses go-cose, already a dependency (ADR-008).
package stamp
