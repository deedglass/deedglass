// Package httpx is the HTTP layer: the server, middleware, the route table, the error envelope,
// idempotency and dated API versions (contract §8, §9).
//
// Invariants:
//   - Every route declares its auth kind in the route table (contract_routes.go is the executable
//     copy of contract §9); an undeclared route doesn't start.
//   - Every error uses the envelope {type, code, message, param, request_id, doc_url}; messages are
//     sentence case with a next step, and come from the catalog in errors.go.
//   - No stack trace or internal error text leaves the process.
//   - Every response carries Deedglass-Request-Id (req_…) and the resolved Deedglass-Version.
//   - Bodies are never logged. CORS is closed except for public metadata.
//
// Planned API (finalized by S03):
//
//	func NewServer(cfg *config.Config, deps Deps) *http.Server
//	type Route struct{ Method, Pattern string; Auth AuthKind; Handler Handler }
//	func WriteError(w http.ResponseWriter, r *http.Request, code ErrorCode, param string)
//
// Filled by: S03
package httpx
