// Package migrations holds the control-plane schema as embedded, forward-only SQL files
// (ADR-019). Add a file with the next free number; never edit one that has been applied.
//
// The binary carries the files gzip-compressed (sql.tar.gz, about 40 KB instead of 190 KB, for
// the image size budget, ADR-032): after adding a file, run make embed-gz. TestArchiveCurrent
// fails while the archive and the .sql files differ.
package migrations

import (
	_ "embed"
	"io/fs"

	"github.com/deedglass/deedglass/internal/embedgz"
)

//go:embed sql.tar.gz
var archive []byte

// FS holds every NNNN_name.sql file.
var FS fs.FS = embedgz.FS(archive)
