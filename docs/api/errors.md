# Errors

Every error Deedglass returns has the same body. Only the status line and a few headers change.

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

- `type` is one of the six types below. Branch on it for broad handling.
- `code` is one of the 39 codes on this page. Branch on it for exact handling. Codes are never
  renamed; new codes are added.
- `message` is one or more sentences for a person, with a next step. Don't parse it: it can get
  more exact over time.
- `param` names the field, query parameter or header the error is about. It is always present,
  and `null` when no single field is at fault.
- `request_id` is the same `req_…` id as the `Deedglass-Request-Id` response header. Quote it to
  support.
- `doc_url` points to this page's section for the code.

The body is JSON (`Content-Type: application/json; charset=utf-8`). It never contains your
request body, header values, SQL, stack traces, file paths or internal host names. The details
behind a 5xx go to the server log under the request id.

| Type | Status | Meaning |
|---|---|---|
| `invalid_request_error` | 400, 404, 405, 409, 413, 415, 422 | The request itself needs to change. |
| `authentication_error` | 401 | Deedglass couldn't tell who is calling. |
| `permission_error` | 403 | Deedglass knows who is calling, and the caller may not do this now. |
| `policy_error` | 403 | A policy in your org blocked the action or asked for approval. |
| `rate_limit_error` | 429 | Too many requests, or a budget is used up. `Retry-After` says when to retry. |
| `api_error` | 500, 502, 503, 504 | Something failed on the Deedglass side or at a connector. |

Some codes come from sessions that are still being built. Each section says which part of
Deedglass sends the code. The conventions around errors (request ids, versions, idempotent
retries) are in [API conventions](../API.md).

Each example below shows the inner `error` object without `request_id` and `doc_url`, which every
error carries. S33 publishes each section at `https://docs.deedglass.com/errors/<code>`.

## invalid_api_key

**401 · authentication_error**

