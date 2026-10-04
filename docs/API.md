# API conventions

These rules hold for every Deedglass endpoint. The code that enforces them is `internal/httpx`;
the machine-readable description is `api/openapi.yaml` (OpenAPI 3.1.1), served at
`GET /api/openapi.yaml`.

## Base URL

| Where | Base URL |
|---|---|
| Local (`make dev`, `deedglass serve`) | `http://127.0.0.1:8080` |
| Cloud beta | `https://api.deedglass.com` |

Endpoints live under `/v1`. The OpenAPI document is the one exception, at `/api/openapi.yaml`.
The server speaks plain HTTP; in production, TLS terminates at a proxy in front of it.

```sh
curl -si http://127.0.0.1:8080/v1/health
```

## Authentication

Each endpoint declares one of four kinds of authentication. The OpenAPI document shows it as
`x-deedglass-auth`.

| Kind | What it accepts |
|---|---|
| `public` | Nothing needed. Health, version and the OpenAPI document. |
| `agent_key` | An API key. |
| `session` | A Console session. |
| `session_or_key` | A Console session or an API key. |

**API keys** go in the `Authorization` header:

```
Authorization: Bearer dg_live_…
```

Test-mode keys start with `dg_test_`. Any other scheme on an endpoint that takes keys is 401
`invalid_api_key`. A key can also need scopes (`actions:execute`, `actions:read`, `deeds:read`,
`admin`, `actions:undo`); a key without one gets 403 `insufficient_scope`.

**Console sessions** use the `__Host-deedglass_session` cookie, plus CSRF protection: the
`__Host-deedglass_csrf` cookie, echoed in the `Deedglass-CSRF` header. The Console handles this
for you; the cookie is `__Host-` prefixed, so it only travels over HTTPS to the exact host.

API keys arrive in S08 and Console sessions in S07. Until then, every endpoint that isn't public
answers 401 `authentication_required`. It never lets a request through without credentials.

When a request sends a Bearer token to an endpoint that takes keys, Deedglass checks the key and
doesn't fall back to the session cookie. Endpoints that take keys send
`WWW-Authenticate: Bearer realm="deedglass"` with their 401 answers.

## Versions

Send `Deedglass-Version: 2026-10-01` to choose a dated version, or leave it out to use your org's
pinned version (the latest, for public endpoints). Every response echoes the version that
applied. Errors raised before the version check (404 `not_found`, 405 `method_not_allowed`, 413
`request_too_large`, 415 `unsupported_media_type` and 401 answers) echo the version you sent if
it is valid, else the latest. A pinned version never changes behaviour; breaking changes ship only under a new date.
The rules and the changelog are in [Versions](api/versions.md).

## Errors

Every error has the same body:

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "validation_failed",
    "message": "`limit` must be between 1 and 100.",
    "param": "limit",
    "request_id": "req_01j9zr3m5k8q2w7x4t6v0b1c9d",
    "doc_url": "https://docs.deedglass.com/errors/validation_failed"
  }
}
```

`type` is one of six: `invalid_request_error`, `authentication_error`, `permission_error`,
`policy_error`, `rate_limit_error` and `api_error`. `code` is one of 41 codes; branch on it, not
on `message`. `param` is always present, and `null` when no single field is at fault. Every code
has a section in [Errors](api/errors.md), which is where `doc_url` points. Two codes add a field:
`policy_invalid` carries `diagnostics`, and a refusal of `POST /v1/actions` carries the sealed
`action` ([Actions and the Gate](#actions-and-the-gate-s13)).

Unknown paths and wrong methods get the same envelope: 404 `not_found` (with a suggestion when a
path is two edits or fewer from a fixed endpoint path) and 405 `method_not_allowed` (with an `Allow`
header). A panic in a handler is a 500 `internal_error`; the stack goes to the server log and
never into the body.

## Idempotent requests

Send an `Idempotency-Key` with a POST, and retrying it is safe: the work runs at most once, and
every retry gets the first response back.

```sh
curl -s -X POST https://api.deedglass.com/v1/agents \
  -H 'Authorization: Bearer dg_live_…' \
  -H 'Idempotency-Key: 5b0c6d2e-8f4a-4c1e-9a53-2f7d1e0b8c44' \
  -H 'Content-Type: application/json' \
  -d @agent.json
