package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLicenceReaders (S48 §3.6, DL-039): free features never read the licence. Only
// internal/licence, ee/ and cmd/deedglass/licence.go may import internal/licence, and only they
// (plus the help text and the config key table) may name licence.json or licence.key.
func TestLicenceReaders(t *testing.T) {
	root := "../.."
	allowedImport := func(rel string) bool {
		return strings.HasPrefix(rel, "internal/licence/") || strings.HasPrefix(rel, "ee/") || rel == "cmd/deedglass/licence.go"
	}
	allowedName := func(rel string) bool {
		return allowedImport(rel) || rel == "internal/config/keys.go" || rel == "cmd/deedglass/registry.go"
	}
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", ".tmp", "testdata", "web", "docs-site", "sdk", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		n++
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == module+"/internal/licence" && !allowedImport(rel) {
				t.Errorf("%s imports internal/licence; only internal/licence, ee/ and cmd/deedglass/licence.go may (free features never read the licence)", rel)
			}
		}
		if !allowedName(rel) && (strings.Contains(string(src), "licence.json") || strings.Contains(string(src), "licence.key")) {
			t.Errorf("%s names the licence file; only the licence code may", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 100 {
		t.Fatalf("walked only %d Go files; the walk is broken", n)
	}
}
