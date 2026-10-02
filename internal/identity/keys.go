package identity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// API keys (S08 §3.3–3.4): agent keys and org admin keys. The secret is returned exactly once, in
// the create or roll response; Deedglass stores only the prefix and sha256(pepper ‖ secret).

// Key limits.
const (
	MaxActiveKeys     = 10
	MaxKeyName        = 64
	DefaultRollWindow = 24 * time.Hour
	MaxRollWindow     = 7 * 24 * time.Hour
)

// APIKey is the api_key object.
type APIKey struct {
	Object     string      `json:"object"` // "api_key"
	ID         string      `json:"id"`
	Agent      *string     `json:"agent"` // null for an org admin key
	Name       string      `json:"name"`
	Mode       Mode        `json:"mode"`
	Prefix     string      `json:"prefix"` // dg_live_jJbEEMWk
	Scopes     []string    `json:"scopes"`
	Secret     *string     `json:"secret,omitempty"` // create and roll only; null in a replay
	ExpiresAt  *httpx.Time `json:"expires_at"`
	LastUsedAt *httpx.Time `json:"last_used_at"`
	RevokedAt  *httpx.Time `json:"revoked_at"`
	RolledFrom *string     `json:"rolled_from"`
	CreatedAt  httpx.Time  `json:"created_at"`
}

// KeyResult is a key after a change, with its deed (null for a no-op revoke).
type KeyResult struct {
	APIKey
	Deed     *string      `json:"deed"`
	Previous *PreviousKey `json:"previous,omitempty"` // roll only
}

// PreviousKey is the rolled key and when it stops working.
type PreviousKey struct {
	ID        string      `json:"id"`
	Prefix    string      `json:"prefix"`
	ExpiresAt *httpx.Time `json:"expires_at"`
}

// CreateKeyInput is POST /v1/agents/{id}/keys's and POST /v1/org/keys's body.
type CreateKeyInput struct {
	Name   string   `json:"name"`
	Mode   Mode     `json:"mode"`   // default live
	Scopes []string `json:"scopes"` // agent keys only; default actions:execute, actions:read, deeds:read
}

const keyCols = `k.id, k.agent_id, COALESCE(k.name, ''), k.mode, k.prefix, k.scopes, k.expires_at, k.last_used_at,
  k.revoked_at, k.rolled_from, k.created_at`

func scanKey(row pgx.Row) (APIKey, error) {
	var (
		k                  APIKey
		agent, rolled      *string
		prefix             string
		exp, used, revoked *time.Time
		created            time.Time
	)
	if err := row.Scan(&k.ID, &agent, &k.Name, &k.Mode, &prefix, &k.Scopes, &exp, &used, &revoked, &rolled, &created); err != nil {
		return APIKey{}, err
	}
	k.Object, k.Agent, k.RolledFrom = "api_key", agent, rolled
	k.Prefix = DisplayPrefix(k.Mode, prefix)
	k.ExpiresAt, k.LastUsedAt, k.RevokedAt = optTime(exp), optTime(used), optTime(revoked)
	k.CreatedAt = httpx.Time(created)
	return k, nil
}

func checkKeyInput(in *CreateKeyInput, admin bool) error {
	in.Name = strings.TrimSpace(in.Name)
	if utf8.RuneCountInString(in.Name) > MaxKeyName || strings.ContainsFunc(in.Name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return httpx.ErrValidation("name", "A key name is at most 64 characters, without line breaks, like prod runner.")
	}
	if in.Mode == "" {
		in.Mode = ModeLive
	}
	if !in.Mode.Valid() {
		return httpx.ErrValidation("mode", "mode is live or test.")
	}
	if admin {
		if len(in.Scopes) > 0 && !slices.Equal(in.Scopes, []string{httpx.ScopeAdmin}) {
			return httpx.ErrValidation("scopes", "Admin keys always have exactly the admin scope. Leave scopes out.")
		}
		in.Scopes = []string{httpx.ScopeAdmin}
		return nil
	}
	if len(in.Scopes) == 0 {
		in.Scopes = slices.Clone(httpx.DefaultAgentScopes)
	}
	seen := map[string]bool{}
	for _, sc := range in.Scopes {
		if sc == httpx.ScopeAdmin {
			return httpx.ErrValidation("scopes", "Agent keys can't have the admin scope: no agent key changes any agent. Create an admin key with deedglass org keys create.")
		}
		if !slices.Contains(httpx.AgentScopes, sc) {
			return httpx.ErrValidation("scopes", fmt.Sprintf("%q isn't a scope. Use actions:execute, actions:read, deeds:read or actions:undo.", clipText(sc, 40)))
		}
		if seen[sc] {
			return httpx.ErrValidation("scopes", fmt.Sprintf("%s is listed twice.", sc))
		}
		seen[sc] = true
	}
	return nil
}

