package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/vault/keyring"
)

// Env is the deployment mode.
type Env string

const (
	Dev  Env = "dev"
	Prod Env = "prod"
)

// Source says where a value came from, in precedence order.
type Source string

const (
	SourceEnv        Source = "env"
	SourceDotEnv     Source = ".env"
	SourceYAML       Source = "deedglass.yaml"
	SourceDevSecrets Source = ".deedglass/dev-secrets.json"
	SourceDefault    Source = "default"
	SourceNotSet     Source = "not set"
)

// Config is the effective configuration: one typed struct for every command.
type Config struct {
	Env                  Env
	DatabaseURL          Secret // printed with only the password hidden
	MigrateDatabaseURL   Secret // the migrator role's URL (S02); only deedglass migrate reads it
	Listen               string
	MetricsListen        string // "" = no metrics listener (S03)
	BaseURL              *url.URL
	SealKeyFile          string
	VaultKey             Secret // a ring of 1–4 comma-separated base64 32-byte keys; the first is active (S16)
	APIKeyPepper         Secret // base64 of at least 32 bytes
	LogLevel             slog.Level
	Signup               string
	ResendAPIKey         Secret
	MailFrom             string // "Deedglass <signin@deedglass.com>" (S07)
	AnthropicAPIKey      Secret
	TrustedProxies       []netip.Prefix
	AllowPrivateNetworks bool
	EgressAllowCIDRs     []netip.Prefix // DEEDGLASS_EGRESS_ALLOW_CIDRS: private networks connectors may reach (S16)
	SlotLagAlertMB       int

	// Client-side: the CLI, MCP commands and SDKs.
	APIKey Secret
	Server *url.URL

	// File-only settings (deedglass.yaml sections; settings.go).
	Gate    GateSettings
	MCP     MCPSettings     // mcp.* (S15)
	Witness WitnessSettings // witness.* (S19)

	raw        map[string]string // resolved raw values, for printing
	sources    map[string]Source
	devSecrets devSecretsState
}

type devSecretsState struct {
	generated bool
	used      int
}

// Need is what a command needs from the config. Commands declare it; Require checks it.
type Need uint

const (
	NeedDatabase Need = 1 << iota
	NeedSealKey
	NeedVault
	NeedPepper
	NeedPublicBaseURL
)

// Problem is one reason the config can't be used, with the next step.
type Problem struct {
	Key     string
	Message string // what is wrong, ending with a period
	Next    string // what to do
}

func (p Problem) String() string { return strings.TrimSpace(p.Message + " " + p.Next) }

// Problems is every problem found; Load returns all of them at once.
type Problems struct {
	Env  Env
	List []Problem
}

