// Package stripe is the Stripe connector (S30 §3.1), sandbox only. A live key is refused when
// it is stored (CheckCredential), again before every call (open), and a live-mode answer anywhere
// in a response trips the breaker, which disables the connector before any further call. Every
// POST carries Idempotency-Key = the action id and metadata[deedglass_action_id]; enrich reads the
// charge from Stripe so policy reasons about real money; every write is read back at L1 from
// Stripe's own objects. Only this package touches the key, always through Instance.Secret, and
// every request leaves through the instance's egress client.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
)

// Type is the connector type.
const Type = "stripe"

// DocsURL is the connector's documentation page.
const DocsURL = "https://docs.deedglass.com/connectors/stripe"

// APIVersion is the pinned Stripe-Version (verified 2026-10-04: the last dahlia release; the
// newer 2026-09-30.endive is a deliberate upgrade for a later session).
const APIVersion = "2026-08-26.dahlia"

// FieldToken is the bearer credential's field.
const FieldToken = "token"

// EnrichTTL is how long an enrich read of one Stripe object is reused.
const EnrichTTL = 30 * time.Second

// now is the clock (tests move it).
var now = time.Now

// Connector is the Stripe connector type. Each Instance carries its parsed config, its enrich
// cache, its breaker state and its egress client.
type Connector struct{}

// New returns the Stripe connector type, for registration (connector.Options.Types).
func New() *Connector { return &Connector{} }

var (
	_ connector.Connector         = (*Connector)(nil)
	_ connector.Enricher          = (*Connector)(nil)
	_ connector.NativeReadBacker  = (*Connector)(nil)
	_ connector.Summarizer        = (*Connector)(nil)
	_ connector.ArgsChecker       = (*Connector)(nil)
	_ connector.CredentialChecker = (*Connector)(nil)
)

// Type implements connector.Connector.
func (c *Connector) Type() string { return Type }

// state is an instance's parsed config, enrich cache and breaker.
type state struct {
	cfg     Config
	base    *url.URL
	tripped atomic.Bool // the breaker tripped: no further call leaves

	mu    sync.Mutex
	cache map[string]cached // enrich reads by object id
}

type cached struct {
	at   time.Time
	body map[string]any
}

func parse(inst *connector.Instance) (*state, error) {
	v, err := inst.Parsed(func() (any, error) {
		var cfg Config
		if err := json.Unmarshal(inst.Config, &cfg); err != nil {
			return nil, fmt.Errorf("stripe: the stored config can't be read: %w", err)
		}
		cfg.defaults()
		u, err := url.Parse(cfg.APIBaseURL)
		if err != nil {
			return nil, fmt.Errorf("stripe: api_base_url: %w", err)
		}
		return &state{cfg: cfg, base: u, cache: map[string]cached{}}, nil
	})
	if err != nil {
		return nil, &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured",
			AgentMessage: "This Stripe connector's config can't be read. An admin can check Console › Connectors.", Err: err}
	}
	return v.(*state), nil
}

const maxBody = 4 << 20

// client is the instance's egress client: no redirects, a 4 MiB body cap, 20 s per request.
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
	if _, err := parse(inst); err != nil {
		return nil, err
	}
	return toolSpecs(), nil
}