```

**The key.** 1 to 255 printable ASCII characters, sent once; a UUID works. Anything else is 400
`idempotency_key_invalid`. Each endpoint says whether a key is optional or required; a required
key that is missing gets the same code. Only POST requests use keys; other methods ignore the
header.

**The window and the scope.** A key is remembered for 24 h from its first use. It is scoped to
your org and the principal that sent it:

- the agent (`agt_…`) for an agent's API key
- the person (`usr_…`) for a Console session
- the key itself (`key_…`) for an org admin key

Two agents can use the same key string without affecting each other. Rolling an agent's API key
keeps its stored responses, so a retry sent with the new key still replays. After 24 h the key is
treated as new.

**The fingerprint.** Deedglass hashes the method, the route pattern, the path and the body bytes
(SHA-256). A retry must match all four. Bodies are compared byte for byte, so the same JSON with
different spacing or member order counts as a different request.

**What happens on a retry:**

| Situation | Answer |
|---|---|
| First request with the key | The handler runs; the response is stored, then sent. |
| Same key, same request, first one finished | The stored status and body, plus `Deedglass-Idempotent-Replayed: true`. The handler doesn't run. |
| Same key, same request, first one still running | 409 `idempotency_key_in_use`, with `Retry-After: 1`. |
| Same key, different request | 422 `idempotency_conflict`, with `param: "Idempotency-Key"`. |
| The first request's lock ran out (its timeout plus 5 s) | The retry takes over and runs; the first attempt is presumed dead. |

**What is stored.** Every completed response, 4xx and 5xx included, so a retry never runs a side
effect twice. A handler that panics is stored as the same 500 the client got. Nothing is stored
when:

- the key was in use (409 `idempotency_key_in_use`)
- the handler answered 429 or 503: those mean the work didn't run, so a retry runs it
- the response body was larger than 1 MiB: the key is released, and a retry runs again
- the request was refused before its handler ran (credentials, version, body size, content type,
  a bad key)

A timeout is a completed response too. If the handler runs past its timeout, the 504 `timeout` is
stored, and a retry with the same key replays that 504 for 24 h without running the work again.
The work may have partly run: fetch the object to see its state, and send a new key if you need to
run it again.

**What a replay carries.** The stored status, body, and these headers when the first response had
them: `Content-Type`, `Retry-After`, `Allow`, `Location` and `WWW-Authenticate`. It also carries
`Deedglass-Idempotent-Replayed: true`, a fresh `Deedglass-Request-Id` for the retry, and the
`Deedglass-Version` the retry resolved. The body is the stored bytes, so a replayed error
envelope keeps the first request's `request_id`, while the `Deedglass-Request-Id` header is the
retry's own. Quote the header's id for the retry, and the body's id for the original request.

**Responses with a secret.** Some endpoints show a secret once, such as creating or rolling an API
key (S08). They are marked Sensitive. Their response is stored with the secret field set to
`null`, so the secret never reaches the database. A replay returns the stored object with
`"secret": null` and `Deedglass-Idempotent-Replayed: true`: you learn the key's id, and can roll
or revoke it.

**Public endpoints.** A public POST has no principal to scope a key to. Deedglass checks the key's
format, then ignores it.

**The one exception.** `POST /v1/actions` doesn't use this mechanism. From S13 the Gate owns its
idempotency, keyed on your org, the agent and the key. There, a duplicate that arrives while the
first is running waits for the original result instead of getting 409. Replays and conflicts
answer the same way as above: `Deedglass-Idempotent-Replayed: true`, and 422
`idempotency_conflict`.

## Lists and cursors

Every list has one shape:

```json
{
  "object": "list",
  "data": [],
  "has_more": false,
  "next_cursor": null
}
```

- `data` is always an array, never `null`.
- `limit` is 1–100 and defaults to 20. Anything else, including an empty `?limit=`, is 400
  `validation_failed` with `param: "limit"`.
- When `has_more` is `true`, `next_cursor` holds a string. Pass it back as `?cursor=` for the next
  page. When `has_more` is `false`, it is `null`.
- A cursor is opaque. Don't build or edit one; pass the value you got back unchanged. A cursor
  this API didn't issue is 400 `validation_failed` with `param: "cursor"`.

Cursors are unsigned on purpose. Row-level security scopes every query to your org, so a crafted
cursor can only move within data you can already read.

The Ledger lists (`GET /v1/actions`, `GET /v1/deeds`) default to 25 and bind their cursor to
the filters it was issued with ([LEDGER.md](LEDGER.md) §3).

## The Ledger routes (S06)

| Route | Scope | Returns |
|---|---|---|
| `GET /v1/actions` | `actions:read` | a list of `action` objects, newest first, with filters |
| `GET /v1/actions/{id}` | `actions:read` | the action with `request`, `result`, `witness.detail`, `events`, `deeds`, `approval`; `?wait=` long-polls (S13) |
| `GET /v1/deeds` | `deeds:read` | a list of deed envelopes by seq |
| `GET /v1/deeds/{id}` | `deeds:read` | the stored canonical bytes; `ETag` = `body_hash`; `Accept: application/cose` for COSE_Sign1 |
| `GET /v1/deeds/export` | `deeds:read` | a streamed export bundle (`application/gzip`) |
| `GET /v1/ledger/check` | `deeds:read` | a `ledger_check` (intact, broken or incomplete) |

All six are `SessionOrKey`, viewer and up. Since S07 they take a Console session (the session
cookie; GETs need no CSRF header); API keys arrive in S08. Without either the answer is 401
`authentication_required`. Examples, filters and error copy: [LEDGER.md](LEDGER.md). The schemas
are in `api/openapi.yaml` (`Action`, `ActionDetail`, `ActionEvent`, `ActionList`, `DeedList`,
`LedgerCheck`).

```sh
curl -s -b cookies.txt 'http://127.0.0.1:8080/v1/actions?verdict=FAILED'
```
```json
{ "error": { "type": "invalid_request_error", "code": "validation_failed",
  "message": "verdict must be VERIFIED, MISMATCH or UNVERIFIABLE. You sent \"FAILED\".",
  "param": "verdict", "request_id": "req_01j9zv…", "doc_url": "https://docs.deedglass.com/errors/validation_failed" } }
