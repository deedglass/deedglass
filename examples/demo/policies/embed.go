// Package demopolicies embeds the Acme demo policy, so deedglass dev seed --demo loads exactly the
// file the demo's cases test (S15).
package demopolicies

import _ "embed"

// AcmeDemo is examples/demo/policies/acme-demo.yaml.
//
//go:embed acme-demo.yaml
var AcmeDemo []byte
