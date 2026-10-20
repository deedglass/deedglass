// Package a2a puts the Gate in front of a remote agent reached over the Agent2Agent (A2A) protocol
// (TY-07, ADR-113): deedglass a2a proxy.
//
// The calling agent sends its A2A JSON-RPC requests to a loopback proxy instead of the peer. A
// request that asks the peer to act (SendMessage, SendStreamingMessage, CancelTask and the push
// notification config writes) becomes a proxy-mode action: the Gate decides first, the proxy
// forwards only an authorized call, then reports what the peer answered. Reads (GetTask, ListTasks,
// SubscribeToTask, the push config reads, GetExtendedAgentCard) pass through unrecorded, like
// tools/list in deedglass mcp wrap. Any other method is refused.
//
// The peer's answer is recorded as the peer reported it (Claimed, L0): Deedglass can't see what a
// remote agent did. When the peer's own tool calls go through Deedglass, those are separate actions
// with their own Checked or Proven verdicts, and they carry the same delegation chain.
//
// Delegation chain: who asked whom, from the person to the peer. It travels in the message's
// metadata under ExtensionURI, the way A2A §4.6 says extensions carry data, and is recorded in the
// action's arguments (bound into the record by request_digest), with the person as on_behalf_of.
//
// Pinned spec: A2A v1.0.1 (github.com/a2aproject/A2A, tag v1.0.1, 2026-05-28): the JSON-RPC binding
// (spec §9), ProtoJSON field names (§5.5), specification/a2a.proto with SHA-256
// e195bf96ab630c69797851970203e1b2b6b19528f2e9803b7d904b91a5104016. Only the fields this package
// reads are modelled; everything else is passed through as bytes.
//
// Invariants:
//   - An acting request never reaches the peer without an authorized answer from the Gate.
//   - The peer's response bytes reach the caller unchanged; the proxy adds nothing to them.
//   - The calling agent's credentials never travel to the peer; the peer's never reach a log line.
package a2a
