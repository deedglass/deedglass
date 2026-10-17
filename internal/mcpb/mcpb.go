// Package mcpb builds Deedglass's Claude Desktop bundles (.mcpb, S47 §3.6): a zip with
// manifest.json (MCPB manifest spec 0.3, checked 2026-10-12 at github.com/anthropics/mcpb
// MANIFEST.md), the platform's deedglass binary under server/, and the icon. One bundle per
// platform: darwin-arm64, darwin-x64 and win32-x64.
//
// The bundle runs `deedglass mcp serve` from the binary it carries. The agent key and the server
// URL are user_config fields Claude Desktop asks for and keeps (the key is sensitive); neither is
// in the bundle. Left empty, mcp serve finds the key the way the CLI does (the key file
// `deedglass init` writes). Writes are byte-identical for the same inputs.
package mcpb

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"time"

	"github.com/deedglass/deedglass/internal/installlinks"
)

// ManifestVersion is the MCPB manifest spec this package writes.
const ManifestVersion = "0.3"

// Platforms are the bundles a release carries, as GoReleaser names their binaries.
var Platforms = []Platform{
	{ID: "darwin-arm64", OS: "darwin", GoOS: "darwin", GoArch: "arm64"},
	{ID: "darwin-x64", OS: "darwin", GoOS: "darwin", GoArch: "amd64"},
	{ID: "win32-x64", OS: "win32", GoOS: "windows", GoArch: "amd64"},
}

// Platform is one bundle's target.
type Platform struct {
	ID     string // the file-name suffix: deedglass-<version>-<id>.mcpb
	OS     string // compatibility.platforms value (Node's process.platform)
	GoOS   string
	GoArch string
}

// Binary is the server's path inside the bundle.
func (p Platform) Binary() string {
	if p.OS == "win32" {
		return "server/deedglass.exe"
	}
	return "server/deedglass"
}

// FileName is the release asset name; internal/installlinks points at the same names.
func (p Platform) FileName(version string) string {
	return "deedglass-" + version + "-" + p.ID + ".mcpb"
}

// ByID finds a platform.
func ByID(id string) (Platform, bool) {
	i := slices.IndexFunc(Platforms, func(p Platform) bool { return p.ID == id })
	if i < 0 {
		return Platform{}, false
	}
	return Platforms[i], true
}

// Manifest is manifest.json.
type Manifest struct {
	ManifestVersion string                `json:"manifest_version"`
	Name            string                `json:"name"`
	DisplayName     string                `json:"display_name"`
	Version         string                `json:"version"`
	Description     string                `json:"description"`
	LongDescription string                `json:"long_description"`
	Author          Author                `json:"author"`
	Repository      Repository            `json:"repository"`
	Homepage        string                `json:"homepage"`
	Documentation   string                `json:"documentation"`
	Support         string                `json:"support"`
	Icon            string                `json:"icon,omitempty"`
	Server          Server                `json:"server"`
	ToolsGenerated  bool                  `json:"tools_generated"`
	Keywords        []string              `json:"keywords"`
	License         string                `json:"license"`
	Compatibility   Compatibility         `json:"compatibility"`
	UserConfig      map[string]UserConfig `json:"user_config"`
}

type Author struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Repository struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type Server struct {
	Type       string    `json:"type"`
	EntryPoint string    `json:"entry_point"`
	MCPConfig  MCPConfig `json:"mcp_config"`
}

type MCPConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

type Compatibility struct {
	Platforms []string `json:"platforms"`
}

type UserConfig struct {
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Sensitive   bool   `json:"sensitive,omitempty"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
}

// DefaultServer is the server a fresh `deedglass init` runs.
const DefaultServer = "http://127.0.0.1:8080"

// NewManifest is the manifest for one platform.
func NewManifest(p Platform, version string, icon bool) Manifest {
	m := Manifest{
		ManifestVersion: ManifestVersion,
		Name:            installlinks.Name,
		DisplayName:     "Deedglass",
		Version:         version,
		Description:     installlinks.Description,
		LongDescription: "Deedglass sits between Claude and your tools. Every tool call is checked against your policies, " +
			"waits for a person when a policy asks, and is sealed as a record you can check offline. " +
			"This bundle runs `deedglass mcp serve` against your own Deedglass server. " +
			"Run `npx deedglass init` in a terminal first; it starts a local server and writes the agent key this bundle uses.",
		Author:        Author{Name: "Deedglass", URL: "https://deedglass.com"},
		Repository:    Repository{Type: "git", URL: "https://github.com/deedglass/deedglass"},
		Homepage:      "https://deedglass.com",
		Documentation: "https://docs.deedglass.com/get-started/claude-desktop/",
		Support:       "https://deedglass.com/support",
		Server: Server{Type: "binary", EntryPoint: p.Binary(), MCPConfig: MCPConfig{
			Command: "${__dirname}/" + p.Binary(),
			Args:    []string{"mcp", "serve"},
			Env:     map[string]string{"DEEDGLASS_API_KEY": "${user_config.api_key}", "DEEDGLASS_SERVER": "${user_config.server_url}"},
		}},
		ToolsGenerated: true,
		Keywords:       []string{"agents", "policies", "approvals", "audit", "postgres", "mcp"},
		License:        "Apache-2.0 AND FSL-1.1-ALv2",
		Compatibility:  Compatibility{Platforms: []string{p.OS}},
		UserConfig: map[string]UserConfig{
			"api_key": {Type: "string", Title: "Agent key", Sensitive: true,
				Description: "An agent key from deedglass init or deedglass agent keys create. Leave it empty to use the key file deedglass init wrote."},
			"server_url": {Type: "string", Title: "Deedglass server", Default: DefaultServer,
				Description: "Your Deedglass server. deedglass init runs one on this address."},
		},
	}
	if icon {
		m.Icon = "icon.png"
	}
	return m
}

// JSON renders a manifest with two-space indents and no HTML escaping (the ${…} placeholders
// stay readable).
func (m Manifest) JSON() []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(m)
	return b.Bytes()
}

// stamp is every entry's time, so the same inputs give the same bytes.
var stamp = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Write writes one bundle: manifest.json, the binary (0755) and the icon when given.
func Write(w io.Writer, p Platform, version string, binary, icon []byte) error {
	if len(binary) == 0 {
		return fmt.Errorf("mcpb: no %s binary", p.ID)
	}
	zw := zip.NewWriter(w)
	add := func(name string, mode uint32, b []byte) error {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: stamp}
		h.SetMode(fs.FileMode(mode))
		f, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		return err
	}
	if err := add("manifest.json", 0o644, NewManifest(p, version, icon != nil).JSON()); err != nil {
		return err
	}
	if icon != nil {
		if err := add("icon.png", 0o644, icon); err != nil {
			return err
		}
	}
	if err := add(p.Binary(), 0o755, binary); err != nil {
		return err
	}
	return zw.Close()
}
