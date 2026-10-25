package docs_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/installlinks"
)

// description is docs/description.json, the one description of Deedglass (VIRAL-3 PRD-04).
type description struct {
	Tagline  string   `json:"tagline"`
	Short    string   `json:"short"`
	GitHub   string   `json:"github"`
	Summary  string   `json:"summary"`
	Licence  string   `json:"licence"`
	Network  string   `json:"network"`
	Keywords []string `json:"keywords"`
}

func loadDescription(t *testing.T) description {
	t.Helper()
	var d description
	if err := json.Unmarshal([]byte(read(t, "description.json")), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// flat collapses runs of whitespace, so a sentence matches however a file wraps it.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

// TestDescription (PRD-04, DOC-01): every file that describes Deedglass to a person, a registry or a
// model carries docs/description.json's words. Change the wording there first, then here.
func TestDescription(t *testing.T) {
	d := loadDescription(t)
	// The MCP Registry caps server.json's description at 100 characters.
	if n := len([]rune(d.Tagline)); n > 100 || !strings.HasSuffix(d.Tagline, ".") {
		t.Errorf("tagline is %d characters (at most 100) or doesn't end with a period: %q", n, d.Tagline)
	}
	for _, w := range []string{"Claimed", "Checked", "Proven", "Matches", "Doesn't match", "Can't check"} {
		if !strings.Contains(d.Summary, w) {
			t.Errorf("summary doesn't name %q (docs/NAMING.md's plain names)", w)
		}
	}
	if !strings.Contains(d.Licence, "Apache-2.0 AND FSL-1.1-ALv2") || !strings.Contains(d.Network, "free while we grow") {
		t.Error("the licence sentence must name the combined licence and the network sentence that nothing checks a licence while every feature is free (P-086, DL-131)")
	}

	// The GitHub repository description: GitHub shows at most 350 characters, and its first sentence
	// is the README's one-line heading.
	if n := len([]rune(d.GitHub)); n == 0 || n > 350 || !strings.HasSuffix(d.GitHub, ".") {
		t.Errorf("github is %d characters (1 to 350) or doesn't end with a period: %q", n, d.GitHub)
	}
	if first, _, _ := strings.Cut(d.GitHub, ". "); !strings.Contains(read(t, "../README.md"), `<h3 align="center">`+first+`.</h3>`) {
		t.Errorf("README.md's heading must be the first sentence of docs/description.json's github: %q", first+".")
	}

	// The tagline, exactly, where a registry or a directory shows one line.
	jsonField := func(path string, get func(map[string]any) any) any {
		var m map[string]any
		if err := json.Unmarshal([]byte(read(t, path)), &m); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return get(m)
	}
	str := func(k string) func(map[string]any) any { return func(m map[string]any) any { return m[k] } }
	for path, got := range map[string]any{
		"../server.json":                jsonField("../server.json", str("description")),
		"../context7.json":              jsonField("../context7.json", str("description")),
		"../npm/deedglass/package.json": jsonField("../npm/deedglass/package.json", str("description")),
		"../integrations/claude-code/.claude-plugin/plugin.json": jsonField("../integrations/claude-code/.claude-plugin/plugin.json", str("description")),
		"../.claude-plugin/marketplace.json": jsonField("../.claude-plugin/marketplace.json", func(m map[string]any) any {
			return m["plugins"].([]any)[0].(map[string]any)["description"]
		}),
	} {
		if got != d.Tagline {
			t.Errorf("%s description is %q, want the tagline %q", path, got, d.Tagline)
		}
	}
	if m := regexp.MustCompile(`(?m)^  description: "(.*)"$`).FindStringSubmatch(read(t, "../deploy/docker-catalog/server.yaml")); m == nil || m[1] != d.Tagline {
		t.Errorf("deploy/docker-catalog/server.yaml about.description is %v, want the tagline", m)
	}
	// The Claude Desktop bundle's manifest and the Goose install link carry installlinks.Description.
	if installlinks.Description != d.Tagline {
		t.Errorf("internal/installlinks.Description is %q, want the tagline %q", installlinks.Description, d.Tagline)
	}
	// Cursor caps a listing's tagline at 80 characters: there, the tagline cut at its last clause.
	if want := strings.TrimSuffix(d.Tagline, " and seals a record.") + "."; d.Short != want || len([]rune(d.Short)) > 80 {
		t.Errorf("short is %q; want %q, at most 80 characters", d.Short, want)
	}
	if got := jsonField("../integrations/cursor/listing.json", str("tagline")); got != d.Short {
		t.Errorf("integrations/cursor/listing.json tagline is %q, want short %q", got, d.Short)
	}
	// The docs home page: its description is the tagline; its <title> is the site's name.
	home := read(t, "../docs-site/src/content/docs/index.mdx")
	if !strings.Contains(home, "\ntitle: Deedglass Docs\n") || !strings.Contains(home, "\ndescription: "+d.Tagline+"\n") {
		t.Error("docs-site/src/content/docs/index.mdx: want title Deedglass Docs and the tagline as its description")
	}
	var pkg struct{ Keywords []string }
	if err := json.Unmarshal([]byte(read(t, "../npm/deedglass/package.json")), &pkg); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pkg.Keywords, d.Keywords) {
		t.Errorf("npm/deedglass/package.json keywords are %q, want %q", pkg.Keywords, d.Keywords)
	}

	// The summary, the licence and the network sentences, word for word, where people and coding agents read.
	for _, path := range []string{"../README.md", "../AGENTS.md"} {
		text := flat(read(t, path))
		for name, s := range map[string]string{"summary": d.Summary, "licence": d.Licence, "network": d.Network} {
			if !strings.Contains(text, flat(s)) {
				t.Errorf("%s doesn't carry the %s sentence of docs/description.json word for word", path, name)
			}
		}
	}

	// The network sentence, word for word, where people read how the licence check works (P-087).
	for _, path := range []string{"LICENSING.md", "../docs-site/src/content/docs/reference/pro-and-licences.mdx"} {
		if !strings.Contains(flat(read(t, path)), flat(d.Network)) {
			t.Errorf("%s doesn't carry the network sentence of docs/description.json word for word", path)
		}
	}

	// The docs site reads the file: the site description is the tagline and llms.txt's is the summary
	// (docs-site/scripts/check-llms.mjs checks the built llms.txt).
	cfg := read(t, "../docs-site/astro.config.mjs")
	for _, want := range []string{"description: desc.tagline,", "description: desc.summary,", "details: llmsDetails(desc.licence),"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("docs-site/astro.config.mjs lacks %q: the docs site must read docs/description.json", want)
		}
	}
	if regexp.MustCompile(`description:\s*\n?\s*'`).MatchString(cfg) {
		t.Error("docs-site/astro.config.mjs hard-codes a description; read docs/description.json instead")
	}

	// llms-install.md is what an agent follows to set Deedglass up for a user (PRD-02, DOC-06).
	inst := read(t, "../llms-install.md")
	if !strings.Contains(inst, "Ask the user before each change.") || strings.Contains(inst, "--yes") {
		t.Error("llms-install.md must ask the user before each change and never pass --yes")
	}
}

