// Package licence verifies Deedglass Pro licences on the buyer's machine (S48, DL-039, contract
// §13.4). It is Apache-2.0 and only verifies: Pro features themselves live in ee/ (FSL-1.1-ALv2).
//
// A licence is two local files in the state directory:
//
//   - licence.json: the signed licence token our site returns after it re-validates the Dodo key.
//     It never holds the key, only "sha256:" + hex(sha256(key)).
//   - licence.key: the Dodo licence key itself, so the CLI can re-validate it. Mode 0600, never
//     printed, never sent anywhere except to Dodo and to our token route.
//
// The token is signed like a deed, with its own domain tag and its own key family:
// sig = Ed25519("deedglass/licence/v1\n" + body_hash), body_hash = sha256(JCS(body)), signed by an
// lk_ key that never seals deeds. The public lk_ keys are embedded in the binary.
//
// Free features never read the licence: only internal/licence, ee/ and cmd/deedglass's licence
// and scan --full code may import this package (TestLicenceReaders).
package licence
