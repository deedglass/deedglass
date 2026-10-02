// Package console serves the Console, the embedded web app (web/ build output, ADR-020, ADR-053),
// and its live event stream over SSE (ADR-054).
//
// Invariants:
//   - No page is served signed out: /app and /app/{path...} redirect to /login?next= without a
//     session (SafeNext checks the return path); pages carry no data.
//   - The Console uses the session cookie plus CSRF; it never sees API keys after creation.
//   - Every page has the strict CSP with Trusted Types 'none' (CSP); assets are public and
//     immutable when hashed or versioned.
//   - A binary without the web build serves the placeholder in dev and refuses prod (ErrNotBuilt).
//   - The SSE stream sends thin, list-shaped events; its id: line is the stream cursor
//     (<instance>.<seq>), not the evt_ id; no org ever receives another org's event.
//
// API: New(Options) and (*Console).Routes() for the pages; NewHub, (*Hub).Run and EventRoutes for
// GET /v1/events; DBHydrator for the list shapes.
//
// Filled by: S24 (pages, tokens, shell, events); S25 and S26 build the screens.
package console
