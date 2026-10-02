# Live events: `GET /v1/events`

`GET /v1/events` streams what Postgres committed in your org, in commit order, as Server-Sent
Events. The Console's live views and `deedglass actions list --follow` read it. Every event is the
same object a webhook delivers (docs/APPROVALS.md, "Webhooks"), plus one field, `hydrated`.

When the stream can't be sure you received everything, it says so with `resync`. It never pretends
nothing was missed.

## Auth

| Credential | Sees |
|---|---|
| A Console session (viewer and up) | Every event of the session's org |
| An org admin key | Every event of the key's org |
| An agent key with `actions:read` | Its own agent's `action.*`, `approval.*` and `deed.sealed` (action and amendment deeds) |
| An agent key without `actions:read` | `403 insufficient_scope` |

The credential is checked again every 60 seconds: a revoked key, an ended session, a stopped agent
or a removed membership ends the stream with `session_ended` (below).

## Request

```
GET /v1/events?types=action.*,deed.sealed&agent=agt_…&last_event_id=7f3k2m.1842
Last-Event-ID: 7f3k2m.1842
```

| Parameter | Meaning |
|---|---|
| `types` | A comma list of event types (`deed.sealed`) or families (`action.*`). Default: every type. |
| `agent` | Only events about this agent. An agent key may name only its own agent. |
| `last_event_id` | Resume after this cursor. For clients that can't set the `Last-Event-ID` header; the header wins when both are sent. |

Filters and the cursor are checked before the first byte. A bad one is the usual JSON error
envelope, `400 validation_failed`, with `param` naming it. Unknown parameters are refused.

## Wire format

```
retry: 3000

id: 7f3k2m.1841
event: hello
data: {"object":"stream","instance":"7f3k2m","resumed":false,"heartbeat_s":15,"server_time":"2026-10-08T14:02:09.100Z"}

id: 7f3k2m.1842
event: action.witnessed
data: {"object":"event","id":"evt_01k6h7mz3c9d8e7f6g5h4j3k2m","type":"action.witnessed","api_version":"2026-10-01","created_at":"2026-10-08T14:02:09.512Z","livemode":false,"org":"org_01k6h7kp6msmzrhdzwtcr3jnkp","hydrated":true,"data":{"object":{"object":"action","id":"act_01k6…","agent":{"id":"agt_01k6…","name":"support-bot"},"tool":"postgres.execute","state":"sealed","status":"executed","witness":{"level":"L2","verdict":"MISMATCH"}}}}

: ping
```

- `retry: 3000` asks the browser to wait 3 seconds before reconnecting.
- The `id:` line is the **stream cursor**, `<instance>.<seq>`, not the event's `evt_` id. Keep the
  last one you saw to resume. `hello` and `resync` carry the stream's position too, so a client
  that reconnects after a quiet spell resumes from there.
- `event:` is the event type, or one of the control messages below.
- `data:` is one line of JSON.
- `: ping` is a comment, sent every 15 seconds when nothing else was sent.
- The server closes every stream after 30 minutes. Reconnect with your cursor.

