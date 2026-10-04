package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
)

// Defaults and limits of the config.
const (
	DefaultAPIBaseURL    = "https://gmail.googleapis.com"
	DefaultTokenURL      = "https://oauth2.googleapis.com/token"
	DefaultMaxRecipients = 20
	MaxRecipientsLimit   = 100
	MaxConfigBytes       = 16 << 10
)

// Scopes Deedglass asks for.
const (
	ScopeCompose  = "https://www.googleapis.com/auth/gmail.compose"
	ScopeReadonly = "https://www.googleapis.com/auth/gmail.readonly"
)

// Config is the normalized config. Email, Scopes and AuthorizedAt are written by
// deedglass connector gmail authorize: they are facts about the grant, never secrets.
type Config struct {
	Auth            string    `json:"auth"`    // oauth | service_account
	Mailbox         string    `json:"mailbox"` // "me", or the user a service account acts as
	InternalDomains []string  `json:"internal_domains"`
	MaxRecipients   int       `json:"max_recipients"`
	AllowBCC        bool      `json:"allow_bcc"`
	Email           string    `json:"email,omitempty"`
	Scopes          []string  `json:"scopes,omitempty"`
	AuthorizedAt    time.Time `json:"authorized_at,omitzero"`
	APIBaseURL      string    `json:"api_base_url"`
	TokenURL        string    `json:"token_url"`
}

func (c *Config) defaults() {
	if c.Auth == "" {
		c.Auth = "oauth"
	}
	if c.Mailbox == "" {
		c.Mailbox = "me"
	}
	if c.MaxRecipients == 0 {
		c.MaxRecipients = DefaultMaxRecipients
	}
	if c.InternalDomains == nil {
		c.InternalDomains = []string{}
	}
	if c.APIBaseURL == "" {
		c.APIBaseURL = DefaultAPIBaseURL
	}
	if c.TokenURL == "" {
		c.TokenURL = DefaultTokenURL
	}
}

// readonly reports whether the grant includes gmail.readonly (get_thread).
func (c Config) readonly() bool {
	return slices.Contains(c.Scopes, ScopeReadonly) || slices.Contains(c.Scopes, "gmail.readonly")
}

var reDomain = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

const configSchema = `{"type":"object","additionalProperties":false,"properties":{
"auth":{"type":"string","enum":["oauth","service_account"],"description":"oauth (deedglass connector gmail authorize) or service_account (Google Workspace, domain-wide delegation)."},
"mailbox":{"type":"string","description":"me, or with a service account the user to act as, like ops@acme.com."},
"internal_domains":{"type":"array","items":{"type":"string"},"description":"Domains that count as inside your org, like acme.com."},
"max_recipients":{"type":"integer","minimum":1,"maximum":100},
"allow_bcc":{"type":"boolean","description":"Default false: drafts with Bcc are refused."},
"email":{"type":"string","description":"Set by authorize."},
"scopes":{"type":"array","items":{"type":"string"},"description":"Set by authorize."},
"authorized_at":{"type":"string","description":"Set by authorize."},
"api_base_url":{"type":"string","description":"https://gmail.googleapis.com (any http(s) URL in dev only)"},
"token_url":{"type":"string","description":"https://oauth2.googleapis.com/token (any http(s) URL in dev only)"}}}`

var knownKeys = []string{"auth", "mailbox", "internal_domains", "max_recipients", "allow_bcc", "email", "scopes", "authorized_at", "api_base_url", "token_url"}

// Validate implements connector.Connector. There is no setting that allows sending.
func (c *Connector) Validate(_ context.Context, env connector.Env, raw json.RawMessage) (json.RawMessage, []connector.FieldError) {
	var errs []connector.FieldError
	add := func(param, format string, a ...any) {
		if len(errs) < 50 {
			errs = append(errs, connector.FieldError{Param: param, Message: fmt.Sprintf(format, a...)})
		}
	}
	if len(raw) > MaxConfigBytes {
		add("config", "The config is %d bytes; the limit is 16 KiB.", len(raw))
		return nil, errs
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
		add("config", "The config must be a JSON object. See %s.", DocsURL)
		return nil, errs
	}
	for k := range probe {
		if !slices.Contains(knownKeys, k) {
			add("config."+clip(k), "isn't a Gmail connector setting. The settings are auth, mailbox, internal_domains, max_recipients and allow_bcc. Sending has no setting: people send drafts themselves.")
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		add("config", "The config can't be read: %s. Check it against %s.", strings.TrimPrefix(err.Error(), "json: "), DocsURL)
		return nil, errs
	}
	cfg.defaults()
	switch cfg.Auth {
	case "oauth":
		if cfg.Mailbox != "me" {
			add("config.mailbox", "must be me with auth oauth: the person who authorized is the mailbox.")
		}
	case "service_account":
		if a, err := mail.ParseAddress(cfg.Mailbox); err != nil || a.Address != cfg.Mailbox {
			add("config.mailbox", "must be the email address the service account acts as, like ops@acme.com.")
		}
	default:
		add("config.auth", "must be oauth or service_account.")
	}
	if cfg.MaxRecipients < 1 || cfg.MaxRecipients > MaxRecipientsLimit {
		add("config.max_recipients", "must be 1 to 100.")
	}
	doms := []string{}
	for i, d := range cfg.InternalDomains {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(d), "@")))
		if !reDomain.MatchString(d) {
			add(fmt.Sprintf("config.internal_domains[%d]", i), "must be a domain like acme.com.")
			continue
		}
		if !slices.Contains(doms, d) {
			doms = append(doms, d)
		}
	}
	cfg.InternalDomains = doms
	if cfg.Email != "" {
		if a, err := mail.ParseAddress(cfg.Email); err != nil || a.Address != cfg.Email {
			add("config.email", "must be an email address.")
		}
	}
	for i, s := range cfg.Scopes {
		if !strings.HasPrefix(s, "https://www.googleapis.com/auth/") && !strings.HasPrefix(s, "gmail.") && s != "openid" && s != "email" {
			add(fmt.Sprintf("config.scopes[%d]", i), "must be a Google scope.")
		}
	}
	cfg.APIBaseURL = strings.TrimSuffix(cfg.APIBaseURL, "/")
	checkURL(env, &errs, "config.api_base_url", cfg.APIBaseURL, DefaultAPIBaseURL, "gmail.googleapis.com", false)
	checkURL(env, &errs, "config.token_url", cfg.TokenURL, DefaultTokenURL, "oauth2.googleapis.com", true)
	if len(errs) > 0 {
		return nil, errs
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		add("config", "The config can't be written back.")
		return nil, errs
	}
	return out, nil
}

func checkURL(env connector.Env, errs *[]connector.FieldError, param, raw, def, host string, withPath bool) {
	u, err := url.Parse(raw)
	bad := func(msg string) { *errs = append(*errs, connector.FieldError{Param: param, Message: msg}) }
	switch {
	case err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (!withPath && u.Path != "" && u.Path != "/"):
		bad("must be a plain URL like " + def + ".")
	case env.Dev():
		if u.Scheme != "https" && u.Scheme != "http" {
			bad("must be an http or https URL.")
		}
	case u.Scheme != "https" || u.Host != host:
		bad("must be " + def + " outside dev. Leave it out to use Google's.")
	}
}

func clip(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}
