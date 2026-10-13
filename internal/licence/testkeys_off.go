//go:build !licencetest

package licence

// A release build embeds no test-mode key: EmbeddedTest is empty, so no test-mode token verifies.
var embeddedTestKeys []byte
