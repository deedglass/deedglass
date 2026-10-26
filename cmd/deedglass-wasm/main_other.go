//go:build !(js && wasm)

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "deedglass-wasm runs in a browser: build it with GOOS=js GOARCH=wasm (make wasm)")
	os.Exit(2)
}
