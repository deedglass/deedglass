# Agents and API keys

Every agent that touches your tools gets its own identity in Deedglass: a name people recognise,
labels that policies can match, budgets, an owner, and API keys that are shown once, rolled without
downtime and revoked at once. Every change is sealed as an admin deed.

Stopping an agent has its own page: [STOP.md](STOP.md). People, sessions and roles are in
[IDENTITY.md](IDENTITY.md).

Code: `internal/identity` (`agents.go`, `keys.go`, `apikey.go`, `keyauth.go`), the bearer pipeline in
`internal/httpx/server.go`, the CLI in `cmd/deedglass/agentcmd.go` (S08).

## 1. Agents

```http
POST /v1/agents
Authorization: Bearer dg_live_…(admin key)
Idempotency-Key: 5f0c1e9e-6d1a-4a53-9a1f-agent-support-bot

{"name":"support-bot","description":"Answers refund tickets in Zendesk",
 "labels":{"env":"prod","team":"support"},
 "budgets":{"calls_per_minute":60,"calls_per_day":5000,"spend_per_day":{"amount":5000,"currency":"usd"}}}
```

`201` returns the `agent` object and the `deed` that sealed `agent.created`.

| Field | Rule | Can change? |
|---|---|---|
| `name` | `^[a-z0-9][a-z0-9-]{1,62}$` (2 to 63 characters), unique in the org; `console`, `system` and `deedglass` are reserved | **No** |
| `description` | up to 500 characters, no control characters | yes |
| `labels` | at most 32; key `^[a-z0-9][a-z0-9._/-]{0,62}$`; value up to 128 printable characters | yes, sealed as `agent.updated` |
| `budgets` | `calls_per_minute` 1–1,000,000 · `calls_per_day` 1–100,000,000 · `spend_per_day` `{amount, currency}` | yes, sealed as `agent.updated` |
| `owner` | the `usr_…` of an active member: the person responsible. Defaults to the creator when a person creates it | yes (`""` clears it) |
| `status` | `active`, `suspended` or `stopped` ([STOP.md](STOP.md)) | through the lifecycle routes only |

**Names never change.** Policies (S10) match on agent names, so a rename could silently move an
agent out of a rule that was written for it. `PATCH /v1/agents/{id}` with `name` is
`422 validation_failed`:

```json
{ "type": "invalid_request_error", "code": "validation_failed", "param": "name",
  "message": "Agent names can't change, because policies match on them. To rename, create a new agent with the new name and move its keys." }
```

To rename, create a new agent, create keys for it, deploy them, then revoke the old agent's keys.
The database backs this up: the runtime role has no `UPDATE` grant on `agents.name`.

**Labels feed policy matching**, so every label change is an admin deed (`agent.updated`, with the
changed field names and the new labels). A `PATCH` that changes nothing seals nothing.

**Budgets use contract money:** integer minor units and lowercase ISO 4217 codes
(`{"amount":5000,"currency":"usd"}` is $50.00). S08 stores and validates budgets; S11 enforces them.
Today a budget limits nothing.

Other routes: `GET /v1/agents` (`?status=`, `?label=env:prod`, `?name=`, cursor pages),
`GET /v1/agents/{id}`. Reading needs a session (viewer and up) or an org admin key; changing needs
admin or owner, or an org admin key.

## 2. API keys

### The format

```
dg_live_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8
└┬┘ └┬─┘ └──┬───┘ └──────────────┬─────────────┘
 │   │      │                    └ secret: 32 base62 characters, about 190 bits
 │   │      └ prefix: 8 base62 characters, public, globally unique
 │   └ mode: live or test
 └ always dg
```

- **Exactly 49 characters.** The parser is strict: no surrounding space, no other case, no other
  length. Its errors never quote what you sent.
- **Generated from `crypto/rand`** with rejection sampling, so every base62 character is equally
  likely.
- **Stored as the prefix plus `sha256(pepper ‖ secret)`.** Deedglass never stores the secret. The
  hash is compared in constant time; an unknown prefix is compared against a fixed dummy hash, so
  an unknown prefix and a wrong secret take the same path and get the same answer.
- **Shown once,** in the create or roll response. Lists show the prefix only: `dg_live_jJbEEMWk`.

```
$ deedglass agent keys create support-bot --name "prod runner"
✓ Key created · support-bot · live · key_01k6jz12ptrvg2zn5vts5xc1ay

  dg_live_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8

  This is the only time Deedglass shows this key. Store it in your secret manager now.
  Scopes · actions:execute · actions:read · deeds:read
```