func (ps *Problems) Error() string {
	if len(ps.List) == 1 {
		return ps.List[0].Key + " " + ps.List[0].String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d problems in the %s config", len(ps.List), ps.Env)
	for _, p := range ps.List {
		fmt.Fprintf(&b, "\n  %s %s", p.Key, p)
	}
	return b.String()
}

// Warning is a notice that doesn't stop the command.
type Warning struct {
	Key     string
	Message string
}

func (w Warning) String() string { return w.Key + " " + w.Message }

// LoadOptions injects everything Load reads, so it can be tested without the process state.
type LoadOptions struct {
	Env        map[string]string // the process environment
	FS         fs.FS             // the working directory: .env and deedglass.yaml
	Dir        string            // the same directory on disk, for .deedglass/; "" disables dev secrets
	ConfigFile string            // default "deedglass.yaml"
	Rand       io.Reader         // default crypto/rand
}

// OSOptions reads the real process environment and working directory.
func OSOptions() LoadOptions {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return LoadOptions{Env: env, FS: os.DirFS("."), Dir: "."}
}

// Load resolves the config: process environment, then .env (dev only), then deedglass.yaml, then
// .deedglass/dev-secrets.json (dev only), then defaults. It returns every problem at once, as
// *Problems; the Config is nil when there are problems.
func Load(o LoadOptions) (*Config, []Warning, error) {
	l := &loader{o: o, raw: map[string]string{}, src: map[string]Source{}}
	if l.o.ConfigFile == "" {
		l.o.ConfigFile = "deedglass.yaml"
	}
	if l.o.Rand == nil {
		l.o.Rand = rand.Reader
	}
	if l.o.Env == nil {
		l.o.Env = map[string]string{}
	}
	return l.load()
}

type loader struct {
	o        LoadOptions
	env      Env
	dotenv   map[string]string
	dotLines map[string]int
	yaml     map[string]yamlValue
	raw      map[string]string
	src      map[string]Source
	problems []Problem
	warnings []Warning
	mcp      *MCPSettings
}

func (l *loader) problem(key, msg, next string) {
	l.problems = append(l.problems, Problem{Key: key, Message: msg, Next: next})
}

func (l *loader) warn(key, msg string) {
	l.warnings = append(l.warnings, Warning{Key: key, Message: msg})
}

func (l *loader) load() (*Config, []Warning, error) {
	l.readDotEnv()
	l.readYAML()
	l.resolveEnv()
	l.checkNames(l.o.Env, SourceEnv)
	if l.dotenv != nil {
		if l.env == Prod {
			l.warn(".env", "is ignored in prod. Set settings in the process environment.")
		} else {
			l.checkNames(l.dotenv, SourceDotEnv)
		}
	}
	for _, k := range Keys {
		l.resolve(k)
	}
	c := &Config{Env: l.env, raw: l.raw, sources: l.src}
	for _, k := range Keys {
		l.parse(c, k)
	}
	l.parseSettings(c)
	if l.mcp != nil {
		c.MCP = *l.mcp
		for _, u := range c.MCP.Upstreams {
			if u.URL.Scheme == "http" && l.env == Prod {
				l.problem("mcp.upstreams."+u.Slug+".url", "must use https in prod.", "Serve the MCP server over TLS, or run Deedglass in dev.")
			}
			if len(u.HeadersFromEnv) > 0 && l.env == Prod {
				// S18 (S16 step 14): upstream credentials live in the vault in prod.
				l.problem("mcp.upstreams."+u.Slug+".headers_from_env", "is dev only; prod keeps upstream credentials in the vault.",
					"Create an mcp_http connector for this server and store its credential with deedglass connector credentials set.")
			}
		}
	}
	l.prodChecks(c)
	if c.ResendAPIKey.IsSet() && c.MailFrom == "" {
		if _, set := l.raw["DEEDGLASS_MAIL_FROM"]; !set {
			l.problem("DEEDGLASS_MAIL_FROM", "isn't set, and DEEDGLASS_RESEND_API_KEY is.",
				"Set it to the address emails come from, for example Deedglass <signin@example.com>.")
		}
	}
	if len(l.problems) == 0 && l.env == Dev {
		l.devSecrets(c)
	}
	sortWarnings(l.warnings)
	if len(l.problems) > 0 {
		return nil, l.warnings, &Problems{Env: l.env, List: l.problems}
	}
	return c, l.warnings, nil
}

// resolveEnv decides dev or prod first, because it changes which sources count.
func (l *loader) resolveEnv() {
	const name = "DEEDGLASS_ENV"
	v, src := l.o.Env[name], SourceEnv
	if v == "" {
		if y, ok := l.yaml["env"]; ok {
			v, src = y.s, SourceYAML
		}
	}
	if v == "" && l.dotenv != nil {
		v, src = l.dotenv[name], SourceDotEnv
	}
	switch v {
	case "", "dev":
		l.env = Dev
	case "prod":
		l.env = Prod
		if src == SourceDotEnv {
			l.problem(name, "is prod in .env, and prod ignores .env.", "Set DEEDGLASS_ENV=prod in the process environment.")
		}
	default:
		l.env = Dev
		l.problem(name, fmt.Sprintf("must be dev or prod (got %q).", v), "Set it to dev or prod.")
	}
}

// checkNames reports unknown DEEDGLASS_* names and any TXV_* name.
func (l *loader) checkNames(m map[string]string, src Source) {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		up := strings.ToUpper(n)
		if strings.HasPrefix(up, "TXV_") {
			l.problem(n, "is from the old name.", "Rename it to "+renamed(up)+".")
			continue
		}
		if !strings.HasPrefix(n, envPrefix) || keyByName[n] != nil || isTooling(n) {
			continue
		}
		where := ""
		if src == SourceDotEnv {
			where = " (in .env)"
		}
		msg := "isn't a Deedglass setting" + where + "."
		next := "See docs/CONFIG.md for the list."
		if s := suggest(n, envNames()); s != "" {
			next = "Did you mean " + s + "?"
		}
		if l.env == Prod {
			l.problem(n, msg, next)
		} else {
			l.warn(n, msg+" "+next)
		}
	}
}

