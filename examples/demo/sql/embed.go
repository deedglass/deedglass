// Package sql holds the Acme demo's SQL (S15, S17, S21), embedded so deedglass init loads the same
// schema and seed into its local demo database as make demo-up does into the demo's orders-db.
package sql

import (
	_ "embed"
	"io/fs"

	"github.com/deedglass/deedglass/internal/embedgz"
)

// The files are embedded gzip-compressed (sql.tar.gz, make embed-gz; the image budget).
//
//go:embed sql.tar.gz
var archive []byte

// FS holds every *.sql file of this directory under its file name.
var FS = embedgz.FS(archive).(interface {
	fs.ReadDirFS
	fs.ReadFileFS
})
