package scan

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed catalog.yaml
var catalogYAML []byte

// CatalogEntry is one known server or package (catalog.yaml).
type CatalogEntry struct {
	ID       string        `yaml:"id"`
	Kind     string        `yaml:"kind"` // mcp_server · sdk · framework
	Database bool          `yaml:"database"`
	Archived bool          `yaml:"archived"`
	Governed bool          `yaml:"governed"`
	Match    []string      `yaml:"match"`
	Tools    []CatalogTool `yaml:"tools"`
	Note     string        `yaml:"note"`
	Source   string        `yaml:"source"`
	Checked  string        `yaml:"checked"`
}

// CatalogTool is one tool and what it can do.
type CatalogTool struct {
	Name       string `yaml:"name"`
	SideEffect string `yaml:"side_effect"`
}

// Catalog returns the embedded catalog.
func Catalog() ([]CatalogEntry, error) {
	var c struct {
		Entries []CatalogEntry `yaml:"entries"`
	}
	if err := yaml.Unmarshal(catalogYAML, &c); err != nil {
		return nil, err
	}
	return c.Entries, nil
}

var catalog = func() []CatalogEntry {
	c, err := Catalog()
	if err != nil {
		panic("scan: catalog.yaml: " + err.Error())
	}
	return c
}()

// lookup finds the entry for a "npm:…", "pypi:…", "docker:…" or "cmd:…" key.
func lookup(key string) *CatalogEntry {
	for i := range catalog {
		for _, m := range catalog[i].Match {
			if strings.EqualFold(m, key) {
				return &catalog[i]
			}
		}
	}
	return nil
}

// ArchivedPostgres is the package the stacked-COMMIT finding looks for, at any version.
const ArchivedPostgres = "@modelcontextprotocol/server-postgres"

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".venv": true, "venv": true,
	"dist": true, "build": true, ".next": true, "target": true, "__pycache__": true}

const maxFileBytes = 1 << 20

type discovered struct {
	configs     []Config
	deps        []Dependency
	env         []EnvNames
	walk        WalkStats
	policyFiles int
}

// reDBVar names a database URL variable (§3.13.1).
var (
	reDBVar = regexp.MustCompile(`(?i)^(.*_)?(DATABASE|POSTGRES|PG|DB|SUPABASE|NEON)(_.*)?_(URL|URI|DSN)$`)
	rePGVar = regexp.MustCompile(`^PG(HOST|USER|PASSWORD|DATABASE)$`)
)

