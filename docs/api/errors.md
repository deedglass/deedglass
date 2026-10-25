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
- `code` is one of the 41 codes on this page. Branch on it for exact handling. Codes are never
  renamed; new codes are added.
- `message` is one or more sentences for a person, with a next step. Don't parse it: it can get
  more exact over time.
- `param` names the field, query parameter or header the error is about. It is always present,
  and `null` when no single field is at fault.
- `request_id` is the same `req_…` id as the `Deedglass-Request-Id` response header. Quote it to
  support.
- `doc_url` points to this page's section for the code.
- `action` appears only when `POST /v1/actions` refuses an action it recorded (S13): the sealed
  action, in the same shape as `GET /v1/actions` returns. The codes that carry it are
  `policy_denied`, `policy_unavailable`, `budget_exceeded`, `agent_stopped`, `agent_suspended`,
  `org_stopped` and `tool_unknown`. Like `diagnostics` on `policy_invalid`, it is an additive
  field: clients that don't know it can ignore it.

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

**On `POST /v1/actions`** (S13) the attempt is still recorded: the action goes
`evaluated → denied → sealed` with the stop decision (`decision.source: stop`), and the envelope
carries the sealed action in `error.action`. The Ledger shows what a stopped agent tried to do. An
action that waits for approval is checked again when someone approves it: if the agent or org is
stopped by then, it ends `denied` the same way and never runs.

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

**On `POST /v1/actions`** (S13) the attempt is still recorded: the action goes
`evaluated → denied → sealed` with the stop decision (`decision.source: stop`), and the envelope
carries the sealed action in `error.action`. The Ledger shows what a stopped agent tried to do. An
action that waits for approval is checked again when someone approves it: if the agent or org is
stopped by then, it ends `denied` the same way and never runs.

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

**On `POST /v1/actions`** (S13) the attempt is still recorded: the action goes
`evaluated → denied → sealed` with the stop decision (`decision.source: stop`), and the envelope
carries the sealed action in `error.action`. The Ledger shows what a stopped agent tried to do. An
action that waits for approval is checked again when someone approves it: if the agent or org is
stopped by then, it ends `denied` the same way and never runs.

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
and gives its reason; it is the decision's own sentence, verbatim. On `POST /v1/actions` the
refusal is recorded and sealed before the answer, and the envelope carries the sealed action in
`error.action` (`status: denied`, its `deed`, and the `decision` with the rule id). Nothing ran.

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

**409 · invalid_request_error**

**When it happens.** A person tried to countersign or deny after the approval's `expires_at`
(S22). Timeout means no: nothing ran, and the expiry sweeper seals the action as `expired` with no
decider. `POST /v1/actions` doesn't use this code: an agent learns about the expiry from the
action's `status: expired` (or `deedglass_check_approval`).

**What to do.** Nothing runs on its own. If the action is still needed, ask the agent to send it
again: that asks for a new approval.

```json
{
  "type": "invalid_request_error",
  "code": "approval_expired",
  "message": "This approval expired at 10:41 IST. Nothing ran. Ask the agent to try again if it's still needed.",
  "param": null
}
```

## budget_exceeded

**429 · rate_limit_error**

**When it happens.** The agent used its budget for the current period (S11, S13). The response
carries `Retry-After` in seconds, and the message says when the period resets. On
`POST /v1/actions` the message is the budget decision's own sentence, verbatim, and the refusal is
recorded and sealed: the envelope carries the sealed action in `error.action`. A budget another
request reserved first becomes a recorded `budget_exceeded` too. A replay of the same
Idempotency-Key answers with the same refusal, and its `Retry-After` counts down to the same reset.

**What to do.** Wait until the time in the message, or ask an admin to raise the budget. A 429 is
not a reason to retry at once: the next attempt is refused and recorded the same way until the
window resets.

```json
{
  "type": "rate_limit_error",
  "code": "budget_exceeded",
  "message": "Budget \"agent-rate\" reached: 60 of 60 calls this minute. Try again in 14 s.",
  "param": null,
  "action": { "object": "action", "id": "act_01k6w4a2c4e6g8j0m2p4r6t8v0", "state": "sealed", "status": "denied", "deed": "deed_01k6w4a4e6g8j0m2p4r6t8w0y2", "…": "…" }
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
| `required_mcp` | `Deedglass-Intent` | `Agent "{agent}" requires a signed intent for every call, and MCP clients can't sign intents yet. Call it through the SDK or the REST API, or an admin can set its intent mode to optional in Console › Agents.` |

`required_mcp` is the Gate's answer (S13) to a call that arrives through an MCP surface
(`context.transport` is `mcp_stdio` or `mcp_http`) for an agent in `required` mode. Like every
identity failure it creates no action: nothing is recorded.

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

**When it happens.** `POST /v1/actions` names a tool no connector serves in gateway mode, or a name
that isn't a tool name at all, such as a look-alike with a Cyrillic letter (S13). Tool names are
`<connector>.<tool>`, split at the first dot. The attempt is recorded: the action goes
`evaluated → denied → sealed` with the Gate's own decision (`decision.source: builtin`,
`decision.code: tool_unknown`), and the envelope carries the sealed action in `error.action`.
Probing is evidence too. A tool string that is empty, longer than 200 bytes or not UTF-8 is a
malformed request instead (400 `validation_failed`, not recorded).

