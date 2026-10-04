package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
)

// Defaults and limits of the config.
const (
	DefaultAPIBaseURL      = "https://slack.com/api"
	DefaultMaxReadMessages = 20
	MaxReadMessages        = 100
	MaxChannels            = 100
	MaxEmailDomains        = 20
	MaxConfigBytes         = 16 << 10
)

// Config is the normalized config: what the connectors row stores and its hash covers.
type Config struct {
	TeamID          string          `json:"team_id"`
	AppID           string          `json:"app_id"`
	Channels        []string        `json:"channels"`
	ReadChannels    []string        `json:"read_channels"`
	MaxReadMessages int             `json:"max_read_messages"`
	Approvals       ApprovalsConfig `json:"approvals"`
	APIBaseURL      string          `json:"api_base_url"`
}

// ApprovalsConfig is approvals in Slack (S29 §3.4).
type ApprovalsConfig struct {
	Enabled             bool     `json:"enabled"`
	Channel             string   `json:"channel"`
	RequireEmailDomains []string `json:"require_email_domains"`
}

var (
	reTeamID      = regexp.MustCompile(`^T[A-Z0-9]{2,20}$`)
	reAppID       = regexp.MustCompile(`^A[A-Z0-9]{2,20}$`)
	reChannelName = regexp.MustCompile(`^#[a-z0-9][a-z0-9._-]{0,79}$`)
	reChannelID   = regexp.MustCompile(`^[CG][A-Z0-9]{6,20}$`)
	reDomain      = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
)

type validator struct{ errs []connector.FieldError }

func (v *validator) add(param, format string, a ...any) {
	if len(v.errs) < 50 {
		v.errs = append(v.errs, connector.FieldError{Param: param, Message: fmt.Sprintf(format, a...)})
	}
}

// Validate implements connector.Connector: it checks and normalizes cfg. Unknown keys are field
// errors.
func (c *Connector) Validate(_ context.Context, env connector.Env, raw json.RawMessage) (json.RawMessage, []connector.FieldError) {
	v := &validator{}
	if len(raw) > MaxConfigBytes {
		v.add("config", "The config is %d bytes; the limit is 16 KiB.", len(raw))
		return nil, v.errs
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
		v.add("config", "The config must be a JSON object. See %s.", DocsURL)
		return nil, v.errs
	}
	known := map[string]bool{"team_id": true, "app_id": true, "channels": true, "read_channels": true, "max_read_messages": true, "approvals": true, "api_base_url": true}
	for k := range probe {
		if !known[k] {
			v.add("config."+clip(k), "isn't a Slack connector setting. The settings are team_id, app_id, channels, read_channels, max_read_messages, approvals and api_base_url.")
		}
	}
	if a, ok := probe["approvals"]; ok {
		var am map[string]json.RawMessage
		if json.Unmarshal(a, &am) == nil {
			for k := range am {
				if k != "enabled" && k != "channel" && k != "require_email_domains" {
					v.add("config.approvals."+clip(k), "isn't an approvals setting. The settings are enabled, channel and require_email_domains.")
				}
			}
		}
	}
	if len(v.errs) > 0 {
		return nil, v.errs
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		v.add("config", "The config can't be read: %s. Check it against %s.", cleanJSONErr(err), DocsURL)
		return nil, v.errs
	}
	normalize(env, &cfg, v)
	if len(v.errs) > 0 {
		return nil, v.errs
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		v.add("config", "The config can't be written back.")
		return nil, v.errs
	}
	return out, nil
}

func normalize(env connector.Env, cfg *Config, v *validator) {
	if !reTeamID.MatchString(cfg.TeamID) {
		v.add("config.team_id", "must be the workspace id, like T0123ABCD. Find it with deedglass connector test slack, or in the workspace URL.")
	}
	if !reAppID.MatchString(cfg.AppID) {
		v.add("config.app_id", "must be the app id, like A0123ABCD, from the app's Basic Information page.")
	}
	cfg.Channels = normList(v, "config.channels", cfg.Channels, true)
	cfg.ReadChannels = normList(v, "config.read_channels", cfg.ReadChannels, true)
	switch {
	case cfg.MaxReadMessages == 0:
		cfg.MaxReadMessages = DefaultMaxReadMessages
	case cfg.MaxReadMessages < 1 || cfg.MaxReadMessages > MaxReadMessages:
		v.add("config.max_read_messages", "must be 1 to 100.")
	}
	a := &cfg.Approvals
	a.Channel = strings.TrimSpace(a.Channel)
	if a.Channel != "" {
		if c, ok := normChannel(a.Channel); ok {
			a.Channel = c
		} else {
			v.add("config.approvals.channel", "must be #name or a channel id (C… or G…).")
		}
	}
	if a.Enabled && a.Channel == "" {
		v.add("config.approvals.channel", "is needed when approvals are enabled, like #deedglass-approvals.")
	}
	if len(a.RequireEmailDomains) > MaxEmailDomains {
		v.add("config.approvals.require_email_domains", "holds %d domains; the limit is %d.", len(a.RequireEmailDomains), MaxEmailDomains)
	}
	doms := []string{}
	seen := map[string]bool{}
	for i, d := range a.RequireEmailDomains {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(d), "@")))
		if !reDomain.MatchString(d) {
			v.add(fmt.Sprintf("config.approvals.require_email_domains[%d]", i), "must be a domain like acme.com.")
			continue
		}
		if !seen[d] {
			seen[d] = true
			doms = append(doms, d)
		}
	}
	a.RequireEmailDomains = doms
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = DefaultAPIBaseURL
	}
	cfg.APIBaseURL = strings.TrimSuffix(cfg.APIBaseURL, "/")
	u, err := url.Parse(cfg.APIBaseURL)
	switch {
	case err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "":
		v.add("config.api_base_url", "must be a plain URL like https://slack.com/api.")
	case env.Dev():
		if u.Scheme != "https" && u.Scheme != "http" {
			v.add("config.api_base_url", "must be an http or https URL.")
		}
	case u.Scheme != "https" || u.Host != "slack.com":
		v.add("config.api_base_url", "must be https://slack.com/api outside dev. Leave it out to use Slack's API.")
	}
}

// normList normalizes an allow-list: "#name" lowercased, ids as they are, ["*"] alone.
func normList(v *validator, param string, in []string, allowStar bool) []string {
	if len(in) > MaxChannels {
		v.add(param, "holds %d channels; the limit is %d.", len(in), MaxChannels)
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for i, c := range in {
		c = strings.TrimSpace(c)
		if c == "*" {
			if !allowStar || len(in) != 1 {
				v.add(fmt.Sprintf("%s[%d]", param, i), `"*" must be the only entry: it means any channel the bot is in.`)
			}
			out = append(out, c)
			continue
		}
		n, ok := normChannel(c)
		if !ok {
			v.add(fmt.Sprintf("%s[%d]", param, i), "must be #name or a channel id (C… or G…).")
			continue
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// normChannel normalizes "#Name" to "#name" and keeps an id.
func normChannel(c string) (string, bool) {
	if reChannelID.MatchString(c) {
		return c, true
	}
	l := strings.ToLower(c)
	if reChannelName.MatchString(l) {
		return l, true
	}
	return "", false
}

func clip(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

func cleanJSONErr(err error) string {
	s := strings.TrimPrefix(err.Error(), "json: ")
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}
