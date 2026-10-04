package connector_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/catalogdocs"
)

// TestConnectorDocsFresh fails when a docs/connectors page's generated section differs from the
// catalog. Fix: go run ./internal/connector/cmd/gendocs
func TestConnectorDocsFresh(t *testing.T) {
	infos := catalogdocs.Infos()
	for _, typ := range catalogdocs.Paged {
		p := filepath.Join("..", "..", "docs", "connectors", typ+".md")
		page, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		out, err := catalogdocs.Rewrite(page, infos[typ])
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if string(out) != string(page) {
			t.Errorf("%s is stale. Run: go run ./internal/connector/cmd/gendocs", p)
		}
	}
}

// TestCatalogComplete checks every type's catalog entry (S30 §3.4): the v2 fields are filled, and
// every write or destructive tool a native read-back covers names what it checks.
func TestCatalogComplete(t *testing.T) {
	infos := catalogdocs.Infos()
	if len(infos) != 8 {
		t.Fatalf("%d types; want 8 (Notion is carried over)", len(infos))
	}
	for typ, info := range infos {
		if info.Summary == "" || info.Status == "" || info.Icon == "" || info.Modes == nil || len(info.Limits) == 0 || len(info.Setup) == 0 || info.Tools == nil {
			t.Errorf("%s: a catalog field is empty: %+v", typ, info)
		}
		if info.ToolsDynamic != (len(info.Tools) == 0) {
			t.Errorf("%s: tools_dynamic %v with %d tools", typ, info.ToolsDynamic, len(info.Tools))
		}
		for _, tl := range info.Tools {
			if tl.SideEffect != connector.SideEffectRead && tl.ReadBack == "" && typ != "fake" {
				t.Errorf("%s: %s has no read_back sentence", typ, tl.Name)
			}
		}
	}
}