| Template | Param |
|---|---|
| `No tool named "{tool}". Tools look like "connector.tool", for example "fake.echo".` | `null` |

**What to do.** Check the spelling of the tool, and that its connector is set up in
Console › Connectors. Send the action again with the right name; the refused attempt stays in the
Ledger.

```json
{
  "type": "invalid_request_error",
  "code": "tool_unknown",
  "message": "No tool named \"fake.ecko\". Tools look like \"connector.tool\", for example \"fake.echo\".",
  "param": null,
  "action": { "object": "action", "id": "act_01k6w4a2c4e6g8j0m2p4r6t8v0", "tool": "fake.ecko", "state": "sealed", "status": "denied", "…": "…" }
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
`Retry-After` in whole seconds, at least 1. Three limits send it:

- **Sign-in limits** (S07, [IDENTITY.md](../IDENTITY.md)): "Too many requests. Retry after 5 s."
- **The failed-auth limiter for API keys** (S08), step 2 of the bearer check, before the key is
  parsed: more than 10 invalid keys a minute from one address (then a penalty of 1 s that doubles
  with each further failure, up to 15 minutes, and resets after 10 clean minutes), or on one key
  prefix. Only `invalid_api_key` failures count; revoked and expired keys and successful checks
  don't. The message: "Too many requests with invalid API keys from this address. Try again in 4
  seconds."

- **Open long polls** (S13): each agent may hold 20 open `GET /v1/actions/{id}?wait=` requests
  at once; the 21st gets `Retry-After: 1` and "This agent already has 20 open waits. Close one, or
  poll without wait."

These limits live in each server process, so behind N instances a client gets up to N times the
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

**On `POST /v1/actions`** the Gate compares keys itself (S13). A key names one action of one agent
for 24 hours, and the comparison is the request digest (the tool, the arguments in their RFC 8785
form, the mode and the tool digest) plus the `Deedglass-On-Behalf-Of` and `Deedglass-Session`
headers. Spacing and member order don't matter there; another tool, other arguments, another
on-behalf-of or another session do. A signed intent isn't compared, so a retry signed again still
replays. The message: "This Idempotency-Key was used with a different request in the last 24
hours. Use a new key for a new request."

**What to do.** Use a new key for a new request. To retry the original request, send exactly the
same bytes (on `POST /v1/actions`, the same tool, arguments and headers).

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

**The Gate's checks** (S13). `POST /v1/actions` refuses a malformed request before anything is
recorded:

| Param | Message |
|---|---|
| `args` | `args is 312 KiB; the limit is 256 KiB. Send a reference to the data instead of the data itself.` |
| `args` | `args is nested more than 32 levels deep. Flatten it, or send a reference to the data.` |
| `args` | `args contains an integer larger than 9007199254740991, which JSON can't carry exactly. Send it as a string.` |
| `args` | `args must be a JSON object. Send the tool's arguments as an object, {} when it takes none.` |
| `tool` | `tool is empty. Name a tool as "connector.tool", for example "fake.echo".` |
| `mode` | `mode must be gateway or proxy (got "{mode}"). Leave it out for gateway, where Deedglass runs the tool.` |
| `wait` | `wait must be a number of seconds from 0 to 25.` |
| `connector_type` | `connector_type is for proxy mode, where the edge runs the tool. In gateway mode Deedglass knows the connector; leave connector_type out.` |

`POST /v1/actions/{id}/result` checks the edge's report the same way: `status` ("status must be
succeeded, failed or unknown."), the times, `error_code`, and `result_digest`, which must match
`result` ("result_digest doesn't match result. Send the digest of the exact result (sha256 of its
RFC 8785 form), or leave it out."). `GET /v1/actions/{id}` takes only `wait` (0 to 25 seconds,
sent once) and `expand[]`.

