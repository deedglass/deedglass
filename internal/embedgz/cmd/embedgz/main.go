// Command embedgz rewrites the gzip archives the binary embeds from the files beside them
// (make embed-gz). Each target is a directory, the glob of the files it embeds, and the archive.
//
//	go run ./internal/embedgz/cmd/embedgz          # rewrite every archive
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deedglass/deedglass/internal/embedgz"
)

// Targets are the embedded directories (paths from the repo root); Glob may list several patterns.
var Targets = []struct{ Dir, Glob, Out string }{
	{"internal/store/migrations", "*.sql", "internal/store/migrations/sql.tar.gz"},
	{"ee/scanfull", "report.html", "ee/scanfull/report.tar.gz"},
	{"internal/securitypack", "questions.yaml", "internal/securitypack/questions.tar.gz"},
	{"internal/approve/pages", "approve.*", "internal/approve/pages.tar.gz"},
	{"internal/identity/pages", "login.html confirm.html tokens.css auth.css auth.js", "internal/identity/pages.tar.gz"},
	{"examples/policies", "*.yaml hashes.json", "examples/policies/policies.tar.gz"},
	{"internal/weekly", "report.html", "internal/weekly/report.tar.gz"},
	{"internal/mailer/templates", "*.tmpl", "internal/mailer/templates.tar.gz"},
	{"ee/securitypack", "pack.html", "ee/securitypack/pack.tar.gz"},
	{"internal/play", "hero.cast.json", "internal/play/cast.tar.gz"},
}

func main() {
	for _, t := range Targets {
		var paths []string
		var err error
		for _, g := range strings.Fields(t.Glob) {
			var ps []string
			if ps, err = filepath.Glob(filepath.Join(t.Dir, g)); err != nil || len(ps) == 0 {
				paths = nil
				break
			}
			paths = append(paths, ps...)
		}
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