```

## Actions and the Gate (S13)

Every tool call an agent makes goes through `POST /v1/actions`, and every action the Gate records
ends as a sealed deed. How the Gate works inside is in [GATE.md](GATE.md); this section is the API.

| Route | Who | Returns |
|---|---|---|
| `POST /v1/actions` | an agent key with `actions:execute` | the `action`; a refusal is an error envelope with the sealed `action` in it |
| `GET /v1/actions/{id}?wait=0..25` | `actions:read` (the agent's own actions), or a Console session | the action, after a long poll |
| `POST /v1/actions/{id}/cancel` | the agent's own key, an admin+ Console session or an org admin key | the action, `cancelled` and sealed; only from `pending_approval` |
| `POST /v1/actions/{id}/result` | the same agent's key | the action, witnessed and sealed; proxy mode only, once |

### The action object

The Gate answers with the same `action` object as the Ledger, so an agent, the CLI and the Console
read one shape. The fields an agent needs first:

| Field | What it says |
|---|---|
| `status` | What happened, in one word (the table below). Branch on it. |
| `state` | The exact lifecycle state ([GATE.md](GATE.md), the state machine). |
| `livemode` | `true` for a `dg_live_` key, `false` for a `dg_test_` key. Recorded, not enforced. |
| `decision` | The policy's answer: `outcome`, `code`, `source` (`rule`, `default`, `budget`, `stop` or `builtin`), `rule_id`, `reason`, the `message` the agent sees (verbatim), the `policy` and `policy_version` that decided, and the `shadow` decision when a shadow policy is active. |
| `approval` | The approval it waits for, or waited for: `id`, `status`, `expires_at`. `null` when no rule asked for one. |
| `execution` | `status` (`succeeded`, `failed` or `unknown`), the times, `error_code`, `error_message`, and, on the Gate's own responses only, `result`. |
| `witness` | `level`, `verdict`, `method` and `reasons`. In v1 the level is L0 and the method is `reported` (the tool reported success) or `edge_reported` (the edge did, in proxy mode). L0 is not an independent check. |
| `next_action` | What to do next: `await_approval` (with `poll_url`) or `execute_at_edge` (with `report_url` and `deadline`). `null` otherwise. |
| `deed` | The deed's id once the action is sealed. |

### The status field

A client that only checks for 2xx never mistakes "not run" for "done": every refusal is 4xx.

| `status` | HTTP on `POST /v1/actions` | Meaning |
|---|---|---|
| `executed` | 200 | The tool ran and the action is sealed, whatever the verdict. |
| `processing` | 202 | Still executing or witnessing when `wait` ended. Poll `GET /v1/actions/{id}?wait=25`. |
| `authorized` | 200 | Proxy mode: run the tool at the edge now, then report the result. |
| `pending_approval` | 202 | Waiting for a person. `next_action.poll_url` waits for the decision. |
| `denied` | 403 or 429, with `error.action` | A policy, a budget or Stop refused it. Nothing ran. The refusal is sealed. |
| `failed` | 200 | The tool certainly didn't apply the call (it refused it, or it was never sent). |
| `expired` | (later) | No one decided before the approval expired. |
| `cancelled` | (later) | Cancelled while it waited for approval. |

The refusal codes: `policy_denied` and `policy_unavailable` (403 `policy_error`), `budget_exceeded`
(429 `rate_limit_error`, with `Retry-After`), `agent_stopped`, `agent_suspended` and `org_stopped`
(403 `permission_error`), and `tool_unknown` (404). A malformed request is 400
`validation_failed`, and an identity failure (`intent_required`, `intent_invalid`,
`intent_replay`) is 403: neither creates an action. Every code is in [Errors](api/errors.md).

### An allowed action

```sh
curl -s -X POST http://127.0.0.1:8080/v1/actions \
  -H "Authorization: Bearer $DEEDGLASS_API_KEY" \
  -H 'Idempotency-Key: 7d1e0b8c-44a2-4c1e-9a53-5b0c6d2e8f4a' \
  -H 'Content-Type: application/json' \
  -d '{"tool":"fake.echo","args":{"message":"hello"},"wait":10}'
