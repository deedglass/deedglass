// Package mcp puts the Gate in front of MCP servers: a stdio wrapper (deedglass mcp wrap, S14), a
// Streamable HTTP gateway at /mcp/{connector} and the native tools (deedglass mcp serve, S15)
// (ADR-018, ADR-036).
//
// Subpackages (S14):
//   - jsonrpc: one JSON-RPC message per line, members kept as raw bytes; a bounded reader and a
//     single-owner writer.
//   - gateclient: the Gate as the MCP surfaces call it (Submit in proxy mode, Report, Resolve,
//     GetAction, Whoami); an answer it can't read is Unreachable.
//   - present: MCP tool results built from internal/gate/present.go, era-correct.
//   - wrap: the stdio relay, upstream supervision, the tool catalog with pinning, the tools/call
//     flow and deedglass.check_approval.
//   - snippet: client config for Claude Desktop, Claude Code, Cursor, VS Code and Windsurf.
//   - mcptest: a fake stdio MCP server for tests.
//
// Invariants:
//   - A governed tools/call never reaches the server without an authorized answer from the Gate
//     (outside dev --fail-open).
//   - Nothing but JSON-RPC lines is written to the client's stdout.
//   - Agents never receive connector credentials; the agent's key never reaches the server's
//     environment or a log line.
//   - Every tool call becomes an action; the MCP tool-name prefix deedglass. is reserved.
//   - Policy errors reach the client as tool results with a next step, never as auth challenges.
//   - The HTTP gateway is sessionless: GET and DELETE answer 405.
package mcp
