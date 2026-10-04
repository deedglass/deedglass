// Package slack is the Slack connector (S29 §3.3): four tools over a bot token (list_channels,
// read_channel, post_message, add_reaction), channel allow-lists compared by id, L1 read-backs
// through message metadata that carries the action id, request.slack enrich facts, and the
// approvals API the Slack notifier and the interaction handler call (VerifyInteraction,
// PostApproval, UpdateApproval, LookupUser, OpenView, Respond). Only this package touches the bot
// token and the signing secret, always through Instance.Secret, and every request leaves through
// the instance's egress client.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
)

// Type is the connector type.
const Type = "slack"

// DocsURL is the connector's documentation page.
const DocsURL = "https://docs.deedglass.com/connectors/slack"

// Cache lifetimes (S29 §3.3, §3.4 step 7).
const (
	ChannelCacheTTL = 5 * time.Minute
	UserCacheTTL    = 10 * time.Minute
	// APITimeout bounds every approvals API call (the caller's ctx may be shorter).
	APITimeout = 5 * time.Second
)

// now is the clock (tests move it).
var now = time.Now

// Connector is the Slack connector type. It holds no state: each Instance carries its parsed
// config, its caches and its egress client.
type Connector struct{}

// New returns the Slack connector type, for registration (connector.Options.Types).
func New() *Connector { return &Connector{} }

var (
	_ connector.Connector        = (*Connector)(nil)
	_ connector.Enricher         = (*Connector)(nil)
	_ connector.NativeReadBacker = (*Connector)(nil)
	_ connector.Summarizer       = (*Connector)(nil)
)

// Type implements connector.Connector.
func (c *Connector) Type() string { return Type }

// configSchema summarizes the config for forms (S26); Validate is the authority.
const configSchema = `{"type":"object","required":["team_id","app_id"],"additionalProperties":false,"properties":{
"team_id":{"type":"string","pattern":"^T[A-Z0-9]{2,20}$","description":"The workspace id, checked against auth.test."},
"app_id":{"type":"string","pattern":"^A[A-Z0-9]{2,20}$","description":"From the app's Basic Information page, checked through bots.info."},
"channels":{"type":"array","items":{"type":"string"},"description":"Channels the bot may post in: #name or a C… or G… id; [\"*\"] is any channel the bot is in."},
"read_channels":{"type":"array","items":{"type":"string"},"description":"Channels the bot may read, in the same form."},
"max_read_messages":{"type":"integer","minimum":1,"maximum":100},
"approvals":{"type":"object","additionalProperties":false,"properties":{"enabled":{"type":"boolean"},"channel":{"type":"string"},"require_email_domains":{"type":"array","items":{"type":"string"}}}},
"api_base_url":{"type":"string","description":"https://slack.com/api (any http(s) URL in dev only)"}}}`

// Info implements connector.Connector.
func (c *Connector) Info() connector.TypeInfo {
	return connector.TypeInfo{Object: "connector_type", Type: Type, Title: "Slack",
		Description: "Post and react in allowed channels as a bot, read recent messages, and approve actions from Slack. Posts are read back through message metadata that carries the action id.",
		Levels:      []string{"L0", "L1"},
		CredentialKinds: []connector.CredentialKind{{Kind: "fields", Fields: []connector.CredentialField{
			{Name: "bot_token", Secret: true, Help: "The Bot User OAuth Token from the app's OAuth & Permissions page.", PrefixHint: "xoxb-"},
			{Name: "signing_secret", Secret: true, Help: "From the app's Basic Information page: 32 hex characters."}}}},
		ConfigSchema: json.RawMessage(configSchema), DocsURL: DocsURL,
		Summary: "Posts, reactions and approvals in the Slack channels you allow.",
		Status:  "beta", Icon: "message-square", Modes: []string{},
		Setup: []connector.SetupStep{
			{Title: "Create the Slack app from the manifest", Command: "deedglass slack manifest --base-url https://your-host", URL: "https://api.slack.com/apps"},
			{Title: "Create the connector", Command: "deedglass connector create --config examples/connectors/slack.yaml"},
			{Title: "Store the bot token and signing secret", Command: "deedglass connector credentials set slack --field bot_token=env:SLACK_BOT_TOKEN --field signing_secret=env:SLACK_SIGNING_SECRET"},
			{Title: "Test the connection", Command: "deedglass connector test slack"},
		},
		Limits: []string{
			"Only channels in the config, compared by id.",
			"No direct messages to people, file uploads or message deletes.",
			"Approvals map a Slack account to a person only by a confirmed email address.",
		},
		Tools: connector.CatalogTools(Type, toolSpecs(DefaultMaxReadMessages))}
}

// state is an instance's parsed config, its caches and its client.
type state struct {
	cfg  Config
	base *url.URL

	// chMu serializes channel-list refreshes (one fetch per instance at a time).
	chMu  sync.Mutex
	chans *channelList

	mu    sync.Mutex
	infos map[string]infoEntry // conversations.info by channel id
	self  *selfInfo            // auth.test, cached
	users map[string]userEntry // users.info by user id
}

type infoEntry struct {
	at time.Time
	ch Channel
}

type userEntry struct {
	at time.Time
	u  User
}

// selfInfo is what auth.test said about the bot.
type selfInfo struct {
	at       time.Time
	TeamID   string
	Team     string
	UserID   string
	UserName string
	BotID    string
	Scopes   []string
}

func parse(inst *connector.Instance) (*state, error) {
	v, err := inst.Parsed(func() (any, error) {
		var cfg Config
		if err := json.Unmarshal(inst.Config, &cfg); err != nil {
			return nil, fmt.Errorf("slack: the stored config can't be read: %w", err)
		}
		if cfg.APIBaseURL == "" {
			cfg.APIBaseURL = DefaultAPIBaseURL
		}
		if cfg.MaxReadMessages <= 0 {
			cfg.MaxReadMessages = DefaultMaxReadMessages
		}
		u, err := url.Parse(cfg.APIBaseURL)
		if err != nil {
			return nil, fmt.Errorf("slack: api_base_url: %w", err)
		}
		return &state{cfg: cfg, base: u, infos: map[string]infoEntry{}, users: map[string]userEntry{}}, nil
	})
	if err != nil {
		return nil, &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured",
			AgentMessage: "This Slack connector's config can't be read. An admin can check Console › Connectors.", Err: err}
	}
	return v.(*state), nil
}

// client is the instance's egress client: no redirects, a 4 MiB body cap, 10 s per request.
func client(inst *connector.Instance) *http.Client {
	return inst.Client(func() *http.Client {
		pol := inst.Env.Egress
		if pol == nil {
			pol = &egress.Policy{Env: egress.EnvProd}
		}
		return pol.Client(egress.ClientOptions{Timeout: 10 * time.Second, MaxResponseBytes: maxBody, Redirects: egress.RedirectNone})
	})
}

const maxBody = 4 << 20

// Tools implements connector.Connector.
func (c *Connector) Tools(_ context.Context, inst *connector.Instance) ([]connector.ToolSpec, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	return toolSpecs(st.cfg.MaxReadMessages), nil
}