func renamed(txv string) string {
	if n, ok := retiredNames[txv]; ok {
		return n
	}
	return envPrefix + strings.TrimPrefix(txv, "TXV_")
}

func isTooling(n string) bool {
	if toolingNames[n] {
		return true
	}
	for _, p := range toolingPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

func envNames() []string {
	out := make([]string, len(Keys))
	for i, k := range Keys {
		out[i] = k.Name
	}
	return out
}

// resolve picks the raw value of one key by precedence.
func (l *loader) resolve(k Key) {
	if v, ok := l.o.Env[k.Name]; ok && v != "" {
		l.raw[k.Name], l.src[k.Name] = v, SourceEnv
		return
	}
	if l.env == Dev && l.dotenv != nil {
		if v, ok := l.dotenv[k.Name]; ok && v != "" {
			l.raw[k.Name], l.src[k.Name] = v, SourceDotEnv
			return
		}
	}
	if y, ok := l.yaml[k.YAMLName()]; ok && y.s != "" {
		l.raw[k.Name], l.src[k.Name] = y.s, SourceYAML
		if k.Secret && !(k.Kind == KindPostgresURL && !hasPassword(y.s)) {
			if l.env == Prod {
				l.problem(k.Name, "is a secret, and prod refuses secrets in deedglass.yaml.", "Set it in the process environment instead.")
			} else {
				l.warn(k.Name, "is a secret in deedglass.yaml. Prod refuses that; use the environment.")
			}
		}
		return
	}
	if k.Default != "" {
		l.raw[k.Name], l.src[k.Name] = k.Default, SourceDefault
		return
	}
	l.src[k.Name] = SourceNotSet
}

// parse validates one raw value into the typed struct.
func (l *loader) parse(c *Config, k Key) {
	v, set := l.raw[k.Name]
	if !set {
		return
	}
	bad := func(msg, next string) { l.problem(k.Name, msg, next) }
	switch k.Name {
	case "DEEDGLASS_ENV":
		// resolveEnv already checked it.
	case "DEEDGLASS_DATABASE_URL":
		if msg := checkPostgresURL(v); msg != "" {
			bad(msg, "Use the form postgres://user:password@host:5432/db?sslmode=require.")
			return
		}
		c.DatabaseURL = NewSecret(v)
	case "DEEDGLASS_MIGRATE_DATABASE_URL":
		if msg := checkPostgresURL(v); msg != "" {
			bad(msg, "Use the form postgres://deedglass_migrator:password@host:5432/deedglass?sslmode=require.")
			return
		}
		c.MigrateDatabaseURL = NewSecret(v)
	case "DEEDGLASS_LISTEN", "DEEDGLASS_METRICS_LISTEN":
		host, port, err := net.SplitHostPort(v)
		p, perr := strconv.Atoi(port)
		if err != nil || perr != nil || p < 0 || p > 65535 {
			bad(fmt.Sprintf("must be host:port (got %q).", v), "For example 127.0.0.1:8080, or :8080 for every interface.")
			return
		}
		_ = host
		if k.Name == "DEEDGLASS_LISTEN" {
			c.Listen = v
		} else {
			c.MetricsListen = v
		}
	case "DEEDGLASS_BASE_URL", "DEEDGLASS_SERVER":
		u, msg := checkHTTPURL(v)
		if msg != "" {
			bad(msg, "Use a full URL such as https://deedglass.example.com.")
			return
		}
		if k.Name == "DEEDGLASS_BASE_URL" {
			c.BaseURL = u
		} else {
			c.Server = u
		}
	case "DEEDGLASS_SEAL_KEY_FILE":
		c.SealKeyFile = v
	case "DEEDGLASS_VAULT_KEY":
		if _, err := keyring.Parse(v); err != nil {
			var pr *keyring.Problem
			if errors.As(err, &pr) {
				bad(pr.Message, pr.Next)
			} else {
				bad("can't be read.", "Generate a key with `openssl rand -base64 32`.")
			}
			return
		}
		c.VaultKey = NewSecret(v)
	case "DEEDGLASS_API_KEY_PEPPER":
		if n, ok := b64Len(v); !ok || n < 32 {
			bad("must be base64 of at least 32 bytes"+gotBytes(n, ok)+".", "Generate one with `openssl rand -base64 32`.")
			return
		}
		c.APIKeyPepper = NewSecret(v)
	case "DEEDGLASS_LOG_LEVEL":
		if !in(k.Enum, v) {
			bad(fmt.Sprintf("must be one of %s (got %q).", strings.Join(k.Enum, ", "), v), "")
			return
		}
		_ = c.LogLevel.UnmarshalText([]byte(v))
	case "DEEDGLASS_SIGNUP":
		if !in(k.Enum, v) {
			bad(fmt.Sprintf("must be one of %s (got %q).", strings.Join(k.Enum, ", "), v), "")
			return
		}
		c.Signup = v
	case "DEEDGLASS_RESEND_API_KEY":
		if !strings.HasPrefix(v, "re_") {
			bad("doesn't look like a Resend API key (they start with re_).", "Copy it again from resend.com › API Keys.")
			return
		}
		c.ResendAPIKey = NewSecret(v)
	case "DEEDGLASS_MAIL_FROM":
		a, err := mail.ParseAddress(v)
		if err != nil || strings.ContainsAny(v, "\r\n") {
			bad(fmt.Sprintf("isn't an email address (got %q).", v), "Use the form Deedglass <signin@example.com>.")
			return
		}
		_ = a
		c.MailFrom = v
	case "DEEDGLASS_ANTHROPIC_API_KEY":
		if !strings.HasPrefix(v, "sk-ant-") {
			bad("doesn't look like an Anthropic API key (they start with sk-ant-).", "Copy it again from console.anthropic.com › API Keys.")
			return
		}
		c.AnthropicAPIKey = NewSecret(v)
	case "DEEDGLASS_TRUSTED_PROXIES":
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			p, err := netip.ParsePrefix(part)
			if err != nil {
				next := "Write each one as a CIDR, for example 10.0.0.0/8."
				if a, aerr := netip.ParseAddr(part); aerr == nil {
					next = fmt.Sprintf("Write a single address as %s/%d.", a, a.BitLen())
				}
				bad(fmt.Sprintf("has an entry that isn't a CIDR (%q).", part), next)
				return
			}
			c.TrustedProxies = append(c.TrustedProxies, p.Masked())
		}
	case "DEEDGLASS_ALLOW_PRIVATE_NETWORKS":
		switch strings.ToLower(v) {
		case "true", "1":
			c.AllowPrivateNetworks = true
		case "false", "0":
		default:
			bad(fmt.Sprintf("must be true or false (got %q).", v), "")
		}
	case "DEEDGLASS_EGRESS_ALLOW_CIDRS":
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			p, err := netip.ParsePrefix(part)
			if err != nil {
				next := "Write each one as a CIDR, for example 10.20.0.0/16."
				if a, aerr := netip.ParseAddr(part); aerr == nil {
					next = fmt.Sprintf("Write a single address as %s/%d.", a, a.BitLen())
				}
				bad(fmt.Sprintf("has an entry that isn't a CIDR (%q).", part), next)
				return
			}
			if err := egress.CheckAllowCIDR(p); err != nil {
				bad(fmt.Sprintf("has an entry Deedglass never allows: %s.", err), "List only the private networks connectors need, for example 10.20.0.0/16.")
				return
			}
			c.EgressAllowCIDRs = append(c.EgressAllowCIDRs, p.Masked())
		}
	case "DEEDGLASS_SLOT_LAG_ALERT_MB":
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1048576 {
			bad(fmt.Sprintf("must be a whole number from 1 to 1048576 (got %q).", v), "The default is 512.")
			return
		}
		c.SlotLagAlertMB = n
	case "DEEDGLASS_API_KEY":
		if !validAPIKey(v) {
			bad("doesn't look like a Deedglass API key (dg_live_… or dg_test_…, 49 characters).", "Create one in Console › Agents, or with `deedglass agent keys create`.")
			return
		}
		c.APIKey = NewSecret(v)
	case "DEEDGLASS_NO_BANNER", "DEEDGLASS_NO_ANIM":
		// Read by the CLI's terminal mark (internal/cli/mark) straight from the environment;
		// checked here so config print and doctor flag a typo.
		switch strings.ToLower(v) {
		case "true", "1", "false", "0":
		default:
			bad(fmt.Sprintf("must be true or false (got %q).", v), "")
		}
	case "DEEDGLASS_THEME":
		if v != "auto" && v != "light" && v != "dark" {
			bad(fmt.Sprintf("must be auto, light or dark (got %q).", v), "Unset it to detect the terminal's background.")
		}
	default:
		panic("config: key " + k.Name + " has no parser")
	}
}

