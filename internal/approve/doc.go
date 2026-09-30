// Package approve handles human approval: requests, one-time approval links, expiry, four-eyes
// rules and resuming the action once decided (contract §5).
//
// Invariants:
//   - The first decision wins; later ones are refused.
//   - A requester can't approve their own action.
//   - Approval tokens are stored hashed and expire; an expired approval seals as expired.
//   - A decision is persisted before the Gate resumes the action.
//
// Planned API (finalized by S22):
//
//	func (s *Service) Request(ctx context.Context, a gate.Action, rule policy.Rule) (Approval, error)
//	func (s *Service) Decide(ctx context.Context, id ids.ApprovalID, by identity.Principal, d Decision) error
//
// Filled by: S22
package approve
