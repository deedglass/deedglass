# testdata

Offline test vectors, so CI never needs the network. Copied on 2026-09-28 from the Day-1 spike
(`~/txv/ops/spikes/go/testdata`), where they were fetched the same day. `SHA256SUMS` covers every
vector file; check with `make testdata-check` (or `sha256sum -c SHA256SUMS` in this directory).

| Path | Source | Used by |
|---|---|---|
| `jcs/input/*.json`, `jcs/output/*.json` | cyberphone/json-canonicalization `testdata/{input,output}` (RFC 8785 reference pairs) | `internal/intent` (TestJCSReferenceVectors) |
| `rfc/rfc8785.txt` | rfc-editor.org, RFC 8785 (JCS) | reference; Appendix B number table used by spike S4 |
| `rfc/rfc8032.txt` | rfc-editor.org, RFC 8032 (EdDSA) | `internal/keys` (§7.1 Ed25519 vectors) |
| `wycheproof/ed25519_test.json` | C2SP/wycheproof `testvectors_v1/ed25519_test.json` | `internal/keys` (public keys accepted at registration) |

The vector files are third-party material under their own terms (RFC text: IETF Trust legal
provisions; json-canonicalization and Wycheproof: Apache-2.0). Do not edit them; add new golden
files (intents, statements) beside them and extend `SHA256SUMS`.