func (l *loader) prodChecks(c *Config) {
	if l.env != Prod {
		return
	}
	if c.BaseURL != nil && c.BaseURL.Scheme != "https" {
		l.problem("DEEDGLASS_BASE_URL", fmt.Sprintf("must use https in prod (got %s).", c.BaseURL), "Set it to this server's public https URL.")
	}
	if _, set := l.raw["DEEDGLASS_VAULT_KEY"]; !set {
		l.problem("DEEDGLASS_VAULT_KEY", "isn't set.", "Generate one with `openssl rand -base64 32` and set it in the environment.")
	}
	if _, set := l.raw["DEEDGLASS_API_KEY_PEPPER"]; !set {
		l.problem("DEEDGLASS_API_KEY_PEPPER", "isn't set.", "Generate one the same way, and keep it stable: changing it invalidates every API key.")
	}
	if c.AllowPrivateNetworks {
		l.problem("DEEDGLASS_ALLOW_PRIVATE_NETWORKS", "is dev only.", "Remove it. In prod, list the private networks connectors may reach in DEEDGLASS_EGRESS_ALLOW_CIDRS.")
	}
	for _, k := range []struct {
		name string
		s    Secret
	}{{"DEEDGLASS_DATABASE_URL", c.DatabaseURL}, {"DEEDGLASS_MIGRATE_DATABASE_URL", c.MigrateDatabaseURL}} {
		if v := k.s.Reveal(); v != "" {
			if msg := prodSSL(v); msg != "" {
				l.problem(k.name, msg, "Add sslmode=verify-full (or require) to the URL.")
			}
		}
	}
}

