// gendocs rewrites the generated section of docs/connectors/<type>.md from the connector catalog
// (S30). Run it from the repository root: go run ./internal/connector/cmd/gendocs
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deedglass/deedglass/internal/connector/catalogdocs"
)

func main() {
	dir := filepath.Join("docs", "connectors")
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	infos := catalogdocs.Infos()
	n := 0
	for _, typ := range catalogdocs.Sorted(infos) {
		p := filepath.Join(dir, typ+".md")
		page, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			fail(err)
		}
		out, err := catalogdocs.Rewrite(page, infos[typ])
		if err != nil {
			fail(fmt.Errorf("%s: %w", p, err))
		}
		if string(out) != string(page) {
			if err := os.WriteFile(p, out, 0o644); err != nil {
				fail(err)
			}
			fmt.Println("wrote", p)
		}
		n++
	}
	fmt.Printf("%d connector pages are current\n", n)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gendocs:", err)
	os.Exit(1)
}