func discover(o Options) (*discovered, error) {
	d := &discovered{}
	root := o.Dir
	fi, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, errors.New("scan: --dir is not a directory")
	}
	err = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: skip, never fail the scan
		}
		if e.IsDir() {
			if p != root && skipDirs[e.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if e.Type()&fs.ModeSymlink != 0 || !e.Type().IsRegular() {
			return nil // never follow a symlink out of --dir
		}
		if d.walk.Files >= o.MaxFiles {
			d.walk.Stopped = true
			return filepath.SkipAll
		}
		d.walk.Files++
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		kind := classify(rel)
		if kind == "" {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		if info.Size() > maxFileBytes {
			d.walk.SkippedBig++
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		switch kind {
		case "env":
			if names := envNames(b); len(names) > 0 {
				d.env = append(d.env, EnvNames{File: rel, Names: names})
			}
		case "package.json":
			d.deps = append(d.deps, packageJSON(rel, b)...)
		case "pyproject.toml":
			d.deps = append(d.deps, pyproject(rel, b)...)
		case "requirements":
			d.deps = append(d.deps, requirements(rel, b)...)
		case "policy":
			if strings.Contains(string(b), "deedglass: policy/v1") {
				d.policyFiles++
			}
		default:
			d.configs = append(d.configs, parseConfig(rel, kind, b, false))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if o.UserConfigs && o.Home != "" {
		for _, uc := range userConfigPaths(o.GOOS, o.Home) {
			b, err := readSmall(uc.path)
			if err != nil {
				continue
			}
			label := "~" + strings.TrimPrefix(filepath.ToSlash(uc.path), filepath.ToSlash(o.Home))
			c := parseConfig(label, uc.client, b, true)
			d.configs = append(d.configs, c)
		}
	}
	sort.SliceStable(d.configs, func(i, j int) bool {
		if d.configs[i].User != d.configs[j].User {
			return !d.configs[i].User
		}
		return d.configs[i].Path < d.configs[j].Path
	})
	sort.SliceStable(d.deps, func(i, j int) bool {
		if d.deps[i].File != d.deps[j].File {
			return d.deps[i].File < d.deps[j].File
		}
		return d.deps[i].Name < d.deps[j].Name
	})
	sort.SliceStable(d.env, func(i, j int) bool { return d.env[i].File < d.env[j].File })
	return d, nil
}

func readSmall(p string) ([]byte, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxFileBytes {
		return nil, errors.New("not a small regular file")
	}
	return os.ReadFile(p)
}

// classify says which reader a file under --dir needs, or "" to ignore it.
func classify(rel string) string {
	base := path.Base(rel)
	dir := path.Base(path.Dir(rel))
	switch {
	case base == ".env" || strings.HasPrefix(base, ".env."):
		return "env"
	case base == "package.json":
		return "package.json"
	case base == "pyproject.toml":
		return "pyproject.toml"
	case strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt"):
		return "requirements"
	case base == "mcp.json" && dir == ".cursor":
		return "cursor"
	case base == "mcp.json" && dir == ".vscode":
		return "vscode"
	case base == ".mcp.json":
		return "root-mcp"
	case base == "claude_desktop_config.json":
		return "claude-desktop"
	case base == "mcp_config.json":
		return "windsurf"
	case strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml"):
		return "policy"
	}
	return ""
}

type userConfig struct{ path, client string }

// userConfigPaths are the standard per-user MCP client configs (S32 §1, A5), old and new paths.
func userConfigPaths(goos, home string) []userConfig {
	j := func(p ...string) string { return filepath.Join(append([]string{home}, p...)...) }
	var out []userConfig
	switch goos {
	case "darwin":
		out = append(out, userConfig{j("Library", "Application Support", "Claude", "claude_desktop_config.json"), "claude-desktop"})
	case "windows":
		if ad := os.Getenv("APPDATA"); ad != "" {
			out = append(out, userConfig{filepath.Join(ad, "Claude", "claude_desktop_config.json"), "claude-desktop"})
		}
	default:
		out = append(out, userConfig{j(".config", "Claude", "claude_desktop_config.json"), "claude-desktop"})
	}
	return append(out,
		userConfig{j(".cursor", "mcp.json"), "cursor"},
		userConfig{j(".codeium", "windsurf", "mcp_config.json"), "windsurf"},
		userConfig{j(".config", "devin", "mcp_config.json"), "windsurf"},
	)
}

// --- MCP client configs ---

type rawServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]any    `json:"env"`
	URL     string            `json:"url"`
	Type    string            `json:"type"`
	Headers map[string]string `json:"headers"`
}

func parseConfig(label, kind string, b []byte, user bool) Config {
	c := Config{Path: label, Client: kind, User: user, Servers: []Server{}}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		c.Error = "not valid JSON"
		return c
	}
	read := func(key, client string) {
		raw, ok := top[key]
		if !ok {
			return
		}
		var m map[string]rawServer
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		if c.Client == "root-mcp" || c.Client == "" {
			c.Client = client
		} else if kind == "root-mcp" && c.Client != client {
			c.Client = "claude-code+vscode"
		}
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			c.Servers = append(c.Servers, server(n, m[n]))
		}
	}
	switch kind {
	case "vscode":
		read("servers", "vscode")
	case "root-mcp":
		read("mcpServers", "claude-code")
		read("servers", "vscode")
		if c.Client == "root-mcp" {
			c.Client = "claude-code"
		}
	default:
		read("mcpServers", kind)
	}
	return c
}

// reDBURL is a database URL in a command-line argument (only its presence is kept, never the value).
var reDBURL = regexp.MustCompile(`(?i)(postgres|postgresql)://`)