```
```json
{
  "object": "action",
  "id": "act_01k6w3p8r2t4v6x8z0b2d4f6h8",
  "livemode": false,
  "tool": "fake.echo",
  "mode": "gateway",
  "state": "sealed",
  "status": "executed",
  "decision": { "outcome": "allow", "code": null, "source": "rule", "rule_id": "allow-fake", "message": null, "…": "…" },
  "approval": null,
  "execution": { "status": "succeeded", "latency_ms": 1, "error_code": null, "result": { "echo": { "message": "hello" } }, "…": "…" },
  "witness": { "level": "L0", "verdict": "VERIFIED", "method": "reported", "reasons": [] },
  "request_digest": "sha256:c2cb9a8c51c029f97049fed02f985cc49cee22836423d2c9a8e1b74e333a3c4c",
  "deed": "deed_01k6w3p9a1c3e5g7j9m1q3s5v7",
  "next_action": null,
  "…": "…"
}
```

`wait` (0 to 25 seconds, default `gate.wait_timeout`, 10 s) is how long the request waits for the
tool. If the tool is still running when it ends, the answer is 202 `processing`; the action keeps
running, and it is recorded and sealed whether or not you are still waiting.

### A denied action

```sh
curl -s -X POST http://127.0.0.1:8080/v1/actions \
  -H "Authorization: Bearer $DEEDGLASS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"tool":"fake.write","args":{"rows":301,"table":"orders"}}'
```
```json
{
  "error": {
    "type": "policy_error",
    "code": "policy_denied",
    "message": "Blocked by policy \"no-bulk-writes\": writes over 100 rows need a person, not an agent.",
    "param": null,
    "request_id": "req_01k6w4a3d5f7h9k1n3q5s7v9x1",
    "doc_url": "https://docs.deedglass.com/errors/policy_denied",
    "action": {
      "object": "action",
      "id": "act_01k6w4a2c4e6g8j0m2p4r6t8v0",
      "state": "sealed",
      "status": "denied",
      "decision": { "outcome": "deny", "code": "policy_denied", "source": "rule", "rule_id": "no-bulk-writes", "…": "…" },
      "deed": "deed_01k6w4a4e6g8j0m2p4r6t8w0y2",
      "…": "…"
    }
  }
}
```

The message is the decision's own sentence, verbatim. `error.action` is the refusal as the Ledger
recorded it, so the agent can name the deed.

### Proxy mode: authorize, then report

In proxy mode the edge (S14's MCP wrapper, or your own code) runs the tool and Deedglass records
what it reports. First ask:

```sh
curl -s -X POST http://127.0.0.1:8080/v1/actions \
  -H "Authorization: Bearer $DEEDGLASS_API_KEY" \
  -H 'Idempotency-Key: 2f7d1e0b-8c44-4a2c-9a53-5b0c6d2e8f4a' \
  -H 'Content-Type: application/json' \
  -d '{"tool":"filesystem.write_file","mode":"proxy","connector_type":"mcp",
       "args":{"path":"/tmp/deedglass-demo/allowed/notes.txt","content":"hello"}}'
```
```json
{
  "object": "action",
  "id": "act_01k6w5b3d5f7h9k1n3q5s7w9y1",
  "mode": "proxy",
  "state": "executing",
  "status": "authorized",
  "next_action": {
    "type": "execute_at_edge",
    "report_url": "/v1/actions/act_01k6w5b3d5f7h9k1n3q5s7w9y1/result",
    "deadline": "2026-10-05T04:30:10.903Z"
  },
  "…": "…"
}
```

Then run the tool, and report what it returned, once, before the deadline
(`gate.edge_result_timeout`, 10 minutes by default):

```sh
curl -s -X POST http://127.0.0.1:8080/v1/actions/act_01k6w5b3d5f7h9k1n3q5s7w9y1/result \
  -H "Authorization: Bearer $DEEDGLASS_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"status":"succeeded","started_at":"2026-10-05T04:20:11.020Z","finished_at":"2026-10-05T04:20:11.061Z",
       "result":{"content":[{"type":"text","text":"Wrote 5 bytes to /tmp/deedglass-demo/allowed/notes.txt"}],"isError":false}}'
```

The answer is the sealed action with `witness.method: "edge_reported"`: the deed records what the
edge said, not an independent check. Send `result_digest` instead of `result` when the result is
too large to send. A second report, or one after the deadline, is 409 `conflict`; an action whose
edge never reported is sealed UNVERIFIABLE with the reason `edge_lost`.

### Idempotent replay

`POST /v1/actions` owns its idempotency ([GATE.md](GATE.md), Idempotency). Send the same request
with the same key again, and you get the original action back; the tool ran once.

```sh
curl -si -X POST http://127.0.0.1:8080/v1/actions \
  -H "Authorization: Bearer $DEEDGLASS_API_KEY" \
  -H 'Idempotency-Key: 7d1e0b8c-44a2-4c1e-9a53-5b0c6d2e8f4a' \
  -H 'Content-Type: application/json' \
  -d '{"tool":"fake.echo","args":{"message":"hello"},"wait":10}'
