// Command geninstall writes the generated install-link files (S47 §3.6):
//
//	README.md's block between <!-- install-links:start --> and <!-- install-links:end -->
//	examples/play/install-links.json (copied to site/src/data/install-links.json by the site's sync)
//
// -check exits 1 when either is stale (TestGeneratedFresh does the same in make ci).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deedglass/deedglass/internal/installlinks"
)

func main() {
	root := flag.String("root", ".", "the product repo root")
	check := flag.Bool("check", false, "compare instead of writing")
	flag.Parse()
	if _, err := os.Stat(filepath.Join(*root, "go.mod")); err != nil {
		*root = filepath.Join("..", "..")
	}
	readmeP := filepath.Join(*root, "README.md")
	readme, err := os.ReadFile(readmeP)
	if err != nil {
		fail(err)
	}
	newReadme, ok := installlinks.ReplaceReadme(string(readme))
	if !ok {
		fail(fmt.Errorf("README.md has no %s … %s markers", installlinks.ReadmeStart, installlinks.ReadmeEnd))
	}
	files := map[string][]byte{readmeP: []byte(newReadme), filepath.Join(*root, "examples", "play", "install-links.json"): installlinks.SiteJSON()}
	stale := 0
	for p, want := range files {
		have, _ := os.ReadFile(p)
		if bytes.Equal(have, want) {
			continue
		}
		if *check {
			fmt.Fprintf(os.Stderr, "✕ %s is stale. Run go generate ./internal/installlinks\n", p)
			stale++
			continue
		}
		if err := os.WriteFile(p, want, 0o644); err != nil {
			fail(err)
		}
	}
	if stale > 0 {
		os.Exit(1)
	}
	fmt.Println("✓ install links current")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "✕ geninstall:", err)
	os.Exit(1)
}
