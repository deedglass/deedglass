# Connectors

A **connector** turns a system your agents act on (an API, or a Postgres database since S17; GitHub, Slack,
Stripe, Gmail and Notion later) into governed **tools**. An admin registers it once and stores its
credential once. From then on every agent call passes through the Gate: it is decided by policy,
runs with the credential added at the last moment, and is sealed as a deed. **The agent never sees
the credential.** (S16) The Postgres type has its own page: [POSTGRES.md](POSTGRES.md).

## Concepts

| Word | Meaning |
|---|---|
| Connector | One configured system in an org: a name (`orders`), a type (`rest`), a config and at most one credential. |
| Tool | One operation a connector serves: `get`, `update_status`. Agents call it by its qualified name. |
| Qualified name | `<connector>.<tool>`, split at the first dot: `orders.get`. The Gate, policies, the Ledger and deeds all use it (at most 200 bytes). |
| Side effect | `read`, `write` or `destructive`. A REST tool's comes from its method (GET and HEAD read, DELETE is destructive, the rest write) and can be raised, never lowered. |
| Level | How far the Witness can check a tool's result: `L0` Reported (the tool's own answer), `L1` Confirmed (a read-back, S19), `L2` Witnessed (the system of record, S20). |

**Names.** Connector names match `^[a-z][a-z0-9-]{1,31}$` and are unique among an org's live
connectors. `deedglass`, `mcp` and `native` are reserved everywhere, and `fake` outside dev. Tool
names of native types (`rest`, `postgres`, …) match `^[a-z][a-z0-9_]{0,63}$`; tools from MCP servers
keep the MCP charset. Anything else, including a non-ASCII look-alike, is `tool_unknown`, recorded
and sealed.

## The lifecycle: create → credentials → test → policy → use

```sh
make mock-orders                                   # the mock orders API on 127.0.0.1:8090 (another terminal)
deedglass connector create --config examples/connectors/orders.yaml
deedglass connector credentials set orders --from-env MOCK_ORDERS_TOKEN
deedglass connector test orders
deedglass policy push my-policy.yaml && deedglass policy activate …
```

```
$ deedglass connector create --config examples/connectors/orders.yaml
✓ Connector created · orders · con_01j9zq7c4m8x2v6t0r3n5k1h7d
  2 tools · orders.get (read) · orders.update_status (write)
  ○ No credentials yet. Next: deedglass connector credentials set orders --from-env ORDERS_TOKEN

$ deedglass connector credentials set orders --from-env ORDERS_TOKEN
✓ Credentials stored · orders · bearer · …9f2c · encrypted with kek:3f9a1c2b7d8e
  Agents never see this value. Deedglass adds it only when the Gate runs a call.

$ deedglass connector test orders
✓ orders · 5 checks passed · 184 ms
  ✓ config        valid · 2 tools
  ✓ credentials   set · bearer · …9f2c
  ✓ destination   orders.example.com → 93.184.216.34 · public
  ✓ tls           TLS 1.3
  ✓ auth          GET /v1/health → 200 · 41 ms
```

Against the plain-http mock in dev, `tls` is skipped (`plain http (dev only; prod needs https)`)
and the summary reads `4 checks passed · 1 skipped`.

Credentials go in only from the environment (`--from-env VAR`), stdin (`--stdin`) or per field
(`--field KEY=env:VAR|file:PATH|stdin`). There is no value flag, because flag values land in shell
history and in `ps`. Basic auth takes two fields:
`--field username=env:ORDERS_USER --field password=env:ORDERS_PASSWORD`.

The connection test answers "will this work?" without changing anything: the config, the
credential (decrypted and checked against `auth.kind`, never shown), the destination (resolved and
classified by the egress rules), TLS, and the config's `test` request, which must be a GET or a
HEAD. It records the result as the connector's `health`. At most 10 runs a minute per connector.

## Agents call tools through the Gate

```sh
curl -s -H "Authorization: Bearer $AGENT_KEY" -H 'Deedglass-Version: 2026-10-01' -H 'Content-Type: application/json' \
  -d '{"tool":"orders.get","args":{"id":"42"}}' http://127.0.0.1:8080/v1/actions
```

The answer is the sealed action: `state: sealed`, the order in `execution.result.body`, and the
`deed` id. What happens on the way:

1. The Gate resolves `orders.get` to the org's connector (one indexed read; the parsed config and
   its egress client are cached by connector id and config hash, so a changed config takes effect
   on the next call).
2. The REST connector builds the request it would send and adds `request.http = {method, path,
   host}` to the policy input, next to `tool.type` and `tool.op` (docs/POLICY_LANGUAGE.md §7.1).
   Arguments that don't build a request (a path value like `../admin`) add no `request.http`, so a
   rule that allows on `request.http.method` doesn't match them: with a default deny they are
   denied and sealed before any connector code runs.