**When it happens.** Step 3 of the bearer check ([AGENTS.md](../AGENTS.md) §5): the token isn't a
Deedglass API key, its prefix is unknown, its mode doesn't match, or the secret is wrong. Every
one of those gets the same message, so the answer doesn't tell a guesser which part was wrong. A
key whose org isn't active gets it too. An `Authorization` header with a scheme other than
`Bearer` gets this code with the message "The Authorization header must be Bearer followed by an
API key (dg_live_… or dg_test_…)." Each failure counts toward the failed-auth limiter
([rate_limited](#rate_limited)).

The response carries
`WWW-Authenticate: Bearer realm="deedglass", error="invalid_token", error_description="<the message>"`.

**What to do.** Copy the key again from where you stored it: all 49 characters, starting with
`dg_live_` or `dg_test_`. Send it as `Authorization: Bearer dg_live_…`. If you no longer have it,
create a new key in Console › Agents. Deedglass shows a key's secret once, at creation.

```json
{
  "type": "authentication_error",
  "code": "invalid_api_key",
  "message": "The API key isn't valid. Check that you copied all of it (it starts with dg_live_ or dg_test_), or create a new one in Console › Agents.",
  "param": null
}
```

## api_key_revoked

**401 · authentication_error**

**When it happens.** Step 4: the key passed the hash check, and someone in your org revoked it.
Every Deedglass instance refuses a revoked key within 2 seconds. Only someone holding the real
secret ever sees this code, and it doesn't count toward the failed-auth limiter. The response
carries the `WWW-Authenticate` challenge with `error="invalid_token"`.

**What to do.** Create a new key in Console › Agents and replace the old one wherever the agent
reads it. A revoked key never works again.

```json
{
  "type": "authentication_error",
  "code": "api_key_revoked",
  "message": "The API key has been revoked. Create a new key in Console › Agents.",
  "param": null
}
```

## api_key_expired

**401 · authentication_error**

**When it happens.** Step 5: the key passed the hash check, and it was rolled, and its overlap
window has ended. Only rolled keys expire. The message names the time in UTC. Like a revoked key,
it doesn't count toward the failed-auth limiter, and the response carries the
`WWW-Authenticate` challenge with `error="invalid_token"`.

**What to do.** Use the key that replaced it (the roll response returned it once). If you don't
have it, create a key in Console › Agents.

```json
{
  "type": "authentication_error",
  "code": "api_key_expired",
  "message": "The API key expired at 2026-10-04 10:03 UTC after it was rolled. Use the new key, or create one in Console › Agents.",
  "param": null
}
```

## agent_suspended

**403 · permission_error**

**When it happens.** Step 8: the key is valid, and its agent is suspended, a planned pause.
`GET /v1/whoami` still answers and shows `"status": "suspended"`. The message never says why or
by whom.

**What to do.** An admin can resume the agent in Console › Agents, or with
`deedglass agent resume <name>`. If you didn't expect the suspension, check the agent's
`agent.suspended` deed first.

```json
{
  "type": "permission_error",
  "code": "agent_suspended",
  "message": "Agent \"support-bot\" is suspended. An admin can resume it in Console › Agents.",
  "param": null
}
```

## agent_stopped

**403 · permission_error**

**When it happens.** Step 7: the key is valid, and someone used Stop on its agent
([STOP.md](../STOP.md)). It takes precedence over `agent_suspended`. `GET /v1/whoami` still
answers and shows `"status": "stopped"`. The message never includes the stop reason or who
stopped the agent: the caller is the agent, and the reason may be sensitive.

**What to do.** Find out why it was stopped: the reason is in the `agent.stopped` deed and in
Console › Agents. An admin can start it again, with a reason, in Console › Agents or with
`deedglass agent start <name> --reason "…"`.

```json
{
  "type": "permission_error",
  "code": "agent_stopped",
  "message": "Agent \"support-bot\" is stopped. Deedglass refuses its calls until an admin starts it in Console › Agents.",
  "param": null
}
```

## org_stopped

**403 · permission_error**

**When it happens.** Step 6: someone used Stop all agents for the org. Every agent key in the org
gets this answer until an owner starts the agents again. It takes precedence over `agent_stopped`
and `agent_suspended`. Org admin keys keep read-only access (`GET`) so incident response can look
around; an admin-key request that changes state (`POST`, `PUT`, `PATCH`, `DELETE`) gets this code
with the message "…so admin keys can only read until an owner starts them again…". `GET
/v1/whoami` still answers and shows `"stopped": true`. Console sessions aren't affected.

**What to do.** Ask an owner of the org. They can start the agents in Console › Settings; the
server operator can run `deedglass org start-all --reason "…"`. Agents that were stopped one by one
stay stopped.

```json
{
  "type": "permission_error",
  "code": "org_stopped",
  "message": "All agents in Acme are stopped. An owner can start them again in Console › Settings.",
  "param": null
}
```

## insufficient_scope

**403 · permission_error**

**When it happens.** Step 9: the API key is valid and active, but lacks a scope the endpoint
lists. The pipeline checks every scope the route declares and names the first one missing. Scopes
are `actions:execute`, `actions:read`, `deeds:read`, `actions:undo` and `admin`
([AGENTS.md](../AGENTS.md) §2). An agent key on any route that manages agents or keys gets this
code with its own message, because no agent key changes any agent, including its own. The
response carries
`WWW-Authenticate: Bearer realm="deedglass", error="insufficient_scope", scope="<scope>"`.

**What to do.** Create a key with the scope named in the message, or use a key that already has
it. To manage agents, use an org admin key or the Console.

```json
{
  "type": "permission_error",
  "code": "insufficient_scope",
  "message": "This key doesn't have the deeds:read scope. Create a key with deeds:read in Console › Agents.",
  "param": null
}
```

An agent key on a management route: "This is an agent key, and managing agents needs an admin key.
Create one with deedglass org keys create on the server."

## policy_denied

**403 · policy_error**

**When it happens.** A policy in your org denied the action (S11, S13). The message names the rule
and gives its reason. On `POST /v1/actions` the envelope also carries the sealed `action`.

**What to do.** Read the reason. Change the request so the rule allows it, or ask an admin to
change the policy. Sending the same request again gets the same answer.

```json
{
  "type": "policy_error",
  "code": "policy_denied",
  "message": "Blocked by policy \"block-destructive-sql\": DROP is never allowed for agents.",
  "param": null
}
```

## approval_required

**403 · policy_error**

**When it happens.** A policy says a person must approve this action, and the endpoint can't wait
for the decision (S22). `POST /v1/actions` doesn't use this code: it answers 202 with
`status: pending_approval`.

**What to do.** Ask someone to approve it in Console › Approvals. The action runs once they do.

```json
{
  "type": "policy_error",
  "code": "approval_required",
  "message": "This action needs approval. It runs once someone approves it in Console › Approvals.",
  "param": null
}
```

## approval_expired

**403 · policy_error**

**When it happens.** An action waited for approval, and the approval window closed with no
decision (S22).

**What to do.** Send the action again to ask for a new approval. Use a new Idempotency-Key for it.

```json
{
  "type": "policy_error",
  "code": "approval_expired",
  "message": "No one decided before the approval window closed. Send the action again to ask for a new approval.",
  "param": null
}
```

## budget_exceeded

**429 · rate_limit_error**

**When it happens.** The agent used its budget for the current period (S11, S13). The response
carries `Retry-After` in seconds, and the message says when the period resets.

**What to do.** Wait until the time in the message, or ask an admin to raise the budget.

```json
{
  "type": "rate_limit_error",
  "code": "budget_exceeded",
  "message": "This agent has used its budget for the period. It resets at 2026-10-02T00:00:00.000Z, or an admin can raise it.",
  "param": null
}
```

## intent_required

**403 · permission_error**

**When it happens.** The agent's intent mode is `required`, and the call has no `Deedglass-Intent`
header (S09; the Gate enforces it from S13). It is 403, never 401: the API key is valid, and a 401
would start an OAuth flow in some MCP clients. MCP clients can't sign intents in v1, so a `required`
agent can act only through the SDKs or the REST API. The full rules are in
[INTENTS.md](../INTENTS.md).

| Reason | Param | Message |
|---|---|---|
| `required` | `Deedglass-Intent` | `Agent "{agent}" requires a signed intent for every call. Send a Deedglass-Intent header; see docs.deedglass.com/intents.` |

**What to do.** Sign each call with the agent's intent key (`pkg/deedglass` `IntentSigner`, or
`deedglass dev sign-intent` to try it) and send the result in `Deedglass-Intent`. If the agent
can't sign (an MCP client, for example), an admin can set its mode to `optional` with
`deedglass agent intent mode <agent> optional`.

```json
{
  "type": "permission_error",
  "code": "intent_required",
  "message": "Agent \"support-bot\" requires a signed intent for every call. Send a Deedglass-Intent header; see docs.deedglass.com/intents.",
  "param": "Deedglass-Intent"
}
```

## intent_invalid

**403 · permission_error**

**When it happens.** The agent's mode is `optional` or `required`, a `Deedglass-Intent` header was
sent, and one of checks 1 to 12 failed (S09; [INTENTS.md](../INTENTS.md) §6 has the order). The
logged reason says which. `POST /v1/intents/verify` checks the header in every mode, `off`
included. It is 403, never 401.

| Reason | Param | Message |
|---|---|---|
| `syntax` | `Deedglass-Intent` | `The Deedglass-Intent header must be two base64url parts joined by a dot: the intent, then its signature.` |
| `too_large` | `Deedglass-Intent` | `The signed intent is larger than 2,048 bytes. Keep only the fields listed at docs.deedglass.com/intents.` |
| `strict` | `Deedglass-Intent` | `The signed intent isn't strict JSON: {detail}. Use the SDK signer, or follow the profile at docs.deedglass.com/intents.` |
| `not_canonical` | `Deedglass-Intent` | `The signed intent isn't canonical JSON (RFC 8785): keys must be sorted and there must be no whitespace. Use the SDK signer, or canonicalize before signing.` |
| `schema` (unknown field) | the field | `The signed intent has an unknown field "{field}". Put arguments in the call, and their digest in args_digest.` |
| `schema` (missing field) | the field | `The signed intent is missing the field "{field}". Sign every required field listed at docs.deedglass.com/intents.` |
| `schema` (wrong type or pattern) | the field | `The signed intent's {field} isn't valid: it must be {want}. See the field table at docs.deedglass.com/intents.` |
| `version` | `v` | `The signed intent's version is "{got}", and Deedglass reads deedglass.intent/1. Sign with deedglass.intent/1.` |
| `org_mismatch` | `org` | `The signed intent names {intent_org}, but this API key belongs to {org}. Sign with the agent's own org id.` |
| `agent_mismatch` | `agent` | `The signed intent names {intent_agent}, but this API key belongs to {agent} ({agent_id}). Sign with the agent's own identity.` |
| `unknown_kid` | `kid` | `The signed intent uses key {kid}, which isn't registered for {agent}. Register it with deedglass agent intent add {agent}, or in Console › Agents › Security.` |
| `bad_signature` | `kid` | `The signature doesn't verify with key {kid}. Sign the bytes "deedglass/intent/v1\n" + intent_hash with the matching private key.` |
| `not_yet_valid` | `iat` | `The signed intent was issued {ahead} in the future. Check the agent's clock; Deedglass allows 60 seconds of skew.` |
| `expired` | `exp` | `The signed intent expired at {time} UTC, {ago} ago. Sign a fresh intent for each call; an intent lasts at most 5 minutes.` |
| `ttl` | `exp` | `The signed intent is valid for {ms} ms, and it must be more than 0 and at most 300000 (5 minutes). Sign it again with exp after iat, at most 5 minutes later.` |
| `tool_mismatch` | `tool` | `The signed intent is for {intent_tool}, but this call is for {tool}. Sign one intent per call.` |
| `big_int` | `args` | `The arguments contain an integer larger than 9007199254740991, which JSON can't carry exactly. Send it as a string.` |
| `args_not_canonicalizable` | `args` | `The arguments can't be canonicalized with RFC 8785: {detail}. Send JSON with unique keys, at most 32 levels deep and at most 256 KiB.` |
| `args_mismatch` | `args_digest` | `The signed intent's args_digest doesn't match the arguments sent. Sign the exact arguments you send, canonicalized with RFC 8785.` |
| `unsigned_obo` | `Deedglass-On-Behalf-Of` or `Deedglass-Session` | `{name} was sent, but the signed intent doesn't include {field}. Sign it too, or leave the header out.` |
| `obo_mismatch` | `Deedglass-On-Behalf-Of` | `Deedglass-On-Behalf-Of says {header}, but the signed intent says {intent}. When you sign an intent, send the same value in both, or only in the intent.` |
| `session_mismatch` | `Deedglass-Session` | `Deedglass-Session says {header}, but the signed intent says {intent}. When you sign an intent, send the same value in both, or only in the intent.` |

`{placeholders}` are filled at the call site. [INTENTS.md](../INTENTS.md) §6 lists every `{detail}`
and `{want}`. A message never quotes the header itself.

**What to do.** Read the message: it names the field and the fix. Most refusals come from signing
something other than what you send. Use the SDK signer, sign the exact tool and arguments of this
call, sign a fresh intent for every call with a clock within 60 seconds of the server's, and put
`on_behalf_of` and `session` in the intent rather than only in headers. To check a signer without
acting, send its header to `POST /v1/intents/verify`.

```json
{
  "type": "permission_error",
  "code": "intent_invalid",
  "message": "The signed intent's args_digest doesn't match the arguments sent. Sign the exact arguments you send, canonicalized with RFC 8785.",
  "param": "args_digest"
}
```

## intent_replay

**403 · permission_error**

**When it happens.** The signed intent passed every other check, but its nonce was already used by
this agent (S09, check 13). Each intent works once: of any number of concurrent sends of one
header, exactly one succeeds. An intent old enough for its nonce to be pruned is refused as
`intent_invalid` with reason `expired` instead.

| Reason | Param | Message |
|---|---|---|
| `replay` | `nonce` | `This signed intent was already used at {time} UTC. Sign a new intent, with a fresh nonce, for every call.` |
| `replay` (the first use was pruned meanwhile) | `nonce` | `This signed intent was already used. Sign a new intent, with a fresh nonce, for every call.` |

**What to do.** Sign a new intent, with 128 fresh random bits as its nonce, for every call,
retries included. If you didn't send the request twice, something on the path did: treat it as a
replay attempt and check what sits between the agent and Deedglass.

```json
{
  "type": "permission_error",
  "code": "intent_replay",
  "message": "This signed intent was already used at 09:30:02 UTC. Sign a new intent, with a fresh nonce, for every call.",
  "param": "nonce"
}
```

## tool_unknown

**404 · invalid_request_error**

**When it happens.** No connector in your org provides the tool the request names (S14, S15).

**What to do.** Check the tool name (`GET /v1/tools` lists them), or add a connector in
Console › Connectors.

```json
{
  "type": "invalid_request_error",
  "code": "tool_unknown",
  "message": "No connector provides \"stripe.refunds.create\". Check the name, or add a connector in Console › Connectors.",
  "param": null
}
```

## connector_unavailable

**503 · api_error**

**When it happens.** The Gate couldn't reach the connector's system (S13, S16). The tool call
didn't run.

**What to do.** Check the connector in Console › Connectors, then retry. A 503 is never stored for
idempotent replay, so a retry with the same Idempotency-Key runs again.

```json
{
  "type": "api_error",
  "code": "connector_unavailable",
  "message": "The connector isn't reachable right now. Check Console › Connectors, then retry.",
  "param": null
}
```

## upstream_error

**502 · api_error**

**When it happens.** The connector's system answered the tool call with an error (S13, S14).

**What to do.** Open the action in Console › Actions to read the details, fix the cause, then send
a new request with a new Idempotency-Key. A retry with the same key returns this 502 again.

```json
{
  "type": "api_error",
  "code": "upstream_error",
  "message": "The tool returned an error. The details are on the action in Console › Actions.",
  "param": null
}
```

## timeout

**504 · api_error**

**When it happens.** The request ran past its endpoint's timeout (30 s unless the endpoint says
otherwise; 5 s for the system endpoints), and the handler stopped before it wrote a response. The
work may have partly run.

