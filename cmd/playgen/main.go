// Command playgen writes the data files of the browser playground on deedglass.com/play (S47).
//
// The playground never decides anything with its own SQL parser or policy compiler. Everything it
// needs is computed here, from the product's own code, when the site is built:
//
//	acme.sql            the real demo schema, seed and trigger (examples/demo/sql), minus psql meta-commands and roles
//	fixtures.json       the eight statements the scripted agent picks from, with sqlguard's facts for each
//	policies.json       acme-demo and postgres-safe as CEL strings (internal/policy/celexport), and the minor() table
//	parity.golden.json  40 cases with policy.Evaluate's answer; the site's parity test runs them through cel-js
//
//	go run ./cmd/playgen                 # rewrite examples/play
//	go run ./cmd/playgen -check          # exit 1 when a file is stale (TestPlayGenFresh does the same)
//	go run ./cmd/playgen -out ../site/src/play/generated
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", "examples/play", "directory to write")
	check := flag.Bool("check", false, "compare instead of writing; exit 1 when a file differs")
	flag.Parse()
	files, err := Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "✕ playgen:", err)
		os.Exit(1)
	}
	stale := 0
	for _, f := range files {
		p := filepath.Join(*out, f.Name)
		if *check {
			old, _ := os.ReadFile(p)
			if !bytes.Equal(old, f.Data) {
				fmt.Fprintf(os.Stderr, "✕ %s is stale. Run go run ./cmd/playgen -out %s\n", p, *out)
				stale++
			}
			continue
		}
		if err := os.MkdirAll(*out, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "✕ playgen:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "✕ playgen:", err)
			os.Exit(1)
		}
	}
	if stale > 0 {
		os.Exit(1)
	}
	if *check {
		fmt.Printf("✓ %d playground files current in %s\n", len(files), *out)
		return
	}
	fmt.Printf("✓ Wrote %d playground files to %s\n", len(files), *out)
}
