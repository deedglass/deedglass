package ledger

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/buildinfo"
)

// The webhook outbox (S22 §3.9). Every event that goes out on deedglass_events also queues, in the
// same transaction and the same round trip, one webhook_deliveries row per active endpoint of the
// org that subscribed to its type. The row holds the exact payload, so a retry sends the same
// bytes; a rolled-back transaction emits nothing. An org with no webhooks costs one index probe.
//
// The delivery id is derived, not drawn: 'whd_', the event id's 10 time characters, and 16 hex
// characters of sha256(webhook id ':' event id), all inside the Crockford alphabet, so it is
// unique per (endpoint, event) like the table's unique key. The endpoint's api_version and the
// action's livemode are filled in by the statement.

// outboxSQL inserts the deliveries of one event: $1 org, $2 type, $3 event id, $4 payload (the
// envelope with placeholders for api_version and livemode), $5 the action id or null.
const outboxSQL = `INSERT INTO deedglass.webhook_deliveries (id, org_id, webhook_id, event_type, event_id, payload_digest, payload, next_attempt_at)
SELECT 'whd_' || pg_catalog.substr($3::text, 5, 10)
         || pg_catalog.substr(pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(w.id || ':' || $3::text, 'UTF8')), 'hex'), 1, 16),
       w.org_id, w.id, $2::text, $3::text,
       'sha256:' || pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(p.body::text, 'UTF8')), 'hex'),
       p.body, pg_catalog.now()
  FROM deedglass.webhooks AS w,
       LATERAL (SELECT pg_catalog.jsonb_set(pg_catalog.jsonb_set($4::jsonb, '{api_version}', pg_catalog.to_jsonb(w.api_version)),
                  '{livemode}', pg_catalog.to_jsonb(COALESCE((SELECT a.livemode FROM deedglass.actions AS a
                                                              WHERE a.org_id = w.org_id AND a.id = $5::text), false))) AS body) AS p
 WHERE w.org_id = $1::text AND w.status = 'active' AND $2::text = ANY (w.events)
ON CONFLICT (webhook_id, event_id) DO NOTHING`

// WebhookEnvelope is the event object delivered to an endpoint (contract §8): thin by design,
// ids and states only, never request or result bodies, context, names or email addresses.
type WebhookEnvelope struct {
	Object     string         `json:"object"` // "event"
	ID         string         `json:"id"`     // evt_…
	Type       string         `json:"type"`
	APIVersion string         `json:"api_version"`
	CreatedAt  string         `json:"created_at"`
	Livemode   bool           `json:"livemode"`
	Org        string         `json:"org"`
	Data       WebhookPayload `json:"data"`
}

// WebhookPayload is the envelope's data.
type WebhookPayload struct {
	Object map[string]any `json:"object"`
}

// Envelope builds the webhook envelope of an event published on deedglass_events.
func Envelope(ev Event) WebhookEnvelope {
	return WebhookEnvelope{Object: "event", ID: ev.Event, Type: ev.Type, APIVersion: buildinfo.LatestAPIVersion,
		CreatedAt: ev.At, Org: ev.Org, Data: WebhookPayload{Object: eventObject(ev)}}
}

// eventObject is data.object: the object the event is about, as ids and states.
func eventObject(ev Event) map[string]any {
	o := map[string]any{"id": ev.ID}
	put := func(k string, v *string) {
		if v != nil {
			o[k] = *v
		}
	}
	switch {
	case strings.HasPrefix(ev.Type, "action."):
		o["object"] = "action"
		put("state", ev.State)
		put("deed", ev.Deed)
		put("level", ev.Level)
		put("verdict", ev.Verdict)
		put("required_level", ev.RequiredLevel)
		if len(ev.Flags) > 0 {
			o["flags"] = ev.Flags
		}
	case ev.Type == EventDeedSealed:
		o["object"] = "deed"
		if ev.Seq != nil {
			o["seq"] = *ev.Seq
		}
		o["action"] = ev.Action
	case strings.HasPrefix(ev.Type, "approval."):
		o["object"] = "approval"
		put("status", ev.Status)
		put("action", ev.Action)
		put("agent", ev.Agent)
		put("tool", ev.Tool)
		put("rule_id", ev.RuleID)
		if ev.Required != nil {
			o["required"] = *ev.Required
		}
		if ev.Countersigned != nil {
			o["countersigned"] = *ev.Countersigned
		}
		o["deed"] = ev.Deed
	case strings.HasPrefix(ev.Type, "agent."):
		o["object"] = "agent"
		put("status", ev.State)
		put("deed", ev.Deed)
	case strings.HasPrefix(ev.Type, "org."):
		o["object"] = "org"
		put("status", ev.State)
		put("deed", ev.Deed)
	case ev.Type == EventPolicyActivated:
		o["object"] = "policy_activation"
		put("mode", ev.State)
		put("deed", ev.Deed)
	case strings.HasPrefix(ev.Type, "decoder."):
		o["object"] = "decoder"
		o["connector"] = ev.ID
		delete(o, "id")
		put("status", ev.State)
		put("reason", ev.Reason)
		put("kind", ev.Kind)
		put("lsn", ev.LSN)
		put("action", ev.Action)
	case ev.Type == EventWebhookTest:
		o["object"] = "webhook"
	default:
		o["object"] = strings.SplitN(ev.Type, ".", 2)[0]
		put("state", ev.State)
	}
	return o
}

// queueOutbox queues ev's deliveries on b, next to its NOTIFY.
func queueOutbox(b *pgx.Batch, ev Event) error {
	if ev.Type == EventWebhookTest {
		return nil // one endpoint only, written by the webhook service
	}
	body, err := json.Marshal(Envelope(ev))
	if err != nil {
		return err
	}
	if len(body) > MaxWebhookPayload {
		return fmt.Errorf("%w: webhook payload for %s is %d bytes; the limit is %d", ErrPayload, ev.Type, len(body), MaxWebhookPayload)
	}
	b.Queue(outboxSQL, ev.Org, ev.Type, ev.Event, string(body), ev.Action)
	return nil
}

// MaxWebhookPayload is the outbox column's limit (webhook_deliveries.payload ≤ 16 KiB).
const MaxWebhookPayload = 16 << 10