**What to do.** Fetch the object the request was about to see its state (for an action,
`GET /v1/actions/{id}`). If the work didn't happen, send the request again with a new
Idempotency-Key. A 504 on a POST with a key is stored like any other completed response: a retry
with the same key replays this 504 for 24 h and doesn't run the work again.

```json
{
  "type": "api_error",
  "code": "timeout",
  "message": "The request timed out after 30 s. It may have partly run, so fetch the object to see its state before you send it again.",
  "param": null
}
```

## rate_limited

**429 · rate_limit_error**

**When it happens.** You sent too many requests in a short time. The response carries
`Retry-After` in whole seconds, at least 1. Two limits send it:

- **Sign-in limits** (S07, [IDENTITY.md](../IDENTITY.md)): "Too many requests. Retry after 5 s."
- **The failed-auth limiter for API keys** (S08), step 2 of the bearer check, before the key is
  parsed: more than 10 invalid keys a minute from one address (then a penalty of 1 s that doubles
  with each further failure, up to 15 minutes, and resets after 10 clean minutes), or on one key
  prefix. Only `invalid_api_key` failures count; revoked and expired keys and successful checks
  don't. The message: "Too many requests with invalid API keys from this address. Try again in 4
  seconds."

Both limits live in each server process, so behind N instances a client gets up to N times the
budget.

