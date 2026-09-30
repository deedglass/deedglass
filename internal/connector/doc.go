// Package connector defines the connector interface, the registry of connector types, and the
// shared parts every connector uses (contract §2). Implementations live in subpackages: rest,
// postgres, github, slack, stripe, gmail, notion and fake.
//
// Invariants:
//   - All outbound traffic goes through connector/egress, the one SSRF-safe door
//     (TestSingleEgressDoor).
//   - Every call has a timeout. A timeout or lost response is execution.status = unknown, never a
//     guess.
//   - Connectors receive credentials from the vault at execution time only.
//   - Connector names deedglass, mcp and native are reserved; fake exists only in dev.
//
// Planned API (finalized by S16):
//
//	type Connector interface {
//		Type() string
//		Tools() []Tool
//		Execute(ctx context.Context, call Call, creds Credentials) (Result, error)
//	}
//
// Filled by: S16 (interface, rest, fake), S17 (postgres), S29–S30 (github, slack, stripe, gmail, notion)
package connector