**Connector read-backs** (S19). Creating or updating a connector checks each write tool's
`readback` and `postcondition` ([CONNECTORS.md](../CONNECTORS.md#read-backs)). `param` is the
field's path in the config; when several fields are wrong, the message names the first and says
how many there are:

| Param | Message |
|---|---|
| `config.tools[1].readback.tool` | `"update_status" writes, so it can't be a read-back. Name a read tool such as "get".` |
| `config.tools[1].readback.tool` | `"list" isn't idempotent, so it can't be a read-back. Name a read tool that is.` |
| `config.tools[1].readback.attempts` | `attempts above 1 need consistency: eventual.` |
| `config.tools[1].postcondition` | `A postcondition needs a read-back to read from. Add readback: { tool: get, args: … } to this tool.` |
| `config.tools[1].postcondition` | `1:28: request.status isn't a field. Did you mean args.status?` |
| `config.tools[1].postcondition` | `This condition could cost up to 520,488,451 units per call; the limit is 100,000. Compare fewer fields, or use expect.` |

**What to do.** Fix the field the message names, then retry.

```json
{
  "type": "invalid_request_error",
  "code": "validation_failed",
  "message": "`limit` must be between 1 and 100.",
  "param": "limit"
}
```

```json
{
  "type": "invalid_request_error",
  "code": "validation_failed",
  "message": "\"update_status\" writes, so it can't be a read-back. Name a read tool such as \"get\".",
  "param": "config.tools[1].readback.tool"
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
changes something another request changed first. The Gate (S13) sends it for an action in the
wrong state:

| Route | Message |
|---|---|
| `POST /v1/actions/{id}/cancel`, while it runs | `This action is already executing, so it can't be cancelled.` |
| `POST /v1/actions/{id}/cancel`, once it ended | `This action has already finished, so there is nothing to cancel.` |
| `POST /v1/actions/{id}/result`, a second time | `A result was already reported for this action.` |
| `POST /v1/actions/{id}/result`, after the deadline | `This action was already sealed as UNVERIFIABLE because its result arrived after the deadline.` |
| `POST /v1/actions/{id}/result`, a gateway action | `This action runs in gateway mode, so Deedglass reported its result itself. Only proxy-mode actions take a result.` |
| `POST /v1/actions/{id}/result`, not yet authorized | `This action hasn't been authorized to run yet, so it has no result to report. Wait for its approval: GET /v1/actions/{id}?wait=25.` |

**What to do.** Fetch the latest version of the object, then retry the change against it. For an
action, `GET /v1/actions/{id}` shows its `status`; a late result stays with the edge (S21's
amendment deeds can attach it later).

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
sign-in page: `You're not signed in. Sign in at https://app.deedglass.com/login.`

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

On `POST /v1/actions` the Gate waits for the original instead (up to the request's `wait`) and
answers with the same action. It sends this code only in a rare race: two requests with the same
key were admitted at the same moment, and the other one's action can't be read yet.

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

## policy_unavailable

**403 · policy_error**

**When it happens.** Deedglass could not evaluate the policy for this action: no policy was loaded,
the evaluator failed, or evaluation ran past its time limit (50 ms by default). The action did not
run. It is always a deny, so a broken or slow policy never lets an action through (S11). On
`POST /v1/actions` the refusal is recorded and sealed, and the envelope carries the sealed action
in `error.action`. Every trip of the time limit also counts in
`deedglass_policy_eval_time_limit_total` and is logged at warn level with the org and agent
([OBSERVABILITY.md](../OBSERVABILITY.md)).

**What to do.** An admin checks the active policy in Console › Policies. The action's trace names
the rule that was being evaluated when the time ran out. Send the action again once the policy is
fixed.

```json
{
  "type": "policy_error",
  "code": "policy_unavailable",
  "message": "Deedglass could not evaluate the policy for this action, so it did not run. An admin can check Console › Policies.",
  "param": null
}
```

## policy_invalid

**400 · invalid_request_error**

**When it happens.** A policy you pushed (`POST /v1/policies`, `POST /v1/policies/{id}/versions`), dry-ran (`POST /v1/policies/evaluate` with a `source`) or tested (`POST /v1/policies/test`) didn't compile. Nothing was saved (S12).

**What to do.** Read `diagnostics`: each entry has the `line` and `column`, a stable `code` (documented in the policy errors reference), a `message` and a `hint`. Fix the file, check it locally with `deedglass policy lint`, and push again. `diagnostics` is an additive envelope field: it appears on this code only.

```json
{
  "type": "invalid_request_error",
  "code": "policy_invalid",
  "message": "The policy has 1 error, on line 5: Unknown key \"desicion\" in rule \"a\".",
  "param": "source",
  "diagnostics": [
    { "object": "policy_diagnostic", "severity": "error", "code": "unknown_key", "line": 5, "column": 5,
      "message": "Unknown key \"desicion\" in rule \"a\".", "hint": "Did you mean \"decision\"?",
      "doc_url": "https://docs.deedglass.com/policies/errors/unknown_key" }
  ]
}
```

## credentials_missing

**409 · invalid_request_error**

**When it happens.** An action called a connector that has no credentials yet (S16). Nothing was
sent: the action is `failed` with `execution.error_code: "credentials_missing"`, and its deed says
so. The Gate never sends a call without the auth its connector is configured with.

**What to do.** An admin stores the credential once, from the environment or stdin (never a flag):
`deedglass connector credentials set orders --from-env ORDERS_TOKEN`. Then send the action again.

```json
{
  "type": "invalid_request_error",
  "code": "credentials_missing",
  "message": "The connector \"orders\" has no credentials yet. Run deedglass connector credentials set orders --from-env ORDERS_TOKEN.",
  "param": null
}
```

## destination_blocked

**400 · invalid_request_error**

**When it happens.** A connector's destination is an address Deedglass never connects to: cloud
metadata (even in dev), reserved or documentation ranges, an IPv6 address that embeds a blocked
IPv4 one, loopback or a private network outside dev, or Deedglass's own database (S16,
[NETWORK_EGRESS.md](../NETWORK_EGRESS.md)). Creating or updating the connector answers with this
code and `param: "config.base_url"`. A destination that changes its DNS answer later is caught when
the call dials: the action is then `failed` with this error code and nothing was sent. Agents see
a terse message that names no address; admins see the address and its class.

**What to do.** Point `base_url` at the service's public address. To reach a private network in
prod on purpose, list it in `DEEDGLASS_EGRESS_ALLOW_CIDRS`.

```json
{
  "type": "invalid_request_error",
  "code": "destination_blocked",
  "message": "base_url resolves to 169.254.169.254, a cloud metadata address. Deedglass never connects to it. Use the service's public address.",
  "param": "config.base_url"
}
```

## vault_unavailable

**503 · api_error**

**When it happens.** Deedglass couldn't open a connector's credential: it is wrapped by a key
that isn't in `DEEDGLASS_VAULT_KEY`, or it failed to authenticate (a row moved to another org or
connector, or changed on disk; the server logs the `vault_integrity` alert). Nothing was sent: the
action is `failed` with this error code. Agents see only that an admin was alerted.

**What to do.** If a key was removed too early, add it back as the second key, restart, and run
`deedglass vault rotate` ([VAULT.md](../VAULT.md)). If the row failed to authenticate, store the
credential again.

```json
{
  "type": "api_error",
  "code": "vault_unavailable",
  "message": "Deedglass couldn't open this connector's credentials. An admin has been alerted.",
  "param": null
}
```

## upstream_auth_failed

**502 · api_error**

**When it happens.** The service a connector calls answered 401 or 403: it refused the stored
credential (S16). The call reached the service and was refused, so the action is `failed`. The
response body is kept in the action's result with every credential value replaced by `[redacted]`.

**What to do.** Replace the credential: `deedglass connector credentials set orders --from-env
ORDERS_TOKEN`, then check it with `deedglass connector test orders`.

```json
{
  "type": "api_error",
  "code": "upstream_auth_failed",
  "message": "The service refused the connector's credentials. An admin can replace them in Console › Connectors.",
  "param": null
}
```

## upstream_not_found

**502 · api_error**

**When it happens.** The service answered 404 for the path the tool built from the agent's
arguments (S16). The action is `failed`.

**What to do.** Check the arguments, for example the order id.

```json
{
  "type": "api_error",
  "code": "upstream_not_found",
  "message": "The service has nothing at that address. Check the arguments.",
  "param": null
}
```

## upstream_rate_limited

**429 · rate_limit_error**

**When it happens.** The service answered 429. Reads and calls with an idempotency header are
retried up to twice, honouring a `Retry-After` of at most 5 s; a write without one is never
retried (S16). The action is `failed`.

**What to do.** Wait, then send the action again. If it happens often, give the agent a budget in
its policy so it slows down before the service does.

```json
{
  "type": "rate_limit_error",
  "code": "upstream_rate_limited",
  "message": "The service is limiting requests. Wait, then send the call again.",
  "param": null
}
```

## response_too_large

**502 · api_error**

**When it happens.** The service's answer passed the connector's `max_response_bytes` (4 MiB by
default, 16 MiB at most), counted after decompression (S16). Deedglass read at most one byte past
the limit and kept none of it. The call ran, so the action's outcome is `unknown` and its witness
verdict `UNVERIFIABLE`.

**What to do.** Narrow the request (a page size, a filter), or raise `max_response_bytes`.

```json
{
  "type": "api_error",
  "code": "response_too_large",
  "message": "The service answered with more than the connector's max_response_bytes, so Deedglass kept none of it. The call ran.",
  "param": null
}
```

## statement_refused

**400 · invalid_request_error**

**When it happens.** The Postgres connector refused the SQL before it reached the database, even
though policy allowed the call (S17). It refuses more than one statement, a kind that doesn't
match the tool (`postgres.query` runs only `SELECT`; `postgres.execute` runs one `INSERT`,
`UPDATE`, `DELETE` or `MERGE`), transaction control, `SET`, `RESET`, `DISCARD`, `LISTEN`,
`NOTIFY`, `COPY`, `DO`, `CALL`, `GRANT`, `REVOKE`, any schema change (including `TRUNCATE`),
`EXPLAIN ANALYZE`, any reference to schema `deedglass`, functions on its denylist
(`pg_logical_emit_message`, `set_config`, `pg_read_file`, `dblink*` and others), and SQL it can't
classify. Nothing was sent, so the action is `failed`.

**What to do.** Send one plain statement to the matching tool, with values as `$1`, `$2` params.

```json
{
  "type": "invalid_request_error",
  "code": "statement_refused",
  "message": "Refused by the Postgres connector: the statement has 2 commands. Deedglass runs one statement per action.",
  "param": null
}
```

## row_limit_exceeded

**400 · invalid_request_error**

**When it happens.** A `postgres.execute` statement changed more rows than the connector's
`max_rows_affected` (1,000 by default). Deedglass rolled the transaction back, so neither the
change nor the ledger row was committed, and the action is `failed`.

**What to do.** Narrow the `WHERE` clause, or raise `max_rows_affected` in the connector's config
if the size is expected.

```json
{
  "type": "invalid_request_error",
  "code": "row_limit_exceeded",
  "message": "The statement changed 1,204 rows, over this connector's limit of 1,000. Nothing was committed. Narrow the WHERE clause.",
  "param": null
}
```

## database_role_unsafe

**503 · api_error**

**When it happens.** The Postgres connector's DSN logs in as a role that isn't least privilege: a
superuser, a role with `BYPASSRLS`, `REPLICATION`, `CREATEROLE` or `CREATEDB`, a member of
`pg_write_all_data`, `rds_superuser`, `cloudsqlsuperuser`, `azure_pg_admin`, `neon_superuser`,
`supabase_admin` or a server-file role, or any role other than `deedglass_agent`. Every new
connection is checked (S17). In prod the connection is refused and nothing runs; in dev Deedglass
logs a warning and `deedglass connector postgres check` fails its `agent_role` line.

**What to do.** Run `deedglass connector postgres init --connector <name>` to create
`deedglass_agent` and store its DSN, then `deedglass connector postgres check`.

```json
{
  "type": "api_error",
  "code": "database_role_unsafe",
  "message": "The connector's database role is a superuser. Deedglass refuses to run agent SQL with it. Run deedglass connector postgres init to create deedglass_agent.",
  "param": null
}
```

## database_not_ready

**503 · api_error**

**When it happens.** The database the connector points at has no `deedglass.begin_action`, or
`deedglass_agent` can't execute it (S17). Nothing ran.

**What to do.** Run `deedglass connector postgres init --connector <name>`, review the plan, apply
it, then `deedglass connector postgres check`.

```json
{
  "type": "api_error",
  "code": "database_not_ready",
  "message": "Deedglass isn't set up in this database yet. Run deedglass connector postgres init.",
  "param": null
}
```

## witness_not_configured

**409 · invalid_request_error**

**When it happens.** `GET` or `DELETE /v1/connectors/{id}/decoder` named a connector that isn't
witnessed (S20): it isn't a `postgres` connector, or its config has `witness.enabled: false`, so
there is no slot and no decoder to show or drop.

**What to do.** Set `witness.enabled: true` (and `witness.watched_tables`) in the connector's
config, run `deedglass connector postgres init` and `deedglass connector postgres check`, then
look again with `deedglass decoder status --connector <name>`.

```json
{
  "type": "invalid_request_error",
  "code": "witness_not_configured",
  "message": "Checks aren't on for this connector, so it has no decoder. Turn on witness.enabled in its config first.",
  "param": null
}
```

## approval_decided

**409 · invalid_request_error**

**When it happens.** A person tried to countersign or deny an approval that already has a final decision (S22): someone else approved or denied it first, or it was cancelled. The first final decision wins under a row lock; nothing is decided twice.

**What to do.** Nothing to do. The message names the outcome, who decided and when; Console › Approvals has the full record and the deed.

```json
{
  "type": "invalid_request_error",
  "code": "approval_decided",
  "message": "This approval was already decided: approved by Priya Raman at 10:14 IST.",
  "param": null
}
```

## already_countersigned

**409 · invalid_request_error**

**When it happens.** You countersigned a four-eyes approval (a rule with `required: 2` or more) and tried to countersign it again (S22). Four-eyes counts distinct people, so a second vote from the same person never counts.

**What to do.** Ask another approver of the rule to countersign. The action runs when the last required person approves.

```json
{
  "type": "invalid_request_error",
  "code": "already_countersigned",
  "message": "You already countersigned this. It needs one more person.",
  "param": null
}
```

## approver_not_eligible

**403 · permission_error**

**When it happens.** The person deciding isn't an approver of the rule that paused the action (S22). Eligibility is read live when you vote: `role:admin` means an admin or owner, `role:owner` an owner, and `user:<email>` that member. A role that changed after the email was sent counts as it is now.

**What to do.** Ask one of the approvers the message names, or ask an owner to change your role in Console › Settings.

```json
{
  "type": "permission_error",
  "code": "approver_not_eligible",
  "message": "Rule \"large-refunds-need-approval\" needs role:admin or user:priya@acme.com to countersign. Ask one of them.",
  "param": null
}
```

## self_approval

**403 · permission_error**

**When it happens.** The action runs on your behalf (its `Deedglass-On-Behalf-Of` names your user id or email), and your org keeps separation of duties: the person an action is for can't countersign it (S22, `settings.approvals.self_countersign: deny`, the default). Denying is still allowed.

**What to do.** Ask another approver to countersign, or deny it. A one-person org can allow self-countersign in Console › Settings › Approvals.

```json
{
  "type": "permission_error",
  "code": "self_approval",
  "message": "This action runs on your behalf, so another approver has to countersign it. You can still deny it.",
  "param": null
}
```

## approval_needs_person

**403 · permission_error**

**When it happens.** An agent key or an org admin key called `POST /v1/approvals/{id}/approve` or `/deny` (S22). Approvals are a person's decision: no key, agent or automation can countersign.

**What to do.** Countersign as a person: in Console › Approvals, with the link in the approval email, or (dev only) with `deedglass approve --as <email>`.

```json
{
  "type": "permission_error",
  "code": "approval_needs_person",
  "message": "Approvals need a person. Agents and API keys can't countersign. Use Console › Approvals or the emailed link.",
  "param": null
}
```

## context_changed

**409 · invalid_request_error**

**When it happens.** The decision carried a `context_digest` that isn't the approval's (S22). Every decision names the exact context snapshot the person reviewed; a page showing something else can't be used to approve.

**What to do.** Reload the approval, review what it shows now, and decide again.

```json
{
  "type": "invalid_request_error",
  "code": "context_changed",
  "message": "What you reviewed is out of date. Reload the approval and review it again.",
  "param": null
}
```

## approval_unsatisfiable

**403 · policy_error**

**When it happens.** A rule asked for more countersigning people than this org has eligible approvers, after leaving out the person the action runs on behalf of (S22). Deedglass records the approval as denied by the system and seals the action as denied in the same transaction; the refusal carries the sealed `action`.

**What to do.** Invite another admin in Console › Settings, lower the rule's `required`, or allow self-countersign in Console › Settings › Approvals.

```json
{
  "type": "policy_error",
  "code": "approval_unsatisfiable",
  "message": "No one in this org can approve this: the rule needs 2 people and only 1 is eligible. Nothing was run. Invite another admin, or allow self-countersign in Console › Settings › Approvals.",
  "param": null
}
```

## approval_link_invalid

**401 · authentication_error**

**When it happens.** An emailed approval link was unknown, already used, replaced by a newer email, past its 24-hour limit, or voided by a final decision (S22). Each answers the same way, at the same speed, so a guess learns nothing.

**What to do.** Open Console › Approvals, which always has the latest. Approval links work once, for one person, for up to 24 hours.

```json
{
  "type": "authentication_error",
  "code": "approval_link_invalid",
  "message": "This approval link has expired or was already used. Open Console › Approvals.",
  "param": null
}
```

## approval_link_review_only

**403 · permission_error**

**When it happens.** Your org set `settings.approvals.email_links: review` (S22): approval emails show the context, and decisions happen only in the Console, where your session and CSRF protection apply.

**What to do.** Open Console › Approvals and countersign there.

```json
{
  "type": "permission_error",
  "code": "approval_link_review_only",
  "message": "Your org decides approvals in the Console. Open Console › Approvals to countersign.",
  "param": null
}
```

## approver_unmapped

**403 · permission_error**

**When it happens.** Someone clicked Approve or Deny on a Deedglass card in Slack (S29), and Deedglass couldn't match their Slack account to an active member of the org. A Slack user maps to a person only by an email address Slack has confirmed, and only when the account is a full member of the workspace (not a guest, a bot or a deactivated account) and, when the Slack connector sets `approvals.require_email_domains`, only when the email is in one of those domains. Slack shows the person a message only they can see; nothing is decided.

**What to do.** Invite the person in Console › Settings › Members with the email they use in Slack, then click again. Or decide in Console › Approvals.

```json
{
  "type": "permission_error",
  "code": "approver_unmapped",
  "message": "No person in this org matches that Slack account. Invite them in Console › Settings › Members.",
  "param": null
}
```


## stripe_live_key_refused

**400 · invalid_request_error**

**When it happens.** Someone tried to store a Stripe key that starts with `sk_live_` or `rk_live_` on a Stripe connector (S30). Deedglass connects to Stripe sandboxes only: the key is refused before it is encrypted, so it is never stored, logged or echoed (the message shows its first 8 characters at most). The CLI refuses it before sending anything. A live key that reaches a stored row some other way is refused again before every call, and the connector is disabled.

**What to do.** In your Stripe sandbox, open Developers › API keys and create a restricted key (it starts with `rk_test_`). Store it with `deedglass connector credentials set stripe --from-env STRIPE_TEST_KEY`. There is no setting that allows live mode.

```json
{
  "type": "invalid_request_error",
  "code": "stripe_live_key_refused",
  "message": "This is a live-mode Stripe key. Deedglass connects to Stripe sandboxes only. Use a restricted key that starts with rk_test_.",
  "param": "secret.token"
}
```

## stripe_livemode_detected

**502 · api_error**

**When it happens.** Stripe answered a call in live mode (`livemode: true` anywhere in the answer). The breaker disabled the Stripe connector at once: no further request leaves, the connector's health reason is `livemode_detected`, the admin deed `connector.disabled` is sealed with Stripe's request id, and an alert goes to the server log. The action is recorded as unknown and its deed is UNVERIFIABLE, because something may have happened in live mode. The agent sees only that the connector is disabled.

**What to do.** Find the request id from the deed (`connector.disabled`) in your Stripe dashboard and check what it did. Replace the key with a sandbox key, then set the connector back to active in Console › Connectors and run `deedglass connector test stripe`.

```json
{
  "type": "api_error",
  "code": "stripe_livemode_detected",
  "message": "The stripe connector is disabled. An admin has been alerted.",
  "param": null
}
```

## upstream_approval_pending

**502 · api_error**

**When it happens.** Stripe held the call for its own approval: the key is tagged as an agent key, and a Stripe approval rule (by default, refunds and subscription cancellations) answered `approval_required` with an approval request (`apreq_…`). Nothing happened yet, but Stripe completes the action by itself if its reviewer approves within 14 days, so Deedglass records the action as unknown and the deed as UNVERIFIABLE. The admin detail names the approval request.

**What to do.** Pick one approval surface. Use a restricted key **without** the agent tag when Deedglass is the gateway (Deedglass shows, approves and seals everything), or keep Stripe's rules and expect UNVERIFIABLE deeds for held actions. Check Settings › Approvals in Stripe for the request.

```json
{
  "type": "api_error",
  "code": "upstream_approval_pending",
  "message": "Stripe is holding this for its own approval. Deedglass can't see whether a Stripe reviewer approves it. Check Settings › Approvals in Stripe.",
  "param": null
}
```
## schedule_needs_approval

**403 · policy_error**

**When it happens.** `POST /v1/actions` carried a `schedule` whose `not_before` is more than 60 seconds away, and the policy decided `allow`. An allowed action runs at once, so it would run before its window: Deedglass denies it and seals the deed instead (S40). A scheduled action must go through `require_approval`; an automatic sender submits each action at its slot instead, with `not_before` at most 60 seconds ahead.

**What to do.** Add `require_approval` to the rule that matches, or drop the schedule, or submit the action when its window opens.

```json
{
  "type": "policy_error",
  "code": "schedule_needs_approval",
  "message": "A scheduled action must wait for an approval. This policy allows it now, so it would run early. Add require_approval to the rule, or drop the schedule.",
  "param": null
}
```
## action_executing

**409 · invalid_request_error**

**When it happens.** `POST /v1/actions/{id}/cancel` on an approved scheduled action whose run has already started: the worker claimed its `approval_resume` job, so the call may be reaching the connector now (S40). Cancelling an approved scheduled action works only while its job is still waiting for the window.

**What to do.** Wait for the action to finish and read its deed (`deedglass actions show <id> --wait`).

```json
{
  "type": "invalid_request_error",
  "code": "action_executing",
  "message": "It's running now and can't be cancelled. Its deed will show what happened.",
  "param": null
}
```
## from_denied

**400 · invalid_request_error**

**When it happens.** An `outbound_mail` connector (ops build only) was created or changed with a `mailbox` or `sending_domain` on `deedglass.com`, a subdomain of it, or a domain in `deny_from_domains`. Cold email never leaves from deedglass.com, so nothing was stored. The same rule runs at authorize, before every send and in the read-back, where a refused From disables the connector.

**What to do.** Use the mailbox on the sending domain, like `arjun@trydeedglass.com`.

```json
{
  "type": "invalid_request_error",
  "code": "from_denied",
  "message": "From on deedglass.com is never allowed for outbound mail. Use the sending-domain mailbox.",
  "param": "config.mailbox"
}
```
## fhir_version_conflict

**409 · invalid_request_error**

**When it happens.** A `fhir` connector sent an update, patch or delete with `If-Match` (the version id the agent sent as `if_match`), and the FHIR server answered that the resource has moved on: HTTP 412 Precondition Failed, or 409 Conflict on HAPI FHIR. The server wrote nothing. The action is recorded as failed, sealed, and never retried: retrying with the same version can't succeed, and a retry without it would overwrite the other writer's change.

**What to do.** Read the resource again (`fhir.read`), check what changed, and send the write with the new version id. If two agents write the same resource, give one of them the job.

```json
{
  "type": "invalid_request_error",
  "code": "fhir_version_conflict",
  "message": "The FHIR server refused the write because the resource changed since the version sent. Nothing was written. Read it again and send the new version id.",
  "param": null
}
```
## undo_unavailable

**409 · invalid_request_error**

**When it happens.** `POST /v1/actions/{id}/undo` previewed the undo again and at least one of the twelve checks failed (S23): the action wasn't witnessed at L2, the connector is gone, a row changed since, a trigger would fire again, and so on. The additive `error.checks` field lists every check, each with its fix and, where it helps, SQL to copy.

**What to do.** Run `deedglass undo preview act_…`, fix each failing check, preview again, and send the new `plan_digest`.

```json
{
  "type": "invalid_request_error",
  "code": "undo_unavailable",
  "message": "Undo isn't available for act_01j9zw6k…: 1 check failed (triggers). Run deedglass undo preview act_01j9zw6kq3m8n2p4r6t8v0x2z4 for the fix.",
  "param": null,
  "checks": [
    {"id": "triggers", "title": "Triggers", "status": "fail", "detail": "public.orders has trigger orders_bulk_touch. Undo would fire this trigger again.",
     "fix": "An admin can pass allow_triggers with a reason; the L2 witness then shows what the trigger did.",
     "fix_sql": "ALTER TABLE public.orders DISABLE TRIGGER orders_bulk_touch;", "doc_url": "https://docs.deedglass.com/undo#the-twelve-checks"}
  ]
}
```

## undo_plan_stale

**409 · invalid_request_error**

**When it happens.** The `plan_digest` you sent isn't the plan Deedglass builds now: the rows, the catalog or the evidence changed since you previewed. The Gate's undo executor checks the digest again right before it runs, so a stale plan never runs; that failure is recorded as the undo action's `execution.error_code`.

**What to do.** Preview the undo again and use the new `plan_digest`.

```json
{
  "type": "invalid_request_error",
  "code": "undo_plan_stale",
  "message": "The rows changed since you previewed this undo. Preview it again and use the new plan.",
  "param": "plan_digest"
}
```

## already_undone

**409 · invalid_request_error**

**When it happens.** Another undo of the same action already holds its one live slot: it was applied, or it is waiting for approval or running. At most one live undo per action is a unique index, so two requests at once get one action and one `already_undone`. A denied, expired, cancelled or failed undo frees the slot.

**What to do.** Look at the action's `undo` object (`undone_by`, `pending`). To put back an undo, preview an undo of the undo action itself (a redo).

```json
{
  "type": "invalid_request_error",
  "code": "already_undone",
  "message": "act_01j9zw6k… was already undone by act_01j9zx8m… at 10:14 IST.",
  "param": null
}
```

## undo_conflict

**409 · invalid_request_error**

**When it happens.** This code is an undo action's `execution.error_code` (the action is `failed`), not an HTTP answer: inside its one transaction the undo found a row that changed after the plan was made, a row locked longer than `undo.lock_timeout`, or a step that didn't change exactly one row. Postgres rolled the whole undo back, so nothing was changed.

**What to do.** Preview the undo again; the new plan follows the rows as they are now. For a lock, try again in a moment.

```json
{
  "type": "invalid_request_error",
  "code": "undo_conflict",
  "message": "Row public.orders id=42 changed after the plan was made (status is now 'cancelled'). Nothing was changed. Preview the undo again.",
  "param": null
}
```

## replay_too_large

**400 · invalid_request_error**

**When it happens.** `POST /v1/policies/{id}/versions/{v}/replay` counts the actions in the window before it queues anything, and the window holds more than 1,000,000 of them. Replay re-evaluates every stored decision input, so the limit keeps one job bounded. Nothing was queued, and the refusal doesn't count toward the 10 replays an hour. A queued replay can also end `failed` with this code in its `error` object, when the window holds more than the limit by the time a runner counts it again.

**What to do.** Send a narrower window with `from` and `to` (RFC 3339). Split a long period into several replays if you need all of it.

```json
{
  "type": "invalid_request_error",
  "code": "replay_too_large",
  "message": "This replay covers 1,240,512 actions; the limit is 1,000,000. Narrow the window with from and to.",
  "param": null
}
```

## replay_window_invalid

**400 · invalid_request_error**

**When it happens.** The window you sent can't be replayed or read: `from` is not before `to`, or the window is longer than 90 days. The replay route, `GET /v1/insights/stats` (with `from` and `to`) and `GET /v1/insights/shadow` check the same rule. `param` names the end at fault: `to` when both ends were sent and `from` isn't before it, otherwise `from`.

**What to do.** Send `from` before `to`, at most 90 days apart, both in RFC 3339 like `2026-09-26T00:00:00Z`. Leave both out to use the last 7 days.

```json
{
  "type": "invalid_request_error",
  "code": "replay_window_invalid",
  "message": "The window must start before it ends, and it can be at most 90 days long. Send from before to.",
  "param": "from"
}
```

## suggestion_stale

**409 · invalid_request_error**

**When it happens.** You accepted a suggestion, but the policy it was written for is no longer the one enforced: someone activated another version since. Deedglass marks the suggestion `superseded` (`stale_reason: base_changed`) and changes nothing, because its diff and replay describe a policy that is no longer live.

**What to do.** Generate suggestions again (`POST /v1/suggestions/generate`, or `deedglass insights suggestions generate`). If the evidence still holds, a new suggestion is written against the version enforced now.

```json
{
  "type": "invalid_request_error",
  "code": "suggestion_stale",
  "message": "acme-sim v2 has been enforced since this suggestion was written for v1. Generate suggestions again: POST /v1/suggestions/generate.",
  "param": null
}
```

## shadow_slot_taken

**409 · invalid_request_error**

**When it happens.** An org runs at most one policy version in shadow. Accepting a suggestion creates a new version and runs it in shadow, and another version is already running there. The suggestion goes back to `open`; nothing changed.

**What to do.** Promote or deactivate the running shadow first, or accept again with `"replace_shadow": true` (`--replace-shadow` in the CLI) to replace it. Replacing seals a deed that names the version it replaced.

```json
{
  "type": "invalid_request_error",
  "code": "shadow_slot_taken",
  "message": "acme-sim v2 is already running in shadow. Promote or clear it first, or accept with replace_shadow: true to replace it.",
  "param": "replace_shadow"
}
```

## suggestion_decided

**409 · invalid_request_error**

**When it happens.** The suggestion was already accepted, rejected or superseded, or another person's accept of it is still running. Exactly one decision is recorded per suggestion, with one deed.

**What to do.** Read the suggestion (`GET /v1/suggestions/{id}`): `status`, `decided_by`, `decided_at` and `resulting_version` say what happened. To change the policy further, generate suggestions again or push a version yourself.

```json
{
  "type": "invalid_request_error",
  "code": "suggestion_decided",
  "message": "This suggestion was already accepted on 2026-10-09 by priya@example.test. It created acme-sim v3.",
  "param": null
}
```

## observe_not_enabled

**409 · invalid_request_error**

**When it happens.** You opened a watched session (`POST /v1/observe/sessions`, `deedglass observe start` or `observe run`) on a Postgres connector whose observe mode is off. Observe mode reads the same change log as the Witness, so it needs `witness.enabled: true` and `witness.observe.enabled: true` in the connector's config. Nothing was opened.

**What to do.** Turn it on in Console › Connectors › your connector, or add `observe: {enabled: true}` under `witness` in the connector's config and update it. Then open the session again.

```json
{
  "type": "invalid_request_error",
  "code": "observe_not_enabled",
  "message": "Observe mode is off for pg-demo. Turn it on in Console › Connectors › pg-demo, or set witness.observe.enabled: true in its config.",
  "param": "connector"
}
```

## pro_required

**403 · permission_error**

**When it happens.** You asked for something that is part of Pro without a Pro licence on the machine that runs Deedglass: today, undoing a whole watched session in one step (`POST /v1/observe/sessions/{id}/undo`, `deedglass observe undo`). Nothing ran, and nothing free changed: the session, its preview, per-action undo and the undo SQL all keep working.

**What to do.** Print the undo SQL and run it yourself, free: `deedglass observe show <id> --undo-sql`. Or add Pro (deedglass.com/pricing) and run `deedglass licence activate` on the machine that runs `deedglass serve`.

```json
{
  "type": "permission_error",
  "code": "pro_required",
  "message": "Undoing a whole session in one step is part of Pro. Per-action undo and the undo SQL stay free: deedglass observe show obs_01k8x2… --undo-sql.",
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
