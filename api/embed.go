// Package api holds the Deedglass OpenAPI 3.1.1 document (openapi.yaml), written by hand and
// embedded in the binary. internal/httpx serves it at GET /api/openapi.yaml, and its tests check
// it against the mounted routes and the error catalog (ADR-023).
//
// The binary carries openapi.yaml.gz (about 85 KB instead of 470 KB, for the image size budget,
// ADR-032) and inflates it once at start. After editing openapi.yaml, run make api-gz;
// TestSpecGzipCurrent fails while the two differ.
package api

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
)

//go:embed openapi.yaml.gz
var specGz []byte

// Spec is api/openapi.yaml, byte for byte.
var Spec = inflate(specGz)

func inflate(b []byte) []byte {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		panic("api: openapi.yaml.gz: " + err.Error())
	}
	out, err := io.ReadAll(r)
	if err != nil {
		panic("api: openapi.yaml.gz: " + err.Error())
	}
	return out
}