// Require checks what a command needs, and returns every missing item as *Problems.
func (c *Config) Require(n Need) error {
	var ps []Problem
	if n&NeedDatabase != 0 && !c.DatabaseURL.IsSet() {
		ps = append(ps, Problem{"DEEDGLASS_DATABASE_URL", "isn't set.", "Set it to the control-plane Postgres URL, for example in .env for dev."})
	}
	if n&NeedSealKey != 0 && c.SealKeyFile == "" {
		ps = append(ps, Problem{"DEEDGLASS_SEAL_KEY_FILE", "isn't set.", "Point it at the seal key file; `deedglass keys gen` creates one."})
	}
	if n&NeedVault != 0 && !c.VaultKey.IsSet() {
		ps = append(ps, Problem{"DEEDGLASS_VAULT_KEY", "isn't set.", "Generate one with `openssl rand -base64 32` and set it in the environment."})
	}
	if n&NeedPepper != 0 && !c.APIKeyPepper.IsSet() {
		ps = append(ps, Problem{"DEEDGLASS_API_KEY_PEPPER", "isn't set.", "Generate one with `openssl rand -base64 32` and set it in the environment."})
	}
	if n&NeedPublicBaseURL != 0 && c.Env == Prod && c.Source("DEEDGLASS_BASE_URL") == SourceDefault {
		ps = append(ps, Problem{"DEEDGLASS_BASE_URL", "isn't set.", "Set it to the https URL people use to reach this server."})
	}
	if len(ps) == 0 {
		return nil
	}
	return &Problems{Env: c.Env, List: ps}
}

