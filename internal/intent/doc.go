// Package intent implements the signed intent format deedglass.intent/1 (spec §5,
// amended by plan §3.0 #6): a closed-schema JSON object whose exact RFC 8785
// (JCS) bytes are signed with pure Ed25519, with amounts carried as strings,
// the Operation Bundle hash inside the signed content, and intent_hash =
// SHA-256(bytes) as its identifier. A strict pre-parser runs before
// canonicalization and rejects invalid UTF-8, duplicate keys, non-integer
// numbers, integers above 2^53-1 (which JCS would silently round), lone
// surrogates, and unknown or missing keys; the bytes must then equal
// JCS(bytes) exactly. The executor applies these checks before BEGIN and the
// verifier applies them again to the decoded ledger row. Signing and signature
// verification helpers land here next (Tue 09-29).
package intent