**Secret scanning.** Keys match `dg_(live|test)_[0-9A-Za-z]{8}_[0-9A-Za-z]{32}`. Add it to your
scanner. If a key leaks, revoke it (section 2.5).

### Modes: live and test

`dg_test_` keys carry `livemode: false` in the principal, and from S13 every action records it.
**Deedglass does not enforce test mode against connectors in v1**: connectors have no test mode
yet, so a test key can reach a live system exactly like a live key. Treat test keys as live
credentials until connector modes ship (v0.2).

### Scopes

| Scope | On | Allows |
|---|---|---|
| `actions:execute` | agent keys (default) | `POST /v1/actions` and `/mcp/*` (S13–S15) |
| `actions:read` | agent keys (default) | reading that agent's own actions |
| `deeds:read` | agent keys (default) | reading that agent's action and amendment deeds |
| `actions:undo` | agent keys (optional) | undo (S23) |
| `admin` | org admin keys only | every control-plane route an admin may call, except the owner-only ones and minting admin keys |

New agent keys get the three defaults; `--scope` (repeatable) picks others. An agent key can never
hold `admin`; an admin key always holds exactly `{admin}` (a database `CHECK` enforces both).

What an agent key can and can't do:

- **It never changes any agent, including its own.** Every agent and key route needs `admin`, so an
  agent key gets `403 insufficient_scope` there, even to list agents. A prompt-injected agent can't
  start itself, relabel itself or mint itself a key.
- **It reads only its own agent's rows:** its actions, and its action and amendment deeds. Admin
  deeds and other agents' rows answer `404 not_found`.
- **The export and the chain check** (`GET /v1/deeds/export`, `GET /v1/ledger/check`) cover the whole
  org, so they need a Console session or an org admin key.

### At most 10 active keys per agent

A key is active until it is revoked or expires. The 11th is `409 conflict`:
`support-bot already has 10 active keys. Revoke one before creating another.` Key lists show the
newest 100 keys per agent, revoked ones included.

### Rolling with an overlap

```
$ deedglass agent keys roll key_01k6jz12ptrvg2zn5vts5xc1ay --overlap 24h
✓ Key rolled · support-bot · key_01k6jt6tgx72gjr876976xak5e
  dg_live_0IDw6L5O_9hxevwY7nqdKIZZi5A2xlI2O2qn8znik
  The old key dg_live_jJbEEMWk keeps working until 2099-10-04 10:03 UTC. Deploy the new one before then.
  This is the only time Deedglass shows the new key. Store it in your secret manager now.
```

- `POST /v1/keys/{id}/roll {"expires_in": 86400}` creates a replacement with the same agent, name,
  mode and scopes, and returns `previous: {id, prefix, expires_at}`.
- `expires_in` is the overlap in seconds, 0–604800 (7 days), default 86400. `0` ends the old key now.
- The old key works until `expires_at`, then answers `401 api_key_expired`:
  `The API key expired at 2026-10-04 10:03 UTC after it was rolled. Use the new key, or create one in Console › Agents.`
- Rolling a revoked or expired key is `409 conflict`. Create a new key instead.
- Sealed as `api_key.rolled` (`{from, to, overlap_s}`).

### Revoking

`POST /v1/keys/{id}/revoke {"reason": "…"}` (reason optional, up to 280 characters). Every
Deedglass instance refuses the key within 2 seconds ([STOP.md](STOP.md) has the measurements),
with `401 api_key_revoked`. Revoking a revoked key returns `200` unchanged with `"deed": null`.
Sealed as `api_key.revoked`.

### Replaying a key create

The create and roll routes are idempotent. Because Deedglass never stores the secret, a replay of
`POST /v1/agents/{id}/keys` with the same `Idempotency-Key` returns the same key object with
`"secret": null` and the header `Deedglass-Idempotent-Replayed: true`. **If the first response was
lost, roll or revoke that key**: nobody can see its secret again.

### `last_used_at`

Kept in memory and written every 30 seconds, one `UPDATE` per org, never on the request path. It is
precise to within a minute ("Last used 2m ago").

## 3. `GET /v1/whoami`

Any key may call it. It **skips the stop checks**, so a stopped or suspended agent can learn why it
is refused. It never shows the stop reason or who stopped the agent.