**What to do.** Wait the number of seconds in `Retry-After`, then retry. A 429 is never stored for
idempotent replay, so the same Idempotency-Key is safe to reuse. For the key limiter, fix the key
first: retrying a bad key only lengthens the penalty.

```json
{
  "type": "rate_limit_error",
  "code": "rate_limited",
  "message": "Too many requests with invalid API keys from this address. Try again in 4 seconds.",
  "param": null
}
```

## idempotency_conflict

**422 · invalid_request_error**

**When it happens.** The Idempotency-Key was already used in the last 24 h, by the same principal,
with a different request: another method, route, path or body. Bodies are compared byte for
byte, so the same JSON with different spacing or member order counts as different. `param` is
`Idempotency-Key`.

**What to do.** Use a new key for a new request. To retry the original request, send exactly the
same bytes.

```json
{
  "type": "invalid_request_error",
  "code": "idempotency_conflict",
  "message": "This Idempotency-Key was already used with a different request. Use a new key for a new request.",
  "param": "Idempotency-Key"
}
```

## validation_failed

**400 · invalid_request_error**

**When it happens.** A field, query parameter or header has a value the endpoint can't accept.
`param` names it, using dots for nested JSON members (`items.0.amount`). The pipeline sends it
for:

- an unknown JSON member ("`amout` isn't a known field. Check the spelling, or the API version.")
- a JSON value of the wrong type ("`limit` has the wrong type: it must be a whole number.")
- a `limit` outside 1–100, or a `cursor` this API didn't issue