```
```
HTTP/1.1 200 OK
Deedglass-Idempotent-Replayed: true
…
```

- The key names one action of this agent for 24 hours. Another agent's key is independent.
- The comparison is the request digest (the tool, the arguments in their RFC 8785 form, the mode
  and the tool digest) plus `Deedglass-On-Behalf-Of` and `Deedglass-Session`. Spacing and member
  order don't matter; a new signed intent doesn't either.
- A duplicate that arrives while the original is still running waits for it (up to its own
  `wait`) and answers with the same action.
- The same key with a different request is 422 `idempotency_conflict`.
- A replayed refusal answers with the same status and message; a budget's `Retry-After` counts
  down to the same reset.

### Long polls

`GET /v1/actions/{id}?wait=25` answers when the action is settled (sealed, or waiting for a
person), when its state changes, or after 25 seconds, whichever comes first. An action already
`pending_approval` when the wait starts waits for its next change, so `next_action.poll_url` is
how a client waits for a person's decision.

```sh
curl -s "http://127.0.0.1:8080/v1/actions/act_01k6w6c4e6g8j0m2p4r6t8w0y2?wait=25" \
  -H "Authorization: Bearer $DEEDGLASS_API_KEY"
```

- `wait` is 0 to 25 seconds, sent once; `0` answers at once. `expand[]=request`, `result` or
  `events` is accepted; the answer always carries all three.
- For an agent key, a Stop ends the wait at once with `agent_stopped` (or `org_stopped`).
- Each agent may hold 20 open waits; the 21st is 429 `rate_limited` with `Retry-After: 1`. The
  count is kept per server instance.
- If the wait ends while nothing changed, the answer is the action as it stands. Poll again.

To cancel an action that is still waiting for a person:

```sh
curl -s -X POST http://127.0.0.1:8080/v1/actions/act_01k6w6c4e6g8j0m2p4r6t8w0y2/cancel \
  -H "Authorization: Bearer $DEEDGLASS_API_KEY"
```

It answers with the action, `cancelled` and sealed. Once the action is executing, the answer is 409
`conflict`: "This action is already executing, so it can't be cancelled."

## Tool visibility for MCP wrappers (S14)

`POST /v1/tools/resolve` (an agent key with `actions:execute`, at most 500 tools) answers, for each
tool an MCP server lists, whether the enforced policy denies every call to it from this agent.
`deedglass mcp wrap` uses it to leave such tools out of `tools/list` ([MCP.md](MCP.md)). Nothing is
recorded, and the answer never replaces the decision on `POST /v1/actions`.

```sh
curl -s -X POST http://127.0.0.1:8080/v1/tools/resolve \
  -H "Authorization: Bearer $DEEDGLASS_API_KEY" -H "Content-Type: application/json" \
  -d '{"connector":"scratch","tools":[{"name":"read_text_file"},{"name":"move_file"}]}'
```

```json
{
  "object": "list",
  "data": [
    { "object": "tool_visibility", "name": "read_text_file", "tool": "scratch.read_text_file",
      "visibility": "visible", "rule_id": "allow-scratch-reads", "reason": null },
    { "object": "tool_visibility", "name": "move_file", "tool": "scratch.move_file",
      "visibility": "hidden", "rule_id": null,
      "reason": "No rule allows this tool, and the policy default is deny." }
  ],
  "has_more": false,
  "next_cursor": null,
  "policy_version": "pv_01k6w4a3d5f7h9k1n3q5s7v9x1"
}
```

The first rule whose `match` covers `<connector>.<name>` decides; a rule with a `when` counts as
visible (it can't be decided ahead of time); with no covering rule, the default decides. Budgets,
Stop and MCP `annotations` are ignored. `connector_type` defaults to `mcp`; `digest` is the
wrapper's pinned definition digest (optional). A malformed connector, name or digest, or more than
500 tools, is 400 `validation_failed`. When policies can't be loaded, every tool is `visible` (the
calls are still refused).

## Native tools and the MCP endpoints (S15)

`GET /v1/tools` (an agent key with `actions:execute`) lists the Gate's native tools (the ones
`/mcp/native` serves), named `<connector>.<tool>`, minus the ones the active policy always denies
to this agent; `?include_hidden=true` adds those with `visibility: hidden`.

```sh
curl -s http://127.0.0.1:8080/v1/tools -H "Authorization: Bearer $DEEDGLASS_API_KEY"
```

```json
{ "object": "list",
  "data": [
    { "object": "tool", "name": "fake.echo", "connector": "fake", "title": "Echo",
      "description": "Returns its arguments.",
      "input_schema": { "type": "object", "properties": { "message": { "type": "string", "maxLength": 65536 } } },
      "side_effect": "read", "visibility": "visible" } ],
  "has_more": false, "next_cursor": null, "policy_version": "pv_01k6w4a3d5f7h9k1n3q5s7v9x1" }