// MigrateURL picks the URL deedglass migrate connects with: the --database-url flag, then
// DEEDGLASS_MIGRATE_DATABASE_URL, then (dev only) DEEDGLASS_DATABASE_URL. fallback reports the dev
// fallback, so the caller can print its notice. Prod never falls back to the runtime role's URL.
func (c *Config) MigrateURL(flag Secret) (url Secret, fallback bool, err error) {
	if flag.IsSet() {
		if msg := checkPostgresURL(flag.Reveal()); msg != "" {
			return Secret{}, false, &Problems{Env: c.Env, List: []Problem{{"--database-url", msg,
				"Use the form postgres://deedglass_migrator:password@host:5432/deedglass."}}}
		}
		return flag, false, nil
	}
	if c.MigrateDatabaseURL.IsSet() {
		return c.MigrateDatabaseURL, false, nil
	}
	if c.Env == Dev && c.DatabaseURL.IsSet() {
		return c.DatabaseURL, true, nil
	}
	next := "Set it to the migrator role's URL, or pass --database-url."
	if c.Env == Dev {
		next = "Set it (or DEEDGLASS_DATABASE_URL in dev) to the control-plane Postgres URL, or pass --database-url."
	}
	return Secret{}, false, &Problems{Env: c.Env, List: []Problem{{"DEEDGLASS_MIGRATE_DATABASE_URL", "isn't set.", next}}}
}

// Source returns where a key's value came from.
func (c *Config) Source(name string) Source {
	if s, ok := c.sources[name]; ok {
		return s
	}
	return SourceNotSet
}

// DevSecrets reports how many values came from .deedglass/dev-secrets.json, and whether the file
// was created by this Load.
func (c *Config) DevSecrets() (used int, generated bool) {
	return c.devSecrets.used, c.devSecrets.generated
}

func in(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// b64Len decodes standard or URL base64, padded or not, and returns the byte length.
func b64Len(s string) (int, bool) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.Strict().DecodeString(s); err == nil {
			return len(b), true
		}
	}
	return 0, false
}

func gotBytes(n int, ok bool) string {
	if !ok {
		return " (it isn't valid base64)"
	}
	return fmt.Sprintf(" (got %d)", n)
}

func validAPIKey(v string) bool {
	if len(v) != 49 || !(strings.HasPrefix(v, "dg_live_") || strings.HasPrefix(v, "dg_test_")) {
		return false
	}
	rest := v[8:] // <8 base62>_<32 base62>
	if rest[8] != '_' {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if i == 8 {
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func checkHTTPURL(v string) (*url.URL, string) {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Sprintf("must be an http or https URL (got %q).", v)
	}
	if u.User != nil {
		return nil, "must not contain a user name or password."
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Sprintf("must not have a query or fragment (got %q).", v)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, ""
}

func sortWarnings(ws []Warning) {
	sort.SliceStable(ws, func(i, j int) bool { return ws[i].Key < ws[j].Key })
}

// Pepper returns DEEDGLASS_API_KEY_PEPPER decoded: the bytes mixed into API key hashes, sign-in
// code hashes and sign-in rate-limit keys. It errors when the pepper isn't set.
func (c *Config) Pepper() ([]byte, error) {
	v := c.APIKeyPepper.Reveal()
	if v == "" {
		return nil, fmt.Errorf("DEEDGLASS_API_KEY_PEPPER isn't set")
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.Strict().DecodeString(v); err == nil && len(b) >= 32 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("DEEDGLASS_API_KEY_PEPPER must be base64 of at least 32 bytes")
}
