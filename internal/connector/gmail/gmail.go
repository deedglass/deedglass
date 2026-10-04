// Package gmail is the Gmail connector (S30 §3.2), drafts only. No tool sends, and the request
// builder refuses every path outside a compiled allow-list (allow.go) before a byte leaves, so
// sending stays with people. Auth is OAuth (PKCE through a loopback redirect, authorize.go) or a
// Google Workspace service account; the refresh token and the key live in the vault, and access
// tokens only in memory (saas.TokenCache).
package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// Type is the connector type.
const Type = "gmail"

// DocsURL is the connector's documentation page.
const DocsURL = "https://docs.deedglass.com/connectors/gmail"

// Credential fields (kind "fields").
const (
	FieldClientID           = "client_id"
	FieldClientSecret       = "client_secret"
	FieldRefreshToken       = "refresh_token"
	FieldServiceAccountJSON = "service_account_json"
)

// now is the clock (tests move it).
var now = time.Now

// tokens holds access tokens in memory only, keyed by connector id and config hash.
var tokens = &saas.TokenCache{}

// Connector is the Gmail connector type.
type Connector struct{}

// New returns the Gmail connector type, for registration (connector.Options.Types).
func New() *Connector { return &Connector{} }

var (
	_ connector.Connector        = (*Connector)(nil)
	_ connector.Enricher         = (*Connector)(nil)
	_ connector.NativeReadBacker = (*Connector)(nil)
	_ connector.Summarizer       = (*Connector)(nil)
	_ connector.ArgsChecker      = (*Connector)(nil)
)

// Type implements connector.Connector.
func (c *Connector) Type() string { return Type }

type state struct {
	cfg   Config
	base  *url.URL
	token *url.URL
}

func parse(inst *connector.Instance) (*state, error) {
	v, err := inst.Parsed(func() (any, error) {
		var cfg Config
		if err := json.Unmarshal(inst.Config, &cfg); err != nil {
			return nil, fmt.Errorf("gmail: the stored config can't be read: %w", err)
		}
		cfg.defaults()
		b, err := url.Parse(cfg.APIBaseURL)
		if err != nil {
			return nil, err
		}
		t, err := url.Parse(cfg.TokenURL)
		if err != nil {
			return nil, err
		}
		return &state{cfg: cfg, base: b, token: t}, nil
	})
	if err != nil {
		return nil, &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured",
			AgentMessage: "This Gmail connector's config can't be read. An admin can check Console › Connectors.", Err: err}
	}
	return v.(*state), nil
}

const maxBody = 4 << 20

func client(inst *connector.Instance) *http.Client {
	return inst.Client(func() *http.Client {
		pol := inst.Env.Egress
		if pol == nil {
			pol = &egress.Policy{Env: egress.EnvProd}
		}
		return pol.Client(egress.ClientOptions{Timeout: 20 * time.Second, MaxResponseBytes: maxBody, Redirects: egress.RedirectNone})
	})
}

// Tools implements connector.Connector: get_thread only when the grant includes gmail.readonly.
func (c *Connector) Tools(_ context.Context, inst *connector.Instance) ([]connector.ToolSpec, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	all := toolSpecs(st.cfg.MaxRecipients)
	if st.cfg.readonly() {
		return all, nil
	}
	out := all[:0:0]
	for _, t := range all {
		if t.Name != ToolGetThread {
			out = append(out, t)
		}
	}
	return out, nil
}