**Renaming an agent is 422, with this code.** Sending `name` in `PATCH /v1/agents/{id}` answers
`422 validation_failed` with `param: "name"`: "Agent names can't change, because policies match on
them. To rename, create a new agent with the new name and move its keys." Names are immutable
because policies match on them ([AGENTS.md](../AGENTS.md)).

**The on-behalf-of headers are 422, with this code.** A `Deedglass-On-Behalf-Of` or
`Deedglass-Session` header that doesn't match `^[A-Za-z0-9._~:@/+=-]{1,256}$`, or is sent twice,
answers `422 validation_failed` with `param` set to the header name: "Deedglass-On-Behalf-Of must
be 1 to 256 characters: letters, digits and . _ ~ : @ / + = -. Use an opaque user id, not a name."
(for `Deedglass-Session`: "… Use an opaque conversation or run id."), or "Send
Deedglass-On-Behalf-Of once. It names one person or session per call." It is checked on every
request an API key authenticates ([IDENTITY.md](../IDENTITY.md), On-behalf-of).

**What to do.** Fix the field the message names, then retry.

```json
{
  "type": "invalid_request_error",
  "code": "validation_failed",
  "message": "`limit` must be between 1 and 100.",
  "param": "limit"
}
```

## not_found

**404 · invalid_request_error**

