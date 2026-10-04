// Package sql holds the Acme demo's SQL (S15, S17, S21), embedded so deedglass init loads the same
// schema and seed into its local demo database as make demo-up does into the demo's orders-db.
package sql

import "embed"

//go:embed *.sql
var FS embed.FS
