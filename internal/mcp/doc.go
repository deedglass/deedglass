// Package mcp puts the Gate in front of MCP servers: a stdio wrapper (deedglass mcp wrap), a
// Streamable HTTP gateway at /mcp/{connector}, and the native tools (deedglass mcp serve) (ADR-018).
//
// Invariants:
//   - Agents never receive connector credentials.
//   - Every tool call becomes an action; the MCP tool-name prefix deedglass. is reserved.
//   - Policy errors reach the client as tool results with a next step, never as auth challenges.
//   - The HTTP gateway is sessionless: GET and DELETE answer 405.
//
// Planned API (finalized by S14–S15):
//
//	func Wrap(ctx context.Context, cmd []string, gate GateClient) error
//	func Handler(g *gate.Gate) http.Handler
//
// Filled by: S14 (wrap, gateway), S15 (native tools, snippets)
package mcp
