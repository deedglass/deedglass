// Command genconfigdoc writes the settings table in internal/config/keys.go as JSON for the docs
// site's configuration page (S33 §3.4):
//
//	go run ./internal/config/cmd/genconfigdoc > docs-site/src/data/config.json
//
// docs-site/scripts/gen-data.mjs runs it; `npm run gen:check` fails when the committed file is stale.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/deedglass/deedglass/internal/config"
)

// Row is one setting as the docs render it. Secrets carry no default and are never printed.
type Row struct {
	Env      string   `json:"env"`
	YAML     string   `json:"yaml"`
	Type     string   `json:"type"`
	Default  string   `json:"default"`
	Values   []string `json:"values,omitempty"`
	Secret   bool     `json:"secret"`
	Client   bool     `json:"client"`
	DevOnly  bool     `json:"dev_only"`
	Commands string   `json:"commands"`
	Doc      string   `json:"doc"`
	Owner    string   `json:"owner"`
}

var kindNames = map[config.Kind]string{
	config.KindString:      "string",
	config.KindSecret:      "secret",
	config.KindEnum:        "enum",
	config.KindInt:         "integer",
	config.KindBool:        "boolean",
	config.KindURL:         "URL",
	config.KindCIDRList:    "CIDR list",
	config.KindPath:        "path",
	config.KindListen:      "host:port",
	config.KindPostgresURL: "Postgres URL",
}

// Rows returns every key in display order.
func Rows() ([]Row, error) {
	var out []Row
	for _, k := range config.Keys {
		name, ok := kindNames[k.Kind]
		if !ok {
			return nil, fmt.Errorf("%s: kind %d has no docs name", k.Name, k.Kind)
		}
		def := k.Default
		if k.Secret {
			def = ""
		}
		out = append(out, Row{Env: k.Name, YAML: k.YAMLName(), Type: name, Default: def, Values: k.Enum,
			Secret: k.Secret, Client: k.Client, DevOnly: k.DevOnly, Commands: k.Commands, Doc: k.Doc, Owner: k.Owner})
	}
	return out, nil
}

func run(w io.Writer) error {
	rows, err := Rows()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "genconfigdoc:", err)
		os.Exit(1)
	}
}
