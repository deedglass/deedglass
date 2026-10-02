# Webhooks: Deedglass events to your endpoint

A webhook endpoint receives Deedglass events as signed HTTPS POSTs: an approval waiting for a
person, a decision, an action executed or witnessed, a deed sealed, an agent stopped. Each event
is recorded in the same database transaction as the change it reports (an outbox), so a committed
change always gets its delivery, and a rolled-back one never does.

## Create an endpoint

```sh
deedglass webhooks create --url https://hooks.example.com/deedglass \
  --events approval.requested,approval.decided
```

or `POST /v1/webhooks` with `{"url": "…", "events": ["approval.requested"], "description": "…"}`
(an admin's session or admin key). The answer carries the signing secret (`whsec_…`) **once**;
store it where your receiver reads it. Lists and reads show only its last characters
(`secret_hint`).

| Route | What it does |
|---|---|
| `GET /v1/webhooks` | the org's endpoints |
| `POST /v1/webhooks` | create one (at most `webhooks.max_endpoints_per_org`, 16) |
| `GET /v1/webhooks/{id}` | one endpoint, with its status |
| `DELETE /v1/webhooks/{id}` | delete it (sealed as `webhook.deleted`) |
| `POST /v1/webhooks/{id}/test` | send a `webhook.test` event now and report the answer |
| `GET /v1/webhooks/{id}/deliveries` | recent deliveries: status, attempt, HTTP status, time |

The CLI has `deedglass webhooks create | list | test | delete`.

## Events

`action.created`, `action.decided`, `action.executed`, `action.witnessed`, `deed.sealed`,
`approval.requested`, `approval.decided`, `agent.stopped`, `agent.resumed`, `agent.suspended`,
`org.stopped`, `org.started`, `policy.activated`, `decoder.degraded`, `decoder.recovered`,
`decoder.alert`, `webhook.test`, and (when Insights ship) `anomaly.detected`,
`suggestion.created`. An endpoint receives only the types it subscribed to.

## The envelope

```json
{
  "object": "event",
  "id": "evt_01m3y2z9w7x6v5t4s3r2q1p0nm",
  "type": "approval.decided",
  "api_version": "2026-10-01",
  "created_at": "2026-10-02T04:44:10.123Z",
  "livemode": false,
  "org": "org_01m3x0…",
  "data": {
    "object": {
      "object": "approval",
      "id": "apr_01m3y2z9q4hk7c8vd5w3e6f2ab",
      "status": "approved",
      "action": "act_01m3y2z8k1m2n3p4q5r6s7t8v9",
      "agent": "agt_01m3x9a1b2c3d4e5f6g7h8j9k0",
      "tool": "postgres.execute",
      "rule_id": "orders-writes-need-approval",
      "required": 1,
      "countersigned": 1,
      "deed": "deed_01m3y2y1…"
    }
  }
}
```

Payloads are **thin**: ids and states, never arguments, results, reasons, email addresses or
tokens. Fetch the object with the API (`GET /v1/approvals/{id}`, `GET /v1/actions/{id}`) when you
need more. A payload is at most 16 KiB.

## Headers

| Header | Value |
|---|---|
| `Content-Type` | `application/json` |
| `Deedglass-Signature` | `t=<unix seconds>,v1=<hex HMAC-SHA256>` |
| `Deedglass-Event-Id` | the event's `evt_…` id |
| `Deedglass-Event-Type` | the event's type |
| `Deedglass-Webhook-Id` | the endpoint's `whk_…` id |
| `User-Agent` | `Deedglass-Webhooks/1.0 (+https://docs.deedglass.com/webhooks)` |

## Verify the signature

The HMAC key is the UTF-8 bytes of the full secret, `whsec_` included. The message is the
timestamp, a period, and the **raw** body: the exact bytes received, before any JSON parsing.
Refuse a timestamp more than 300 seconds from now, so a captured delivery can't be replayed later.
A header may carry several `v1=` values while a secret rotates; any one matching is enough.

**Go** (the repo's package `github.com/deedglass/deedglass/pkg/deedglass/webhook`):

```go
body, _ := io.ReadAll(r.Body)
if err := webhook.Verify(secret, r.Header.Get(webhook.Header), body, 0); err != nil {
	http.Error(w, "bad signature", http.StatusBadRequest)
	return
}
```

**Node** (22 or later, no dependencies):

```js
import crypto from 'node:crypto';

function verify(secret, header, rawBody, toleranceSec = 300) {
  const parts = Object.groupBy(header.split(',').map((p) => p.trim().split('=')), ([k]) => k);
  const t = Number(parts.t?.[0]?.[1]);
  if (!Number.isInteger(t) || Math.abs(Date.now() / 1000 - t) > toleranceSec) return false;
  const want = crypto.createHmac('sha256', secret).update(`${t}.`).update(rawBody).digest();
  return (parts.v1 ?? []).some(([, v]) => {
    const got = Buffer.from(v ?? '', 'hex');
    return got.length === want.length && crypto.timingSafeEqual(got, want);
  });
}
```

Pass `rawBody` as the Buffer you received (for Express, `express.raw({type: 'application/json'})`).

**Python** (3.9 or later, standard library):

```python
import hashlib, hmac, time

def verify(secret: str, header: str, raw_body: bytes, tolerance: int = 300) -> bool:
    t, sigs = None, []
    for part in header.split(","):
        k, _, v = part.strip().partition("=")
        if k == "t" and v.isdigit():
            t = int(v)
        elif k == "v1":
            sigs.append(v)
    if t is None or abs(time.time() - t) > tolerance:
        return False
    want = hmac.new(secret.encode(), f"{t}.".encode() + raw_body, hashlib.sha256).hexdigest()
    return any(hmac.compare_digest(want, s) for s in sigs)
```

## Delivery, retries and auto-disable

- A delivery succeeds on any 2xx within `webhooks.timeout` (10 s). Anything else (another
  status, a redirect, which Deedglass doesn't follow, a timeout, a refused connection) is retried.
- Retries after the first attempt: 30 s, 2 min, 10 min, 30 min, 1 h, 3 h, 6 h and 12 h, each
  ±20%. After the last (`webhooks.max_attempts`, 8), the delivery is dead.
- At most 4 deliveries to one endpoint are in flight at a time.
- An endpoint that has failed every delivery for `webhooks.disable_after` (72 h) is disabled, and
  a `webhook.disabled` admin deed is sealed. Create it again to resume.

**Dedupe by `Deedglass-Event-Id`.** A retry, or a delivery whose 2xx got lost, can arrive more
than once; the event id is the same each time. **Ordering is not promised:** a retry can arrive
after a later event. Use `created_at` and the object's state, or fetch the object.

## Security

- Every URL goes through Deedglass's egress door at create time and at every delivery: `https`
  only in prod; private, loopback, link-local and cloud metadata addresses are refused
  (`destination_blocked`), also when a hostname resolves to one, and redirects are never
  followed. A dev server allows private networks only with `DEEDGLASS_ALLOW_PRIVATE_NETWORKS`.
- The secret is sealed in the vault, bound to the endpoint's id; only the dispatcher opens it.
- Responses are read up to 64 KiB, then discarded. Deedglass never stores your response body.

See [LIMITATIONS.md](LIMITATIONS.md) for what webhooks don't do.