```json
{ "object": "whoami",
  "org":   { "id": "org_01k6h7kp6msmzrhdzwtcr3jnkp", "name": "Acme", "slug": "acme", "stopped": false },
  "agent": { "id": "agt_01k633rrmh3bcz2p39gg4vp46q", "name": "support-bot", "status": "active",
             "labels": { "env": "prod", "team": "support" } },
  "key":   { "id": "key_01k6jt6tgx72gjr876976xak5e", "prefix": "dg_live_0IDw6L5O", "mode": "live",
             "scopes": ["actions:execute","actions:read","deeds:read"], "expires_at": null },
  "budgets": { "calls_per_minute": 60, "calls_per_day": 5000, "spend_per_day": { "amount": 5000, "currency": "usd" } } }
```

For an org admin key, `agent` and `budgets` are `null`.

## 4. Org admin keys

An org admin key (`agent: null`, scopes exactly `{admin}`) lets scripts and the CLI manage agents
without a browser.

- **It acts as the Admin role, never as Owner.** It can create, change, suspend, stop and start
  agents, create, roll and revoke agent keys, and Stop all.
- **It can't call `start-all`, and it can't mint or roll admin keys** (`403 insufficient_role`). So a
  stolen admin key can't persist access by minting another, and can't release the brake an owner
  pulled.
- **Only two ways to mint one:** a signed-in admin or owner (`POST /v1/org/keys`; `GET /v1/org/keys`
  lists them), or the server operator, who works on the database directly:

```
deedglass org keys create --org acme --name ci      # prints the key once; sealed with actor operator
deedglass org keys list --org acme
deedglass org create --name Acme --owner-email priya@acme.com --admin-key
```

- **While the org is stopped, admin keys can only read.** `GET` requests (agents, keys, whoami,
  deeds, the Ledger) still answer; anything that changes state gets `403 org_stopped`. A person
  signed in to the Console can still act.

