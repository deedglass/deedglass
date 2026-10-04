// Package catalogdocs renders the generated part of docs/connectors/<type>.md from the connector
// catalog (S30 §3.4): the tools table, credentials, setup and limits, between the markers
// <!-- deedglass:generated start --> and <!-- deedglass:generated end -->. gendocs writes the
// pages; TestConnectorDocsFresh fails when one is stale. The catalog, GET /v1/tools, the CLI and
// these pages all come from the same TypeInfo.
package catalogdocs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/fake"
	"github.com/deedglass/deedglass/internal/connector/github"
	"github.com/deedglass/deedglass/internal/connector/gmail"
	"github.com/deedglass/deedglass/internal/connector/notion"
	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/connector/rest"
	"github.com/deedglass/deedglass/internal/connector/slack"
	"github.com/deedglass/deedglass/internal/connector/stripe"
	"github.com/deedglass/deedglass/internal/mcp/upstream"
)

// Markers around the generated section.
const (
	Start = "<!-- deedglass:generated start -->"
	End   = "<!-- deedglass:generated end -->"
)

// Paged are the types that must have a page under docs/connectors.
var Paged = []string{"github", "gmail", "notion", "slack", "stripe"}

// Types returns every connector type this build serves, the way serve registers them.
func Types() []connector.Connector {
	p, _ := upstream.New(upstream.Options{})
	return []connector.Connector{fake.New(), rest.New(), postgres.New(slog.New(slog.NewTextHandler(io.Discard, nil))), upstream.NewType(p),
		github.New(), slack.New(), stripe.New(), gmail.New(), notion.New()}
}

// Infos is the catalog, by type.
func Infos() map[string]connector.TypeInfo {
	out := map[string]connector.TypeInfo{}
	for _, c := range Types() {
		out[c.Type()] = c.Info()
	}
	return out
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}

// Section renders the generated part of a type's page.
func Section(t connector.TypeInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n<!-- Generated from the connector catalog by go run ./internal/connector/cmd/gendocs. Don't edit by hand. -->\n\n", Start)
	fmt.Fprintf(&b, "**%s** · %s · levels %s", t.Title, t.Status, strings.Join(t.Levels, " "))
	if len(t.Modes) > 0 {
		fmt.Fprintf(&b, " · %s", strings.ReplaceAll(strings.Join(t.Modes, ", "), "_", " "))
	}
	fmt.Fprintf(&b, "\n\n%s\n\n", t.Summary)
	if len(t.Tools) > 0 {
		b.WriteString("### Tools\n\n| Tool | Side effect | Levels | What L1 checks |\n|---|---|---|---|\n")
		for _, tl := range t.Tools {
			rb := tl.ReadBack
			if rb == "" {
				rb = "Not read back"
			}
			if tl.RequiresScope != "" {
				rb += " Needs " + tl.RequiresScope + "."
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", tl.Name, tl.SideEffect, strings.Join(tl.Levels, " "), cell(rb))
		}
		b.WriteString("\n")
		b.WriteString("| Tool | What it does |\n|---|---|\n")
		for _, tl := range t.Tools {
			fmt.Fprintf(&b, "| `%s` | %s |\n", tl.Name, cell(tl.Description))
		}
		b.WriteString("\n")
	}
	b.WriteString("### Credentials\n\n")
	for _, k := range t.CredentialKinds {
		fmt.Fprintf(&b, "Kind `%s`:\n\n", k.Kind)
		for _, f := range k.Fields {
			sec := "not secret"
			if f.Secret {
				sec = "secret"
			}
			line := fmt.Sprintf("- `%s` (%s)", f.Name, sec)
			if f.Help != "" {
				line += ": " + f.Help
			}
			if f.PrefixHint != "" {
				line += " Starts with `" + f.PrefixHint + "`."
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}
	if len(t.Setup) > 0 {
		b.WriteString("### Setup\n\n")
		for i, s := range t.Setup {
			fmt.Fprintf(&b, "%d. **%s.**", i+1, s.Title)
			if s.Detail != "" {
				b.WriteString(" " + s.Detail)
			}
			if s.URL != "" {
				b.WriteString(" " + s.URL)
			}
			b.WriteString("\n")
			if s.Command != "" {
				fmt.Fprintf(&b, "   `%s`\n", s.Command)
			}
		}
		b.WriteString("\n")
	}
	if len(t.Limits) > 0 {
		b.WriteString("### What it doesn't do\n\n")
		for _, l := range t.Limits {
			b.WriteString("- " + l + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(End)
	return b.String()
}

// ErrNoMarkers is a page without the generated markers.
var ErrNoMarkers = errors.New("catalogdocs: the page has no deedglass:generated markers")

// Rewrite replaces the generated section of page with t's.
func Rewrite(page []byte, t connector.TypeInfo) ([]byte, error) {
	i := bytes.Index(page, []byte(Start))
	j := bytes.Index(page, []byte(End))
	if i < 0 || j < i {
		return nil, ErrNoMarkers
	}
	var out bytes.Buffer
	out.Write(page[:i])
	out.WriteString(Section(t))
	out.Write(page[j+len(End):])
	return out.Bytes(), nil
}

// Sorted is the type names in order.
func Sorted(m map[string]connector.TypeInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
