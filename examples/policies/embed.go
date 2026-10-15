// Package policies holds Deedglass's starter policies (S12 §3.9) and their test cases, embedded so
// onboarding (default-deny), dev seed and the tests read the same bytes as the files on disk.
// go:embed can't reach outside its own directory, which is why this tiny package exists.
package policies

import (
	_ "embed"
	"io/fs"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/embedgz"
)

// The files are embedded gzip-compressed (policies.tar.gz, make embed-gz; the image budget).
//
//go:embed policies.tar.gz
var archive []byte

var files = embedgz.FS(archive).(interface {
	fs.ReadDirFS
	fs.ReadFileFS
})

// FS is every starter policy (<name>.yaml), its cases (<name>.cases.yaml) and hashes.json.
var FS fs.FS = files

// Starters lists the starter policy names, sorted.
func Starters() []string {
	entries, _ := fs.ReadDir(files, ".")
	var out []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".yaml") && !strings.HasSuffix(n, ".cases.yaml") {
			out = append(out, strings.TrimSuffix(n, ".yaml"))
		}
	}
	sort.Strings(out)
	return out
}

// Source returns a starter policy's YAML by name ("postgres-safe").
func Source(name string) ([]byte, error) { return files.ReadFile(name + ".yaml") }

// Cases returns a starter policy's cases file by name.
func Cases(name string) ([]byte, error) { return files.ReadFile(name + ".cases.yaml") }

// Hashes returns hashes.json: every starter's pinned version_hash.
func Hashes() []byte {
	b, _ := files.ReadFile("hashes.json")
	return b
}
