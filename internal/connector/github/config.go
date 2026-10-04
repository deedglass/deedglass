package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
)

// Config limits and defaults (S29 §3.2).
const (
	DefaultAPIBaseURL   = "https://api.github.com"
	DefaultAPIVersion   = "2026-03-10"
	DefaultMaxFileBytes = 1 << 20
	MaxMaxFileBytes     = 1 << 20
	MaxRepos            = 50
	MaxProtected        = 50
	MaxConfigBytes      = 32 << 10
)

// DefaultProtectedBranches are the protected branch globs when the config names none.
var DefaultProtectedBranches = []string{"main", "master", "release/*"}

// Config is the normalized config: what the connectors row stores and its hash covers.
type Config struct {
	Auth              string     `json:"auth"` // pat | app
	App               *AppConfig `json:"app,omitempty"`
	Repos             []string   `json:"repos"` // "owner/name" or "owner/*": the hard boundary
	ProtectedBranches []string   `json:"protected_branches"`
	IssueMarker       bool       `json:"issue_marker"`
	APIBaseURL        string     `json:"api_base_url"`
	APIVersion        string     `json:"api_version"`
	MaxFileBytes      int64      `json:"max_file_bytes"`
}

// AppConfig names a GitHub App installation (auth: app).
type AppConfig struct {
	ClientID       string `json:"client_id"`
	AppID          int64  `json:"app_id"`
	InstallationID int64  `json:"installation_id"`
}

var (
	reRepo       = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)
	reRepoRule   = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/([A-Za-z0-9._-]{1,100}|\*)$`)
	reClientID   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reAPIVersion = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

var (
	configKeys = []string{"auth", "app", "repos", "protected_branches", "issue_marker", "api_base_url", "api_version", "max_file_bytes"}
	appKeys    = []string{"client_id", "app_id", "installation_id"}
)

type validator struct{ errs []connector.FieldError }

func (v *validator) add(param, format string, a ...any) {
	if len(v.errs) < 50 {
		v.errs = append(v.errs, connector.FieldError{Param: param, Message: fmt.Sprintf(format, a...)})
	}
}

// Validate implements connector.Connector: strict decoding (an unknown key is a field error named
// config.<key>), defaults, and the checks of S29 §3.2. api_base_url is checked by shape only (https
// outside dev); the test resolves it.
func (c *Connector) Validate(_ context.Context, env connector.Env, raw json.RawMessage) (json.RawMessage, []connector.FieldError) {
	v := &validator{}
	if len(raw) > MaxConfigBytes {
		v.add("config", "The config is %d bytes; the limit is 32 KiB.", len(raw))
		return nil, v.errs
	}
	top, ok := decodeObject(raw)
	if !ok {
		v.add("config", "The config must be a JSON object. See docs/connectors/github.md.")
		return nil, v.errs
	}
	unknownKeys(v, "config", top, configKeys)
	var cfg Config
	field := func(key string, dst any, what string) bool {
		b, ok := top[key]
		if !ok || string(b) == "null" {
			return false
		}
		if err := strictDecode(b, dst); err != nil {
			v.add("config."+key, "%s must be %s.", key, what)
			return false
		}
		return true
	}

	// auth
	if !field("auth", &cfg.Auth, `"pat" or "app"`) {
		cfg.Auth = "pat"
	}
	if cfg.Auth != "pat" && cfg.Auth != "app" {
		v.add("config.auth", `auth must be "pat" (a fine-grained token) or "app" (a GitHub App).`)
	}
	// app
	if b, ok := top["app"]; ok && string(b) != "null" {
		if cfg.Auth != "app" {
			v.add("config.app", "app is only read with auth: app. Remove it, or set auth: app.")
		} else {
			cfg.App = decodeApp(v, b)
		}
	} else if cfg.Auth == "app" {
		v.add("config.app", "auth: app needs app: {client_id, app_id, installation_id}.")
	}
	// repos
	if field("repos", &cfg.Repos, "a list of owner/name entries") {
		normalizeRepos(v, &cfg)
	} else if b, present := top["repos"]; !present || string(b) == "null" {
		v.add("config.repos", "repos is required: 1 to 50 entries like arjun/deedglass-sandbox or arjun/*.")
	}
	// protected_branches
	if !field("protected_branches", &cfg.ProtectedBranches, "a list of branch names or globs") {
		cfg.ProtectedBranches = slices.Clone(DefaultProtectedBranches)
	}
	if len(cfg.ProtectedBranches) > MaxProtected {
		v.add("config.protected_branches", "protected_branches has %d entries; the limit is %d.", len(cfg.ProtectedBranches), MaxProtected)
	}
	for i, p := range cfg.ProtectedBranches {
		if p == "" || len(p) > 255 || strings.ContainsAny(p, " \\\x00") {
			v.add(fmt.Sprintf("config.protected_branches[%d]", i), "Each protected branch is a name or a glob of 1 to 255 characters, like main or release/*.")
			continue
		}
		if _, err := path.Match(p, ""); err != nil {
			v.add(fmt.Sprintf("config.protected_branches[%d]", i), "%q isn't a valid glob. Use * and ? the way path.Match does, like release/*.", p)
		}
	}
	// issue_marker
	cfg.IssueMarker = true
	field("issue_marker", &cfg.IssueMarker, "true or false")
	// api_base_url
	if !field("api_base_url", &cfg.APIBaseURL, "a URL like https://api.github.com") {
		cfg.APIBaseURL = DefaultAPIBaseURL
	}
	pol := env.Egress
	if pol == nil {
		pol = &egress.Policy{Env: env.Name}
		if pol.Env == "" {
			pol.Env = egress.EnvProd
		}
	}
	if chk, err := pol.ParseURL(cfg.APIBaseURL); err != nil {
		if be, ok := egress.IsBlocked(err); ok {
			v.errs = append(v.errs, connector.FieldError{Param: "config.api_base_url", Message: be.About("api_base_url"), Code: connector.CodeDestinationBlocked})
		} else {
			v.add("config.api_base_url", "api_base_url can't be read. Use https://api.github.com.")
		}
	} else {
		if chk.URL.RawQuery != "" || chk.URL.Fragment != "" || chk.URL.User != nil {
			v.add("config.api_base_url", "api_base_url can't carry a query, a fragment or a user name.")
		}
		u := *chk.URL
		u.Path = strings.TrimSuffix(u.Path, "/")
		u.RawPath = ""
		cfg.APIBaseURL = u.String()
	}
	// api_version
	if !field("api_version", &cfg.APIVersion, `a date like "2026-03-10"`) {
		cfg.APIVersion = DefaultAPIVersion
	}
	if !reAPIVersion.MatchString(cfg.APIVersion) {
		v.add("config.api_version", `api_version is GitHub's REST API version, a date like "2026-03-10".`)
	}
	// max_file_bytes
	if !field("max_file_bytes", &cfg.MaxFileBytes, "an integer") {
		cfg.MaxFileBytes = DefaultMaxFileBytes
	}
	if cfg.MaxFileBytes < 1 || cfg.MaxFileBytes > MaxMaxFileBytes {
		v.add("config.max_file_bytes", "max_file_bytes must be 1 to 1048576 (1 MiB).")
	}

	if len(v.errs) > 0 {
		return nil, v.errs
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		v.add("config", "The config can't be written back: %v.", err)
		return nil, v.errs
	}
	return out, nil
}