// server reads one entry: the package and version from the command line, env names only.
func server(name string, s rawServer) Server {
	out := Server{Name: name, EnvNames: []string{}, Transport: "stdio"}
	for k := range s.Env {
		out.EnvNames = append(out.EnvNames, k)
	}
	sort.Strings(out.EnvNames)
	if s.URL != "" {
		out.Transport = "http"
		if s.Type == "sse" {
			out.Transport = "sse"
		}
		// A Deedglass gateway URL (…/mcp/native or …/mcp/<connector> on a deedglass host).
		if strings.Contains(s.URL, "/mcp/") && strings.Contains(strings.ToLower(s.URL), "deedglass") {
			out.Package, out.CatalogID, out.Routed = "deedglass", "mcp.deedglass", true
		}
		return out
	}
	args := s.Args
	for _, a := range args {
		if reDBURL.MatchString(a) {
			out.DatabaseURLInArgs = true
		}
	}
	cmd := path.Base(filepath.ToSlash(s.Command))
	out.Command = cmd
	// deedglass mcp wrap … -- <cmd>: routed through the Gate; read the inner command.
	if cmd == "deedglass" && len(args) >= 2 && args[0] == "mcp" {
		switch args[1] {
		case "wrap":
			out.Routed = true
			for i, a := range args {
				if a == "--" && i+1 < len(args) {
					inner := server(name, rawServer{Command: args[i+1], Args: args[i+2:]})
					out.Package, out.Version, out.CatalogID = inner.Package, inner.Version, inner.CatalogID
					out.Command = inner.Command
					break
				}
			}
			return out
		case "serve":
			out.Package, out.CatalogID, out.Routed = "deedglass", "mcp.deedglass", true
			return out
		}
	}
	pkg, ver, eco := packageFromCommand(cmd, args)
	out.Package, out.Version = pkg, ver
	if e := matchEntry(eco, pkg, cmd); e != nil {
		out.CatalogID = e.ID
		if e.Governed {
			out.Routed = true
		}
	}
	return out
}

func matchEntry(eco, pkg, cmd string) *CatalogEntry {
	if pkg != "" {
		if e := lookup(eco + ":" + pkg); e != nil {
			return e
		}
	}
	return lookup("cmd:" + cmd)
}

// packageFromCommand reads `npx -y @scope/pkg@1.2.3`, `uvx pkg`, `pipx run pkg` and
// `docker run … image:tag`.
func packageFromCommand(cmd string, args []string) (pkg, ver, eco string) {
	switch cmd {
	case "npx", "pnpx", "bunx":
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			p, v := splitNPM(a)
			return p, v, "npm"
		}
	case "npm", "pnpm":
		for i, a := range args {
			if (a == "exec" || a == "dlx") && i+1 < len(args) {
				for _, b := range args[i+1:] {
					if !strings.HasPrefix(b, "-") {
						p, v := splitNPM(b)
						return p, v, "npm"
					}
				}
			}
		}
	case "uvx", "pipx":
		for _, a := range args {
			if strings.HasPrefix(a, "-") || a == "run" {
				continue
			}
			p, v, _ := strings.Cut(a, "==")
			if i := strings.IndexAny(p, "@["); i > 0 {
				p = p[:i]
			}
			return p, v, "pypi"
		}
	case "docker", "podman":
		seenRun := false
		skipNext := false
		for _, a := range args {
			if skipNext {
				skipNext = false
				continue
			}
			if a == "run" {
				seenRun = true
				continue
			}
			if !seenRun {
				continue
			}
			if strings.HasPrefix(a, "-") {
				if !strings.Contains(a, "=") && dockerFlagTakesValue(a) {
					skipNext = true
				}
				continue
			}
			img := a
			if i := strings.Index(img, "@"); i > 0 {
				img = img[:i]
			}
			p, v := img, ""
			if i := strings.LastIndex(img, ":"); i > 0 && !strings.Contains(img[i:], "/") {
				p, v = img[:i], img[i+1:]
			}
			return p, v, "docker"
		}
	}
	return "", "", ""
}

func dockerFlagTakesValue(f string) bool {
	switch f {
	case "-e", "--env", "-v", "--volume", "-p", "--publish", "--name", "--network", "-w", "--workdir", "-u", "--user", "--env-file", "--entrypoint", "--mount", "--platform", "-l", "--label":
		return true
	}
	return false
}

