//go:build integration

package storetest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// Org is a seeded org and the ids of the rows Seed wrote for it.
type Org struct {
	ID         ids.OrgID
	Owner      ids.UserID
	Membership ids.MembershipID
	Agent      ids.AgentID
	Key        ids.APIKeyID
	Policy     ids.PolicyID
	Version    ids.PolicyVersionID
	Connector  ids.ConnectorID
	Action     ids.ActionID
	Approval   ids.ApprovalID
	Webhook    ids.WebhookID
	Replay     ids.ReplayID
	Deed       ids.DeedID
}

// Genesis is contract §7's prev_hash for an org's first deed.
func Genesis(org string) string {
	sum := sha256.Sum256([]byte("deedglass/genesis\n" + org))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Digest returns "sha256:" + hex(sha256(b)).
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// SealKey inserts a random seal key (global) and returns its id.
func SealKey(t testing.TB, ctx context.Context, tx pgx.Tx) ids.SealKeyID {
	t.Helper()
	pub := make([]byte, 32)
	_, _ = rand.Read(pub)
	id := ids.New[ids.SealKeyKind]()
	if _, err := tx.Exec(ctx, `INSERT INTO deedglass.seal_keys (id, public_key, thumbprint) VALUES ($1, $2, $3)`,
		id, pub, Digest(pub)); err != nil {
		t.Fatalf("seal key: %v", err)
	}
	return id
}

// Seal is a well-formed (not a real) seal value.
func Seal() string {
	b := make([]byte, 64)
	_, _ = rand.Read(b)
	return "ed25519:" + base64.RawURLEncoding.EncodeToString(b)
}

// NewOrg creates an org with its owner, then Seed fills every org table for it.
func NewOrg(t testing.TB, db *store.DB, slug string) Org {
	t.Helper()
	ctx := context.Background()
	o, u, m, err := db.CreateOrg(ctx, store.NewOrg{Name: "Org " + slug, Slug: slug, OwnerEmail: "owner@" + slug + ".example.test"})
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	org := Org{ID: o.ID, Owner: u.ID, Membership: m.ID}
	var sk ids.SealKeyID
	if err := db.SystemTx(ctx, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		sk = SealKey(t, ctx, tx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.OrgTx(ctx, org.ID, func(ctx context.Context, tx pgx.Tx) error {
		Seed(t, ctx, tx, &org, sk)
		return nil
	}); err != nil {
		t.Fatalf("seed %s: %v", slug, err)
	}
	return org
}

// Row is one insert that Seed performs; InsertAs replays it for another org (the RLS matrix inserts
// org B's row from inside org A's transaction).
type Row struct {
	Table  string
	Insert func(ctx context.Context, tx pgx.Tx, o *Org) error
}

// Rows is one insert per org table, in dependency order. o must already hold the ids the row
// refers to; each insert uses fresh ids for its own row.
func Rows(sk ids.SealKeyID) []Row {
	ex := func(q string, args func(o *Org) []any) func(context.Context, pgx.Tx, *Org) error {
		return func(ctx context.Context, tx pgx.Tx, o *Org) error { _, err := tx.Exec(ctx, q, args(o)...); return err }
	}
	body := []byte(`{"v":1}`)
	return []Row{
		{"memberships", ex(`INSERT INTO deedglass.memberships (id, org_id, user_id, role, status) VALUES ($1, $2, $3, 'viewer', 'invited')
		    ON CONFLICT (org_id, user_id) DO NOTHING`,
			func(o *Org) []any { return []any{ids.New[ids.MembershipKind](), o.ID, o.Owner} })},
		{"sessions", ex(`INSERT INTO deedglass.sessions (id, org_id, user_id, token_hash, csrf_hash, idle_expires_at, expires_at)
		    VALUES ($1, $2, $3, $4, $5, pg_catalog.now() + interval '1 hour', pg_catalog.now() + interval '1 day')`,
			func(o *Org) []any { return []any{ids.New[ids.SessionKind](), o.ID, o.Owner, rnd(32), rnd(32)} })},
		{"login_tokens", ex(`INSERT INTO deedglass.login_tokens (id, org_id, user_id, purpose, token_hash, expires_at)
		    VALUES ($1, $2, $3, 'login', $4, pg_catalog.now() + interval '15 minutes')`,
			func(o *Org) []any { return []any{ids.New[ids.LoginTokenKind](), o.ID, o.Owner, rnd(32)} })},
		{"agents", func(ctx context.Context, tx pgx.Tx, o *Org) error {
			id := ids.New[ids.AgentKind]()
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.agents (id, org_id, name) VALUES ($1, $2, $3)`, id, o.ID, "agent-"+id.String()[len(id.String())-8:])
			if err == nil && o.Agent.IsZero() {
				o.Agent = id
			}
			return err
		}},
		{"api_keys", func(ctx context.Context, tx pgx.Tx, o *Org) error {
			id := ids.New[ids.APIKeyKind]()
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.api_keys (id, org_id, agent_id, mode, prefix, secret_hash, scopes)
			    VALUES ($1, $2, $3, 'test', $4, $5, ARRAY['actions:execute'])`, id, o.ID, o.Agent, prefix8(), rnd(32))
			if err == nil && o.Key.IsZero() {
				o.Key = id
			}
			return err
		}},
		{"intent_nonces", ex(`INSERT INTO deedglass.intent_nonces (org_id, agent_id, nonce, intent_hash, expires_at)
		    VALUES ($1, $2, $3, $4, pg_catalog.now() + interval '5 minutes')`,
			func(o *Org) []any { return []any{o.ID, o.Agent, hex.EncodeToString(rnd(16)), Digest(rnd(8))} })},
		{"policies", func(ctx context.Context, tx pgx.Tx, o *Org) error {
			id := ids.New[ids.PolicyKind]()
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.policies (id, org_id, name) VALUES ($1, $2, $3)`, id, o.ID, "policy-"+hex.EncodeToString(rnd(4)))
			if err == nil && o.Policy.IsZero() {
				o.Policy = id
			}
			return err
		}},
		{"policy_versions", func(ctx context.Context, tx pgx.Tx, o *Org) error {
			id := ids.New[ids.PolicyVersionKind]()
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.policy_versions (id, org_id, policy_id, version, yaml, version_hash, source_hash, author_type)
			    VALUES ($1, $2, $3::deedglass.pol_id, (SELECT COALESCE(pg_catalog.max(version), 0) + 1 FROM deedglass.policy_versions WHERE policy_id = $3::deedglass.pol_id), 'rules: []', $4, $5, 'system')`,
				id, o.ID, o.Policy, Digest(rnd(16)), Digest([]byte("rules: []")))
			if err == nil && o.Version.IsZero() {
				o.Version = id
			}
			return err
		}},
		{"usage_counters", ex(`INSERT INTO deedglass.usage_counters (org_id, scope, counter, window_kind, window_start, value)
		    VALUES ($1, 'org', 'calls', 'minute', pg_catalog.date_trunc('minute', pg_catalog.clock_timestamp()) + $2 * interval '1 minute', 1)`,
			func(o *Org) []any { return []any{o.ID, int(rnd(1)[0])} })},
		{"connectors", func(ctx context.Context, tx pgx.Tx, o *Org) error {
			id := ids.New[ids.ConnectorKind]()
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.connectors (id, org_id, type, name) VALUES ($1, $2, 'postgres', $3)`, id, o.ID, "db-"+hex.EncodeToString(rnd(4)))
			if err == nil && o.Connector.IsZero() {
				o.Connector = id
			}
			return err
		}},
		{"credentials", ex(`INSERT INTO deedglass.credentials (org_id, connector_id, version, kind, field_names, kek_id, wrapped_dek, ciphertext, updated_by)
		    VALUES ($1, $2, 1, 'dsn', '{dsn}', 'kek:0123456789ab', $3, $4, 'operator') ON CONFLICT DO NOTHING`,
			func(o *Org) []any { return []any{o.ID, o.Connector, rnd(60), rnd(40)} })},
		{"actions", func(ctx context.Context, tx pgx.Tx, o *Org) error {
			id, err := NewAction(ctx, tx, o.ID, o.Agent)
			if err == nil && o.Action.IsZero() {
				o.Action = id
			}
			return err
		}},
		{"action_events", ex(`INSERT INTO deedglass.action_events (id, org_id, action_id, seq, to_state, actor)
		    VALUES ($1, $2, $3::deedglass.act_id, (SELECT COALESCE(pg_catalog.max(seq), 0) + 1 FROM deedglass.action_events WHERE action_id = $3::deedglass.act_id), 'received', 'system')`,
			func(o *Org) []any { return []any{ids.New[ids.ActionEventKind](), o.ID, o.Action} })},
		{"approvals", func(ctx context.Context, tx pgx.Tx, o *Org) error {
			act, err := NewAction(ctx, tx, o.ID, o.Agent)
			if err != nil {
				return err
			}
			id := ids.New[ids.ApprovalKind]()
			_, err = tx.Exec(ctx, `INSERT INTO deedglass.approvals (id, org_id, action_id, expires_at) VALUES ($1, $2, $3, pg_catalog.now() + interval '1 hour')`, id, o.ID, act)
			if err == nil && o.Approval.IsZero() {
				o.Approval = id
			}
			return err
		}},
		{"approval_tokens", ex(`INSERT INTO deedglass.approval_tokens (id, org_id, approval_id, user_id, token_hash, purpose, expires_at)
		    VALUES ($1, $2, $3, $4, $5, 'approve', pg_catalog.now() + interval '1 hour')`,
			func(o *Org) []any { return []any{ids.New[ids.ApprovalTokenKind](), o.ID, o.Approval, o.Owner, rnd(32)} })},
		{"idempotency_keys", ex(`INSERT INTO deedglass.idempotency_keys (id, org_id, principal, key, method, route, request_hash, status, locked_until, expires_at)
		    VALUES ($1, $2, $3, $4, 'POST', 'POST /v1/agents', $5, 'in_progress', pg_catalog.now() + interval '1 minute', pg_catalog.now() + interval '24 hours')`,
			func(o *Org) []any {
				return []any{ids.New[ids.IdempotencyKind](), o.ID, o.Agent, hex.EncodeToString(rnd(8)), Digest(rnd(8))}
			})},
		{"deeds", func(ctx context.Context, tx pgx.Tx, o *Org) error {
			var seq int64
			var prev *string
			if err := tx.QueryRow(ctx, `SELECT COALESCE(pg_catalog.max(seq), 0) + 1,
			    (SELECT d.body_hash FROM deedglass.deeds AS d WHERE d.org_id = $1 ORDER BY d.seq DESC LIMIT 1)
			    FROM deedglass.deeds WHERE org_id = $1`, o.ID).Scan(&seq, &prev); err != nil {
				return err
			}
			p := Genesis(o.ID.String())
			if prev != nil {
				p = *prev
			}
			b := append(append([]byte(nil), body...), []byte(fmt.Sprint(seq))...)
			id := ids.New[ids.DeedKind]()
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.deeds (id, org_id, seq, kind, action_id, body, body_jcs, body_hash, prev_hash, seal, seal_key_id, envelope, issued_at)
			    VALUES ($1, $2, $3, 'action', $4, '{}', $5, $6, $7, $8, $9, $10, pg_catalog.clock_timestamp())`,
				id, o.ID, seq, o.Action, b, Digest(b), p, Seal(), sk, []byte("{}"))
			if err == nil && o.Deed.IsZero() {
				o.Deed = id
			}
			return err
		}},
		// An ended activation (so repeated inserts never meet the one-live-enforce index). Since S12
		// an ended activation names who ended it and the deed that sealed it, so it comes after deeds.
		{"policy_activations", ex(`INSERT INTO deedglass.policy_activations (id, org_id, policy_id, version_id, mode,
		    activated_at, deactivated_at, deactivated_by, deactivation_deed_id)
		    VALUES ($1, $2, $3, $4, 'enforce', pg_catalog.now(), pg_catalog.now(), 'system', $5)`,
			func(o *Org) []any {
				return []any{ids.New[ids.PolicyActivationKind](), o.ID, o.Policy, o.Version, o.Deed}
			})},
		{"admin_events", ex(`INSERT INTO deedglass.admin_events (id, org_id, actor_type, action) VALUES ($1, $2, 'system', 'agent.created')`,
			func(o *Org) []any { return []any{ids.New[ids.AdminEventKind](), o.ID} })},
		{"witness_jobs", func(ctx context.Context, tx pgx.Tx, o *Org) error {
			act, err := NewAction(ctx, tx, o.ID, o.Agent)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO deedglass.witness_jobs (org_id, action_id) VALUES ($1, $2)`, o.ID, act)
			return err
		}},
		{"decoded_txs", ex(`INSERT INTO deedglass.decoded_txs (id, org_id, connector_id, xid, commit_lsn, end_lsn, commit_time, change_count)
		    VALUES ($1, $2, $3, 1, $4::pg_lsn, $4::pg_lsn, pg_catalog.now(), 0)`,
			func(o *Org) []any {
				b := rnd(3)
				return []any{ids.New[ids.DecodedTxKind](), o.ID, o.Connector, fmt.Sprintf("0/%X", int(b[0])<<16|int(b[1])<<8|int(b[2]))}
			})},
		{"decoder_slots", ex(`INSERT INTO deedglass.decoder_slots (org_id, connector_id, slot_name) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			func(o *Org) []any { return []any{o.ID, o.Connector, "deedglass_" + o.Connector.String()[4:]} })},
		{"replay_jobs", func(ctx context.Context, tx pgx.Tx, o *Org) error {
			id := ids.New[ids.ReplayKind]()
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.replay_jobs (id, org_id, policy_id, version_id, from_ts, to_ts)
			    VALUES ($1, $2, $3, $4, pg_catalog.now() - interval '1 day', pg_catalog.now())`, id, o.ID, o.Policy, o.Version)
			if err == nil && o.Replay.IsZero() {
				o.Replay = id
			}
			return err
		}},
		{"suggestions", ex(`INSERT INTO deedglass.suggestions (id, org_id, kind, title, replay_job_id) VALUES ($1, $2, 'advisory', 'Look at this', $3)`,
			func(o *Org) []any { return []any{ids.New[ids.SuggestionKind](), o.ID, o.Replay} })},
		{"anomalies", ex(`INSERT INTO deedglass.anomalies (id, org_id, agent_id, kind, severity, window_start, window_end)
		    VALUES ($1, $2, $3, 'call_spike', 'info', pg_catalog.now() - interval '1 hour', pg_catalog.now())`,
			func(o *Org) []any { return []any{ids.New[ids.AnomalyKind](), o.ID, o.Agent} })},
		{"webhooks", func(ctx context.Context, tx pgx.Tx, o *Org) error {
			id := ids.New[ids.WebhookKind]()
			_, err := tx.Exec(ctx, `INSERT INTO deedglass.webhooks (id, org_id, url, events, secret_ciphertext, secret_nonce)
			    VALUES ($1, $2, 'https://hooks.example.test/deedglass', ARRAY['deed.sealed'], $3, $4)`, id, o.ID, rnd(48), rnd(12))
			if err == nil && o.Webhook.IsZero() {
				o.Webhook = id
			}
			return err
		}},
		{"webhook_deliveries", ex(`INSERT INTO deedglass.webhook_deliveries (id, org_id, webhook_id, event_type, event_id, payload_digest)
		    VALUES ($1, $2, $3, 'deed.sealed', $4, $5)`,
			func(o *Org) []any {
				return []any{ids.New[ids.WebhookDeliveryKind](), o.ID, o.Webhook, ids.New[ids.ActionEventKind](), Digest(rnd(8))}
			})},
	}
}

// Seed inserts one row into every org table for o, inside tx (an OrgTx for o.ID).
func Seed(t testing.TB, ctx context.Context, tx pgx.Tx, o *Org, sk ids.SealKeyID) {
	t.Helper()
	for _, r := range Rows(sk) {
		if err := r.Insert(ctx, tx, o); err != nil {
			t.Fatalf("seed %s: %v", r.Table, err)
		}
	}
}

// NewAction inserts an action in state received and returns its id.
func NewAction(ctx context.Context, tx pgx.Tx, org ids.OrgID, agent ids.AgentID) (ids.ActionID, error) {
	id := ids.New[ids.ActionKind]()
	_, err := tx.Exec(ctx, `INSERT INTO deedglass.actions (id, org_id, agent_id, mode, tool, request_digest)
	    VALUES ($1, $2, $3, 'gateway', 'postgres.query', $4)`, id, org, agent, Digest([]byte(id.String())))
	return id, err
}

func rnd(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

const alnum = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func prefix8() string {
	b := rnd(8)
	for i := range b {
		b[i] = alnum[int(b[i])%len(alnum)]
	}
	return string(b)
}