```

`POST /mcp/{connector}` and `POST /mcp/native` are MCP Streamable HTTP endpoints: one JSON-RPC
message per POST, POST only (GET and DELETE answer 405), sessionless, both MCP eras. They are
documented in [MCP.md](MCP.md); `api/openapi.yaml` describes them without modelling JSON-RPC.
Their refusals before the message is read (401, 403, 413, 415, 429) are JSON-RPC errors whose
`data` carries the usual `code`, `doc_url` and `request_id`.

## Connectors and credentials (S16)

Connectors turn an API into governed tools; their credentials are write-only
([CONNECTORS.md](CONNECTORS.md), [VAULT.md](VAULT.md)). Every route takes a Console session (viewers
read, admins change) or an org admin key; agent keys get 403 `insufficient_scope` everywhere.

```sh
deedglass connector credentials set orders --from-env ORDERS_TOKEN   # PUT /v1/connectors/{id}/credentials
```

```json
{ "object": "credential", "connector": "con_01j9zq7c4m8x2v6t0r3n5k1h7d", "set": true, "kind": "bearer", "fields": ["token"],
  "hint": "…9f2c", "version": 1, "kek_id": "kek:3f9a1c2b7d8e", "updated_at": "2026-10-06T05:14:02Z", "updated_by": "key_01j9…" }
```

No route returns a credential value. `PUT …/credentials` bodies are never logged and never stored
for replay. A blocked `base_url` is 400 `destination_blocked` with `param: "config.base_url"`. The
nine routes, their objects and their errors are in `api/openapi.yaml` (tag Connectors).

## The policy routes (S12)

| Route | Role | Returns |
|---|---|---|
| `GET /v1/policies` | viewer+ | a list of `policy` objects by name |
| `POST /v1/policies` | admin+ | 201: the `policy` and its v1 (`version`), not active; 400 `policy_invalid` with `diagnostics` |
| `GET /v1/policies/active` | viewer+ | the `policy_active_set`: the org's enforce and shadow |
| `GET /v1/policies/{id}` | viewer+ | a `policy` (`{id}` is a `pol_` id or the name) |
| `GET /v1/policies/{id}/versions` | viewer+ | a list of `policy_version` objects, newest first, without `source` |
| `POST /v1/policies/{id}/versions` | admin+ | 201 a new `policy_version`; 200 the existing one when the content is identical |
| `GET /v1/policies/{id}/versions/{v}` | viewer+ | a `policy_version` with its `source` (`{v}` is a number or a `pv_` id) |
| `POST /v1/policies/{id}/versions/{v}/activate` | admin+ | a `policy_activation` (`pa_` id) with the sealed `deeds` |
| `POST /v1/policies/{id}/versions/{v}/deactivate` | admin+ | the ended shadow `policy_activation` with its deed |
| `POST /v1/policies/lint` | viewer+ | a `policy_lint_result` (200 even when the policy is invalid) |
| `POST /v1/policies/evaluate` | viewer+ | a `policy_decision` (the dry run; nothing is reserved) |
| `POST /v1/policies/test` | viewer+ | a `policy_test_result` |

Every one takes a Console session (by role; POSTs need the `Deedglass-CSRF` header) or an org admin
key (scope `admin`, acting as admin). **Agent keys are refused on every policy route, reads
included** (403 `insufficient_scope`: "Agents cannot read or test policies. Use the Console or an
admin key."), so an agent can't probe its own policy for gaps. The changing POSTs accept
`Idempotency-Key`; lint, evaluate and test change nothing and take 60 requests a minute per person
or key, a 2 s timeout and bodies up to 1 MiB (2 MiB for test). Request and response shapes, with
examples: `api/openapi.yaml` (tag `Policies`) and [POLICIES_GUIDE.md](POLICIES_GUIDE.md).

```sh
curl -s -X POST http://127.0.0.1:8080/v1/policies/postgres-safe/versions/1/activate \
  -H "Authorization: Bearer $DEEDGLASS_API_KEY" -H "Idempotency-Key: pilot-enforce-1" \
  -H "Content-Type: application/json" \
  -d '{"mode":"enforce","reason":"Guard the orders database before the pilot."}'
