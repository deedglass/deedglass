// Package deedglass is a small public Go client for Deedglass: send actions, sign intents, and
// check deeds offline.
//
// Invariants:
//   - The API key comes from the caller or DEEDGLASS_API_KEY and is never logged.
//   - Every request has a timeout and an Idempotency-Key on POST.
//   - Offline verification needs only the deed and the public keys.
//
// Built in S08 (the CLI uses it):
//
//	type Client struct{ BaseURL, APIKey string; HTTP *http.Client; UserAgent string }
//	func NewClient(apiKey, server string) *Client
//	func (c *Client) Do(ctx context.Context, method, path string, in, out any, opts ...Option) (*Response, error)
//	type Error struct{ Status int; Type, Code, Message, Param, RequestID, DocURL, RetryAfter string }
//
// Planned (S09, S31):
//
//	func (c *Client) Act(ctx context.Context, req ActionRequest) (*Action, error)
//	func SignIntent(key ed25519.PrivateKey, in Intent) (header string, err error)
package deedglass
