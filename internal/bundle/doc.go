// Package bundle holds Operation Bundles (spec §6): a vendor-authored,
// content-addressed manifest (bundle_hash = "sha256:" + hex(SHA-256(JCS(manifest))))
// that fixes the intent schema, the entry function and the SHA-256 of its exact
// prosrc, the expected schema, the write set and the marker count, plus the
// registry of rules compiled into the verifier (map[bundle_hash]Rule, starting
// with rules/transfer_v1.go). The customer admin activates a bundle by hash in
// deedglass.bundles, the principal signs that hash inside the intent, and the
// verifier computes the expected delta with its own rule code, never the
// database copy; an unknown hash yields INCONCLUSIVE BUNDLE_UNKNOWN. New
// semantics mean a new manifest, a new hash and a new verifier release.
package bundle
