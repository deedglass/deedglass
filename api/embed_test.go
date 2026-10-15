package api

import (
	"bytes"
	"os"
	"testing"
)

// The embedded openapi.yaml.gz inflates to openapi.yaml exactly.
func TestSpecGzipCurrent(t *testing.T) {
	want, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(Spec, want) {
		t.Fatal("api/openapi.yaml.gz is stale: run make api-gz and commit api/openapi.yaml.gz")
	}
}
