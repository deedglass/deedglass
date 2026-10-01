// Package connector is the connector framework (S16, contract §2): the Connector interface every
// type implements (rest today; postgres in S17; github, slack, stripe, gmail and notion in
// S29–S30; fake in dev), the per-org Registry the Gate resolves tools through, the typed error
// taxonomy, the naming rules and the JSON Schema subset tool arguments are checked against.
//
// Invariants:
//   - All outbound traffic goes through connector/egress, the one SSRF-safe door
//     (TestSingleEgressDoor).
//   - Instance.Secret is the only path to a decrypted credential (TestOpenerCallSites). It is
//     called only while the Gate runs a call, never with a database transaction open, and its
//     values join the call's redactor: outputs, messages and log lines never carry them.
//   - A credential that can't be opened fails the call (vault_unavailable). Nothing is ever sent
//     without the auth its connector is configured with.
//   - Every call has a timeout. A timeout or lost response is execution.status = unknown, never a
//     guess.
//   - Connector names deedglass, mcp and native are reserved; fake exists only in dev.
package connector
