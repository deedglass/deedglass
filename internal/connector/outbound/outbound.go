//go:build outboundmail

// Package outbound is the outbound-mail connector (S40, ADR-076 as amended): one Google Workspace
// mailbox on a sending domain, sending plain-text email through the Gmail API's messages.send.
// It exists only in the ops build (go build -tags outboundmail, make ops-build) and only for the
// org ids in connectors.outbound_mail.orgs: the release binary carries no sending code
// (TestNoOutboundMailInRelease), and the product's Gmail connector stays drafts only.
//
// What it guarantees, in code rather than discipline:
//   - From is always the authorised mailbox, and never on deedglass.com or a subdomain, nor on any
//     domain in deny_from_domains: refused at create, at authorize, before every call and again in
//     the read-back (a denied From there disables the connector).
//   - The body that leaves is the body that was approved: body_sha256 binds it, and the connector
//     refuses a mismatch before any byte leaves.
//   - send is never retried: Gmail's send has no idempotency key. A lost answer is unknown, and the
//     read-back finds the message by its Message-ID instead of sending it again.
//   - Only the paths in allow.go can be requested: no drafts, modify, trash, delete, settings,
//     import, insert, batch or watch.
package outbound

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// Type is the connector type.
const Type = "outbound_mail"

// DocsURL is the connector's page.
const DocsURL = "https://docs.deedglass.com/connectors/outbound_mail"

// Credential fields (kind "fields").
const (
	FieldClientID     = "client_id"
	FieldClientSecret = "client_secret"
	FieldRefreshToken = "refresh_token"
)

// Scopes: the narrowest send scope, and readonly for the read-back, history and bounces.
const (
	ScopeSend     = "https://www.googleapis.com/auth/gmail.send"
	ScopeReadonly = "https://www.googleapis.com/auth/gmail.readonly"
)

// now is the clock (tests move it).
var now = time.Now

var logger = slog.Default

// tokens holds access tokens in memory only, keyed by connector id and config hash.
var tokens = &saas.TokenCache{}

// Options configure the type (serve passes connectors.outbound_mail.api_base).
type Options struct {
	APIBase string // https://gmail.googleapis.com; dev may point at fakegmail
}

// Connector is the outbound_mail connector type.
type Connector struct {
	apiBase  string
	tokenURL string
}

// New returns the type for registration.
func New(o Options) *Connector {
	base := o.APIBase
	if base == "" {
		base = DefaultAPIBase
	}
	tok := DefaultTokenURL
	if base != DefaultAPIBase {
		tok = base + "/token" // a fake mailbox (dev) serves its own token endpoint
	}
	return &Connector{apiBase: base, tokenURL: tok}
}

var (
	_ connector.Connector        = (*Connector)(nil)
	_ connector.Enricher         = (*Connector)(nil)
	_ connector.NativeReadBacker = (*Connector)(nil)
	_ connector.ArgsChecker      = (*Connector)(nil)
)

// Type implements connector.Connector.
func (c *Connector) Type() string { return Type }

// APIBase is the Gmail API base this type talks to.
func (c *Connector) APIBase() string { return c.apiBase }

// TokenURL is the token endpoint this type refreshes against.
func (c *Connector) TokenURL() string { return c.tokenURL }

type state struct {
	cfg     Config
	base    *url.URL
	token   *url.URL
	tripped atomic.Bool
}

func (c *Connector) parse(inst *connector.Instance) (*state, error) {
	v, err := inst.Parsed(func() (any, error) {
		var cfg Config
		if err := json.Unmarshal(inst.Config, &cfg); err != nil {
			return nil, fmt.Errorf("outbound: the stored config can't be read: %w", err)
		}
		b, err := url.Parse(c.apiBase)
		if err != nil {
			return nil, err
		}
		t, err := url.Parse(c.tokenURL)
		if err != nil {
			return nil, err
		}
		return &state{cfg: cfg, base: b, token: t}, nil
	})
	if err != nil {
		return nil, &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured",
			AgentMessage: "This mail connector's config can't be read. Nothing was sent. An admin can run deedglass connector test on it.", Err: err}
	}
	return v.(*state), nil
}

const maxBody = 1 << 20

func client(inst *connector.Instance) *http.Client {
	return inst.Client(func() *http.Client {
		pol := inst.Env.Egress
		if pol == nil {
			pol = &egress.Policy{Env: egress.EnvProd}
		}
		return pol.Client(egress.ClientOptions{Timeout: 20 * time.Second, MaxResponseBytes: maxBody, Redirects: egress.RedirectNone})
	})
}

// Tools implements connector.Connector.
func (c *Connector) Tools(_ context.Context, inst *connector.Instance) ([]connector.ToolSpec, error) {
	if _, err := c.parse(inst); err != nil {
		return nil, err
	}
	return toolSpecs(), nil
}

// trip disables the connector: the instance refuses every further call at once, the breaker (the
// serve wiring) sets status disabled with health.reason and seals connector.disabled.
func trip(ctx context.Context, inst *connector.Instance, st *state, reason, detail string) {
	if !st.tripped.CompareAndSwap(false, true) {
		return
	}
	logger().ErrorContext(ctx, "outbound: the connector was disabled; nothing more is sent", "alert", "outbound_"+reason,
		"connector", inst.Name, "connector_id", inst.ID.String(), "org_id", inst.OrgID.String())
	if inst.Env.Breaker == nil {
		return
	}
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := inst.Env.Breaker.Trip(bctx, inst, reason, detail); err != nil {
		logger().ErrorContext(ctx, "outbound: recording the disabled connector failed; the instance still refuses every call",
			"alert", "outbound_"+reason, "connector", inst.Name, "err", err)
	}
}

func disabledErr(inst *connector.Instance) *connector.Error {
	return &connector.Error{Kind: connector.KindConfig, Code: connector.CodeConnectorUnavailable,
		AgentMessage: fmt.Sprintf(`The mail connector "%s" is disabled because it saw a From on a refused domain. Nothing was sent. An admin can check it with deedglass connector test %s.`, inst.Name, inst.Name)}
}
