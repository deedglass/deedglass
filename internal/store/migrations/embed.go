// Package migrations holds the control-plane schema as embedded, forward-only SQL files
// (ADR-019). Add a file with the next free number; never edit one that has been applied.
package migrations

import "embed"

// FS holds every NNNN_name.sql file.
//
//go:embed *.sql
var FS embed.FS
