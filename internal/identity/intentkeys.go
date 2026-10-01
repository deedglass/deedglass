package identity

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/intent"
	"github.com/deedglass/deedglass/internal/keys"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Intent keys (S09 §3.6). An agent has at most two, so a new key can be deployed before the old
// one is removed. Every change is sealed as an admin deed and announced on deedglass_identity in the
// same transaction, so every instance's cache drops the agent's old keys.

// IntentKeyObject is the intent_key object.
type IntentKeyObject struct {
	Object    string     `json:"object"` // "intent_key"
	KID       string     `json:"kid"`
	Agent     string     `json:"agent"`
	PublicKey string     `json:"public_key"`
	CreatedAt httpx.Time `json:"created_at"`
	CreatedBy string     `json:"created_by"`
	Deed      *string    `json:"deed,omitempty"` // the agent.intent_key_added deed, on create
}

// IntentKeyDeleted is DELETE /v1/agents/{id}/intent-keys/{kid}'s answer.
type IntentKeyDeleted struct {
	Object  string  `json:"object"` // "intent_key"
	KID     string  `json:"kid"`
	Agent   string  `json:"agent"`
	Deleted bool    `json:"deleted"`
	Deed    *string `json:"deed"`
}

func keyObject(agent string, k IntentKey) IntentKeyObject {
	t, _ := time.Parse(time.RFC3339, k.CreatedAt)
	return IntentKeyObject{Object: "intent_key", KID: k.KID, Agent: agent, PublicKey: k.PublicKey, CreatedAt: httpx.Time(t), CreatedBy: k.CreatedBy}
}

// lockAgentIntent locks the agent row and returns its name, intent mode and keys.
func lockAgentIntent(ctx context.Context, tx pgx.Tx, org ids.OrgID, id string) (string, IntentMode, []IntentKey, error) {
	if ids.Check(ids.Agent, id) != nil {
		return "", "", nil, store.ErrNotFound
	}
	var (
		name, mode string
		raw        []byte
	)
	err := tx.QueryRow(ctx, `SELECT name, intent_mode, intent_keys FROM deedglass.agents WHERE org_id = $1 AND id = $2 FOR UPDATE`, org, id).
		Scan(&name, &mode, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil, store.ErrNotFound
	}
	if err != nil {
		return "", "", nil, err
	}
	ks, err := parseIntentKeys(raw)
	return name, IntentMode(mode), ks, err
}

