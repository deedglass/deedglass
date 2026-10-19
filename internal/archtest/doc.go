// Package archtest holds the import-layering test (ADR-013). It has no code of its own.
//
// Invariants (checked by archtest_test.go with go list -deps):
//   - ids and buildinfo import only the standard library.
//   - config imports the standard library plus go.yaml.in/yaml/v3.
//   - deed, sqlguard, policy and witness import none of net/http, database/sql,
//     github.com/jackc/*, internal/store or internal/httpx.
//   - Nothing in internal/ imports cmd/.
//   - TY-04 (core_test.go): gate, witness and deed (and every package below them) depend on no MCP
//     package, no internal/connector/<vendor> package (only internal/connector and its schema and
//     egress leaves) and no coding-agent hook package; a fixture module in testdata/planted shows
//     the rule catching planted imports.
//
// rename_guard_test.go also runs scripts/rename_guard.sh in a throwaway git repo (ADR-012).
//
// Filled by: S01
package archtest
