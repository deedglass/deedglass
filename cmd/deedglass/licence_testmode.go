//go:build licencetest

package main

// A development build (go build -tags licencetest ./cmd/deedglass) honours
// DEEDGLASS_LICENCE_MODE=test, for the founder's Dodo test-mode purchase. Release builds never set
// this tag (Makefile, .goreleaser.yaml), so they accept live licence tokens only.
func init() { licenceTestModeBuild = true }
