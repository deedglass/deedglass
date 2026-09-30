// Package deedglass is a small public Go client for Deedglass: send actions, sign intents, and
// check deeds offline.
//
// Invariants:
//   - The API key comes from the caller or DEEDGLASS_API_KEY and is never logged.
//   - Every request has a timeout and an Idempotency-Key on POST.
//   - Offline verification needs only the deed and the public keys.
//
// Planned API (finalized by S09, S31):
//
//	func NewClient(apiKey, server string) *Client
//	func (c *Client) Act(ctx context.Context, req ActionRequest) (*Action, error)
//	func SignIntent(key ed25519.PrivateKey, in Intent) (header string, err error)
//
// Filled by: S09 (intents), S31 (client)
package deedglass