3. The active policy decides. A denial never reaches the connector.
4. With no database transaction open, the call runs: the arguments are checked against the tool's
   schema, the credential is opened from the vault (the only decrypt path), added last, and
   destroyed after the request; the request leaves only through the egress door.
5. The output and every message are redacted with the call's credential values, capped at 1 MiB,
   and recorded; the deed seals the action.

## The REST connector

### Config reference

| Key | Default | Limit | What it does |
|---|---|---|---|
| `base_url` | required | https in prod (http in dev only) | The API's base. Checked by the egress rules when the connector is created and at every dial. No userinfo, query or fragment. A path (`/api`) is kept and tool paths go under it. |
| `auth.kind` | `none` | `bearer`, `header`, `basic`, `query`, `none` | How the credential travels: `Authorization: Bearer …`, a header (`auth.header`, default `X-API-Key`), HTTP basic, or a query parameter (`auth.param`, default `api_key`). |
| `allowed_methods` | required | GET, HEAD, POST, PUT, PATCH, DELETE | The methods tools may use. |
| `allowed_path_prefixes` | required | | Tool paths must sit under one on a segment boundary: `/v1/orders` admits `/v1/orders/42`, never `/v1/orders-admin`. |
| `timeout_ms` | 10000 | 100 to 60000 | The whole request. The Gate's own execute timeout also bounds it. |
| `max_response_bytes` | 4194304 (4 MiB) | 1024 to 16 MiB | Counted after decompression. Past it the call fails as `response_too_large`. |
| `redirects` | `none` | `none`, `same_host` | `same_host` follows at most 3 hops to the same scheme, host and port. Writes never follow a redirect. |
| `send_action_id` | `true` | | Sends `Deedglass-Action-Id: act_…`. |
| `headers` | none | 16, each one line ≤ 1 KiB | Default headers. `Authorization`, `Cookie`, `Host`, `Content-Length`, `Transfer-Encoding`, `Connection`, `Proxy-*`, `X-Forwarded-*`, `Deedglass-*` and `User-Agent` are refused. |
| `test` | none | GET or HEAD | The request `connector test` sends with the credential, like `{method: GET, path: /v1/health}`. |
| `tools` | required | 1 to 64; the config is at most 64 KiB | Below. |

A tool:

| Key | What it does |
|---|---|
| `name` | `^[a-z][a-z0-9_]{0,63}$`, unique in the connector. |
| `description` | Up to 1,024 characters, shown to agents. |
| `method`, `path` | One method from `allowed_methods`; a path under an allowed prefix whose `{placeholders}` are whole segments and equal the `in: path` args. |
| `side_effect` | Optional; only higher than the method's default. `tools[2] is a DELETE, so its side effect can't be "read". Use "destructive", or leave side_effect out.` |
| `idempotent`, `idempotency_header` | Mark a write safe to retry. With `idempotency_header: Idempotency-Key` the header carries the action id, the same on every retry. |
| `args` | Each arg: `in` (`path`, `query`, `body` or `header`), `type` (`string`, `integer`, `number`, `boolean`, `object`, `array`), and optionally `enum`, `pattern` (RE2), `min`, `max`, `max_length`, `required`, `sensitive`, `description`. A header arg names its header with `header: X-…` (`^X-[A-Za-z0-9-]{1,64}$`). At most 32. |
| `readback`, `postcondition` | The L1 read-back after a write, and an optional CEL condition over what it reads (S19). See [Read-backs](#read-backs). |

The tool's input schema is generated from its args (`additionalProperties: false`), and
`connector/schema` checks every call against it before anything is sent.

### Templating and safety

- Path values must match `^[A-Za-z0-9._~-]{1,256}$` and can't be `.` or `..`. The filled path must
  equal its own `path.Clean` and stay under an allowed prefix. `../admin`, `42/../../x` and `%2e%2e`
  are `validation_failed`, and no request is sent.
- Query args go through `url.Values`; body args become one JSON object; header args are one line of
  text up to 1 KiB.
- The credential is added last, after every other header, and its `Secret` is destroyed when the
  request is done.
- `sensitive: true` adds the arg's value to the call's redactor, so the service can't echo it back
  into the output or the logs. The action row still stores the arguments as sent (S22 moves
  sensitive args to digests; see Limits).

### Retries

Only idempotent calls retry: GET and HEAD, a tool with an `idempotency_header`, or one marked
`idempotent: true`. At most 2 more attempts, on a connection error before anything was sent, 429,
502, 503 or 504, with 200 ms·2ⁿ ±20% backoff, honouring `Retry-After` up to 5 s, and never past
the deadline. A write without an idempotency header is sent once.

### The output

```json
{ "status": 200,
  "headers": { "content-type": "application/json", "etag": "\"v1\"" },
  "body": { "id": 42, "status": "pending" } }