// TestLicenceMetadata (PRD-02): the release binary links ee/ (FSL-1.1-ALv2), so everything that ships
// the binary says "Apache-2.0 AND FSL-1.1-ALv2" and carries both licence texts.
func TestLicenceMetadata(t *testing.T) {
	const combined = "Apache-2.0 AND FSL-1.1-ALv2"
	mains, _ := filepath.Glob("../cmd/deedglass/*.go")
	linksEE := false
	for _, f := range mains {
		if !strings.HasSuffix(f, "_test.go") && strings.Contains(read(t, f), `"github.com/deedglass/deedglass/ee/`) {
			linksEE = true
		}
	}
	if !linksEE {
		t.Fatal("cmd/deedglass no longer imports ee/: the binary may be Apache-2.0 alone; update docs/description.json, the npm packages, .goreleaser.yaml, the Dockerfiles and this test together")
	}
	licence, eeLicence := read(t, "../LICENSE"), read(t, "../ee/LICENSE")
	for _, p := range []string{"darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64", "win32-x64"} {
		dir := "../npm/cli-" + p
		var pkg struct {
			License string
			Files   []string
		}
		if err := json.Unmarshal([]byte(read(t, dir+"/package.json")), &pkg); err != nil {
			t.Fatal(err)
		}
		if pkg.License != combined || !slices.Contains(pkg.Files, "LICENSE") || !slices.Contains(pkg.Files, "ee/LICENSE") {
			t.Errorf("%s/package.json: license %q, files %q; want %q with LICENSE and ee/LICENSE", dir, pkg.License, pkg.Files, combined)
		}
		if read(t, dir+"/LICENSE") != licence || read(t, dir+"/ee/LICENSE") != eeLicence {
			t.Errorf("%s: LICENSE and ee/LICENSE must be byte copies of the repo's (cp LICENSE %s/; cp ee/LICENSE %s/ee/)", dir, dir, dir)
		}
	}
	gr := read(t, "../.goreleaser.yaml")
	if !regexp.MustCompile(`(?m)^      - ee/LICENSE\b`).MatchString(gr) || !strings.Contains(gr, "license: "+combined) {
		t.Error(".goreleaser.yaml: every archive needs ee/LICENSE, and the cask the combined licence")
	}
	for _, f := range []string{"../Dockerfile", "../Dockerfile.release", "../deploy/mcp/Dockerfile"} {
		if !strings.Contains(read(t, f), `org.opencontainers.image.licenses="`+combined+`"`) {
			t.Errorf("%s: the image licence label must be %q", f, combined)
		}
	}
	if !strings.Contains(read(t, "../internal/mcpb/mcpb.go"), `License:        "`+combined+`"`) {
		t.Errorf("internal/mcpb: the Claude Desktop bundle carries the binary, so its licence is %q", combined)
	}
	if !strings.Contains(read(t, "../integrations/cursor/listing.json"), `"license": "`+combined+`"`) {
		t.Errorf("integrations/cursor/listing.json: the server it installs runs the binary; licence %q", combined)
	}
}

