package connector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/vault"
)

// Env is what a connector may know about where it runs.
type Env struct {
	Name    string         // "dev" or "prod"
	Egress  *egress.Policy // the only outbound path
	Breaker Breaker        // S30: disables a connector after a safety trip; nil in tests that don't need it
}

// Dev reports whether this is a dev deployment.
func (e Env) Dev() bool { return e.Name == egress.EnvDev }

// CredentialField is one field of a credential kind. Help and PrefixHint are S30's catalog
// additions for S26's forms ("A restricted key from your Stripe sandbox.", "rk_test_").
type CredentialField struct {
	Name       string `json:"name"`
	Secret     bool   `json:"secret"`
	Help       string `json:"help,omitempty"`
	PrefixHint string `json:"prefix_hint,omitempty"`
}

// CredentialKind is a kind a connector type accepts, with its fields (S26 builds forms from it).
type CredentialKind struct {
	Kind   string            `json:"kind"`
	Fields []CredentialField `json:"fields"`
}

// TypeInfo describes a connector type: the connector_type object of GET /v1/connector-types.
// S30 (TypeInfo v2) adds the catalog fields, additively: S16's fields keep their names and
// meaning, so S26's forms keep working. The catalog is the one source for the Console's forms, the
// docs tables (gendocs), the CLI and the SDKs.
type TypeInfo struct {
	Object          string           `json:"object"` // "connector_type"
	Type            string           `json:"type"`
	Title           string           `json:"title"`
	Description     string           `json:"description"`
	Levels          []string         `json:"levels"` // L0, L1, L2 the type can reach
	CredentialKinds []CredentialKind `json:"credential_kinds"`
	ConfigSchema    json.RawMessage  `json:"config_schema"`
	DocsURL         string           `json:"docs_url"`

	// S30.
	Summary      string      `json:"summary"`       // one sentence: what it does
	Status       string      `json:"status"`        // stable | beta | dev
	Icon         string      `json:"icon"`          // a Lucide icon name
	Modes        []string    `json:"modes"`         // sandbox_only, drafts_only, dev_only; [] when none
	Setup        []SetupStep `json:"setup"`         // the steps to connect it
	Limits       []string    `json:"limits"`        // what it doesn't do, honestly
	ToolsDynamic bool        `json:"tools_dynamic"` // rest, mcp_http: tools come from config or the server
	Tools        []ToolInfo  `json:"tools"`         // the fixed tools; [] when ToolsDynamic
}

// SetupStep is one step of connecting a type.
type SetupStep struct {
	Title   string `json:"title"`
	Detail  string `json:"detail,omitempty"`
	Command string `json:"command,omitempty"`
	URL     string `json:"url,omitempty"`
}

// ToolInfo is a tool in the catalog: the tool object's shape plus what the Witness checks.
type ToolInfo struct {
	Object        string          `json:"object"` // "tool"
	Name          string          `json:"name"`   // qualified with the type: "stripe.create_refund"
	Title         string          `json:"title"`
	Description   string          `json:"description"`
	SideEffect    SideEffect      `json:"side_effect"`
	Idempotent    bool            `json:"idempotent"`
	Levels        []string        `json:"levels"`
	ReadBack      string          `json:"read_back"` // the L1 sentence; "" when the tool isn't read back
	RequiresScope string          `json:"requires_scope,omitempty"`
	InputSchema   json.RawMessage `json:"input_schema"`
}

// FieldError is one refused config field: param is its path ("config.tools[1].path").
type FieldError struct {
	Param   string `json:"param"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"` // "destination_blocked" for an egress refusal; "" is validation_failed
}

func (e FieldError) Error() string { return e.Param + ": " + e.Message }

// CheckStatus is a test check's outcome.
type CheckStatus string

const (
	CheckPass CheckStatus = "pass"
	CheckWarn CheckStatus = "warn"
	CheckFail CheckStatus = "fail"
	CheckSkip CheckStatus = "skip"
)

