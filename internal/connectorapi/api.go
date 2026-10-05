// Package connectorapi mounts S16's connector routes (contract §9): connector CRUD, write-only
// credentials, the connection test and the connector types. Like policyapi, it lives outside
// httpx (which can't import identity) and holds only a vault.Writer: no handler can read a
// credential back.
//
// Access: a Console session by role (viewers read, admins change) or an org admin key (scope
// admin). Agent keys are refused on every route, reads included: agents can't list, create or
// read connectors.
package connectorapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

// AgentKeyMessage is insufficient_scope's message for an agent key on any connector route.
const AgentKeyMessage = "Agents can't list, create or read connectors. Use the Console or an admin key."

// Limits.
const (
	TestsPerMinute  = 10       // per connector
	MaxCreateBody   = 96 << 10 // a 64 KiB config, its name and description
	MaxSecretBody   = 20 << 10 // the credential document is at most 16 KiB
	TestTimeout     = 20 * time.Second
	MaxDescription  = 500
	defaultPageSize = 25
)

// Vault is what the routes need from the vault: write-only, plus the in-transaction delete that
// connector deletion uses.
type Vault interface {
	vault.Writer
	DeleteTx(ctx context.Context, tx pgx.Tx, con store.Connector, actor vault.Actor) (*string, error)
}

// API is the connector routes' dependencies.
type API struct {
	DB       *store.DB
	Registry *connector.Registry
	Vault    Vault // nil: credential routes answer vault_unavailable
	Ledger   *ledger.Ledger
	Log      *slog.Logger
	Now      func() time.Time
	// Decoder is the decoder's supervisor (S20): connector writes keep its slot rows in sync, and
	// the decoder routes drop slots through it. nil: the decoder isn't running here.
	Decoder DecoderControl

	tests *httpx.Limiter

	catOnce sync.Once
	cat     *catalogCache
}

// Routes returns the connector routes: S16's nine, S17's setup record and S20's two decoder routes.
func Routes(a *API) []httpx.Route {
	if a.Log == nil {
		a.Log = slog.Default()
	}
	a.tests = httpx.NewLimiter(TestsPerMinute, time.Minute, a.Now)
	admin := []string{httpx.ScopeAdmin}
	r := func(method, pattern, op string, min httpx.Role, h httpx.HandlerFunc) httpx.Route {
		return httpx.Route{Method: method, Pattern: pattern, OperationID: op, Auth: httpx.SessionOrKey, MinRole: min, Scopes: admin,
			AgentKeyMessage: AgentKeyMessage, Handler: h}
	}
	routes := []httpx.Route{
		r("GET", "/v1/connectors", "listConnectors", identity.RoleViewer, a.list),
		r("POST", "/v1/connectors", "createConnector", identity.RoleAdmin, a.create),
		r("GET", "/v1/connectors/{id}", "getConnector", identity.RoleViewer, a.get),
		r("PATCH", "/v1/connectors/{id}", "updateConnector", identity.RoleAdmin, a.patch),
		r("DELETE", "/v1/connectors/{id}", "deleteConnector", identity.RoleAdmin, a.delete),
		r("PUT", "/v1/connectors/{id}/credentials", "setConnectorCredentials", identity.RoleAdmin, a.putCredentials),
		r("DELETE", "/v1/connectors/{id}/credentials", "deleteConnectorCredentials", identity.RoleAdmin, a.deleteCredentials),
		r("POST", "/v1/connectors/{id}/test", "testConnector", identity.RoleAdmin, a.test),
		r("POST", "/v1/connectors/{id}/initialized", "recordConnectorInitialized", identity.RoleAdmin, a.initialized),
		r("GET", "/v1/connector-types", "listConnectorTypes", identity.RoleViewer, a.types),
		r("GET", "/v1/connector-types/{type}", "getConnectorType", identity.RoleViewer, a.connectorType),
	}
	routes = append(routes, DecoderRoutes(a)...)
	for i := range routes {
		rt := &routes[i]
		switch rt.OperationID {
		case "createConnector":
			rt.Idempotency, rt.MaxBody, rt.Purpose = httpx.IdemOptional, MaxCreateBody, "adding connectors"
		case "updateConnector":
			rt.MaxBody, rt.Purpose = MaxCreateBody, "changing connectors"
		case "deleteConnector":
			rt.Purpose = "removing connectors"
		case "setConnectorCredentials":
			// The body holds a secret: never stored for replay (PUT has no idempotency store) and
			// never logged (httpx logs no bodies).
			rt.MaxBody, rt.Purpose = MaxSecretBody, "storing credentials"
		case "deleteConnectorCredentials":
			rt.Purpose = "removing credentials"
		case "recordConnectorInitialized":
			rt.Idempotency, rt.MaxBody, rt.Purpose = httpx.IdemOptional, 4<<10, "recording database setup"
		case "testConnector":
			rt.Idempotency, rt.MaxBody, rt.Purpose, rt.Timeout = httpx.IdemNever, 1<<10, "testing connectors", TestTimeout+5*time.Second
		}
	}
	return routes
}

