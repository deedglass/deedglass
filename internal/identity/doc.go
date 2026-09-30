// Package identity holds orgs, users, memberships, sessions, magic links, agents, API keys, signed
// intents and on-behalf-of references (contract §3, §8).
//
// Invariants:
//   - Secrets are stored hashed (API keys: sha256 with the pepper; tokens: sha256), compared in
//     constant time, and shown once.
//   - An API key is dg_live_ or dg_test_ + an 8-character prefix + a 32-character secret; the
//     prefix is globally unique and the secret is never stored.
//   - Users are global; access comes only from memberships (owner, admin, viewer).
//   - Agent keys never change any agent, including their own. Admin keys act as the admin role,
//     never owner.
//   - Signed intents are checked with a ±60 s skew and a TTL of at most 5 minutes, and a nonce is
//     accepted once.
//
// Planned API (finalized by S07–S09):
//
//	func (s *Service) CreateOrg(ctx context.Context, name, ownerEmail string) (Org, error)
//	func (s *Service) AuthenticateKey(ctx context.Context, bearer string) (Principal, error)
//	func (s *Service) VerifyIntent(ctx context.Context, agent ids.AgentID, header string) (Intent, error)
//
// Filled by: S07 (orgs, users, sessions), S08 (agents, keys, Stop), S09 (signed intents, OBO)
package identity