```

## The Overview and agent usage (S25)

| Route | Who | Returns |
|---|---|---|
| `GET /v1/overview?window=24h\|7d` | a Console session (viewer and up) or an org admin key; agent keys get 403 `insufficient_scope` | an `overview`: action, block and verdict counts with their series, pending approvals, the 8 most recently active agents with today's usage, and up to 8 `attention_item`s |
| `GET /v1/agents?expand[]=usage`, `GET /v1/agents/{id}?expand[]=usage` | as the agent routes | each agent plus `usage` (`calls_minute`, `calls_hour`, `calls_today`, `spend_today`) and `last_action_at` |

Every number, window and attention rule is defined in [OVERVIEW_METRICS.md](OVERVIEW_METRICS.md).
The overview is cached for 10 seconds per org and window; `generated_at` says when it was read.
`expand=usage` works too; any other expand value is 400 `validation_failed`, and without the
expansion the agent object is unchanged. The schemas are `Overview`, `OverviewCount`,
`AttentionItem` and `AgentUsage` in `api/openapi.yaml`.

## Insights (S27)

Replay, stats, shadow disagreements, anomalies and the Insights settings. Insights reads and
reports; its only input into decisions is the opt-in anomaly facts ([INSIGHTS.md](INSIGHTS.md)).

| Route | Role | Returns |
|---|---|---|
| `POST /v1/policies/{id}/versions/{v}/replay` | admin+ | 202 a queued `replay` (`rpl_` id); optional `Idempotency-Key`; `{v}` is the version number |
| `GET /v1/replays` | viewer+ | a list of `replay` objects, newest first (`?policy=pol_…&status=…`) |
| `GET /v1/replays/{id}` | viewer+ | a `replay`, with its result once `status` is `succeeded` |
| `POST /v1/replays/{id}/cancel` | admin+ | the `replay`, `cancelled`; 409 `conflict` once it finished |
| `GET /v1/insights/stats` | viewer+ | an `insights_stats` (`?window=7d\|30d` or `from`/`to`, `by=rule\|tool\|agent\|none`, `agent`, `tool`, `compare=previous`) |
| `GET /v1/insights/shadow` | viewer+ | a `shadow_report` (`?shadow_version=pv_…&from=&to=`) |
| `GET /v1/insights/anomalies` | viewer+ | a list of `anomaly` objects plus `active_now`, `anomaly_facts`, `notice` and `baseline_building` |
| `GET /v1/insights/anomalies/{id}` | viewer+ | an `anomaly` |
| `POST /v1/insights/anomalies/{id}/acknowledge` | admin+ | the `anomaly`, `acknowledged` |
| `POST /v1/insights/anomalies/{id}/resolve` | admin+ | the `anomaly`, `resolved`, with the sealed `anomaly.resolved` deed; `{"reason":"…"}`, 3 to 280 characters |
| `GET /v1/insights/settings` | viewer+ | the `insights_settings` |
| `PATCH /v1/insights/settings` | admin+ | the `insights_settings`, with the sealed `insights.settings_changed` deed when something changed |

Every route takes a Console session (by role; POST and PATCH need the `Deedglass-CSRF` header) or
an org admin key (scope `admin`, acting as admin). **Agent keys get 403 `insufficient_scope` on every
Insights route, reads included**: "Agent keys can't read Insights. Use the Console or an org admin key." The
Insights lists take `limit` 1 to 100 and default to 25. Stats and the shadow report have a 10 s
timeout; every Insights read runs in a read-only org transaction with a 5 s statement timeout.

Errors specific to Insights: 400 `replay_window_invalid` (a window over 90 days, or `from` not
before `to`), 400 `replay_too_large` (over 1,000,000 actions), and 429 `rate_limited` after 10
replays created by an org in an hour (`Retry-After: 300`). The rest are canonical codes:
`not_found`, `conflict`, `validation_failed`, `insufficient_role`, `insufficient_scope`.

```sh
curl -s -X POST http://127.0.0.1:8080/v1/policies/acme-sim/versions/3/replay \
  -H "Authorization: Bearer $DEEDGLASS_API_KEY" -H "Idempotency-Key: replay-v3-week-41" \
  -H "Content-Type: application/json" \
  -d '{"from":"2026-10-02T00:00:00Z","to":"2026-10-09T00:00:00Z","baseline":{"type":"recorded"}}'

curl -s -H "Authorization: Bearer $DEEDGLASS_API_KEY" \
  "http://127.0.0.1:8080/v1/insights/stats?window=7d&by=rule" | jq '.groups[0].approval'

curl -s -X PATCH http://127.0.0.1:8080/v1/insights/settings \
  -H "Authorization: Bearer $DEEDGLASS_API_KEY" -H "Content-Type: application/json" \
  -d '{"anomaly_facts":true,"reason":"Require approval while an agent spikes"}'
