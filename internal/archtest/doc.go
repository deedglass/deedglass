// Package archtest holds the import-layering test (ADR-013). It has no code of its own.
//
// Invariants (checked by archtest_test.go with go list -deps):
//   - ids and buildinfo import only the standard library.
//   - config imports the standard library plus go.yaml.in/yaml/v3.
//   - deed, sqlguard, policy and witness import none of net/http, database/sql,
//     github.com/jackc/*, internal/store or internal/httpx.
//   - Nothing in internal/ imports cmd/.
//
// rename_guard_test.go also runs scripts/rename_guard.sh in a throwaway git repo (ADR-012).
//
// Filled by: S01
package archtest
