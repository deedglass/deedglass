package installlinks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestEveryLinkDecodes: every link and command installs exactly the one server object.
func TestEveryLinkDecodes(t *testing.T) {
	want := Server()
	n := 0
	for _, l := range Links() {
		if strings.HasPrefix(l.ID, "claude-desktop") || l.ID == "docker" {
			continue
		}
		got, ok := Decode(l)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s decodes to %+v (ok=%v), want %+v", l.ID, got, ok, want)
		}
		n++
	}
	if n != 9 {
		t.Errorf("decoded %d links, want 9", n)
	}
}

// TestClaudeDesktopLinks: no link points at a .mcpb file the release doesn't carry (it would answer
// 404); until MCPBOnRelease the three Claude Desktop entries, and the README, point at the docs page.
func TestClaudeDesktopLinks(t *testing.T) {
	platform := map[string]string{"claude-desktop": "darwin-arm64", "claude-desktop-intel": "darwin-x64", "claude-desktop-windows": "win32-x64"}
	n := 0
	for _, l := range Links() {
		if !MCPBOnRelease && (strings.Contains(l.URL, ".mcpb") || l.Kind == "download") {
			t.Errorf("%s points at %s, but MCPBOnRelease is false (the release carries no bundles)", l.ID, l.URL)
		}
		p, ok := platform[l.ID]
		if !ok {
			continue
		}
		n++
		want := Link{ID: l.ID, Client: l.Client, Kind: "web", URL: ClaudeDesktopGuide, Note: l.Note}
		if MCPBOnRelease {
			want.Kind, want.URL = "download", BundleURL(p)
		}
		if l != want {
			t.Errorf("%s is %+v, want %+v", l.ID, l, want)
		}
	}
	if n != 3 {
		t.Errorf("%d Claude Desktop entries, want 3 (deedglass.com/install renders all three)", n)
	}
	if !strings.HasSuffix(BundleURL("win32-x64"), "/v"+Version+"/deedglass-"+Version+"-win32-x64.mcpb") {
		t.Errorf("BundleURL is %s", BundleURL("win32-x64"))
	}
	if _, err := os.Stat(filepath.Join("..", "..", "docs-site", "src", "content", "docs", "get-started", "claude-desktop.mdx")); err != nil {
		t.Errorf("ClaudeDesktopGuide's page is missing: %v", err)
	}
	if !MCPBOnRelease && (!strings.Contains(ReadmeBlock(), "[Claude Desktop]("+ClaudeDesktopGuide+")") || strings.Contains(ReadmeBlock(), "#claude-desktop")) {
		t.Error("README's Claude Desktop link must point at the docs page until a release carries the bundles")
	}
}

func TestCursorLength(t *testing.T) {
	for _, l := range Links() {
		if strings.HasPrefix(l.ID, "cursor") && len(l.URL) > CursorMaxURL {
			t.Errorf("%s is %d characters", l.ID, len(l.URL))
		}
	}
}

func TestNoSecrets(t *testing.T) {
	all, _ := json.Marshal(Links())
	if HasSecret(string(all)) || HasSecret(ReadmeBlock()) || HasSecret(string(SiteJSON())) {
		t.Fatal("a link holds a key or credentials")
	}
	for _, s := range []string{"dg_test_abc", "postgres://u:p@h/db", "https://user@example.com/x", "Bearer x"} {
		if !HasSecret(s) {
			t.Errorf("HasSecret(%q) = false", s)
		}
	}
	for _, s := range []string{"https://deedglass.com/install#goose", "cursor://anysphere.cursor-deeplink/mcp/install?name=deedglass"} {
		if HasSecret(s) {
			t.Errorf("HasSecret(%q) = true", s)
		}
	}
}

func TestReadmeHTTPSOnly(t *testing.T) {
	for _, f := range strings.FieldsFunc(ReadmeBlock(), func(r rune) bool { return r == '(' || r == ')' }) {
		if strings.Contains(f, "://") && !strings.HasPrefix(f, "https://") {
			t.Errorf("README link isn't https: %s", f)
		}
	}
}

// TestGeneratedFresh: README.md and examples/play/install-links.json match the generator.
func TestGeneratedFresh(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), ReadmeBlock()) {
		t.Error("README.md's install block is stale: go generate ./internal/installlinks")
	}
	site, _ := os.ReadFile(filepath.Join("..", "..", "examples", "play", "install-links.json"))
	if !bytes.Equal(site, SiteJSON()) {
		t.Error("examples/play/install-links.json is stale: go generate ./internal/installlinks")
	}
}

// TestVersionsAgree: the links, the npm package and the registry server.json name one version.
func TestVersionsAgree(t *testing.T) {
	var pkg struct {
		Version string `json:"version"`
		MCPName string `json:"mcpName"`
	}
	b, _ := os.ReadFile(filepath.Join("..", "..", "npm", "deedglass", "package.json"))
	if err := json.Unmarshal(b, &pkg); err != nil || pkg.Version != Version || pkg.MCPName != "io.github.deedglass/deedglass" {
		t.Fatalf("npm/deedglass: %+v %v", pkg, err)
	}
	var srv struct {
		Name     string `json:"name"`
		Version  string `json:"version"`
		Packages []struct {
			Identifier       string `json:"identifier"`
			Version          string `json:"version"`
			PackageArguments []struct {
				Value string `json:"value"`
			} `json:"packageArguments"`
		} `json:"packages"`
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "server.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &srv); err != nil || srv.Name != pkg.MCPName || srv.Version != Version || srv.Packages[0].Version != Version || srv.Packages[0].Identifier != "deedglass" {
		t.Fatalf("server.json: %+v %v", srv, err)
	}
	args := []string{}
	for _, a := range srv.Packages[0].PackageArguments {
		args = append(args, a.Value)
	}
	if strings.Join(args, " ") != "mcp serve" || HasSecret(string(b)) {
		t.Errorf("server.json arguments %v", args)
	}
}
