package scanfull

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/embedgz"
)

// TestEmbeddedArchiveCurrent: the embedded archive holds exactly the files on disk (S52 moved them
// into a gzip archive for the image budget; make embed-gz rewrites it).
func TestEmbeddedArchiveCurrent(t *testing.T) {
	archive, dir, globs := reportGz, ".", "report.html"
	want := map[string][]byte{}
	for _, g := range strings.Fields(globs) {
		paths, err := filepath.Glob(filepath.Join(dir, g))
		if err != nil || len(paths) == 0 {
			t.Fatalf("no %s in %s", g, dir)
		}
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			want[filepath.Base(p)] = b
		}
	}
	if d := embedgz.Diff(archive, want); d != "" {
		t.Fatalf("the embedded archive is stale (%s): run make embed-gz and commit it", d)
	}
}