```

The body is the JSON itself, `{"text": …}` for other text up to 64 KiB, or
`{"digest": "sha256:…", "bytes": N}`. Only `content-type`, `etag`, `last-modified`, `location`,
`request-id`, `x-request-id`, `retry-after` and `x-ratelimit-*` pass; `Set-Cookie` never does. The
action's `Meta` carries `http_status`, `method`, the templated `path`, `host` and `attempts` for the
Witness and the Console; agents don't see it.

### Status mapping

| Upstream | Error code | The action |
|---|---|---|
| 2xx | none | `executed`, `succeeded` |
| 401, 403 | `upstream_auth_failed` | `failed` (the response body is kept, redacted) |
| 404 | `upstream_not_found` | `failed` |
| 429 | `upstream_rate_limited` | `failed`, after the retries the call allows |
| other 4xx | `upstream_error` | `failed` |
| 5xx | `upstream_error` | `unknown` (it may have run) → `UNVERIFIABLE` |
| no answer in time | `timeout` | `unknown` → `UNVERIFIABLE` |
| over `max_response_bytes` | `response_too_large` | `unknown` (it ran) |
| blocked destination | `destination_blocked` | `failed`, nothing sent |
| a redirect to another host, or to a blocked address (read tools with `redirects: same_host`; S18) | `destination_blocked` | `failed`, nothing sent to the new address |
| no credential | `credentials_missing` | `failed`, nothing sent |
| credential can't be opened | `vault_unavailable` | `failed`, nothing sent |
| arguments don't fit the schema or the path rules | `validation_failed` | `failed`, nothing sent |

Every code has a page in [api/errors.md](api/errors.md).

## Read-backs

After a write succeeds, the Witness can read the result back from the target (L1 Confirmed). A
REST write tool names a read tool of the same connector and the fields to compare; a Postgres
single-row `UPDATE` by primary key is read back without any config. How verdicts are decided, the
CEL environment and the Postgres row rule are in [WITNESS.md](WITNESS.md).

The orders example (`examples/connectors/orders.yaml`):

```yaml
    - name: update_status
      description: Set an order's status.
      method: PATCH
      path: /v1/orders/{id}
      idempotency_header: Idempotency-Key
      args:
        id:     { in: path, type: string, pattern: "^[0-9]{1,12}$", required: true }
        status: { in: body, type: string, enum: [pending, shipped, cancelled], required: true }
      readback:
        tool: get                                     # a read tool of this connector (checked when saved)
        args: { id: "{{args.id}}" }                   # whole-value templates only
        expect: { "body.status": "{{args.status}}" }  # exact, per-field comparison
        consistency: strong                           # strong (1 attempt) | eventual (attempts × interval_ms)
        attempts: 1                                   # 1 to 5; eventual defaults to 3
        interval_ms: 300                              # 100 to 2000
        on_not_found: gap                             # gap | violation
      postcondition: 'observation.body.status == args.status'   # optional CEL
