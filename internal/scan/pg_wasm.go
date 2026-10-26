//go:build js && wasm

package scan

import "context"

// The browser build (cmd/deedglass-wasm) has no database part and no HTML report: it scans pasted
// config text only, so pgx and html/template stay out of the WebAssembly file.

// Connector is unused in the browser build; Options.DSN is always "" there.
type Connector func()

func readPostgres(context.Context, Options) (*Postgres, *PostgresFail) { return nil, nil }