// ghcrImagePublic is false while ghcr.io/deedglass/deedglass is private (anonymous pulls get 401/403;
// only the founder can change that). When it is public, set this to true and bring the docker pull
// lines back to README.md and docs/INSTALL.md.
const ghcrImagePublic = false

// TestNoPrivateImageInstructions: while the image is private, nothing people follow tells them to pull
// it, and README.md and docs/INSTALL.md say a public image is coming.
func TestNoPrivateImageInstructions(t *testing.T) {
	if ghcrImagePublic {
		t.Skip("the image is public")
	}
	files := []string{"../README.md", "../AGENTS.md", "../llms-install.md", "../npm/deedglass/README.md", "../scripts/release-notes.sh"}
	for _, root := range []string{".", "../docs-site/src/content/docs", "../docs-site/src/components", "../integrations", "../.github/ISSUE_TEMPLATE"} {
		err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() || !regexp.MustCompile(`\.(md|mdx|astro|json|yml)$`).MatchString(p) {
				return nil
			}
			// docs/RELEASING.md is the maintainer's release check, run with access to the private image.
			if filepath.ToSlash(p) == "RELEASING.md" {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			// Imported and generated docs-site pages are copies of docs/*.md (checked here) or of data.
			if bytes.Contains(b, []byte("\nimported: ")) || bytes.Contains(b, []byte("sidebar:\n  hidden: true")) {
				return nil
			}
			files = append(files, p)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	image := regexp.MustCompile(`ghcr\.io/deedglass/deedglass`)
	for _, f := range files {
		for i, line := range strings.Split(read(t, f), "\n") {
			if image.MatchString(line) {
				t.Errorf("%s:%d names ghcr.io/deedglass/deedglass, which is private (anonymous pulls fail). Point to npx, the install script, a release binary or make web-build docker; flip ghcrImagePublic when the founder makes it public", f, i+1)
			}
		}
	}
	// The cloud kit's scripts take only the ghcr.io image by digest: each kit page that names it says
	// it is private for now, and the production guide says the kit needs registry access.
	kit, _ := filepath.Glob("../deploy/cloud/*.md")
	for _, f := range kit {
		if text := flat(read(t, f)); image.MatchString(text) && !strings.Contains(text, "private for now") {
			t.Errorf("%s names ghcr.io/deedglass/deedglass without saying it is private for now (anonymous pulls fail)", f)
		}
	}
	if !strings.Contains(flat(read(t, "../docs-site/src/content/docs/guides/production.mdx")), "private for now, so the kit needs a registry login") {
		t.Error("docs-site guides/production.mdx must say deploy/cloud needs registry access to the private image")
	}
	if !strings.Contains(flat(read(t, "../README.md")), "A public container image is coming") ||
		!strings.Contains(flat(read(t, "INSTALL.md")), "a public one is coming") {
		t.Error("README.md and docs/INSTALL.md must say a public container image is coming")
	}
}