// --- objects ---

// Tool is a connector's tool in the connector object.
type Tool struct {
	Object      string   `json:"object"` // "tool"
	Name        string   `json:"name"`   // qualified
	Description string   `json:"description,omitempty"`
	SideEffect  string   `json:"side_effect"`
	Idempotent  bool     `json:"idempotent"`
	Levels      []string `json:"levels"`
}

// Health is the last test result.
type Health struct {
	Status   string     `json:"status"` // unknown | pass | warn | fail
	TestedAt *time.Time `json:"tested_at"`
	Reason   string     `json:"reason,omitempty"` // S30: why a breaker disabled the connector (livemode_detected)
}

// Connector is the connector object.
type Connector struct {
	Object      string          `json:"object"` // "connector"
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Status      string          `json:"status"`
	Config      json.RawMessage `json:"config"`
	ConfigHash  string          `json:"config_hash"`
	Tools       []Tool          `json:"tools"`
	Credentials vault.Meta      `json:"credentials"`
	Health      Health          `json:"health"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Deed        *string         `json:"deed,omitempty"` // the admin deed of the change that answered with this object
}

// TestResult is the connector_test object.
type TestResult struct {
	Object     string            `json:"object"` // "connector_test"
	Connector  string            `json:"connector"`
	Status     string            `json:"status"`
	DurationMS int64             `json:"duration_ms"`
	Checks     []connector.Check `json:"checks"`
}

// --- helpers ---

func caller(r *http.Request) (*identity.Principal, vault.Actor, error) {
	p, ok := identity.PrincipalFrom(r.Context())
	if !ok || p.OrgID.IsZero() {
		return nil, vault.Actor{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	a, err := identity.ActorOf(p)
	if err != nil {
		return nil, vault.Actor{}, err
	}
	return p, vault.Actor{Ref: a.Ref, By: a.By}, nil
}

func principal(r *http.Request) (*identity.Principal, error) {
	p, ok := identity.PrincipalFrom(r.Context())
	if !ok || p.OrgID.IsZero() {
		return nil, httpx.E(httpx.CodeAuthenticationRequired)
	}
	if p.Kind == httpx.KindAgentKey {
		e := httpx.ErrInsufficientScope(httpx.ScopeAdmin)
		e.Message = AgentKeyMessage
		return nil, e
	}
	return p, nil
}

func errNotFound(ref string) *httpx.Error {
	return httpx.E(httpx.CodeNotFound, httpx.Msgf("There's no connector %s in this org. List them with deedglass connector list.", clip(ref, 64)))
}

func clip(s string, n int) string {
	if len(s) > n || !utf8.ValidString(s) {
		return "(that id)"
	}
	return s
}

// lookup finds a live connector by id (con_…) or name.
func lookup(ctx context.Context, tx pgx.Tx, ref string, forUpdate bool) (store.Connector, error) {
	var (
		c   store.Connector
		err error
	)
	if strings.HasPrefix(ref, "con_") {
		if ids.Check(ids.Connector, ref) != nil {
			return c, errNotFound(ref)
		}
		c, err = store.GetConnector(ctx, tx, ref, forUpdate)
	} else {
		if !connector.ReConnector.MatchString(ref) {
			return c, errNotFound(ref)
		}
		c, err = store.ConnectorByName(ctx, tx, ref, forUpdate)
	}
	if errors.Is(err, store.ErrNotFound) {
		return c, errNotFound(ref)
	}
	return c, err
}

// object builds the connector object. Its tools come from the type (a type this build doesn't
// serve lists none).
func (a *API) object(ctx context.Context, tx pgx.Tx, org ids.OrgID, c store.Connector) (Connector, error) {
	out := Connector{Object: "connector", ID: c.ID, Type: c.Type, Name: c.Name, Description: c.Description, Status: c.Status,
		Config: c.Config, ConfigHash: c.ConfigHash, Tools: []Tool{}, CreatedAt: c.CreatedAt.UTC(), UpdatedAt: c.UpdatedAt.UTC()}
	var h struct {
		Status   string     `json:"status"`
		TestedAt *time.Time `json:"tested_at"`
	}
	_ = json.Unmarshal(c.Health, &h)
	out.Health = Health{Status: h.Status, TestedAt: h.TestedAt}
	if out.Health.Status == "" {
		out.Health.Status = "unknown"
	}
	if impl, ok := a.Registry.Type(c.Type); ok {
		inst, err := a.Registry.Instance(org, c)
		if err == nil {
			specs, err := impl.Tools(ctx, inst)
			if err == nil {
				levels := impl.Info().Levels
				for _, s := range specs {
					if s.Internal {
						continue
					}
					out.Tools = append(out.Tools, Tool{Object: "tool", Name: c.Name + "." + s.Name, Description: s.Description,
						SideEffect: string(s.SideEffect), Idempotent: s.Idempotent, Levels: levels})
				}
			}
		}
	}
	m, err := vault.MetaTx(ctx, tx, c.ID)
	if err != nil {
		return out, err
	}
	out.Credentials = m
	return out, nil
}

// --- handlers ---

func (a *API) list(w http.ResponseWriter, r *http.Request) error {
	p, err := principal(r)
	if err != nil {
		return err
	}
	for k := range r.URL.Query() {
		if k != "limit" && k != "cursor" {
			return httpx.ErrValidation(k, "`"+clip(k, 32)+"` isn't a filter here. Use limit and cursor.")
		}
	}
	lp, err := httpx.ParseListParams(r)
	if err != nil {
		return err
	}
	after := ""
	if lp.Cursor != nil {
		if ids.Check(ids.Connector, lp.Cursor.ID) != nil {
			return httpx.ErrValidation("cursor", "`cursor` isn't a cursor this API issued. Pass the next_cursor value from the previous page unchanged.")
		}
		after = lp.Cursor.ID
	}
	var (
		out  []Connector
		more bool
	)
	err = a.DB.OrgReadTx(r.Context(), p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, m, err := store.ListConnectors(ctx, tx, after, lp.Limit)
		if err != nil {
			return err
		}
		more = m
		out = make([]Connector, 0, len(rows))
		for _, c := range rows {
			o, err := a.object(ctx, tx, p.OrgID, c)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return nil
	})
	if err != nil {
		return err
	}
	var next *httpx.Cursor
	if more && len(out) > 0 {
		if next, err = httpx.NewCursor(out[len(out)-1].ID); err != nil {
			return err
		}
	}
	return httpx.WriteJSON(w, http.StatusOK, httpx.NewList(out, more, next))
}

func (a *API) get(w http.ResponseWriter, r *http.Request) error {
	p, err := principal(r)
	if err != nil {
		return err
	}
	var out Connector
	err = a.DB.OrgReadTx(r.Context(), p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		c, err := lookup(ctx, tx, r.PathValue("id"), false)
		if err != nil {
			return err
		}
		out, err = a.object(ctx, tx, p.OrgID, c)
		return err
	})
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

type createBody struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Config      json.RawMessage `json:"config"`
}

// validate checks a config with its type and returns it normalized with its hash, or the error
// envelope (destination_blocked for a blocked base_url, else validation_failed on the first
// field, counting the rest).
func (a *API) validate(ctx context.Context, impl connector.Connector, cfg json.RawMessage) (json.RawMessage, string, error) {
	if len(cfg) == 0 || string(cfg) == "null" {
		cfg = json.RawMessage(`{}`)
	}
	norm, errs := impl.Validate(ctx, a.Registry.Env(), cfg)
	if len(errs) > 0 {
		first := errs[0]
		msg := first.Message
		if len(errs) > 1 {
			msg += fmt.Sprintf(" (1 of %d problems; fix it and send the config again to see the next.)", len(errs))
		}
		for _, e := range errs {
			if e.Code == connector.CodeDestinationBlocked {
				return nil, "", httpx.E(httpx.CodeDestinationBlocked, httpx.Param(e.Param), httpx.Msg(e.Message))
			}
		}
		return nil, "", httpx.ErrValidation(first.Param, msg)
	}
	hash, err := deed.DigestJSON(norm)
	if err != nil {
		return nil, "", err
	}
	return norm, hash, nil
}

func (a *API) create(w http.ResponseWriter, r *http.Request) error {
	p, actor, err := caller(r)
	if err != nil {
		return err
	}
	var in createBody
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	impl, ok := a.Registry.Type(in.Type)
	if !ok {
		names := []string{}
		for _, t := range a.Registry.Types() {
			names = append(names, t.Type())
		}
		return httpx.ErrValidation("type", fmt.Sprintf("type must be one of %s. More types arrive in later releases.", strings.Join(names, ", ")))
	}
	if err := checkName(in.Name, a.Registry); err != nil {
		return err
	}
	desc, err := checkDescription(in.Description)
	if err != nil {
		return err
	}
	norm, hash, err := a.validate(r.Context(), impl, in.Config)
	if err != nil {
		return err
	}
	id := ids.New[ids.ConnectorKind]().String()
	var out Connector
	err = a.DB.OrgTx(r.Context(), p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		err := store.InsertConnector(ctx, tx, store.Connector{ID: id, OrgID: p.OrgID.String(), Type: in.Type, Name: in.Name, Description: desc,
			Config: norm, ConfigHash: hash, Status: "active", CreatedBy: actor.By})
		if errors.Is(err, store.ErrConflict) {
			return httpx.E(httpx.CodeConflict, httpx.Param("name"), httpx.Msgf("A connector named %s already exists in this org. Pick another name, or update that one.", in.Name))
		}
		if e := slackTaken(err); e != nil {
			return e
		}
		if err != nil {
			return err
		}
		env, err := a.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminConnectorCreated, Actor: actor.Ref,
			Target: &deed.Ref{Type: "connector", ID: id}, RequestID: httpx.RequestID(ctx),
			Data: map[string]any{"type": in.Type, "connector": in.Name, "config_hash": hash}})
		if err != nil {
			return err
		}
		c, err := store.GetConnector(ctx, tx, id, false)
		if err != nil {
			return err
		}
		out, err = a.object(ctx, tx, p.OrgID, c)
		out.Deed = &env.ID
		return err
	})
	if err != nil {
		return err
	}
	a.syncDecoder(r.Context())
	return httpx.WriteJSON(w, http.StatusCreated, out)
}

func checkName(name string, reg *connector.Registry) error {
	if !connector.ReConnector.MatchString(name) {
		return httpx.ErrValidation("name", "Connector names use lowercase letters, digits and hyphens, 2 to 32 characters, starting with a letter, like orders.")
	}
	if connector.Reserved(name, reg.Env().Dev()) || reg.IsBuiltin(name) {
		return httpx.ErrValidation("name", fmt.Sprintf("%s is reserved for Deedglass. Pick another name, like orders.", name))
	}
	return nil
}

func checkDescription(d string) (string, error) {
	d = strings.TrimSpace(d)
	if utf8.RuneCountInString(d) > MaxDescription || !utf8.ValidString(d) || strings.ContainsFunc(d, func(r rune) bool { return r < 0x20 && r != '\n' || r == 0x7f }) {
		return "", httpx.ErrValidation("description", "The description must be at most 500 characters, without control characters.")
	}
	// S34 (SR-013): a description is shown to every member and sealed in admin deeds, so text the
	// vault's scrubber recognizes as a credential (a Stripe, Slack, GitHub or Deedglass key, …) is
	// refused instead of stored. The message never echoes it.
	if vault.Scrub(d) != d {
		return "", httpx.ErrValidation("description", "The description looks like it contains a credential. Remove it; store credentials with deedglass connector credentials set.")
	}
	return d, nil
}

type patchBody struct {
	Description        *string         `json:"description"`
	Status             *string         `json:"status"`
	Config             json.RawMessage `json:"config"`
	ExpectedConfigHash *string         `json:"expected_config_hash"`
}

func (a *API) patch(w http.ResponseWriter, r *http.Request) error {
	p, actor, err := caller(r)
	if err != nil {
		return err
	}
	var in patchBody
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	if in.Description == nil && in.Status == nil && len(in.Config) == 0 {
		return httpx.ErrValidation("", "Send at least one of description, status or config.")
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "disabled" {
		return httpx.ErrValidation("status", "status must be active or disabled.")
	}
	var desc string
	if in.Description != nil {
		if desc, err = checkDescription(*in.Description); err != nil {
			return err
		}
	}
	var out Connector
	var forget string
	err = a.DB.OrgTx(r.Context(), p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		c, err := lookup(ctx, tx, r.PathValue("id"), true)
		if err != nil {
			return err
		}
		if in.ExpectedConfigHash != nil && *in.ExpectedConfigHash != c.ConfigHash { //deedglass:allow-compare a config digest an admin already holds, not a secret
			return httpx.E(httpx.CodeConflict, httpx.Param("expected_config_hash"),
				httpx.Msgf("The config changed since you read it (it is %s now). Fetch the connector, apply your change to it, and send it again.", c.ConfigHash))
		}
		changed := []string{}
		if in.Description != nil && desc != c.Description {
			c.Description = desc
			changed = append(changed, "description")
		}
		if in.Status != nil && *in.Status != c.Status {
			c.Status = *in.Status
			changed = append(changed, "status")
		}
		if len(in.Config) > 0 {
			impl, ok := a.Registry.Type(c.Type)
			if !ok {
				return httpx.ErrValidation("config", fmt.Sprintf("This build doesn't serve connectors of type %s, so their config can't be checked.", c.Type))
			}
			norm, hash, err := a.validate(ctx, impl, in.Config)
			if err != nil {
				return err
			}
			if hash != c.ConfigHash { //deedglass:allow-compare a digest of a request or config, not a secret
				c.Config, c.ConfigHash = norm, hash
				changed = append(changed, "config")
			}
		}
		if len(changed) > 0 {
			c.UpdatedBy = actor.By
			if err := store.UpdateConnector(ctx, tx, c); err != nil {
				if e := slackTaken(err); e != nil {
					return e
				}
				return err
			}
			env, err := a.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminConnectorUpdated, Actor: actor.Ref,
				Target: &deed.Ref{Type: "connector", ID: c.ID}, RequestID: httpx.RequestID(ctx),
				Data: map[string]any{"type": c.Type, "connector": c.Name, "config_hash": c.ConfigHash, "changed": changed, "status": c.Status}})
			if err != nil {
				return err
			}
			out.Deed = &env.ID
			forget = c.ID
		}
		row, err := store.GetConnector(ctx, tx, c.ID, false)
		if err != nil {
			return err
		}
		d := out.Deed
		out, err = a.object(ctx, tx, p.OrgID, row)
		out.Deed = d
		return err
	})
	if err != nil {
		return err
	}
	if forget != "" {
		a.Registry.Forget(forget)
		if c := (store.Connector{ID: out.ID, Type: out.Type, Config: out.Config}); c.Type == "postgres" && (!witnessed(c) || out.Status != "active") {
			a.retireDecoder(r.Context(), p.OrgID, c, "not_witnessed")
		}
		a.syncDecoder(r.Context())
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func (a *API) delete(w http.ResponseWriter, r *http.Request) error {
	p, actor, err := caller(r)
	if err != nil {
		return err
	}
	var id string
	var deeds []string
	// A witnessed connector's slot is dropped first, while its credential still exists (S20).
	var pre store.Connector
	err = a.DB.OrgReadTx(r.Context(), p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		c, err := lookup(ctx, tx, r.PathValue("id"), false)
		if err != nil {
			return err
		}
		pre = c
		busy, err := store.ConnectorExecuting(ctx, tx, c.Name)
		if err == nil && busy {
			return httpx.E(httpx.CodeConflict, httpx.Msgf("An action on %s is running right now. Wait for it to finish (deedglass actions list --connector %s), then delete the connector.", c.Name, c.Name))
		}
		return err
	})
	if err != nil {
		return err
	}
	a.retireDecoder(r.Context(), p.OrgID, pre, "connector_deleted")
	err = a.DB.OrgTx(r.Context(), p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		c, err := lookup(ctx, tx, r.PathValue("id"), true)
		if err != nil {
			return err
		}
		id = c.ID
		busy, err := store.ConnectorExecuting(ctx, tx, c.Name)
		if err != nil {
			return err
		}
		if busy {
			return httpx.E(httpx.CodeConflict, httpx.Msgf("An action on %s is running right now. Wait for it to finish (deedglass actions list --connector %s), then delete the connector.", c.Name, c.Name))
		}
		if a.Vault != nil {
			d, err := a.Vault.DeleteTx(ctx, tx, c, actor)
			if err != nil {
				return err
			}
			if d != nil {
				deeds = append(deeds, *d)
			}
		} else if _, err := store.DeleteCredential(ctx, tx, c.ID); err != nil {
			return err
		}
		if err := store.SoftDeleteConnector(ctx, tx, c.ID, actor.By); err != nil {
			return err
		}
		env, err := a.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminConnectorDeleted, Actor: actor.Ref,
			Target: &deed.Ref{Type: "connector", ID: c.ID}, RequestID: httpx.RequestID(ctx),
			Data: map[string]any{"type": c.Type, "connector": c.Name, "config_hash": c.ConfigHash}})
		if err != nil {
			return err
		}
		deeds = append(deeds, env.ID)
		return nil
	})
	if err != nil {
		return err
	}
	a.Registry.Forget(id)
	a.syncDecoder(r.Context())
	return httpx.WriteJSON(w, http.StatusOK, map[string]any{"object": "connector", "id": id, "deleted": true, "deed": deeds[len(deeds)-1]})
}

type credentialsBody struct {
	Kind   string            `json:"kind"`
	Secret map[string]string `json:"secret"`
}

func (a *API) putCredentials(w http.ResponseWriter, r *http.Request) error {
	p, actor, err := caller(r)
	if err != nil {
		return err
	}
	if a.Vault == nil {
		return httpx.E(httpx.CodeVaultUnavailable, httpx.Msg("DEEDGLASS_VAULT_KEY isn't set on this server, so it can't store credentials. Set it and restart."))
	}
	var in credentialsBody
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	c, err := a.connectorRow(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	impl, ok := a.Registry.Type(c.Type)
	if ok {
		kinds := impl.Info().CredentialKinds
		allowed := false
		names := make([]string, 0, len(kinds))
		for _, k := range kinds {
			names = append(names, k.Kind)
			allowed = allowed || k.Kind == in.Kind
		}
		if !allowed {
			return httpx.ErrValidation("kind", fmt.Sprintf("A %s connector takes credentials of kind %s.", c.Type, strings.Join(names, ", ")))
		}
		if want := configAuthKind(c.Config); want != "" && want != "none" && want != in.Kind {
			return httpx.ErrValidation("kind", fmt.Sprintf("This connector's auth.kind is %s, so its credential must be a %s. Change auth.kind first to store another kind.", want, want))
		}
	}
	if cc, ok := impl.(connector.CredentialChecker); ok {
		// S30: refused before encryption, so the value is never stored, logged or echoed.
		if ref := cc.CheckCredential(in.Kind, in.Secret); ref != nil {
			if ref.Code != "" {
				return httpx.E(httpx.Code(ref.Code), httpx.Msg(ref.Message), httpx.Param(ref.Param))
			}
			return httpx.ErrValidation(ref.Param, ref.Message)
		}
	}
	fields := make(map[string][]byte, len(in.Secret))
	for k, v := range in.Secret {
		fields[k] = []byte(v)
	}
	defer func() {
		for _, v := range fields {
			clear(v)
		}
	}()
	ref := vault.Ref{Org: p.OrgID, Connector: ids.MustParse[ids.ConnectorKind](c.ID)}
	m, err := a.Vault.Put(r.Context(), ref, in.Kind, fields, actor)
	var fe *vault.FieldError
	if errors.As(err, &fe) {
		return httpx.ErrValidation(fe.Param, fe.Message)
	}
	if err != nil {
		return a.vaultErr(err)
	}
	a.syncDecoder(r.Context())
	return httpx.WriteJSON(w, http.StatusOK, m)
}

func (a *API) deleteCredentials(w http.ResponseWriter, r *http.Request) error {
	p, actor, err := caller(r)
	if err != nil {
		return err
	}
	if a.Vault == nil {
		return httpx.E(httpx.CodeVaultUnavailable, httpx.Msg("DEEDGLASS_VAULT_KEY isn't set on this server. Set it and restart."))
	}
	c, err := a.connectorRow(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	m, err := a.Vault.Delete(r.Context(), vault.Ref{Org: p.OrgID, Connector: ids.MustParse[ids.ConnectorKind](c.ID)}, actor)
	if err != nil {
		return a.vaultErr(err)
	}
	m.Set = false
	return httpx.WriteJSON(w, http.StatusOK, m)
}

func (a *API) vaultErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return errNotFound("")
	}
	var uk *vault.UnknownKEKError
	if errors.As(err, &uk) {
		return httpx.E(httpx.CodeVaultUnavailable, httpx.Msg(uk.Error()), httpx.Cause(err))
	}
	return err
}

// connectorRow reads one live connector (by id or name).
func (a *API) connectorRow(ctx context.Context, org ids.OrgID, ref string) (store.Connector, error) {
	var c store.Connector
	err := a.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = lookup(ctx, tx, ref, false)
		return err
	})
	return c, err
}

func configAuthKind(cfg json.RawMessage) string {
	var c struct {
		Auth struct {
			Kind string `json:"kind"`
		} `json:"auth"`
	}
	_ = json.Unmarshal(cfg, &c)
	return c.Auth.Kind
}

func (a *API) test(w http.ResponseWriter, r *http.Request) error {
	p, err := principal(r)
	if err != nil {
		return err
	}
	if _, err := identity.ActorOf(p); err != nil {
		return err
	}
	c, err := a.connectorRow(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if ok, retry := a.tests.Allow(c.ID); !ok {
		return httpx.ErrRateLimited(int(retry.Seconds() + 1))
	}
	impl, ok := a.Registry.Type(c.Type)
	if !ok {
		return httpx.E(httpx.CodeConnectorUnavailable, httpx.Msgf("This build doesn't serve connectors of type %s.", c.Type))
	}
	inst, err := a.Registry.Instance(p.OrgID, c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), TestTimeout)
	defer cancel()
	rep, err := impl.Test(vault.WithRedactor(ctx, vault.NewRedactor()), inst)
	if err != nil {
		return err
	}
	for i := range rep.Checks {
		rep.Checks[i].Detail = vault.Scrub(rep.Checks[i].Detail)
		rep.Checks[i].Fix = vault.Scrub(rep.Checks[i].Fix)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if a.Now != nil {
		now = a.Now().UTC().Truncate(time.Second)
	}
	health, _ := json.Marshal(map[string]any{"status": string(rep.Status), "tested_at": now})
	if err := a.DB.OrgTx(r.Context(), p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		return store.SetConnectorHealth(ctx, tx, c.ID, health)
	}); err != nil {
		a.Log.WarnContext(r.Context(), "connectors: could not record the test result", "connector_id", c.ID, "err", err.Error())
	}
	return httpx.WriteJSON(w, http.StatusOK, TestResult{Object: "connector_test", Connector: c.ID, Status: string(rep.Status),
		DurationMS: rep.Duration.Milliseconds(), Checks: rep.Checks})
}

func (a *API) types(w http.ResponseWriter, r *http.Request) error {
	if _, err := principal(r); err != nil {
		return err
	}
	c := a.catalog()
	return writeCached(w, r, c.list, c.listTag)
}

// connectorType is GET /v1/connector-types/{type} (S30): one catalog entry, or 404.
func (a *API) connectorType(w http.ResponseWriter, r *http.Request) error {
	if _, err := principal(r); err != nil {
		return err
	}
	c := a.catalog()
	t := r.PathValue("type")
	b, ok := c.one[t]
	if !ok {
		return httpx.E(httpx.CodeNotFound, httpx.Msgf("There is no connector type %q. deedglass connector types lists them.", clipName(t)), httpx.Param("type"))
	}
	return writeCached(w, r, b, c.oneTag[t])
}

func clipName(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

// catalogCache is the connector catalog, computed once (S30 §3.4): every type's TypeInfo in title
// order, the list body and each type's body with its ETag.
type catalogCache struct {
	list    []byte
	listTag string
	one     map[string][]byte
	oneTag  map[string]string
}

// Catalog returns the catalog in its deterministic order (by title, then type).
func Catalog(reg *connector.Registry) []connector.TypeInfo {
	out := []connector.TypeInfo{}
	for _, t := range reg.Types() {
		out = append(out, t.Info())
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func (a *API) catalog() *catalogCache {
	a.catOnce.Do(func() {
		infos := Catalog(a.Registry)
		c := &catalogCache{one: map[string][]byte{}, oneTag: map[string]string{}}
		c.list, _ = json.Marshal(httpx.NewList(infos, false, nil))
		c.listTag = etagOf(c.list)
		for _, t := range infos {
			b, _ := json.Marshal(t)
			c.one[t.Type], c.oneTag[t.Type] = b, etagOf(b)
		}
		a.cat = c
	})
	return a.cat
}

func etagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return `"sha256:` + hex.EncodeToString(sum[:]) + `"`
}

// writeCached answers a catalog body with its ETag, or 304 when If-None-Match names it.
func writeCached(w http.ResponseWriter, r *http.Request, body []byte, tag string) error {
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "private, max-age=300")
	for _, m := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		m = strings.TrimPrefix(strings.TrimSpace(m), "W/")
		if m == tag || m == "*" {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(append(body, '\n'))
	return err
}

// InitializedBody is what deedglass connector postgres init records after it applied a plan (S17
// §3.9 step 6): the plan's identity and size, never its SQL and never a credential.
type InitializedBody struct {
	PlanSHA256    string   `json:"plan_sha256"`
	Changes       int      `json:"changes"`
	Database      string   `json:"database"`
	ServerVersion string   `json:"server_version"`
	Provider      string   `json:"provider"`
	RolesCreated  []string `json:"roles_created"`
}

// Setup is the connector_setup object: the sealed record of one applied setup plan.
type Setup struct {
	Object     string `json:"object"` // "connector_setup"
	Connector  string `json:"connector"`
	PlanSHA256 string `json:"plan_sha256"`
	Changes    int    `json:"changes"`
	Deed       string `json:"deed"`
}

var (
	reDigest   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reDBName   = regexp.MustCompile(`^[A-Za-z0-9_$.-]{1,63}$`)
	reVersion  = regexp.MustCompile(`^[0-9]{1,2}(\.[0-9]{1,4}){0,2}$`)
	reProvider = regexp.MustCompile(`^[a-z]{1,16}$`)
)

// initialized seals connector.postgres_initialized for a postgres connector.
func (a *API) initialized(w http.ResponseWriter, r *http.Request) error {
	p, actor, err := caller(r)
	if err != nil {
		return err
	}
	var in InitializedBody
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	switch {
	case !reDigest.MatchString(in.PlanSHA256):
		return httpx.ErrValidation("plan_sha256", "plan_sha256 must be sha256: and 64 lowercase hex digits.")
	case in.Changes < 1 || in.Changes > 10000:
		return httpx.ErrValidation("changes", "changes must be between 1 and 10000.")
	case !reDBName.MatchString(in.Database):
		return httpx.ErrValidation("database", "database must be a database name of at most 63 characters.")
	case !reVersion.MatchString(in.ServerVersion):
		return httpx.ErrValidation("server_version", "server_version must look like 18.6.")
	case !reProvider.MatchString(in.Provider):
		return httpx.ErrValidation("provider", "provider must be a lowercase word, like local or rds.")
	}
	for _, role := range in.RolesCreated {
		if role != "deedglass_owner" && role != "deedglass_agent" && role != "deedglass_witness" {
			return httpx.ErrValidation("roles_created", "roles_created lists only deedglass_owner, deedglass_agent and deedglass_witness.")
		}
	}
	if in.RolesCreated == nil {
		in.RolesCreated = []string{}
	}
	var out Setup
	err = a.DB.OrgTx(r.Context(), p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		c, err := lookup(ctx, tx, r.PathValue("id"), true)
		if err != nil {
			return err
		}
		if c.Type != "postgres" {
			return httpx.ErrValidation("id", fmt.Sprintf("%s is a %s connector. Only postgres connectors record a database setup.", c.Name, c.Type))
		}
		env, err := a.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminConnectorPostgresInitialized, Actor: actor.Ref,
			Target: &deed.Ref{Type: "connector", ID: c.ID}, RequestID: httpx.RequestID(ctx),
			Data: map[string]any{"type": c.Type, "connector": c.Name, "config_hash": c.ConfigHash, "plan_sha256": in.PlanSHA256,
				"changes": in.Changes, "database": in.Database, "server_version": in.ServerVersion, "provider": in.Provider, "roles_created": in.RolesCreated}})
		if err != nil {
			return err
		}
		out = Setup{Object: "connector_setup", Connector: c.ID, PlanSHA256: in.PlanSHA256, Changes: in.Changes, Deed: env.ID}
		return nil
	})
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusCreated, out)
}

// slackTaken is S29's 409 for a second Slack connector receiving approvals (migration 0026).
func slackTaken(err error) error {
	switch {
	case errors.Is(err, store.ErrSlackAppTaken):
		return httpx.E(httpx.CodeConflict, httpx.Param("config.approvals.enabled"),
			httpx.Msg("Another Deedglass connector already receives approvals for this Slack app (the same team_id and app_id). Turn approvals off there first, or use another Slack app."))
	case errors.Is(err, store.ErrSlackApprovalsTaken):
		return httpx.E(httpx.CodeConflict, httpx.Param("config.approvals.enabled"),
			httpx.Msg("This org already has a Slack connector that receives approvals. Turn approvals off on that connector first."))
	}
	return nil
}