// insertKey generates a key and inserts it, retrying on the (rare) global prefix collision. Each
// attempt runs in a savepoint, so a collision doesn't abort the transaction.
func (s *Service) insertKey(ctx context.Context, tx pgx.Tx, org ids.OrgID, agent *string, in CreateKeyInput, rolledFrom *string,
	by string) (APIKey, string, error) {
	id := ids.New[ids.APIKeyKind]()
	for attempt := 0; attempt < 5; attempt++ {
		full, p, err := NewAPIKey(in.Mode)
		if err != nil {
			return APIKey{}, "", err
		}
		h := HashSecret(s.pepper, p.Secret)
		sp, err := tx.Begin(ctx)
		if err != nil {
			return APIKey{}, "", err
		}
		k, err := scanKey(sp.QueryRow(ctx, `INSERT INTO deedglass.api_keys AS k (id, org_id, agent_id, name, mode, prefix, secret_hash, scopes, rolled_from, created_by)
		    VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9, $10) RETURNING `+keyCols,
			id, org, agent, in.Name, string(in.Mode), p.Prefix, h[:], in.Scopes, rolledFrom, by))
		if store.SQLState(err) == store.CodeUniqueViolation {
			_ = sp.Rollback(ctx)
			continue
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			return APIKey{}, "", err
		}
		if err := sp.Commit(ctx); err != nil {
			return APIKey{}, "", err
		}
		return k, full, nil
	}
	return APIKey{}, "", errors.New("identity: five key prefixes collided in a row")
}

