// Command gendocs writes the API error catalog in internal/httpx/errors.go as JSON for the docs
// site (S33 §3.4), so every page under docs.deedglass.com/errors/ comes from the one catalog:
//
//	go run ./internal/httpx/cmd/gendocs errors > docs-site/src/data/errors.json
//	go run ./internal/httpx/cmd/gendocs cli-errors > docs-site/src/data/cli-errors.json
//
// docs-site/scripts/gen-data.mjs runs it; `npm run gen:check` fails when the committed file is stale.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/deedglass/deedglass/internal/httpx"
)

// Entry is one code as the docs render it.
type Entry struct {
	Code           string `json:"code"`
	Status         int    `json:"status"`
	Type           string `json:"type"`
	DefaultMessage string `json:"default_message"`
	Template       string `json:"template,omitempty"`
	Owner          string `json:"owner"`
	Retryable      bool   `json:"retryable"`
	DocURL         string `json:"doc_url"`
}

// Retryable reports whether a client may send the same request again unchanged (after the
// Retry-After time when the response has one). Everything else needs a change first.
func Retryable(e httpx.CatalogEntry) bool {
	switch e.Code {
	case httpx.CodeIdempotencyKeyInUse, httpx.CodeTimeout, httpx.CodeServiceUnavailable:
		return true
	case httpx.CodeBudgetExceeded:
		return true // after the reset time in Retry-After
	}
	return e.Status == 429 || e.Status == 503 || e.Status == 504
}

// Entries returns the catalog in contract order.
func Entries() []Entry {
	var out []Entry
	for _, e := range httpx.Catalog() {
		out = append(out, Entry{
			Code:           string(e.Code),
			Status:         e.Status,
			Type:           string(e.Type),
			DefaultMessage: e.Message,
			Template:       e.Template,
			Owner:          e.Added,
			Retryable:      Retryable(e),
			DocURL:         httpx.DocURLPrefix + string(e.Code),
		})
	}
	return out
}

func run(args []string, w io.Writer) error {
	if len(args) != 1 || (args[0] != "errors" && args[0] != "cli-errors") {
		return fmt.Errorf("usage: gendocs errors|cli-errors")
	}
	var v any = Entries()
	if args[0] == "cli-errors" {
		ce, err := CLIErrors("cmd/deedglass")
		if err != nil {
			return err
		}
		v = ce
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(2)
	}
}
