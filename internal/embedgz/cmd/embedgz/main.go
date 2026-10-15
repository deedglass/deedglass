// Command embedgz rewrites the gzip archives the binary embeds from the files beside them
// (make embed-gz). Each target is a directory, the glob of the files it embeds, and the archive.
//
//	go run ./internal/embedgz/cmd/embedgz          # rewrite every archive
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/deedglass/deedglass/internal/embedgz"
)

// Targets are the embedded directories (paths from the repo root).
var Targets = []struct{ Dir, Glob, Out string }{
	{"internal/store/migrations", "*.sql", "internal/store/migrations/sql.tar.gz"},
}

func main() {
	for _, t := range Targets {
		paths, err := filepath.Glob(filepath.Join(t.Dir, t.Glob))
		if err != nil || len(paths) == 0 {
			fmt.Fprintf(os.Stderr, "embedgz: no %s in %s\n", t.Glob, t.Dir)
			os.Exit(1)
		}
		files := map[string][]byte{}
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				fmt.Fprintln(os.Stderr, "embedgz:", err)
				os.Exit(1)
			}
			files[filepath.Base(p)] = b
		}
		gz, err := embedgz.Pack(files)
		if err != nil {
			fmt.Fprintln(os.Stderr, "embedgz:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(t.Out, gz, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "embedgz:", err)
			os.Exit(1)
		}
		fmt.Printf("%s · %d files · %d bytes\n", t.Out, len(files), len(gz))
	}
}