```

The condition reads `args.status`, the write's own argument. S16's version of this example said
`request.status`, which isn't a field; the config check now names the fix.

| Key | Default | Limit |
|---|---|---|
| `readback.tool` | required | a tool of this connector with `side_effect: read` that is idempotent |
| `readback.args` | none | each key an argument of the read tool; literals or `{{args.<path>}}` / `{{result.body.<path>}}` |
| `readback.expect` | none | at most 32 paths like `body.status` or `body.items[0].id` |
| `readback.consistency` | `strong` | `strong` or `eventual` |
| `readback.attempts` | 1 (`eventual`: 3) | 1 to 5; above 1 needs `eventual` |
| `readback.interval_ms` | 300 | 100 to 2000 |
| `readback.on_not_found` | `gap` | `gap` or `violation` |
| `postcondition` | none | CEL returning true or false; at most 2,048 characters and an estimated cost of 100,000; needs `readback` |

The read-back runs through the same path as any call to the read tool (argument schema, path rules,
egress and the pinned address, the credential added last, redaction, the size cap). It is not an
action: it gets no deed of its own and doesn't count against the agent's budgets. Policy `verify:`
sets the level a write must reach ([POLICY_LANGUAGE.md](POLICY_LANGUAGE.md#66-verify-and-the-witness)).

### Config errors

Creating or updating the connector checks every read-back and answers `400 validation_failed` with
the field's `param`. The CLI prints `param: message`:

```text
config.tools[1].readback.tool: "update_status" writes, so it can't be a read-back. Name a read tool such as "get".
config.tools[1].readback.tool: "fetch" isn't a tool of this connector. Name a read tool such as "get".
config.tools[1].readback.tool: "list" isn't idempotent, so it can't be a read-back. Name a read tool that is.
config.tools[1].readback.attempts: attempts above 1 need consistency: eventual.
config.tools[1].postcondition: A postcondition needs a read-back to read from. Add readback: { tool: get, args: … } to this tool.
config.tools[1].postcondition: 1:28: request.status isn't a field. Did you mean args.status?
config.tools[1].postcondition: The postcondition must be true or false. Compare a field, like observation.body.status == args.status.
config.tools[1].postcondition: This condition could cost up to 520,488,451 units per call; the limit is 100,000. Compare fewer fields, or use expect.
```

The position in a postcondition error is `line:column`, counted in characters from the start of the
condition: `1:28` is where `request` starts in `observation.body.status == request.status`. The cost example is three nested `all()` loops over `observation.body.items`; compare
fixed fields instead, or move the check to `expect`. When several fields are wrong, the API names the
first and says how many there are; fix it and send the config again to see the next.

## The `mcp_http` connector (a remote MCP server)

A remote MCP server over Streamable HTTP, as one org's connector. Its credential lives in the
vault; the Gate adds it only when it opens a session. See
[MCP.md](MCP.md#put-the-gate-in-front-of-a-remote-mcp-server) for the gateway route (`/mcp/<name>`).

| Key | Default | Rules |
|---|---|---|
| `url` | required | `https://…` (`http` in dev with the private-network override); no credentials or fragment; passes the egress door at create time and at every dial |
| `auth.kind` | `bearer` | `bearer` (credential `{"token"}` → `Authorization: Bearer …`), `header` (credential `{"value"}`), `none` |
| `auth.header` | — | `header` only: a plain header name, not one the transport or MCP sets (`Host`, `Cookie`, `Mcp-*`, `Deedglass-*`, …) |
| `timeout_ms` | `30000` | 100–600000, per HTTP request |
| `tools_ttl_ms` | `30000` | 0–3600000; the shorter of this and the server's `ttlMs` |

Its tools are the server's own names (the MCP charset), with side effect `unknown`, so write
policies by tool name. A call is never retried. `connector test` runs config, destination,
credentials and a `tools/list`.

## The API

| Route | Who | What |
|---|---|---|
| `GET /v1/connectors` · `GET /v1/connectors/{id}` | viewer+ session, admin key | Connectors with their tools, the credential's metadata and `health`. `{id}` is a `con_…` id or the name. |
| `POST /v1/connectors` | admin+ session, admin key | Create (`Idempotency-Key` honoured). A blocked `base_url` is 400 `destination_blocked`; a taken name is 409 `conflict`. |
| `PATCH /v1/connectors/{id}` | admin+ | Replace `description`, `status` (`active`, `disabled`) or the whole `config`; `expected_config_hash` makes a concurrent change a 409. |
| `DELETE /v1/connectors/{id}` | admin+ | Soft-delete and crypto-shred the credential; 409 while an action on it is executing. |
| `PUT /v1/connectors/{id}/credentials` | admin+ | Write-only: `{"kind":"bearer","secret":{"token":"…"}}` (≤ 16 KiB). Never logged, never stored for replay. |
| `DELETE /v1/connectors/{id}/credentials` | admin+ | Remove (crypto-shred) the credential. |
| `POST /v1/connectors/{id}/test` | admin+ | The checklist above, as a `connector_test` object. |
| `GET /v1/connector-types` | viewer+ | Each type's config schema and credential kinds, with a `secret` flag per field. |

Agent keys get 403 `insufficient_scope` on every connector route, reads included: agents can't
list, create or read connectors. Every change seals an admin deed (`connector.created`,
`connector.updated`, `connector.deleted`, `connector.credentials_updated`,
`connector.credentials_removed`) with the type, the name, the config hash and, for credentials, the
kind, version and key id.

## What connectors don't do

- **No OpenAPI import yet.** You write the REST config by hand (`connector import` is planned).
- **No proxies.** Environment proxies are ignored for connector traffic (v2).
- **No custom trust roots** for private APIs with their own TLS authority yet.
- **A read-back sees only what the target answers, and only the fields you name.** A service that
  answers 200 without saving (`MOCK_ORDERS_MODE=lying`) is an L1 `MISMATCH` when its read tool
  tells the truth; one that lies on reads too can't be caught by reading it back
  ([WITNESS.md](WITNESS.md#what-the-witness-doesnt-do)).
- **Sensitive args are redacted, not digested.** The action row keeps the arguments as sent.
- **The MCP HTTP upstreams of S15 are still configured in `deedglass.yaml`** with their headers
  from the environment; moving them to connector rows with vault credentials (`mcp_http`) is S18
  carry-over.
- **Egress sees addresses, not intent.** Misuse of an allowed API is policy's job.
