// Package api holds the Deedglass OpenAPI 3.1.1 document (openapi.yaml), written by hand and
// embedded in the binary. internal/httpx serves it at GET /api/openapi.yaml, and its tests check
// it against the mounted routes and the error catalog (ADR-023).
package api

import _ "embed"

// Spec is api/openapi.yaml, byte for byte.
//
//go:embed openapi.yaml
var Spec []byte