func decodeApp(v *validator, b json.RawMessage) *AppConfig {
	m, ok := decodeObject(b)
	if !ok {
		v.add("config.app", "app must be an object: {client_id, app_id, installation_id}.")
		return nil
	}
	unknownKeys(v, "config.app", m, appKeys)
	app := &AppConfig{}
	if err := strictDecode(m["client_id"], &app.ClientID); err != nil || !reClientID.MatchString(app.ClientID) {
		v.add("config.app.client_id", "client_id is the App's client ID from its settings page, like Iv23liAbC123.")
	}
	if err := strictDecode(m["app_id"], &app.AppID); err != nil || app.AppID < 1 {
		v.add("config.app.app_id", "app_id is the App's numeric ID, like 1234567.")
	}
	if err := strictDecode(m["installation_id"], &app.InstallationID); err != nil || app.InstallationID < 1 {
		v.add("config.app.installation_id", "installation_id is the number at the end of the installation's settings URL, like 76543210.")
	}
	return app
}

func normalizeRepos(v *validator, cfg *Config) {
	if len(cfg.Repos) == 0 || len(cfg.Repos) > MaxRepos {
		v.add("config.repos", "repos needs 1 to %d entries; it has %d.", MaxRepos, len(cfg.Repos))
		return
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(cfg.Repos))
	for i, r := range cfg.Repos {
		_, name, _ := strings.Cut(r, "/")
		if !reRepoRule.MatchString(r) || name == "." || name == ".." {
			v.add(fmt.Sprintf("config.repos[%d]", i), "%q isn't owner/name or owner/*. Use an entry like arjun/deedglass-sandbox.", clip(r, 120))
			continue
		}
		k := strings.ToLower(r)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	cfg.Repos = out
}

// decodeObject reads a JSON object into its raw members; ok is false for anything else.
func decodeObject(b []byte) (map[string]json.RawMessage, bool) {
	if t := bytes.TrimSpace(b); len(t) == 0 || t[0] != '{' {
		return nil, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	return m, true
}

func unknownKeys(v *validator, prefix string, m map[string]json.RawMessage, known []string) {
	var keys []string
	for k := range m {
		if !slices.Contains(known, k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		v.add(prefix+"."+clip(k, 64), "%s has no key %q. The keys are %s.", prefix, clip(k, 64), strings.Join(known, ", "))
	}
}

func strictDecode(b json.RawMessage, dst any) error {
	if len(b) == 0 {
		return fmt.Errorf("missing")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// clip shortens s to n bytes on a rune boundary, with "…".
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// repoRule is one entry of repos.
type repoRule struct {
	owner, name string // lowercased; name "*" is every repository of owner
}

func parseRules(repos []string) []repoRule {
	out := make([]repoRule, 0, len(repos))
	for _, r := range repos {
		o, n, _ := strings.Cut(strings.ToLower(r), "/")
		out = append(out, repoRule{owner: o, name: n})
	}
	return out
}

// allowed reports whether repo ("owner/name") is in the rules. GitHub compares names without case.
func allowed(rules []repoRule, repo string) bool {
	o, n, ok := strings.Cut(strings.ToLower(repo), "/")
	if !ok {
		return false
	}
	for _, r := range rules {
		if r.owner == o && (r.name == "*" || r.name == n) {
			return true
		}
	}
	return false
}

// wildcard reports whether any rule is "owner/*" (an App token then can't be downscoped by name).
func wildcard(rules []repoRule) bool {
	for _, r := range rules {
		if r.name == "*" {
			return true
		}
	}
	return false
}

// protected reports whether branch matches one of the protected globs.
func protected(globs []string, branch string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, branch); ok {
			return true
		}
	}
	return false
}
