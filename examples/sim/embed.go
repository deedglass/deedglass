// Package simfiles embeds the default scenario for deedglass dev simulate (S27) and the three
// versions of its policy, so the CLI, the generator and the tests read the same bytes as the
// files on disk. go:embed can't reach outside its own directory, which is why this tiny package
// exists.
package simfiles

import "embed"

// FS holds acme.yaml and the three policy versions it names, under their file names.
//
//go:embed acme.yaml acme-sim.v1.yaml acme-sim.v2.yaml acme-sim.v3.yaml
var FS embed.FS

// AcmeScenario is examples/sim/acme.yaml, the default scenario.
//
//go:embed acme.yaml
var AcmeScenario []byte

// AcmeSimV1 is examples/sim/acme-sim.v1.yaml, the enforced policy (version 1 of acme-sim).
//
//go:embed acme-sim.v1.yaml
var AcmeSimV1 []byte

// AcmeSimV2 is examples/sim/acme-sim.v2.yaml, the shadow policy from day 3 (version 2).
//
//go:embed acme-sim.v2.yaml
var AcmeSimV2 []byte

// AcmeSimV3 is examples/sim/acme-sim.v3.yaml, the replay demo (version 3).
//
//go:embed acme-sim.v3.yaml
var AcmeSimV3 []byte
