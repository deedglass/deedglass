//go:build outboundmail

package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
)

// Defaults and limits.
const (
	DefaultAPIBase  = "https://gmail.googleapis.com"
	DefaultTokenURL = "https://oauth2.googleapis.com/token"
	MaxConfigBytes  = 8 << 10
)

// AlwaysDenied are refused as a From whatever the config says (O1): cold email never leaves from
// Deedglass's own domain.
var AlwaysDenied = []string{"deedglass.com"}

// CodeFromDenied is the field error code (and API error) for a refused From.
const CodeFromDenied = "from_denied"

// MsgFromDenied is its message (S40 §5.5).
const MsgFromDenied = "From on deedglass.com is never allowed for outbound mail. Use the sending-domain mailbox."

// Config is the normalized config. Email, Scopes and AuthorizedAt are written by
// deedglass connector outbound authorize: facts about the grant, never secrets.
type Config struct {
	Mailbox         string    `json:"mailbox"`
	FromName        string    `json:"from_name"`
	SendingDomain   string    `json:"sending_domain"`
	DenyFromDomains []string  `json:"deny_from_domains"`
	UnsubscribeBase string    `json:"unsubscribe_base"`
	RequireWindow   bool      `json:"require_window"`
	Email           string    `json:"email,omitempty"`
	Scopes          []string  `json:"scopes,omitempty"`
	AuthorizedAt    time.Time `json:"authorized_at,omitzero"`
}

