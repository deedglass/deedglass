package packs

import (
	"bytes"
	"maps"
	"os"
	"testing"

	"github.com/deedglass/deedglass/internal/embedgz"
)

// TestEmbeddedArchiveCurrent: snapshot.tar.gz holds exactly the files under snapshot/ (moved into a
// gzip archive for the image budget; make embed-gz rewrites it after go run ./cmd/packgen).
func TestEmbeddedArchiveCurrent(t *testing.T) {
	want, err := embedgz.ReadTree("snapshot")
	if err != nil || len(want) == 0 {
		t.Fatalf("snapshot/: %d files, %v", len(want), err)
	}
	if d := embedgz.Diff(snapshotArchive, want); d != "" {
		t.Fatalf("the embedded snapshot is stale (%s): run make embed-gz and commit it", d)
	}
	src, err := FromFS(os.DirFS("snapshot"), "disk")
	if err != nil {
		t.Fatal(err)
	}
	if got := Embedded(); !maps.EqualFunc(got.Files, src.Files, bytes.Equal) || got.Origin != "embedded" {
		t.Fatal("Embedded() differs from FromFS over snapshot/")
	}
}
