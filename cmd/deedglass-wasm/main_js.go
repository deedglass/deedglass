//go:build js && wasm

// Command deedglass-wasm is the browser build behind deedglass.com/check (growth pick 3). It runs the
// same scan and shell-guard code as the CLI on text pasted into the page: no file system, no
// database, no network. It sets globalThis.deedglass to two functions that return JSON strings:
//
//	deedglass.scanConfig(text, filename) // the report `deedglass scan --json` prints for a directory
//	                                     // holding only that file
//	deedglass.checkCommand(text)         // what `deedglass guard shell --no-files --command … --json` prints
package main

import (
	"syscall/js"
)

func main() {
	js.Global().Set("deedglass", js.ValueOf(map[string]any{
		"version": version,
		"scanConfig": js.FuncOf(func(_ js.Value, a []js.Value) any {
			if len(a) < 2 {
				return errJSON("scanConfig needs the text and a file name")
			}
			return ScanConfig(a[0].String(), a[1].String())
		}),
		"checkCommand": js.FuncOf(func(_ js.Value, a []js.Value) any {
			if len(a) < 1 {
				return errJSON("checkCommand needs the command")
			}
			return CheckCommand(a[0].String())
		}),
	}))
	select {}
}