// Check is one line of a connector test: config, credentials, destination, tls, auth (S16), and
// S17's Postgres checks. S26's panel renders the same shape.
type Check struct {
	ID     string      `json:"id"`
	Title  string      `json:"title"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail"`
	Fix    string      `json:"fix,omitempty"`
	FixSQL string      `json:"fix_sql,omitempty"`
	DocURL string      `json:"doc_url,omitempty"`
}

// TestReport is a connector test: the worst check decides the status.
type TestReport struct {
	Status   CheckStatus `json:"status"`
	Checks   []Check     `json:"checks"`
	Duration time.Duration
}

// Settle sets Status from the checks: fail beats warn beats pass; all skipped is skip.
func (r *TestReport) Settle() {
	st, any := CheckSkip, false
	for _, c := range r.Checks {
		switch c.Status {
		case CheckFail:
			st = CheckFail
		case CheckWarn:
			if st != CheckFail {
				st = CheckWarn
			}
		case CheckPass:
			if st == CheckSkip {
				st = CheckPass
			}
		}
		any = true
	}
	if !any {
		st = CheckSkip
	}
	r.Status = st
}

// Connector is one connector type. Every type (rest, postgres in S17, the SaaS types in S29–S30,
// fake) implements it; the registry holds one value per type and binds it to per-org Instances.
type Connector interface {
	Type() string
	Info() TypeInfo
	// Validate checks and normalizes a config. It returns the normalized config, or field errors.
	Validate(ctx context.Context, env Env, cfg json.RawMessage) (json.RawMessage, []FieldError)
	Tools(ctx context.Context, inst *Instance) ([]ToolSpec, error)
	// Execute runs one call. It must respect ctx and return a *Error for typed failures.
	Execute(ctx context.Context, inst *Instance, call Call) (Result, error)
	Test(ctx context.Context, inst *Instance) (TestReport, error)
}

// Enricher adds facts to the policy input before the decision (REST: request.http).
type Enricher interface {
	Enrich(ctx context.Context, inst *Instance, t ToolSpec, args json.RawMessage) (map[string]any, error)
}

// ArgsChecker is a type with its own argument rules, checked before the schema so its message
// can say exactly what to send (S30: Stripe's "Send amount as an integer in minor units"). It
// returns a *Error (validation_failed) or nil. Nothing is sent when it refuses.
type ArgsChecker interface {
	CheckArgs(t ToolSpec, args json.RawMessage) error
}

// CredentialRefusal is a credential value a type refuses before it is stored (S30: a live Stripe
// key). Message never contains the value; it may name the value's first 8 characters at most.
type CredentialRefusal struct {
	Code    string // "stripe_live_key_refused", or "" for validation_failed
	Param   string // "secret.token"
	Message string
}

// CredentialChecker is a type that refuses some credential values on PUT …/credentials, before
// encryption, so a refused value is never stored, logged or echoed.
type CredentialChecker interface {
	CheckCredential(kind string, fields map[string]string) *CredentialRefusal
}

// Breaker disables a connector after a safety trip (S30: Stripe answered in live mode). The serve
// wiring implements it over the store and the Ledger: the connector goes to status disabled with
// health.reason, and the admin deed connector.disabled is sealed. It must be safe to call twice.
type Breaker interface {
	Trip(ctx context.Context, inst *Instance, reason, detail string) error
}

// Observation is what a read-back saw (S19). Body is the response body as JSON (REST) or the
// row read-back's result (Postgres); it reaches the Witness only, never an agent or a deed, and the
// witness detail keeps only the compared fields and a digest of it.
type Observation struct {
	Body      json.RawMessage
	Target    string         // S29 native read-backs: what was read, as people name it ("issue #42 on GitHub")
	Status    int            // HTTP status, or 0
	Meta      map[string]any // latency_ms, attempts, …
	LatencyMS int64
}

// ReadBacker reads a write back for the L1 witness (S19). For a REST connector, call is the read
// tool's call (its Tool and resolved Args); for Postgres, call and res are the statement that ran
// and its result (Evidence["xid8"]). A read-back never writes. Errors are *Error values: a
// KindNotFound answer means the target has nothing there; *ErrReadBackSkipped means the read-back
// doesn't apply to this call.
type ReadBacker interface {
	ReadBack(ctx context.Context, inst *Instance, call Call, res Result) (Observation, error)
}