```

The replay object, the result digest and full examples are in [REPLAY.md](REPLAY.md); every stats
definition, the shadow report and the anomaly lifecycle in [INSIGHTS.md](INSIGHTS.md); the
schemas in `api/openapi.yaml` (tag `Insights`). `anomaly.detected` reaches the event stream and
webhooks with `object: "anomaly"`, the episode `id`, `agent`, `kind`, `tool`, `severity` and
`status`.

## Objects

Every object carries `"object"`, naming its type (`"health"`, `"version"`, `"list"`, and so on),
and `"id"` when it has one. Ids are prefixed, lowercase ULIDs such as
`agt_01j9zr3m5k8q2w7x4t6v0b1c9d`; the prefix says what the id is. See [Ids](IDS.md).

Nullable fields are sent as an explicit `null`, not left out. For example, in `GET /v1/health`
the check fields `latency_ms`, `applied` and `expected` are `null` when their check didn't produce
a number.

## Timestamps

Timestamps are RFC 3339 strings in UTC with milliseconds: `2026-10-01T09:30:00.123Z`. When you
send one, any RFC 3339 precision and offset is accepted, and Deedglass converts it to UTC.

## Money

Money is an object with an integer `amount` in the currency's minor units and a lowercase ISO 4217
`currency`:

```json
{ "amount": 1250, "currency": "usd" }
```

That is 12.50 US dollars. Amounts are never floats.

## Request ids

Every response carries `Deedglass-Request-Id: req_…`, set before anything else can fail. Error
bodies repeat it as `request_id`, except in a replayed response, whose body keeps the original
request's id ([Idempotent requests](#idempotent-requests)). Quote it to support; the server log has the same id.

- An inbound `Deedglass-Request-Id` is ignored. Deedglass always mints its own.
- Send your own id in `X-Request-Id` (up to 64 printable ASCII characters) and it is logged next
  to ours as `client_request_id`. A longer or non-printable value is ignored.

## Headers

**Request headers:**

| Header | Used for |
|---|---|
| `Authorization` | `Bearer dg_live_…` or `Bearer dg_test_…` (S08). |
| `Cookie` | `__Host-deedglass_session` and `__Host-deedglass_csrf`, from the Console (S07). |
| `Deedglass-CSRF` | The CSRF token for Console sessions (S07). |
| `Deedglass-Version` | The API version, `YYYY-MM-DD`. Send it once. |
| `Idempotency-Key` | Safe retries for POST, 1 to 255 printable ASCII characters. |
| `Content-Type` | `application/json`, optionally with `; charset=utf-8`, on any request with a body. |
| `X-Request-Id` | Your own request id, logged as `client_request_id`. |
| `Deedglass-Intent` | A signed intent from the agent (S09). |
| `Deedglass-On-Behalf-Of` | The person an agent acts for (S09 and later). |
| `Deedglass-Session` | The agent's session reference (S09 and later). |
| `X-Forwarded-For` | Read only when the connection comes from a proxy listed in `DEEDGLASS_TRUSTED_PROXIES`. |

**Response headers:**

| Header | When |
|---|---|
| `Deedglass-Request-Id` | Every response. |
| `Deedglass-Version` | Every response, errors included. |
| `Deedglass-Idempotent-Replayed` | `true` on a replayed response, `POST /v1/actions` included. |
| `Content-Type` | `application/json; charset=utf-8` for JSON; `application/yaml` for the OpenAPI document. |
| `Content-Length` | Every response the pipeline writes. |
| `Allow` | 405 answers. |
| `Retry-After` | 429 answers (for `budget_exceeded`, the seconds until the budget resets), and 409 `idempotency_key_in_use` (`1`). |
| `WWW-Authenticate` | 401 answers from endpoints that take API keys. |
| `Cache-Control` | `no-store`, unless the endpoint sets its own (`public, max-age=300` for the OpenAPI document). |
| Security headers | Every response; see below. |

## Strict JSON

Request bodies are decoded strictly. Each of these is refused, and the body is never echoed in the
error:

| Body | Answer |
|---|---|
| A syntax error | 400 `invalid_json`, with the line and column (in bytes). |
| A member name that appears twice | 400 `invalid_json`. |
| Invalid UTF-8 | 400 `invalid_json`. |
| Data after the JSON value | 400 `invalid_json`. |
| An empty body | 400 `invalid_json`. |
| A member the endpoint doesn't know | 400 `validation_failed`, `param` set to the member. |
| A value of the wrong type | 400 `validation_failed`, `param` set to its path (`items.0.amount`). |

Responses use 2-space indentation and end with a newline.

## Limits

| Limit | Value |
|---|---|
| Request body | 1 MiB by default; an endpoint can set its own. Larger is 413 `request_too_large`. |
| Body on GET, HEAD and DELETE | None, unless the endpoint says otherwise; sending one is 413 `request_too_large`. |
| Request time | 30 s by default; 5 s for health, version and the OpenAPI document. Past it, 504 `timeout`. |
| Request headers | 64 KiB. |
| `limit` on lists | 1 to 100, default 20 (25 on the Insights lists). |
| `Idempotency-Key` | 1 to 255 printable ASCII characters. |
| `X-Request-Id` | 64 printable ASCII characters (longer values are ignored). |

## Security headers

Every response carries:

```
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
X-Frame-Options: DENY
Content-Security-Policy: default-src 'none'; frame-ancestors 'none'
Cross-Origin-Resource-Policy: same-origin
Cache-Control: no-store
```

In production with an `https` base URL, it also carries
`Strict-Transport-Security: max-age=63072000; includeSubDomains`. There is no `Server` header.

## What this doesn't do

- **No CORS.** The API sends no CORS headers, so browsers block cross-origin calls. Call it from
  your server, not a browser. The Console is same-origin and doesn't need them.
- **No unsigned webhooks.** Every webhook is signed in `Deedglass-Signature` (S22). Check the
  signature before you act on one.
- **No uncapped bodies.** Every endpoint has a body limit, and every request has a timeout.
- **No plain-HTTP redirects.** The server never redirects `http` to `https`, or one path to
  another. Use the `https` base URL.
- **No path guessing.** Paths are exact: `/v1/health/` is 404, and so are `//` and `..` forms. A
  404 may suggest the right path; it never serves it.
