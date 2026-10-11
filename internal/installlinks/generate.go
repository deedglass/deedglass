package installlinks

//go:generate go run ./cmd/geninstall

import (
	"bytes"
	"encoding/json"
	"strings"
)

// SiteJSON is site/src/data/install-links.json (deedglass.com/install renders from it).
func SiteJSON() []byte {
	v := map[string]any{"object": "install_links", "v": 1, "version": Version, "server": Server(), "links": Links()}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return b.Bytes()
}

// README markers for the generated block.
const (
	ReadmeStart = "<!-- install-links:start -->"
	ReadmeEnd   = "<!-- install-links:end -->"
)

// ReadmeBlock is the README's install section: https links only (GitHub drops other schemes) and
// Cursor's own badge image, which GitHub proxies.
func ReadmeBlock() string {
	by := map[string]Link{}
	for _, l := range Links() {
		by[l.ID] = l
	}
	var b strings.Builder
	b.WriteString(ReadmeStart + "\n")
	b.WriteString("[![Add to Cursor](https://cursor.com/deeplink/mcp-install-dark.svg)](" + by["cursor-web"].URL + ")\n")
	b.WriteString("[Install in VS Code](" + by["vs-code-web"].URL + ") · ")
	b.WriteString("[LM Studio](https://deedglass.com/install#lm-studio) · [Goose](https://deedglass.com/install#goose) · ")
	b.WriteString("[Claude Desktop](https://deedglass.com/install#claude-desktop) · [Every client](https://deedglass.com/install)\n\n")
	b.WriteString("```sh\n" + by["claude-code"].Command + "\n```\n\n")
	b.WriteString("No link holds a key. Run `npx deedglass init` first; each client asks Deedglass for the key it wrote.\n")
	b.WriteString(ReadmeEnd)
	return b.String()
}

// ReplaceReadme swaps the block between the markers; ok is false when the markers are missing.
func ReplaceReadme(readme string) (string, bool) {
	i := strings.Index(readme, ReadmeStart)
	j := strings.Index(readme, ReadmeEnd)
	if i < 0 || j < i {
		return readme, false
	}
	return readme[:i] + ReadmeBlock() + readme[j+len(ReadmeEnd):], true
}