Response headers: `Content-Type: text/event-stream; charset=utf-8`, `Cache-Control: no-store`,
`X-Accel-Buffering: no` (so a reverse proxy doesn't buffer the stream).

## The event object

```json
{
  "object": "event",
  "id": "evt_…",
  "type": "action.witnessed",
  "api_version": "2026-10-01",
  "created_at": "2026-10-08T14:02:09.512Z",
  "livemode": false,
  "org": "org_…",
  "hydrated": true,
  "data": { "object": { … } }
}
```

With `hydrated: true`, `data.object` is the object in exactly the shape its REST list returns, read
when the event was streamed (so it may already show a later state than the event's own type):

| Type | `data.object` when hydrated |
|---|---|
| `action.created`, `action.decided`, `action.executed`, `action.witnessed` | the action, as `GET /v1/actions` lists it |
| `approval.requested`, `approval.decided` | the approval, as `GET /v1/approvals` lists it for an org admin key, without `context`, `approvers` and `you` |
| `agent.stopped`, `agent.resumed`, `agent.suspended` | the agent, as `GET /v1/agents` lists it |
| `deed.sealed` | `{"object":"deed_summary","id","seq","kind","action","issued_at"}` (`kind` is `action`, `amendment` or `admin`; `action` is null for admin deeds) |

With `hydrated: false`, `data.object` is the thin webhook object: ids and states only. Events are
sent thin when nobody needed them hydrated, when reading the objects failed, for types with no list
shape (`org.*`, `policy.activated`, `decoder.*`), and for approvals on an agent key's stream (the
thin approval is exactly what an agent key may read). Refetch with the REST API when you need more.

Events never carry request or result payloads, approval context or email addresses, and never a
field the same credential couldn't read from the matching REST list.

## Control messages

| `event:` | `data:` | Then |
|---|---|---|
| `hello` | `{"object":"stream","instance","resumed","heartbeat_s","server_time"}` | First message of every stream. `resumed` is true when your cursor was replayed from. |
| `resync` | `{"object":"resync","reason":…}` | You may have missed events: refetch your lists' first page. The stream continues, except after `lagged`. |
| `session_ended` | `{"code":"api_key_revoked","message":"…"}` | The credential stopped being valid (the code and message are the API's error). The stream closes. |

Resync reasons:

| `reason` | Why |
|---|---|
| `cursor_expired` | Your cursor is older than what this instance keeps (about 5 minutes, at most 1,024 events per org). |
| `other_instance` | Your cursor came from another instance, or from before a restart. |
| `bus_reconnected` | The server's connection to Postgres reconnected, so events may be missing. Sent to every stream. |
| `lagged` | Your client didn't read fast enough (or the server couldn't tell whose an event was). The stream closes after it; reconnect. |

## Resume

Send the last cursor you saw as `Last-Event-ID` (EventSource does this itself when it reconnects
within one `EventSource` object) or as `?last_event_id=`. If the same instance still holds
everything after that cursor, it replays it, exactly once, then streams live, and `hello` says
`"resumed": true`. Otherwise you get `resync`.

The honest limit: resume is best effort, within about 5 minutes, on the same instance. Behind a load
balancer, or after a deploy, expect `resync` and refetch.

## Limits

- 8 open streams per person or key. The 9th is `429 rate_limited`: "You have 8 live connections
  open. Close a Console tab and try again."
- 200 open streams per org on each instance.
- Each stream is closed after 30 minutes, and re-checks its credential every 60 seconds.
- A stream that falls 256 events behind gets `resync lagged` and is closed.

## Examples

curl:

```sh
curl -N -H "Authorization: Bearer $DEEDGLASS_API_KEY" \
  "http://127.0.0.1:8080/v1/events?types=action.*,deed.sealed"
```

In the browser (same origin, with the Console's session cookie):

```js
const es = new EventSource('/v1/events?types=action.*,deed.sealed');
es.addEventListener('hello', (e) => console.log('live', JSON.parse(e.data)));
es.addEventListener('action.witnessed', (e) => render(JSON.parse(e.data)));
es.addEventListener('resync', () => refetchFirstPage());
es.addEventListener('session_ended', () => { es.close(); showSignIn(); });
```

After the server closes a stream (30 minutes, or `lagged`), a new `EventSource` can't set
`Last-Event-ID`: open it with `?last_event_id=<the last id you saw>`.

The CLI, with an API key:

```
$ deedglass actions list --follow
Following actions · org acme · live · Ctrl-C to stop
14:02:09  ✓ postgres.execute       allowed   41 ms    L2 · VERIFIED        act_01k6…
◐ Reconnecting · resumed from 7f3k2m.1842 · nothing missed
```

It reconnects with backoff (1, 2, 4, 8, 16, 30 s, ±20%), and falls back to polling the Ledger
directly when the server has no event stream. Without an API key it polls the Ledger in direct
database mode, as before.

## How it works

One LISTEN connection per process carries `deedglass_events` (with `deedglass_identity` and the
other internal channels). `ledger.Bus` fans it out inside the process: the Gate's long polls, the
webhook worker's wake, and the Console's event hub (`internal/console`). The hub keeps a short ring
of events per watched org for resume, reads the objects a batch of events is about in one read-only
transaction per kind (batches of up to 50 ms or 100 ids, only for orgs someone is watching), and
hands each stream its events without ever waiting for a slow client. Budgets and the measured
numbers: `make bench-events` (bench/results/). The design record is ADR-054 (Console pages and the
event stream).
