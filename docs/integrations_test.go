package docs_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestFrameworkRecipes keeps the framework pages (docs/integrations/<name>.md) and their runnable files
// (examples/frameworks/<name>/) in step: every code block titled with a path under examples/frameworks/
// equals that file byte for byte, every agent and native file is on its page and is 15 lines or fewer,
// every folder has a check and pinned packages, and every page is imported by the docs site and linked
// from INSTALL_CLIENTS.md.
func TestFrameworkRecipes(t *testing.T) {
	const exDir = "../examples/frameworks"
	entries, err := os.ReadDir(exDir)
	if err != nil {
		t.Fatal(err)
	}
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	sync := read("../docs-site/scripts/sync-docs.mjs")
	sidebar := read("../docs-site/sidebar.mjs")
	clients := read("INSTALL_CLIENTS.md")
	block := regexp.MustCompile("(?s)```[a-z]+ title=\"(examples/frameworks/[^\"]+)\"\n(.*?)\n```")
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		n++
		dir := filepath.Join(exDir, name)
		page := filepath.Join("integrations", name+".md")
		md := read(page)
		shown := map[string]bool{}
		for _, m := range block.FindAllStringSubmatch(md, -1) {
			rel := m[1]
			shown[rel] = true
			if got := strings.TrimRight(read(filepath.Join("..", rel)), "\n"); got != m[2] {
				t.Errorf("%s: the block titled %s differs from the file; regenerate the page from the file", page, rel)
			}
		}
		files, _ := filepath.Glob(filepath.Join(dir, "*"))
		hasCheck, hasPins := false, false
		for _, f := range files {
			base := filepath.Base(f)
			switch {
			case strings.HasPrefix(base, "check."):
				hasCheck = true
			case base == "package.json" || base == "requirements.txt":
				hasPins = true
			case strings.HasPrefix(base, "agent.") || strings.HasPrefix(base, "native."):
				rel := "examples/frameworks/" + name + "/" + base
				if !shown[rel] {
					t.Errorf("%s isn't shown on %s", rel, page)
				}
				if lines := strings.Count(strings.TrimRight(read(f), "\n"), "\n") + 1; lines > 15 {
					t.Errorf("%s has %d lines; a recipe stays at 15 or fewer", rel, lines)
				}
			}
		}
		if !hasCheck || !hasPins {
			t.Errorf("examples/frameworks/%s needs a check file and pinned packages (package.json or requirements.txt)", name)
		}
		if !strings.Contains(sync, "'docs/integrations/"+name+".md': 'frameworks/"+name+"'") {
			t.Errorf("docs-site/scripts/sync-docs.mjs doesn't import docs/integrations/%s.md as frameworks/%s", name, name)
		}
		if !strings.Contains(sidebar, "p('frameworks/"+name+"'") {
			t.Errorf("docs-site/sidebar.mjs doesn't list frameworks/%s", name)
		}
		if !strings.Contains(clients, "(integrations/"+name+".md)") {
			t.Errorf("INSTALL_CLIENTS.md doesn't link integrations/%s.md", name)
		}
	}
	if n < 9 {
		t.Errorf("examples/frameworks has %d folders; adoption-v1 item 4 lists nine frameworks", n)
	}
}