**When it happens.** Either no endpoint matches the method and path, or the object id doesn't
exist in your org. An id that belongs to another org gets the same answer. Paths are exact: a
trailing slash, `//` or `..` never matches. For a path within two edits of a fixed endpoint
path, the message suggests it.

**What to do.** Check the path or the id. For an id, check that you're using a key or session from
the right org.

```json
{
  "type": "invalid_request_error",
  "code": "not_found",
  "message": "There's no endpoint at GET /v1/helth. Did you mean /v1/health?",
  "param": null
}
```

For an object: "There's no agent with id agt_01j9zr3m5k8q2w7x4t6v0b1c9d."

## conflict

**409 · invalid_request_error**

**When it happens.** The request conflicts with the object's current state: for example, it
changes something another request changed first.

**What to do.** Fetch the latest version of the object, then retry the change against it.

```json
{
  "type": "invalid_request_error",
  "code": "conflict",
  "message": "That conflicts with the current state. Fetch the latest version, then retry.",
  "param": null
}
```

## internal_error

**500 · api_error**

**When it happens.** Something failed inside Deedglass: an unexpected error or a panic. The
details are logged under the request id and never sent to you.

**What to do.** Retry with the same Idempotency-Key. If the 500 was stored for replay you get the
same 500 back, and the work didn't run twice. If it happens again, share the request id with
support.

```json
{
  "type": "api_error",
  "code": "internal_error",
  "message": "Something went wrong on our side. It's logged under this request id. Retry, and share the id if it happens again.",
  "param": null
}
```

## authentication_required

**401 · authentication_error**

**When it happens.** The endpoint isn't public, and the request carries no credentials it accepts:
no session cookie, a cookie for a session that doesn't exist, or no `Authorization` header on a
route that takes API keys (step 1 of the bearer check, [AGENTS.md](../AGENTS.md) §5). An API key on
a route only a signed-in person may call is `insufficient_role` instead. Every 401 carries
`WWW-Authenticate: Bearer realm="deedglass"`. For a Console route the message names the
sign-in page: "You're not signed in. Sign in at https://app.deedglass.com/login."

**What to do.** Send `Authorization: Bearer dg_live_…`, or call the endpoint from a signed-in
Console session.

