// Package demopolicies embeds the Acme demo policy, so deedglass dev seed --demo loads exactly the
// file the demo's cases test (S15).
package demopolicies

import (
	_ "embed"
	"io/fs"

	"github.com/deedglass/deedglass/internal/embedgz"
)

// The file is embedded gzip-compressed (policies.tar.gz, make embed-gz; the image budget).
//
//go:embed policies.tar.gz
var archive []byte

// AcmeDemo is examples/demo/policies/acme-demo.yaml.
var AcmeDemo = mustRead("acme-demo.yaml")

func mustRead(name string) []byte {
	b, err := fs.ReadFile(embedgz.FS(archive), name)
	if err != nil {
		panic("demopolicies: " + err.Error())
	}
	return b
}
