// Package vault keeps connector credentials (S16, contract §2 and §4): AES-256-GCM envelope
// encryption with one data key per credential version, a key ring for rotation, and redaction of
// credential values everywhere else.
//
// Invariants:
//   - Plaintext credentials exist only in memory while the Gate runs a call. They are never logged,
//     returned by any API or stored unencrypted. Only internal/connector (Instance.Secret) and the
//     vault's own rotation call Opener.Open (TestOpenerCallSites).
//   - DEEDGLASS_VAULT_KEY holds a ring of 1–4 keys; the first wraps new data keys, all unwrap.
//   - Each ciphertext is bound to its org, connector, version and kind, and each wrapped data key
//     to its org, connector and key id (the associated data). A row moved anywhere else fails to
//     open (ErrIntegrity, the vault_integrity alert) and no call is ever sent without its auth.
//   - Replacing a credential destroys its old data key, which crypto-shreds the old secret.
//
// Handlers hold only a Writer; the execute path holds an Opener; deedglass vault holds a Rotator.
package vault