// ListIntentKeys returns an agent's intent keys, oldest first.
func (s *Service) ListIntentKeys(ctx context.Context, org ids.OrgID, agentID string) ([]IntentKeyObject, error) {
	if ids.Check(ids.Agent, agentID) != nil {
		return nil, store.ErrNotFound
	}
	var out []IntentKeyObject
	err := s.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT intent_keys FROM deedglass.agents WHERE org_id = $1 AND id = $2`, org, agentID).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		ks, err := parseIntentKeys(raw)
		if err != nil {
			return err
		}
		out = make([]IntentKeyObject, 0, len(ks))
		for _, k := range ks {
			out = append(out, keyObject(agentID, k))
		}
		return nil
	})
	return out, err
}

// errPublicKey is the 422 for a public key that fails registration.
func errPublicKey(err error, agent string) *httpx.Error {
	next := " Generate a fresh key with deedglass agent intent keygen " + agent + "."
	var msg string
	switch {
	case errors.Is(err, keys.ErrSmallOrder):
		msg = "That public key is a small-order point, so a single signature would verify for every message." + next
	case errors.Is(err, keys.ErrNonCanonical):
		msg = "That public key isn't a canonical Ed25519 encoding, so two different strings would name the same key." + next
	case errors.Is(err, keys.ErrNotOnCurve):
		msg = "That public key isn't a point on the Ed25519 curve." + next
	default:
		msg = `The public key must be "ed25519:" followed by 43 base64url characters (32 bytes), like ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo.` + next
	}
	e := httpx.ErrValidation("public_key", msg)
	e.Status = http.StatusUnprocessableEntity
	return e
}

// AddIntentKey registers an intent public key ("ed25519:<base64url>") for an agent and seals
// agent.intent_key_added. A small-order or non-canonical key is 422; a duplicate kid or a third key
// is 409.
func (s *Service) AddIntentKey(ctx context.Context, a Actor, org ids.OrgID, agentID, publicKey string) (IntentKeyObject, error) {
	pub, kid, perr := ParseIntentPublicKey(publicKey)
	var out IntentKeyObject
	err := s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		name, _, ks, err := lockAgentIntent(ctx, tx, org, agentID)
		if err != nil {
			return err
		}
		if perr != nil {
			return errPublicKey(perr, name)
		}
		if slices.ContainsFunc(ks, func(k IntentKey) bool { return k.KID == kid }) {
			return httpx.E(httpx.CodeConflict, httpx.Msgf("%s already has the intent key %s. Keys are identified by their kid, so the same key can't be added twice.", name, ShortKID(kid)))
		}
		if len(ks) >= MaxIntentKeys {
			return httpx.E(httpx.CodeConflict, httpx.Msgf("%s already has two intent keys. Remove one first; two lets you rotate without downtime.", name))
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.now()`).Scan(&now); err != nil {
			return err
		}
		k := IntentKey{KID: kid, PublicKey: FormatIntentPublicKey(pub), CreatedAt: now.UTC().Format(time.RFC3339), CreatedBy: a.By, pub: ed25519.PublicKey(pub)}
		ks = append(ks, k)
		if err := writeIntentKeys(ctx, tx, org, agentID, ks); err != nil {
			return err
		}
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminAgentIntentKeyAdded, Actor: a.Ref,
			Target: &deed.Ref{Type: "agent", ID: agentID}, Agent: &deed.AgentRef{ID: agentID, Name: name}, Data: map[string]any{"kid": kid}})
		if err != nil {
			return err
		}
		if err := s.notifyAgent(ctx, tx, org, agentID); err != nil {
			return err
		}
		out = keyObject(agentID, k)
		out.Deed = d
		return nil
	})
	return out, err
}

// RemoveIntentKey removes one intent key and seals agent.intent_key_removed. Removing the last key
// of an agent that requires signed intents is 409: every call would be refused.
func (s *Service) RemoveIntentKey(ctx context.Context, a Actor, org ids.OrgID, agentID, kid string) (IntentKeyDeleted, error) {
	var out IntentKeyDeleted
	err := s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		name, mode, ks, err := lockAgentIntent(ctx, tx, org, agentID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(ks, func(k IntentKey) bool { return k.KID == kid })
		if i < 0 {
			return httpx.E(httpx.CodeNotFound, httpx.Msgf("%s has no intent key %s. List its keys: GET /v1/agents/%s/intent-keys.", name, clipText(kid, 40), agentID))
		}
		if len(ks) == 1 && mode == IntentRequired {
			return httpx.E(httpx.CodeConflict, httpx.Msgf("%s requires signed intents, so removing its last key would refuse every call. Add the new key first, or set intent_mode to optional.", name))
		}
		ks = slices.Delete(ks, i, i+1)
		if err := writeIntentKeys(ctx, tx, org, agentID, ks); err != nil {
			return err
		}
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminAgentIntentKeyRemoved, Actor: a.Ref,
			Target: &deed.Ref{Type: "agent", ID: agentID}, Agent: &deed.AgentRef{ID: agentID, Name: name}, Data: map[string]any{"kid": kid}})
		if err != nil {
			return err
		}
		if err := s.notifyAgent(ctx, tx, org, agentID); err != nil {
			return err
		}
		out = IntentKeyDeleted{Object: "intent_key", KID: kid, Agent: agentID, Deleted: true, Deed: d}
		return nil
	})
	return out, err
}