// splitNPM splits "@scope/pkg@1.2.3" into ("@scope/pkg", "1.2.3").
func splitNPM(a string) (string, string) {
	at := strings.LastIndex(a, "@")
	if at > 0 {
		return a[:at], a[at+1:]
	}
	return a, ""
}

// --- dependency files ---

func packageJSON(rel string, b []byte) []Dependency {
	var p struct {
		Deps    map[string]string `json:"dependencies"`
		DevDeps map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	var out []Dependency
	for _, m := range []map[string]string{p.Deps, p.DevDeps} {
		for n, v := range m {
			if e := lookup("npm:" + n); e != nil {
				out = append(out, Dependency{File: rel, Name: n, Spec: v, CatalogID: e.ID})
			}
		}
	}
	return out
}

var (
	rePyReq     = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(\[[^\]]*\])?\s*([<>=!~][^;#]*)?`)
	rePoetryDep = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*=\s*(.+)$`)
)

func pyDep(rel, line string) *Dependency {
	m := rePyReq.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	name := strings.ToLower(strings.ReplaceAll(m[1], "_", "-"))
	if e := lookup("pypi:" + name); e != nil {
		return &Dependency{File: rel, Name: name, Spec: strings.TrimSpace(m[3]), CatalogID: e.ID}
	}
	return nil
}

func requirements(rel string, b []byte) []Dependency {
	var out []Dependency
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		if d := pyDep(rel, line); d != nil {
			out = append(out, *d)
		}
	}
	return out
}

// pyproject reads [project].dependencies and [tool.poetry.dependencies] without a TOML library:
// the list's quoted strings, and poetry's "name = spec" lines.
func pyproject(rel string, b []byte) []Dependency {
	var out []Dependency
	section := ""
	inDeps := false
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ")
			inDeps = false
			continue
		}
		switch {
		case section == "project" && strings.HasPrefix(line, "dependencies"):
			inDeps = true
			line = line[strings.Index(line, "=")+1:]
			fallthrough
		case section == "project" && inDeps:
			for _, q := range regexp.MustCompile(`"([^"]+)"|'([^']+)'`).FindAllStringSubmatch(line, -1) {
				s := q[1] + q[2]
				if d := pyDep(rel, s); d != nil {
					out = append(out, *d)
				}
			}
			if strings.Contains(line, "]") {
				inDeps = false
			}
		case section == "tool.poetry.dependencies":
			if m := rePoetryDep.FindStringSubmatch(line); m != nil {
				if d := pyDep(rel, m[1]); d != nil {
					d.Spec = strings.Trim(strings.TrimSpace(m[2]), `"'`)
					out = append(out, *d)
				}
			}
		}
	}
	return out
}

// --- .env names (never values) ---

// envNames returns the database URL variable names in a .env file. Each line is split at the
// first "=" and the right side is dropped before anything else sees it.
func envNames(b []byte) []string {
	var names []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		name, _, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "export "))
		if name == "" || strings.HasPrefix(name, "#") || seen[name] {
			continue
		}
		if reDBVar.MatchString(name) || rePGVar.MatchString(name) {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// --- tools ---

// toolsFor maps every discovered server and dependency to the catalog's tools.
func toolsFor(configs []Config, deps []Dependency) []Tool {
	var out []Tool
	seen := map[string]bool{}
	add := func(t Tool) {
		k := t.Server + "\x00" + t.Tool + "\x00" + t.Where
		if !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	for _, c := range configs {
		for _, s := range c.Servers {
			e := entryByID(s.CatalogID)
			if e == nil {
				continue
			}
			for _, t := range e.Tools {
				add(Tool{Server: s.Name, Tool: t.Name, SideEffect: t.SideEffect, Source: "catalog", Routed: s.Routed, Where: c.Path})
			}
		}
	}
	for _, d := range deps {
		e := entryByID(d.CatalogID)
		if e == nil || e.Kind == "framework" {
			continue
		}
		for _, t := range e.Tools {
			add(Tool{Server: d.Name, Tool: t.Name, SideEffect: t.SideEffect, Source: "catalog", Where: d.File})
		}
	}
	return out
}

func entryByID(id string) *CatalogEntry {
	if id == "" {
		return nil
	}
	for i := range catalog {
		if catalog[i].ID == id {
			return &catalog[i]
		}
	}
	return nil
}