// countActiveKeys counts an agent's keys that still work (not revoked, not expired).
func countActiveKeys(ctx context.Context, tx pgx.Tx, org ids.OrgID, agent string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*)::integer FROM deedglass.api_keys
	    WHERE org_id = $1 AND agent_id = $2 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > pg_catalog.now())`, org, agent).Scan(&n)
	return n, err
}

// CreateAgentKey creates a key for an agent (at most 10 active) and seals api_key.created.
func (s *Service) CreateAgentKey(ctx context.Context, a Actor, org ids.OrgID, agentID string, in CreateKeyInput) (KeyResult, error) {
	if err := checkKeyInput(&in, false); err != nil {
		return KeyResult{}, err
	}
	var res KeyResult
	err := s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		name, _, err := lockAgent(ctx, tx, org, agentID) // serializes the count below
		if err != nil {
			return err
		}
		if kind, err := agentKind(ctx, tx, org, agentID); err != nil {
			return err
		} else if kind == "system" {
			return httpx.ErrValidation("agent", fmt.Sprintf(msgSystemAgentKeys, name))
		}
		n, err := countActiveKeys(ctx, tx, org, agentID)
		if err != nil {
			return err
		}
		if n >= MaxActiveKeys {
			return httpx.E(httpx.CodeConflict, httpx.Msgf("%s already has %d active keys. Revoke one before creating another.", name, MaxActiveKeys))
		}
		k, full, err := s.insertKey(ctx, tx, org, &agentID, in, nil, a.By)
		if err != nil {
			return err
		}
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminAPIKeyCreated, Actor: a.Ref,
			Target: &deed.Ref{Type: "api_key", ID: k.ID}, Agent: &deed.AgentRef{ID: agentID, Name: name},
			Data: map[string]any{"key": k.ID, "mode": string(k.Mode), "scopes": k.Scopes}})
		if err != nil {
			return err
		}
		k.Secret = &full
		res = KeyResult{APIKey: k, Deed: d}
		return nil
	})
	return res, err
}

// CreateAdminKey creates an org admin key (scopes exactly {admin}) and seals api_key.created. Only
// a signed-in admin or owner, or the server operator, calls it: admin keys can't mint admin keys.
func (s *Service) CreateAdminKey(ctx context.Context, a Actor, org ids.OrgID, in CreateKeyInput) (KeyResult, error) {
	if a.Ref.Type == "api_key" {
		return KeyResult{}, httpx.E(httpx.CodeInsufficientRole, httpx.Msg("Admin keys can't create admin keys. Sign in to the Console as an admin, or run deedglass org keys create on the server."))
	}
	if err := checkKeyInput(&in, true); err != nil {
		return KeyResult{}, err
	}
	var res KeyResult
	err := s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		k, full, err := s.insertKey(ctx, tx, org, nil, in, nil, a.By)
		if err != nil {
			return err
		}
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminAPIKeyCreated, Actor: a.Ref,
			Target: &deed.Ref{Type: "api_key", ID: k.ID}, Data: map[string]any{"key": k.ID, "mode": string(k.Mode), "scopes": k.Scopes}})
		if err != nil {
			return err
		}
		k.Secret = &full
		res = KeyResult{APIKey: k, Deed: d}
		return nil
	})
	return res, err
}

// ListAgentKeys returns an agent's keys, newest first, revoked ones included (at most 100).
func (s *Service) ListAgentKeys(ctx context.Context, org ids.OrgID, agentID string) ([]APIKey, error) {
	var out []APIKey
	err := s.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := getAgent(ctx, tx, org, agentID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+keyCols+` FROM deedglass.api_keys AS k WHERE k.org_id = $1 AND k.agent_id = $2
		    ORDER BY k.id DESC LIMIT 100`, org, agentID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (APIKey, error) { return scanKey(r) })
		return err
	})
	return out, err
}

// ListAdminKeys returns the org's admin keys, newest first (at most 100).
func (s *Service) ListAdminKeys(ctx context.Context, org ids.OrgID) ([]APIKey, error) {
	var out []APIKey
	err := s.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+keyCols+` FROM deedglass.api_keys AS k WHERE k.org_id = $1 AND k.agent_id IS NULL
		    ORDER BY k.id DESC LIMIT 100`, org)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (APIKey, error) { return scanKey(r) })
		return err
	})
	return out, err
}

// GetKey returns one key, or store.ErrNotFound.
func (s *Service) GetKey(ctx context.Context, org ids.OrgID, id string) (APIKey, error) {
	var k APIKey
	err := s.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		k, err = getKey(ctx, tx, org, id, false)
		return err
	})
	return k, err
}

func getKey(ctx context.Context, tx pgx.Tx, org ids.OrgID, id string, lock bool) (APIKey, error) {
	if ids.Check(ids.APIKey, id) != nil {
		return APIKey{}, store.ErrNotFound
	}
	q := `SELECT ` + keyCols + ` FROM deedglass.api_keys AS k WHERE k.org_id = $1 AND k.id = $2`
	if lock {
		q += ` FOR UPDATE`
	}
	k, err := scanKey(tx.QueryRow(ctx, q, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, store.ErrNotFound
	}
	return k, err
}

// ErrKeyNotFound is the 404 for a key id.
func ErrKeyNotFound(id string) *httpx.Error {
	return httpx.E(httpx.CodeNotFound, httpx.Msgf("There's no API key %s in this org. Check the id; deedglass agent keys list shows them.", clipText(id, 64)))
}

func rawPrefix(display string) string {
	if i := strings.LastIndexByte(display, '_'); i >= 0 {
		return display[i+1:]
	}
	return display
}

// RollKey creates a replacement key with the same agent, name, mode and scopes, and lets the old
// one keep working for overlap (0 = it stops now), so the new one can be deployed first. It seals
// api_key.rolled. A revoked or expired key can't be rolled.
func (s *Service) RollKey(ctx context.Context, a Actor, org ids.OrgID, id string, overlap time.Duration) (KeyResult, error) {
	if overlap < 0 || overlap > MaxRollWindow {
		return KeyResult{}, httpx.ErrValidation("expires_in", "expires_in is the overlap in seconds, from 0 to 604800 (7 days). The default is 86400.")
	}
	var res KeyResult
	err := s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		old, err := getKey(ctx, tx, org, id, false)
		if err != nil {
			return err
		}
		var agentName string
		if old.Agent != nil {
			if agentName, _, err = lockAgent(ctx, tx, org, *old.Agent); err != nil { // lock order: agent, then key
				return err
			}
		} else if a.Ref.Type == "api_key" {
			// Rolling an admin key mints one: only a person or the operator may.
			return httpx.E(httpx.CodeInsufficientRole, httpx.Msg("Admin keys can't roll admin keys, because that creates one. Sign in to the Console as an admin, or run deedglass org keys create on the server."))
		}
		if old, err = getKey(ctx, tx, org, id, true); err != nil {
			return err
		}
		now := s.now()
		switch {
		case old.RevokedAt != nil:
			return httpx.E(httpx.CodeConflict, httpx.Msgf("Key %s is revoked, so it can't be rolled. Create a new key instead.", old.Prefix))
		case old.ExpiresAt != nil && !time.Time(*old.ExpiresAt).After(now):
			return httpx.E(httpx.CodeConflict, httpx.Msgf("Key %s has expired, so it can't be rolled. Create a new key instead.", old.Prefix))
		}
		in := CreateKeyInput{Name: old.Name, Mode: old.Mode, Scopes: old.Scopes}
		nk, full, err := s.insertKey(ctx, tx, org, old.Agent, in, &old.ID, a.By)
		if err != nil {
			return err
		}
		var until time.Time
		if err := tx.QueryRow(ctx, `UPDATE deedglass.api_keys SET expires_at = LEAST(COALESCE(expires_at, 'infinity'::timestamptz), pg_catalog.now() + pg_catalog.make_interval(secs => $3::double precision))
		    WHERE org_id = $1 AND id = $2 RETURNING expires_at`, org, old.ID, float64(overlap/time.Second)).Scan(&until); err != nil {
			return err
		}
		ev := ledger.AdminEvent{Action: ledger.AdminAPIKeyRolled, Actor: a.Ref, Target: &deed.Ref{Type: "api_key", ID: nk.ID},
			Data: map[string]any{"from": old.ID, "to": nk.ID, "overlap_s": int64(overlap / time.Second)}}
		if old.Agent != nil {
			ev.Agent = &deed.AgentRef{ID: *old.Agent, Name: agentName}
		}
		d, err := s.recordAdmin(ctx, tx, ev)
		if err != nil {
			return err
		}
		if err := s.notify(ctx, tx, identityMsg{T: "key", Org: org.String(), ID: old.ID, P: rawPrefix(old.Prefix), S: "rolled"}); err != nil {
			return err
		}
		nk.Secret = &full
		res = KeyResult{APIKey: nk, Deed: d, Previous: &PreviousKey{ID: old.ID, Prefix: old.Prefix, ExpiresAt: optTime(&until)}}
		return nil
	})
	return res, err
}

// RevokeKey revokes a key at once (every instance refuses it within 2 seconds) and seals
// api_key.revoked. Revoking a revoked key changes nothing and seals nothing.
func (s *Service) RevokeKey(ctx context.Context, a Actor, org ids.OrgID, id, reason string) (KeyResult, error) {
	reason, err := checkReason("reason", reason, 0)
	if err != nil {
		return KeyResult{}, err
	}
	var res KeyResult
	err = s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		k, err := getKey(ctx, tx, org, id, false)
		if err != nil {
			return err
		}
		var agentName string
		if k.Agent != nil {
			if agentName, _, err = lockAgent(ctx, tx, org, *k.Agent); err != nil {
				return err
			}
		}
		if k, err = getKey(ctx, tx, org, id, true); err != nil {
			return err
		}
		if k.RevokedAt != nil {
			res = KeyResult{APIKey: k}
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.api_keys SET revoked_at = pg_catalog.now(), revoked_by = $3, revoke_reason = NULLIF($4, '')
		    WHERE org_id = $1 AND id = $2`, org, id, a.By, reason); err != nil {
			return err
		}
		ev := ledger.AdminEvent{Action: ledger.AdminAPIKeyRevoked, Actor: a.Ref, Reason: reason, Target: &deed.Ref{Type: "api_key", ID: id},
			Data: map[string]any{"key": id}}
		if k.Agent != nil {
			ev.Agent = &deed.AgentRef{ID: *k.Agent, Name: agentName}
		}
		d, err := s.recordAdmin(ctx, tx, ev)
		if err != nil {
			return err
		}
		if err := s.notify(ctx, tx, identityMsg{T: "key", Org: org.String(), ID: id, P: rawPrefix(k.Prefix), S: "revoked"}); err != nil {
			return err
		}
		if k, err = getKey(ctx, tx, org, id, false); err != nil {
			return err
		}
		res = KeyResult{APIKey: k, Deed: d}
		return nil
	})
	return res, err
}
