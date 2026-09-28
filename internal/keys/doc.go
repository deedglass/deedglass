// Package keys manages V0 Ed25519 keys (spec §12, amended by plan §3.0 #7):
// file-based principal and verifier key pairs (0600 files; KMS/HSM signing is
// V1), the key id kid = lowercase hex of the first 16 bytes of SHA-256 of the
// raw 32-byte public key, and the verifier's own trusted key registry, which
// it never takes from the database copy. Registration refuses small-order and
// non-canonical public keys: spike S4 showed that with the identity key one
// signature verifies every message under Go's crypto/ed25519, so the check is
// mandatory and is exercised by attack case A13. Principal keys never live in
// the agent or application process; the executor holds no signing key.
package keys
