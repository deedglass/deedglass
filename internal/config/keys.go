package config

// The key table. Every DEEDGLASS_* setting is declared here once; config print, unknown-name
// detection, did-you-mean and the table in docs/CONFIG.md all derive from it (TestDocsTable fails
// when the docs drift). A new setting is one row here, in the session that needs it.

// Kind is how a key's value is parsed.
type Kind int

const (
	KindString Kind = iota
	KindSecret
	KindEnum
	KindInt
	KindBool
	KindURL
	KindCIDRList
	KindPath
	KindListen
	KindPostgresURL
)

// Key describes one setting.
type Key struct {
	Name     string   // DEEDGLASS_DATABASE_URL
	Kind     Kind     // how the value is parsed
	Default  string   // "" means no default
	Secret   bool     // redacted everywhere; refused in deedglass.yaml in prod
	Client   bool     // read by the CLI, MCP commands and SDKs; `serve` ignores it
	DevOnly  bool     // refused in prod when set
	Enum     []string // allowed values for KindEnum
	Commands string   // who needs it, for the docs
	Doc      string   // one sentence for the docs
	Owner    string   // the session that added it
}

// YAMLName is the key's name in deedglass.yaml: the env name without the prefix, lowercased.
func (k Key) YAMLName() string { return yamlName(k.Name) }

const envPrefix = "DEEDGLASS_"

// Keys lists every server and client setting, in display order.
var Keys = []Key{
	{Name: "DEEDGLASS_ENV", Kind: KindEnum, Default: "dev", Enum: []string{"dev", "prod"},
		Commands: "all", Doc: "dev or prod. Prod ignores .env and dev secrets and refuses unsafe settings.", Owner: "S01"},
	{Name: "DEEDGLASS_DATABASE_URL", Kind: KindPostgresURL, Secret: true,
		Commands: "serve, migrate, org, dev", Doc: "The control-plane Postgres URL for the runtime role. Printed with only the password hidden. Prod needs sslmode=require or verify-full unless the host is loopback or a Unix socket.", Owner: "S01"},
	{Name: "DEEDGLASS_MIGRATE_DATABASE_URL", Kind: KindPostgresURL, Secret: true,
		Commands: "migrate", Doc: "The control-plane Postgres URL for the migrator role (deedglass_migrator), used only by deedglass migrate. Dev falls back to DEEDGLASS_DATABASE_URL; prod requires it or --database-url.", Owner: "S02"},
	{Name: "DEEDGLASS_LISTEN", Kind: KindListen, Default: "127.0.0.1:8080",
		Commands: "serve", Doc: "Address the API, the Gate and the Console listen on (host:port).", Owner: "S01"},
	{Name: "DEEDGLASS_BASE_URL", Kind: KindURL, Default: "http://127.0.0.1:8080",
		Commands: "serve, org login-link", Doc: "The URL people and agents use to reach this server. Prod needs https.", Owner: "S01"},
	{Name: "DEEDGLASS_SEAL_KEY_FILE", Kind: KindPath,
		Commands: "serve, keys", Doc: "Path to the seal key file used to seal deeds.", Owner: "S01"},
	{Name: "DEEDGLASS_VAULT_KEY", Kind: KindSecret, Secret: true,
		Commands: "serve, vault, connector credentials", Doc: "Base64 of exactly 32 bytes; encrypts connector credentials. Dev generates one in .deedglass/.", Owner: "S01"},
	{Name: "DEEDGLASS_API_KEY_PEPPER", Kind: KindSecret, Secret: true,
		Commands: "serve, org keys, agent keys", Doc: "Base64 of at least 32 bytes, mixed into API key hashes. Keep it stable: changing it invalidates every API key. Dev generates one in .deedglass/.", Owner: "S01"},
	{Name: "DEEDGLASS_LOG_LEVEL", Kind: KindEnum, Default: "info", Enum: []string{"debug", "info", "warn", "error"},
		Commands: "all", Doc: "Log level.", Owner: "S01"},
	{Name: "DEEDGLASS_SIGNUP", Kind: KindEnum, Default: "invite", Enum: []string{"invite", "closed"},
		Commands: "serve", Doc: "Who can join: invite (people an admin invites) or closed.", Owner: "S01"},
	{Name: "DEEDGLASS_RESEND_API_KEY", Kind: KindSecret, Secret: true,
		Commands: "serve", Doc: "Resend API key for sign-in and approval emails (starts with re_). Without it, dev prints emails to the log.", Owner: "S01"},
	{Name: "DEEDGLASS_ANTHROPIC_API_KEY", Kind: KindSecret, Secret: true,
		Commands: "serve, insights", Doc: "Optional Anthropic API key for Insights phrasing (starts with sk-ant-).", Owner: "S01"},
	{Name: "DEEDGLASS_TRUSTED_PROXIES", Kind: KindCIDRList,
		Commands: "serve", Doc: "Comma-separated CIDRs whose X-Forwarded-For is believed.", Owner: "S01"},
	{Name: "DEEDGLASS_ALLOW_PRIVATE_NETWORKS", Kind: KindBool, Default: "false", DevOnly: true,
		Commands: "serve", Doc: "Dev only: lets connectors reach private networks. Refused in prod.", Owner: "S01"},
	{Name: "DEEDGLASS_SLOT_LAG_ALERT_MB", Kind: KindInt, Default: "512",
		Commands: "serve", Doc: "Alert when a Postgres replication slot holds more WAL than this (1-1048576 MB).", Owner: "S01"},

	// Client-side keys: the CLI, MCP commands and SDKs read them; serve ignores them.
	{Name: "DEEDGLASS_API_KEY", Kind: KindSecret, Secret: true, Client: true,
		Commands: "CLI, mcp, SDKs", Doc: "An agent or admin API key (dg_live_… or dg_test_…) for client commands.", Owner: "S01"},
	{Name: "DEEDGLASS_SERVER", Kind: KindURL, Client: true,
		Commands: "CLI, mcp, SDKs", Doc: "The Deedglass server client commands talk to.", Owner: "S01"},
}

// toolingPrefixes are DEEDGLASS_ names that scripts use and the binary ignores.
var toolingPrefixes = []string{"DEEDGLASS_IT_", "DEEDGLASS_DEV_"}

var toolingNames = map[string]bool{"DEEDGLASS_REPO_PUBLIC": true}

// retiredNames maps V0 tooling names whose new name isn't just the prefix swap.
var retiredNames = map[string]string{
	"TXV_PG":          "DEEDGLASS_IT_PG",
	"TXV_PG_PASSWORD": "DEEDGLASS_DEV_PG_PASSWORD",
}

var keyByName = func() map[string]*Key {
	m := make(map[string]*Key, len(Keys))
	for i := range Keys {
		m[Keys[i].Name] = &Keys[i]
	}
	return m
}()

// Lookup returns the key with this env name.
func Lookup(name string) (Key, bool) {
	k, ok := keyByName[name]
	if !ok {
		return Key{}, false
	}
	return *k, true
}