// setIntentMode changes the agent's intent mode inside PatchAgent's transaction (the row is already
// locked) and seals agent.intent_mode_changed {from, to}. The same mode seals nothing.
func (s *Service) setIntentMode(ctx context.Context, tx pgx.Tx, a Actor, org ids.OrgID, id, name string, from, to IntentMode) (*string, error) {
	if !to.Valid() {
		return nil, httpx.ErrValidation("intent_mode", "intent_mode must be off, optional or required.")
	}
	if to == from {
		return nil, nil
	}
	if to == IntentRequired {
		var n int
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.jsonb_array_length(intent_keys) FROM deedglass.agents WHERE org_id = $1 AND id = $2`, org, id).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, httpx.E(httpx.CodeConflict, httpx.Msgf("%s has no intent keys yet, so requiring signed intents would refuse every call. Register a key first: deedglass agent intent add %s.", name, name))
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE deedglass.agents SET intent_mode = $3 WHERE org_id = $1 AND id = $2`, org, id, string(to)); err != nil {
		return nil, err
	}
	d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminAgentIntentMode, Actor: a.Ref,
		Target: &deed.Ref{Type: "agent", ID: id}, Agent: &deed.AgentRef{ID: id, Name: name},
		Data: map[string]any{"from": string(from), "to": string(to)}})
	if err != nil {
		return nil, err
	}
	return d, s.notifyAgent(ctx, tx, org, id)
}

func writeIntentKeys(ctx context.Context, tx pgx.Tx, org ids.OrgID, id string, ks []IntentKey) error {
	if ks == nil {
		ks = []IntentKey{}
	}
	b, err := json.Marshal(ks)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE deedglass.agents SET intent_keys = $3::jsonb WHERE org_id = $1 AND id = $2`, org, id, string(b))
	return err
}

// notifyAgent tells every instance the agent changed (its keys or mode), with its current status.
func (s *Service) notifyAgent(ctx context.Context, tx pgx.Tx, org ids.OrgID, id string) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM deedglass.agents WHERE org_id = $1 AND id = $2`, org, id).Scan(&status); err != nil {
		return err
	}
	return s.notify(ctx, tx, identityMsg{T: "agent", Org: org.String(), ID: id, S: status})
}

// --- routes ---

// IntentRoutes are S09's intent-key routes and the conformance check (contract §9). Key management
// takes a Console session (by role) or an org admin key; the check takes an agent key.
func (s *Service) IntentRoutes(checker *Intents) []httpx.Route {
	admin := []string{httpx.ScopeAdmin}
	return []httpx.Route{
		{Method: "GET", Pattern: "/v1/agents/{id}/intent-keys", OperationID: "listIntentKeys", Auth: httpx.SessionOrKey, MinRole: RoleViewer,
			Scopes: admin, Handler: s.handleListIntentKeys},
		{Method: "POST", Pattern: "/v1/agents/{id}/intent-keys", OperationID: "addIntentKey", Auth: httpx.SessionOrKey, MinRole: RoleAdmin,
			Scopes: admin, Idempotency: httpx.IdemOptional, MaxBody: 4 << 10, Purpose: "registering intent keys", Handler: s.handleAddIntentKey},
		{Method: "DELETE", Pattern: "/v1/agents/{id}/intent-keys/{kid}", OperationID: "removeIntentKey", Auth: httpx.SessionOrKey, MinRole: RoleAdmin,
			Scopes: admin, Purpose: "removing intent keys", Handler: s.handleRemoveIntentKey},
		{Method: "POST", Pattern: "/v1/intents/verify", OperationID: "verifyIntent", Auth: httpx.AgentKey, Scopes: []string{httpx.ScopeActionsExecute},
			Idempotency: httpx.IdemNever, MaxBody: intentVerifyMaxBody, Handler: verifyHandler(checker)},
	}
}

func (s *Service) handleListIntentKeys(w http.ResponseWriter, r *http.Request) error {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	id := r.PathValue("id")
	ks, err := s.ListIntentKeys(r.Context(), p.OrgID, id)
	if err != nil {
		return notFoundAs(err, func() *httpx.Error { return ErrAgentNotFound(id) })
	}
	return httpx.WriteJSON(w, http.StatusOK, httpx.NewList(ks, false, nil))
}

