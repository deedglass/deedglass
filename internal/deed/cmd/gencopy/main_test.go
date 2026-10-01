package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../../../.."

// TestGeneratedFilesFresh fails when messages.go, errors.go or the key lists changed without
// re-running gencopy.
func TestGeneratedFilesFresh(t *testing.T) {
	files, err := generate(root)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range files {
		got, err := os.ReadFile(filepath.Join(root, p))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s is stale; run go run ./internal/deed/cmd/gencopy from the repo root", p)
		}
	}
	if !strings.Contains(string(files["web/verify/src/messages.gen.ts"]), `"keys_missing"`) {
		t.Error("keys_missing missing from the TypeScript codes")
	}
}

func TestGenerateTwiceIsIdentical(t *testing.T) {
	a, err := generate(root)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := generate(root)
	for p := range a {
		if !bytes.Equal(a[p], b[p]) {
			t.Errorf("%s differs between runs", p)
		}
	}
	if _, err := generate(t.TempDir()); err == nil {
		t.Error("a root without testdata must fail")
	}
	if _, err := keysTS([]byte("{")); err == nil {
		t.Error("bad JSON must fail")
	}
}
