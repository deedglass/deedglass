package stripe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/money"
)

// Defaults and limits of the config.
const (
	DefaultAPIBaseURL = "https://api.stripe.com"
	MaxAmount         = 99_999_999
	MaxConfigBytes    = 16 << 10
)

// RefundReasons are the reasons Stripe accepts on a refund.
var RefundReasons = []string{"duplicate", "fraudulent", "requested_by_customer"}

// DefaultRefundLimits are the per-refund caps in minor units: defence in depth under policy.
var DefaultRefundLimits = map[string]int64{"usd": 100000, "eur": 100000, "inr": 5000000, "jpy": 150000}

// Config is the normalized config: what the connectors row stores and its hash covers. There is
// no setting that allows live mode.
type Config struct {
	APIVersion    string           `json:"api_version"`
	RefundLimits  map[string]int64 `json:"refund_limits"`
	RefundReasons []string         `json:"refund_reasons"`
	APIBaseURL    string           `json:"api_base_url"`
}

func (c *Config) defaults() {
	if c.APIVersion == "" {
		c.APIVersion = APIVersion
	}
	if c.RefundLimits == nil {
		c.RefundLimits = map[string]int64{}
		for k, v := range DefaultRefundLimits {
			c.RefundLimits[k] = v
		}
	}
	if c.RefundReasons == nil {
		c.RefundReasons = slices.Clone(RefundReasons)
	}
	if c.APIBaseURL == "" {
		c.APIBaseURL = DefaultAPIBaseURL
	}
}

var (
	reVersion  = regexp.MustCompile(`^20[0-9]{2}-[01][0-9]-[0-3][0-9](\.[a-z]{2,20})?$`)
	reCurrency = regexp.MustCompile(`^[a-z]{3}$`)
)

// configSchema summarizes the config for forms (S26); Validate is the authority.
const configSchema = `{"type":"object","additionalProperties":false,"properties":{
"api_version":{"type":"string","description":"Sent as Stripe-Version on every request. Default 2026-08-26.dahlia."},
"refund_limits":{"type":"object","description":"The most one refund may be, in minor units, per currency. Currencies not listed are refused. Each value is 1 to 99,999,999."},
"refund_reasons":{"type":"array","items":{"type":"string","enum":["duplicate","fraudulent","requested_by_customer"]}},
"api_base_url":{"type":"string","description":"https://api.stripe.com (any http(s) URL in dev only)"}}}`

// Validate implements connector.Connector: it checks and normalizes cfg. Unknown keys are field
// errors, so a key like "live_mode" can never be stored, let alone read.
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
		switch k {
		case "api_version", "refund_limits", "refund_reasons", "api_base_url":
		default:
			add("config."+clip(k), "isn't a Stripe connector setting. The settings are api_version, refund_limits, refund_reasons and api_base_url. Live mode has no setting: Deedglass connects to Stripe sandboxes only.")
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		add("config", "The config can't be read: %s. Check it against %s.", cleanJSONErr(err), DocsURL)
		return nil, errs
	}
	cfg.defaults()
	if !reVersion.MatchString(cfg.APIVersion) {
		add("config.api_version", "must be a Stripe API version like %s.", APIVersion)
	}
	if len(cfg.RefundLimits) == 0 {
		add("config.refund_limits", "needs at least one currency, like {\"usd\": 100000} for %s.", money.Format(100000, "usd"))
	}
	norm := map[string]int64{}
	keys := make([]string, 0, len(cfg.RefundLimits))
	for k := range cfg.RefundLimits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := cfg.RefundLimits[k]
		lk := strings.ToLower(strings.TrimSpace(k))
		if !reCurrency.MatchString(lk) {
			add("config.refund_limits."+clip(k), "must be a lowercase ISO 4217 currency code, like usd.")
			continue
		}
		if v < 1 || v > MaxAmount {
			add("config.refund_limits."+lk, "must be 1 to 99,999,999 minor units.")
			continue
		}
		norm[lk] = v
	}
	cfg.RefundLimits = norm
	reasons := []string{}
	for i, r := range cfg.RefundReasons {
		if !slices.Contains(RefundReasons, r) {
			add(fmt.Sprintf("config.refund_reasons[%d]", i), "must be duplicate, fraudulent or requested_by_customer.")
			continue
		}
		if !slices.Contains(reasons, r) {
			reasons = append(reasons, r)
		}
	}
	cfg.RefundReasons = reasons
	cfg.APIBaseURL = strings.TrimSuffix(cfg.APIBaseURL, "/")
	u, err := url.Parse(cfg.APIBaseURL)
	switch {
	case err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/"):
		add("config.api_base_url", "must be a plain URL like https://api.stripe.com.")
	case env.Dev():
		if u.Scheme != "https" && u.Scheme != "http" {
			add("config.api_base_url", "must be an http or https URL.")
		}
	case u.Scheme != "https" || u.Host != "api.stripe.com":
		add("config.api_base_url", "must be https://api.stripe.com outside dev. Leave it out to use Stripe's API.")
	}
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