func (s *Service) handleAddIntentKey(w http.ResponseWriter, r *http.Request) error {
	p, a, err := caller(r)
	if err != nil {
		return err
	}
	var in struct {
		PublicKey string `json:"public_key"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	if in.PublicKey == "" {
		return httpx.ErrValidation("public_key", `Send the public key as "ed25519:" followed by 43 base64url characters. deedglass agent intent keygen prints it.`)
	}
	id := r.PathValue("id")
	httpx.Annotate(r.Context(), "", id, "")
	out, err := s.AddIntentKey(r.Context(), a, p.OrgID, id, in.PublicKey)
	if err != nil {
		return notFoundAs(err, func() *httpx.Error { return ErrAgentNotFound(id) })
	}
	return httpx.WriteJSON(w, http.StatusCreated, out)
}

func (s *Service) handleRemoveIntentKey(w http.ResponseWriter, r *http.Request) error {
	p, a, err := caller(r)
	if err != nil {
		return err
	}
	if err := onlyQuery(r); err != nil {
		return err
	}
	id := r.PathValue("id")
	httpx.Annotate(r.Context(), "", id, "")
	out, err := s.RemoveIntentKey(r.Context(), a, p.OrgID, id, r.PathValue("kid"))
	if err != nil {
		return notFoundAs(err, func() *httpx.Error { return ErrAgentNotFound(id) })
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

// intentVerifyMaxBody bounds POST /v1/intents/verify: 256 KiB of arguments plus the envelope.
const intentVerifyMaxBody = 256<<10 + 4<<10

// IntentCheck is POST /v1/intents/verify's answer: what the checker accepted, with the nonce
// consumed.
type IntentCheck struct {
	Object        string      `json:"object"` // "intent_check"
	Signed        bool        `json:"signed"`
	IntentHash    *string     `json:"intent_hash"`
	KID           *string     `json:"kid"`
	Agent         string      `json:"agent"`
	Tool          string      `json:"tool"`
	ArgsDigest    string      `json:"args_digest"`
	OnBehalfOf    *string     `json:"on_behalf_of"`
	Session       *string     `json:"session"`
	ExpiresAt     *httpx.Time `json:"expires_at"`
	NonceConsumed bool        `json:"nonce_consumed"`
	Mode          IntentMode  `json:"intent_mode"`
}

// verifyHandler is POST /v1/intents/verify: a conformance check for SDK authors. It checks the
// header in every mode (off included), consumes the nonce exactly as the Gate will, and never acts.
func verifyHandler(c *Intents) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		p, ok := PrincipalFrom(r.Context())
		if !ok || p.Kind != httpx.KindAgentKey {
			return httpx.E(httpx.CodeAuthenticationRequired)
		}
		var in struct {
			Tool string          `json:"tool"`
			Args json.RawMessage `json:"args"`
		}
		if err := httpx.DecodeJSON(r, &in); err != nil {
			return err
		}
		if !validTool(in.Tool) {
			return httpx.ErrValidation("tool", "tool must be the connector.tool name the Gate uses, like stripe.refund.")
		}
		if len(in.Args) == 0 || string(in.Args) == "null" {
			return httpx.ErrValidation("args", "Send args: the exact JSON arguments the intent was signed over ({} for none).")
		}
		hs := r.Header.Values(HeaderIntent)
		if len(hs) > 1 {
			return httpx.ErrIntent("syntax", "")
		}
		header := ""
		if len(hs) == 1 {
			header = hs[0]
		}
		if c == nil {
			return httpx.E(httpx.CodeServiceUnavailable)
		}
		res, err := c.CheckAlways(r.Context(), IntentInput{Principal: p, Header: header, Tool: in.Tool, Args: in.Args, OBO: OBOFrom(r.Context())})
		if err != nil {
			return err
		}
		if !res.Signed {
			e := httpx.ErrValidation(HeaderIntent, "Send a Deedglass-Intent header to check.")
			e.Status = http.StatusUnprocessableEntity
			return e
		}
		digest, _ := ArgsDigest(in.Args)
		out := IntentCheck{Object: "intent_check", Signed: true, IntentHash: &res.IntentHash, KID: &res.KID, Agent: p.AgentID.String(),
			Tool: res.Tool, ArgsDigest: digest, OnBehalfOf: optStr(res.OBO.OnBehalfOf), Session: optStr(res.OBO.Session),
			NonceConsumed: true, Mode: res.Mode}
		t := httpx.Time(res.ExpiresAt)
		out.ExpiresAt = &t
		return httpx.WriteJSON(w, http.StatusOK, out)
	}
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func validTool(s string) bool { return intent.ValidTool(s) }