The CLI reads the admin key from `--api-key` or `DEEDGLASS_API_KEY`, and the server from `--server`,
`DEEDGLASS_SERVER`, `DEEDGLASS_BASE_URL`, the `.deedglass/stack.json` that `deedglass init` wrote in
this directory or a parent, then `http://127.0.0.1:8080` ([Server resolution](CLI.md#server-resolution)).
An agent key gets:

```
✕ This is an agent key, and managing agents needs an admin key. Create one with deedglass org keys create on the server.
```

Exit codes: 0 ok, 1 failure, 2 usage, 3 not authenticated.

## 5. How a bearer key is checked

The checks run in this order; the first failure wins.

| # | Check | Failure | Answer |
|---|---|---|---|
| 1 | `Authorization: Bearer …` is present | missing | 401 · authentication_error · `authentication_required` |
| 2 | failed-auth limiter (address, then prefix) | over the limit | 429 · rate_limit_error · `rate_limited` + `Retry-After` |
| 3 | strict parse → prefix lookup (cache, else `preauth_api_key`) → mode matches → constant-time hash | any failure | 401 · authentication_error · `invalid_api_key` (one message for all) |
| 4 | not revoked | revoked | 401 · authentication_error · `api_key_revoked` |
| 5 | not expired | expired | 401 · authentication_error · `api_key_expired` |
| 6 | org not stopped (every key) | stopped | 403 · permission_error · `org_stopped` |
| 7 | agent not stopped (agent keys) | stopped | 403 · permission_error · `agent_stopped` |
| 8 | agent not suspended (agent keys) | suspended | 403 · permission_error · `agent_suspended` |
| 9 | the route's scopes | missing | 403 · permission_error · `insufficient_scope` |

- **Revoked and expired come after the hash check,** so only someone holding the real secret can
  learn that a key is revoked or expired.
- **`GET /v1/whoami` skips steps 6–8.**
- **Another scheme** (`Authorization: Basic …`) is `401 invalid_api_key`: `The Authorization header
  must be Bearer followed by an API key (dg_live_… or dg_test_…).`
- **An org whose own status isn't active** answers `invalid_api_key` at step 3.
- **If Postgres can't be reached** on a cache miss, the answer is `503 service_unavailable`, never a
  pass.
- **A key on a route only a signed-in person may call** (`start-all`, `/v1/org/keys`) is
  `403 insufficient_role`.

**Challenges.** Every 401 on a key route carries:

```
WWW-Authenticate: Bearer realm="deedglass", error="invalid_token", error_description="<the message>"
```

(the description is the message in printable ASCII: `›` becomes `>`). A missing header carries
`Bearer realm="deedglass"`. Step 9 carries
`Bearer realm="deedglass", error="insufficient_scope", scope="<scope>"`.

On success, the principal (kind, org, agent, key, scopes, livemode) goes into the request context,
and `org_id`, `agent_id` and `key_id` go onto the request's log line. The key never does.

### The failed-auth limiter

| Limit | Key | Budget | Then |
|---|---|---|---|
| Invalid keys | client address | 10 a minute | a penalty of 1 s, doubling with each further failure (2 s, 4 s, … up to 15 minutes); 10 minutes without a failure resets it |
| Invalid keys | key prefix | 10 a minute | 429 until the minute passes |

Only `invalid_api_key` failures count. A revoked or expired key doesn't, because whoever sends it
holds a real secret and should keep getting the useful answer. Successful checks never count.
Unknown prefixes are never cached; the limiter handles floods. The 429 reads:
`Too many requests with invalid API keys from this address. Try again in 4 seconds.`

The limiter lives in each server process: behind N instances, a client gets up to N times the
budget.

## 6. Admin deeds

| Action | Data |
|---|---|
| `agent.created` | `{name, labels, budgets}` |
| `agent.updated` | `{fields}`, plus the new `labels` and `budgets` when they changed |
| `agent.suspended` · `agent.resumed` · `agent.stopped` · `agent.started` | `{from, to}`, and the reason |
| `org.stopped_all` · `org.started_all` | `{agents_affected}`, and the reason |
| `api_key.created` | `{key, mode, scopes}` |
| `api_key.rolled` | `{from, to, overlap_s}` |
| `api_key.revoked` | `{key}`, and the reason |

The actor is `{type: "user", id: "usr_…"}`, `{type: "api_key", id: "key_…"}` for an org admin key, or
`{type: "system", id: "operator"}` for the database-direct CLI commands. A no-op (stopping a stopped
agent, revoking a revoked key) seals nothing and returns `"deed": null`.

## 7. CLI

```
$ deedglass agent create support-bot --label env=prod --label team=support --calls-per-minute 60
✓ Agent created · support-bot · agt_01k633rrmh3bcz2p39gg4vp46q
✓ Deed sealed · agent.created · deed_01k6vrddq2atgb2023qvhhct3g
  Next: create its key with `deedglass agent keys create support-bot`.

$ deedglass agent list
NAME           STATUS    KEYS   LABELS                  LAST USED
support-bot    active    2      env=prod team=support   2m ago
refund-agent   stopped   1      env=prod                1h ago

$ deedglass agent list          # with a revoked admin key
✕ The API key has been revoked. Create a new key in Console › Agents.
```

Only the status word is coloured (red for `stopped`), and only on a terminal; `NO_COLOR` is
respected. Every command takes `--json`. The full list is in [CLI.md](CLI.md).

## Signed intents

An API key says which agent is calling; it is a bearer secret. A signed intent adds a narrower
signal: the agent's runtime signs each call (the tool, a digest of the exact arguments, a one-time
nonce and a short window) with an Ed25519 key that never leaves it. Each agent has an intent mode
(`off` by default, `optional` or `required`, set with `PATCH /v1/agents/{id} {"intent_mode": …}`)
and at most two intent keys. Agents also say whom they serve with `Deedglass-On-Behalf-Of` and
`Deedglass-Session`.

```
deedglass agent intent keygen support-bot      # on the agent's machine
deedglass agent intent add support-bot --public-key ed25519:…
deedglass agent intent mode support-bot required
```

The spec, the checks in order and what intents don't do: [INTENTS.md](INTENTS.md). On-behalf-of and
the header table: [IDENTITY.md](IDENTITY.md). The commands: [CLI.md](CLI.md). MCP clients
authenticate with API keys as bearer tokens ([MCP_AUTHORIZATION.md](MCP_AUTHORIZATION.md)).

## 8. What this doesn't do

- **No renames.** Create a new agent instead.
- **Budgets aren't enforced yet.** S08 stores and validates them; S11 enforces them.
- **Test keys aren't enforced against connectors.** A `dg_test_` key is recorded as
  `livemode: false` from S13, but nothing stops it reaching a live system in v1.
- **No secret recovery.** A lost secret is gone; roll or revoke the key.
- **No per-key IP allowlists, and no expiry for keys that were never rolled.** A key works until
  it is rolled or revoked.
- **The failed-auth limiter is per instance,** as above.
- **`last_used_at` is approximate,** to within a minute.
- **An API key alone is enough for an agent in intent mode `off` (the default).** Set the agent to
  `required` to make every call carry a signed intent ([INTENTS.md](INTENTS.md)); the Gate enforces
  the mode from S13.
