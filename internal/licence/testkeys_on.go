//go:build licencetest

package licence

import _ "embed"

// A development build (go build -tags licencetest) also embeds the test-mode lk_ key, so
// DEEDGLASS_LICENCE_MODE=test can verify tokens from a Dodo test-mode purchase. The site signs
// those with LICENCE_SIGNING_JWK_TEST, never with the live key.
//
//go:embed licence-keys-test.json
var embeddedTestKeys []byte
