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
`policy_error`, `rate_limit_error` and `api_error`. `code` is one of 33 codes; branch on it, not
on `message`. `param` is always present, and `null` when no single field is at fault. Every code
has a section in [Errors](api/errors.md), which is where `doc_url` points.

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

**The key.** 1–255 printable ASCII characters, sent once; a UUID works. Anything else is 400
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
| `GET /v1/actions/{id}` | `actions:read` | the action with `request`, `result`, `witness.detail`, `events`, `deeds`, `approval` |
| `GET /v1/deeds` | `deeds:read` | a list of deed envelopes by seq |
| `GET /v1/deeds/{id}` | `deeds:read` | the stored canonical bytes; `ETag` = `body_hash`; `Accept: application/cose` for COSE_Sign1 |
| `GET /v1/deeds/export` | `deeds:read` | a streamed export bundle (`application/gzip`) |
| `GET /v1/ledger/check` | `deeds:read` | a `ledger_check` (intact, broken or incomplete) |

All six are `SessionOrKey`, viewer and up. Until S07/S09 the only principal is the dev principal
(`DEEDGLASS_ENV=dev`, loopback, `Deedglass-Dev-Org: org_…`); anything else is 401
`authentication_required`. Examples, filters and error copy: [LEDGER.md](LEDGER.md). The schemas
are in `api/openapi.yaml` (`Action`, `ActionDetail`, `ActionEvent`, `ActionList`, `DeedList`,
`LedgerCheck`).

```sh
curl -s -H 'Deedglass-Dev-Org: org_01j9z8m2k4p6r8t0v2x4z6b8d0' 'http://127.0.0.1:8080/v1/actions?verdict=FAILED'
```
```json
{ "error": { "type": "invalid_request_error", "code": "validation_failed",
  "message": "verdict must be VERIFIED, MISMATCH or UNVERIFIABLE. You sent \"FAILED\".",
  "param": "verdict", "request_id": "req_01j9zv…", "doc_url": "https://docs.deedglass.com/errors/validation_failed" } }
```

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
| `Idempotency-Key` | Safe retries for POST, 1–255 printable ASCII characters. |
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
| `Deedglass-Idempotent-Replayed` | `true` on a replayed response. |
| `Content-Type` | `application/json; charset=utf-8` for JSON; `application/yaml` for the OpenAPI document. |
| `Content-Length` | Every response the pipeline writes. |
| `Allow` | 405 answers. |
| `Retry-After` | 429 answers, and 409 `idempotency_key_in_use` (`1`). |
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
| `limit` on lists | 1–100, default 20. |
| `Idempotency-Key` | 1–255 printable ASCII characters. |
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
