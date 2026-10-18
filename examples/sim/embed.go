// Package simfiles embeds the default scenario for deedglass dev simulate (S27) and the three
// versions of its policy, so the CLI, the generator and the tests read the same bytes as the
// files on disk. go:embed can't reach outside its own directory, which is why this tiny package
// exists.
package simfiles

import (
	_ "embed"
	"io/fs"

	"github.com/deedglass/deedglass/internal/embedgz"
)

// The files are embedded gzip-compressed (sim.tar.gz, make embed-gz; the image budget).
//
//go:embed sim.tar.gz
var archive []byte

// FS holds acme.yaml and the three policy versions it names, under their file names.
var FS = embedgz.FS(archive).(fs.ReadFileFS)

// The named files are read from FS once.
var (
	// AcmeScenario is examples/sim/acme.yaml, the default scenario.
	AcmeScenario = mustRead("acme.yaml")
	// AcmeSimV1 is examples/sim/acme-sim.v1.yaml, the enforced policy (version 1 of acme-sim).
	AcmeSimV1 = mustRead("acme-sim.v1.yaml")
	// AcmeSimV2 is examples/sim/acme-sim.v2.yaml, the shadow policy from day 3 (version 2).
	AcmeSimV2 = mustRead("acme-sim.v2.yaml")
	// AcmeSimV3 is examples/sim/acme-sim.v3.yaml, the replay demo (version 3).
	AcmeSimV3 = mustRead("acme-sim.v3.yaml")
)

func mustRead(name string) []byte {
	b, err := FS.ReadFile(name)
	if err != nil {
		panic("simfiles: " + err.Error())
	}
	return b
}
