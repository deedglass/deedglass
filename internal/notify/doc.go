// Package notify delivers events to people and systems: email, Slack and signed webhooks
// (contract §8).
//
// Invariants:
//   - Webhooks are signed: Deedglass-Signature: t=<unix>,v1=<hex hmac-sha256> over "<t>.<body>".
//   - Payloads are thin: ids, states and list shapes, never request or result bodies, context or
//     email addresses.
//   - Every destination goes through connector/egress, with timeouts and bounded retries.
//
// Planned API (finalized by S22, S29):
//
//	type Notifier interface{ Notify(ctx context.Context, ev Event) error }
//
// Filled by: S22 (webhooks, email), S29 (Slack)
package notify
