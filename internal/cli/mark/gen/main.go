// Command gen writes orb_gen.go, the Split Orb's pixel grid, from the brand geometry
// (internal/cli/mark/internal/orbgen). Run it with go generate ./internal/cli/mark.
package main

import (
	"fmt"
	"os"

	"github.com/deedglass/deedglass/internal/cli/mark/internal/orbgen"
)

func main() {
	if err := os.WriteFile("orb_gen.go", orbgen.Generate(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