// ErrReadBackSkipped is a read-back that doesn't apply (a Postgres WHERE that isn't the primary
// key). Why becomes the witness's readback_skipped note.
type ErrReadBackSkipped struct{ Why string }

func (e *ErrReadBackSkipped) Error() string { return "connector: no read-back: " + e.Why }

// Instance is one configured connector of one org (a connectors row), or a built-in.
type Instance struct {
	ID         ids.ConnectorID
	OrgID      ids.OrgID
	Name       string
	Type       string
	Config     json.RawMessage // normalized
	ConfigHash string          // "sha256:…" of the normalized config
	Env        Env

	opener vault.Opener
	once   sync.Once
	parsed any
	perr   error
	client *http.Client
	cmu    sync.Mutex
}

// NewInstance builds an instance. opener may be nil for connectors without credentials.
func NewInstance(id ids.ConnectorID, org ids.OrgID, name, typ string, cfg json.RawMessage, hash string, env Env, opener vault.Opener) *Instance {
	return &Instance{ID: id, OrgID: org, Name: name, Type: typ, Config: cfg, ConfigHash: hash, Env: env, opener: opener}
}

// Secret opens the instance's credential: the only decrypt path a connector has. Call Destroy on
// the result as soon as the value has been used. A missing credential is credentials_missing; a
// credential that can't be opened is vault_unavailable, and nothing is ever sent without auth.
func (in *Instance) Secret(ctx context.Context) (*vault.Secret, error) {
	if in.opener == nil {
		return nil, ErrCredentialsMissing(in.Name)
	}
	s, err := in.opener.Open(ctx, vault.Ref{Org: in.OrgID, Connector: in.ID})
	switch {
	case err == nil:
		// The call's redactor (Registry.Execute put it in ctx) now covers this secret: its output,
		// errors and log lines are redacted with it.
		vault.RedactorFrom(ctx).Merge(s.Redactor())
		return s, nil
	case errors.Is(err, vault.ErrNotFound):
		return nil, ErrCredentialsMissing(in.Name)
	}
	return nil, ErrVaultUnavailable(err)
}

// Parsed returns the connector's parsed form of the config, built once per instance (the
// registry caches instances by id and config hash).
func (in *Instance) Parsed(build func() (any, error)) (any, error) {
	in.once.Do(func() { in.parsed, in.perr = build() })
	return in.parsed, in.perr
}

// Client returns the instance's egress client, built once by build.
func (in *Instance) Client(build func() *http.Client) *http.Client {
	in.cmu.Lock()
	defer in.cmu.Unlock()
	if in.client == nil {
		in.client = build()
	}
	return in.client
}

// Static is an in-process connector with one fixed instance: S13's interface, which the Gate's
// built-ins (fake) and /mcp/native's tool source use.
type Static interface {
	Name() string
	Type() string
	Tools() []ToolSpec
	Execute(ctx context.Context, call Call) (Result, error)
}

// Builtin binds a connector without config or credentials (fake) to its one instance, named
// after its type.
func Builtin(c Connector) Static {
	return &builtin{c: c, inst: &Instance{Name: c.Type(), Type: c.Type(), Config: json.RawMessage(`{}`)}}
}

type builtin struct {
	c    Connector
	inst *Instance
}

func (b *builtin) Name() string { return b.inst.Name }
func (b *builtin) Type() string { return b.c.Type() }
func (b *builtin) Tools() []ToolSpec {
	ts, _ := b.c.Tools(context.Background(), b.inst)
	return ts
}
func (b *builtin) Execute(ctx context.Context, call Call) (Result, error) {
	return b.c.Execute(ctx, b.inst, call)
}

// Unwrap returns the connector a Builtin wraps.
func Unwrap(s Static) (Connector, bool) {
	b, ok := s.(*builtin)
	if !ok {
		return nil, false
	}
	return b.c, true
}
