// Package console serves the Console, the embedded web app (web/ build output), and its live
// event stream over SSE (brand §13.1, ADR-020).
//
// Invariants:
//   - The Console uses the session cookie plus CSRF; it never sees API keys after creation.
//   - Every screen has loading, empty, error and permission-denied states.
//   - The SSE stream sends thin events; its id: line is the stream cursor, not the evt_ id.
//
// Planned API (finalized by S24–S26):
//
//	func Handler(assets fs.FS) http.Handler
//	func Events(ctx context.Context, org ids.OrgID, since Cursor) (<-chan Event, error)
//
// Filled by: S24 (shell, tokens), S25 (screens), S26 (live feed)
package console