// denied is the effective deny list: the config's plus AlwaysDenied.
func (c Config) denied() []string {
	out := slices.Clone(AlwaysDenied)
	for _, d := range c.DenyFromDomains {
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// DeniedDomain reports whether the address's domain is d or a subdomain of d for any denied d.
func DeniedDomain(addr string, deny []string) bool {
	_, dom, ok := strings.Cut(strings.ToLower(strings.TrimSpace(addr)), "@")
	if !ok {
		return true // not an address: refuse
	}
	dom = strings.TrimSuffix(dom, ".")
	for _, d := range deny {
		if dom == d || strings.HasSuffix(dom, "."+d) {
			return true
		}
	}
	return false
}

var reDomain = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

const configSchema = `{"type":"object","additionalProperties":false,"required":["mailbox","sending_domain","deny_from_domains","unsubscribe_base"],"properties":{
"mailbox":{"type":"string","description":"The Workspace mailbox that sends, on the sending domain, like arjun@trydeedglass.com. It must be the account that authorizes."},
"from_name":{"type":"string","maxLength":64,"description":"The From display name, like Arjun Prabhakaran."},
"sending_domain":{"type":"string","description":"The mailbox's domain."},
"deny_from_domains":{"type":"array","minItems":1,"items":{"type":"string"},"description":"Domains that may never appear as From, subdomains included. deedglass.com is always refused."},
"unsubscribe_base":{"type":"string","description":"https://<sending_domain>/u/ : the one-click unsubscribe endpoint (RFC 8058)."},
"require_window":{"type":"boolean","description":"Default true: mail.send runs only inside its args.window."},
"email":{"type":"string","description":"Set by authorize."},
"scopes":{"type":"array","items":{"type":"string"},"description":"Set by authorize."},
"authorized_at":{"type":"string","description":"Set by authorize."}}}`

var knownKeys = []string{"mailbox", "from_name", "sending_domain", "deny_from_domains", "unsubscribe_base", "require_window", "email", "scopes", "authorized_at"}

// Validate implements connector.Connector.
func (c *Connector) Validate(_ context.Context, _ connector.Env, raw json.RawMessage) (json.RawMessage, []connector.FieldError) {
	var errs []connector.FieldError
	add := func(code, param, format string, a ...any) {
		if len(errs) < 50 {
			errs = append(errs, connector.FieldError{Param: param, Message: fmt.Sprintf(format, a...), Code: code})
		}
	}
	if len(raw) > MaxConfigBytes {
		add("", "config", "The config is %d bytes; the limit is 8 KiB.", len(raw))
		return nil, errs
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
		add("", "config", "The config must be a JSON object. See %s.", DocsURL)
		return nil, errs
	}
	for k := range probe {
		if !slices.Contains(knownKeys, k) {
			add("", "config."+clip(k), "isn't an outbound_mail setting. The settings are mailbox, from_name, sending_domain, deny_from_domains, unsubscribe_base and require_window.")
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	if _, set := probe["require_window"]; !set {
		raw = withDefaultWindow(raw)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		add("", "config", "The config can't be read: %s. Check it against %s.", strings.TrimPrefix(err.Error(), "json: "), DocsURL)
		return nil, errs
	}
	cfg.Mailbox = strings.ToLower(strings.TrimSpace(cfg.Mailbox))
	cfg.SendingDomain = strings.ToLower(strings.TrimSpace(cfg.SendingDomain))
	deny := []string{}
	for i, d := range cfg.DenyFromDomains {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(d), "@")))
		if !reDomain.MatchString(d) {
			add("", fmt.Sprintf("config.deny_from_domains[%d]", i), "must be a domain like deedglass.com.")
			continue
		}
		if !slices.Contains(deny, d) {
			deny = append(deny, d)
		}
	}
	if len(cfg.DenyFromDomains) == 0 {
		add("", "config.deny_from_domains", "must list at least one domain, like [deedglass.com]. Outbound mail always refuses some From domains.")
	}
	cfg.DenyFromDomains = deny
	a, err := mail.ParseAddress(cfg.Mailbox)
	switch {
	case cfg.Mailbox == "" || err != nil || a.Address != cfg.Mailbox || a.Name != "":
		add("", "config.mailbox", "must be one email address, like arjun@trydeedglass.com.")
	case DeniedDomain(cfg.Mailbox, cfg.denied()):
		add(CodeFromDenied, "config.mailbox", "%s", MsgFromDenied)
	}
	if !reDomain.MatchString(cfg.SendingDomain) {
		add("", "config.sending_domain", "must be a domain like trydeedglass.com.")
	} else if _, dom, _ := strings.Cut(cfg.Mailbox, "@"); dom != cfg.SendingDomain {
		add("", "config.sending_domain", "must be the mailbox's own domain (%s).", clip(dom))
	} else if DeniedDomain("x@"+cfg.SendingDomain, cfg.denied()) {
		add(CodeFromDenied, "config.sending_domain", "%s", MsgFromDenied)
	}
	if want := "https://" + cfg.SendingDomain + "/"; cfg.SendingDomain == "" || !strings.HasPrefix(cfg.UnsubscribeBase, want) ||
		strings.ContainsAny(cfg.UnsubscribeBase, "?#@ ") || !strings.HasSuffix(cfg.UnsubscribeBase, "/") {
		add("", "config.unsubscribe_base", "must be an https URL on the sending domain ending in /, like https://%s/u/.", cfg.SendingDomain)
	}
	if strings.ContainsAny(cfg.FromName, "\r\n\"<>") || len(cfg.FromName) > 64 {
		add("", "config.from_name", "must be one line of at most 64 characters, without quotes or angle brackets.")
	}
	if cfg.Email != "" {
		if a, err := mail.ParseAddress(cfg.Email); err != nil || a.Address != cfg.Email {
			add("", "config.email", "must be an email address.")
		} else if cfg.Email != cfg.Mailbox {
			add("", "config.email", "must be the mailbox: the account that authorized is %s, not %s.", clip(cfg.Email), clip(cfg.Mailbox))
		}
	}
	for i, s := range cfg.Scopes {
		if !strings.HasPrefix(s, "https://www.googleapis.com/auth/") && !strings.HasPrefix(s, "gmail.") {
			add("", fmt.Sprintf("config.scopes[%d]", i), "must be a Google scope.")
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		add("", "config", "The config can't be written back.")
		return nil, errs
	}
	return out, nil
}

// withDefaultWindow sets require_window true when the config leaves it out.
func withDefaultWindow(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	m["require_window"] = json.RawMessage("true")
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return b
}

func clip(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}
