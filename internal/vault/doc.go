// Package vault encrypts connector credentials with AES-256-GCM envelope encryption, rotates the
// key ring, and redacts credentials everywhere else (contract §11).
//
// Invariants:
//   - Plaintext credentials exist only in memory during execution; they are never logged, returned
//     or stored unencrypted.
//   - DEEDGLASS_VAULT_KEY holds a ring of 1–4 keys; the first encrypts, all decrypt.
//   - Each credential has its own data key; the record binds org and connector as associated data.
//
// Planned API (finalized by S16):
//
//	func New(ring []config.Secret) (*Vault, error)
//	func (v *Vault) Seal(org ids.OrgID, con ids.ConnectorID, plaintext []byte) (Sealed, error)
//	func (v *Vault) Open(org ids.OrgID, con ids.ConnectorID, s Sealed) ([]byte, error)
//
// Filled by: S16
package vault