```json
{
  "type": "authentication_error",
  "code": "authentication_required",
  "message": "This endpoint needs an API key (Authorization: Bearer dg_live_…) or a Console session.",
  "param": null
}
```

## method_not_allowed

**405 · invalid_request_error**

**When it happens.** The path exists, but not for this method. The `Allow` header lists the
methods that work. `HEAD` works wherever `GET` does.

**What to do.** Use one of the methods in `Allow`.

```json
{
  "type": "invalid_request_error",
  "code": "method_not_allowed",
  "message": "DELETE isn't supported on /v1/health. Use GET or HEAD.",
  "param": null
}
```

## request_too_large

**413 · invalid_request_error**

**When it happens.** The body is larger than the endpoint's limit (1 MiB unless the endpoint says
otherwise), or the endpoint takes no body and the request sent one. For the second case the
message reads "This endpoint doesn't take a request body. Send the request without one."

**What to do.** Send a smaller body, or none. For large data, split it across requests.

```json
{
  "type": "invalid_request_error",
  "code": "request_too_large",
  "message": "The request body is larger than 1 MiB. Send a smaller body.",
  "param": null
}
```

## unsupported_media_type

**415 · invalid_request_error**

**When it happens.** A POST, PUT or PATCH sent a body without `Content-Type: application/json`.
The only parameter accepted is `charset=utf-8`.

**What to do.** Send `Content-Type: application/json`, or `application/json; charset=utf-8`.

```json
{
  "type": "invalid_request_error",
  "code": "unsupported_media_type",
  "message": "Send the body as JSON, with Content-Type: application/json.",
  "param": null
}
```

## invalid_json

**400 · invalid_request_error**

**When it happens.** The body isn't strict JSON: a syntax error, a duplicate member name, invalid
UTF-8, or data after the value. The message gives the line and column (in bytes) of the first
problem. An empty body gets "The body is empty. Send a JSON object, and retry."

**What to do.** Fix the JSON at the position given, then retry. Send each member once.

```json
{
  "type": "invalid_request_error",
  "code": "invalid_json",
  "message": "The body isn't valid JSON (line 1, column 6). Fix it and retry.",
  "param": null
}
```

## api_version_invalid

**400 · invalid_request_error**

**When it happens.** `Deedglass-Version` isn't a date in `YYYY-MM-DD` form, isn't a version this
server knows, or was sent more than once. `param` is `Deedglass-Version`. See
[Versions](versions.md).

**What to do.** Send `Deedglass-Version: 2026-10-01` once, or leave the header out to use your
org's pinned version.

```json
{
  "type": "invalid_request_error",
  "code": "api_version_invalid",
  "message": "Deedglass-Version isn't a known version. Use 2026-10-01, or leave it out to use your org's pinned version.",
  "param": "Deedglass-Version"
}
```

## idempotency_key_invalid

**400 · invalid_request_error**

**When it happens.** `Idempotency-Key` is empty, longer than 255 characters, holds characters
outside printable ASCII, or was sent more than once. On an endpoint that requires a key, a request
without one gets this code with "This endpoint needs an Idempotency-Key header. Send a new unique
value, such as a UUID, with each new request." `param` is `Idempotency-Key`.

**What to do.** Send one key of 1 to 255 printable ASCII characters. A UUID works.

```json
{
  "type": "invalid_request_error",
  "code": "idempotency_key_invalid",
  "message": "Idempotency-Key must be 1 to 255 printable ASCII characters.",
  "param": "Idempotency-Key"
}
```

## idempotency_key_in_use

**409 · invalid_request_error**

**When it happens.** An earlier request with the same Idempotency-Key is still running. The
response carries `Retry-After: 1`. Nothing is stored for it.

**What to do.** Wait a second and retry with the same key. You get the first request's response
once it finishes.

```json
{
  "type": "invalid_request_error",
  "code": "idempotency_key_in_use",
  "message": "A request with this Idempotency-Key is still running. Retry in a moment.",
  "param": null
}
```

## service_unavailable

**503 · api_error**

**When it happens.** The server is starting or shutting down and can't take the request. A 503 is
never stored for idempotent replay.

