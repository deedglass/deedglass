package migrations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/deedglass/deedglass/internal/embedgz"
)

// The embedded sql.tar.gz holds exactly the .sql files on disk.
func TestArchiveCurrent(t *testing.T) {
	paths, err := filepath.Glob("*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatal("no migrations on disk")
	}
	want := map[string][]byte{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		want[p] = b
	}
	if d := embedgz.Diff(archive, want); d != "" {
		t.Fatalf("internal/store/migrations/sql.tar.gz is stale (%s): run make embed-gz and commit it", d)
	}
}
