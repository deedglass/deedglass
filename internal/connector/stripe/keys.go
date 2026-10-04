package stripe

import (
	"regexp"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
)

// The one shape of key Deedglass stores and sends: a sandbox secret or restricted key. There is
// no config key, env var or flag that widens it (TestNoLiveSwitch greps this package).
var reSandboxKey = regexp.MustCompile(`^(sk|rk)_test_[0-9A-Za-z]{10,247}$`)

// Refusal messages (S30 §5). None of them contains the value; Prefix shows 8 characters at most.
const (
	MsgLiveKey       = "This is a live-mode Stripe key. Deedglass connects to Stripe sandboxes only. Use a restricted key that starts with rk_test_."
	MsgOrgKey        = "This is a Stripe organization key, which acts across accounts. Use a restricted key from one sandbox that starts with rk_test_."
	MsgPublishable   = "This is a publishable key, which can't read charges or refund. Use a restricted key that starts with rk_test_."
	MsgWebhookSecret = "This is a webhook signing secret, not an API key. Use a restricted key that starts with rk_test_."
	MsgNotSandbox    = "This isn't a Stripe sandbox key. Use a restricted key that starts with rk_test_."
	MsgSecretKeyWarn = "Secret keys reach every Stripe API. A restricted key limits Deedglass to the tools you use."
	KeysURL          = "https://dashboard.stripe.com/test/apikeys"
)

// CheckKey returns the error code and sentence for a key Deedglass refuses, or "", "" for a
// sandbox key. Live keys get stripe_live_key_refused; every other refusal is validation_failed.
func CheckKey(v string) (code, msg string) {
	v = strings.TrimSpace(v)
	switch {
	case strings.HasPrefix(v, "sk_live_"), strings.HasPrefix(v, "rk_live_"):
		return connector.CodeStripeLiveKeyRefused, MsgLiveKey
	case strings.HasPrefix(v, "sk_org_"):
		return connector.CodeValidationFailed, MsgOrgKey
	case strings.HasPrefix(v, "pk_"):
		return connector.CodeValidationFailed, MsgPublishable
	case strings.HasPrefix(v, "whsec_"):
		return connector.CodeValidationFailed, MsgWebhookSecret
	case !reSandboxKey.MatchString(v):
		return connector.CodeValidationFailed, MsgNotSandbox
	}
	return "", ""
}

// Prefix is what a message may show of a key: its first 8 characters and an ellipsis
// ("sk_live_…"), or "…" when it is shorter.
func Prefix(v string) string {
	v = strings.TrimSpace(v)
	if len(v) < 8 {
		return "…"
	}
	return v[:8] + "…"
}

// Hint is how a stored key is shown: "rk_test_…9f2c".
func Hint(v string) string {
	if len(v) < 12 {
		return Prefix(v)
	}
	return v[:8] + "…" + v[len(v)-4:]
}

// restricted reports whether a sandbox key is a restricted key.
func restricted(v string) bool { return strings.HasPrefix(v, "rk_test_") }

// CheckCredential implements connector.CredentialChecker: PUT …/credentials refuses every key
// that isn't a sandbox key before encryption, so a live key is never stored, logged or echoed.
func (c *Connector) CheckCredential(kind string, fields map[string]string) *connector.CredentialRefusal {
	v := fields[FieldToken]
	code, msg := CheckKey(v)
	if code == "" {
		return nil
	}
	if code == connector.CodeValidationFailed {
		code = ""
	}
	return &connector.CredentialRefusal{Code: code, Param: "secret." + FieldToken, Message: msg}
}