**What to do.** Retry in a few seconds with the same Idempotency-Key. Another instance, or this one
after it restarts, will take it.

```json
{
  "type": "api_error",
  "code": "service_unavailable",
  "message": "Deedglass is starting or shutting down. Retry in a few seconds.",
  "param": null
}
```

## session_expired

**401 · authentication_error**

**When it happens.** The session cookie names a session that ended: 12 hours passed without a
request, 7 days passed since sign-in, you signed out (here or everywhere), you switched org, or you
were removed from the org. The message says which.

**What to do.** Sign in again at `/login`. A new session gets new cookies.

```json
{
  "type": "authentication_error",
  "code": "session_expired",
  "message": "Your session ended after 12 hours without activity. Sign in again at https://app.deedglass.com/login.",
  "param": null
}
```

## csrf_failed

**403 · permission_error**

**When it happens.** A Console request that changes something (POST, PUT, PATCH or DELETE) came
without the `Deedglass-CSRF` header, with a header that doesn't match the `__Host-deedglass_csrf`
cookie or the session, or from another site (`Sec-Fetch-Site: cross-site`, or an `Origin` that isn't
this server). It stops other websites from acting with your session.

**What to do.** Reload the page. Scripts send the `csrf_token` from `GET /v1/me` as
`Deedglass-CSRF`. API keys (`Authorization: Bearer`) never need it.

```json
{
  "type": "permission_error",
  "code": "csrf_failed",
  "message": "This request is missing its security token. Reload the page and try again.",
  "param": null
}
```

## insufficient_role

**403 · permission_error**

**When it happens.** You're signed in, and your role in the org is below what the endpoint needs.
Viewers read; admins also change agents, keys, policies and connectors, invite and remove viewers
and admins, and stop agents; owners also invite owners, change roles and start all agents again.
Roles are read on every request, so a change applies at once.

**What to do.** Ask an owner to change your role (Console › Settings › Members, or
`PATCH /v1/org/members/{id}`).

```json
{
  "type": "permission_error",
  "code": "insufficient_role",
  "message": "Your role in Acme is Viewer, and inviting people needs Admin. Ask an owner to change your role.",
  "param": null
}
```

## login_link_invalid

**400 · invalid_request_error**

**When it happens.** A sign-in or invite link was already used, was replaced by a newer email,
expired (sign-in links after 15 minutes, invites after 7 days), or isn't a Deedglass link. Opening a
link never uses it; only the button on the page does.

**What to do.** Ask for a new link at `/login`, or ask an admin to invite you again.

```json
{
  "type": "invalid_request_error",
  "code": "login_link_invalid",
  "message": "This sign-in link has expired or was already used. Get a new one at https://app.deedglass.com/login.",
  "param": null
}
```

## login_code_invalid

**400 · invalid_request_error**

**When it happens.** The 6-digit code doesn't match the newest sign-in email for that address, it
expired, or it was tried 5 times. Every one of these, and an address with no account, gets the same
answer, so the code form can't be used to find out who has an account.

**What to do.** Check the latest email, or ask for a new code at `/login`.

```json
{
  "type": "invalid_request_error",
  "code": "login_code_invalid",
  "message": "That code doesn't match, or it has expired. Check the latest email, or get a new code.",
  "param": null
}
```

## last_owner

**409 · invalid_request_error**

**When it happens.** The change would leave the org with no active owner: demoting or removing the
only owner, or the only owner leaving.

**What to do.** Make someone else an owner first (`PATCH /v1/org/members/{id}` with
`{"role":"owner"}`), then make the change.

```json
{
  "type": "invalid_request_error",
  "code": "last_owner",
  "message": "Acme needs at least one owner. Make someone else an owner first.",
  "param": null
}
```

## What this doesn't do

- Errors don't carry localized messages. The copy is English, and the `code` is the stable part.
- Errors don't echo your request. Find what you sent in your own logs, and match it by
  `request_id`.
- A message isn't a contract. It can get more exact between releases; branch on `type` and `code`.
- Deedglass doesn't send a different error for an object in another org. It is `not_found`, the
  same as an id that never existed.
