# The TypeScript SDK (`@deedglass/sdk`)

`@deedglass/sdk` puts Deedglass in front of a TypeScript agent's tools: every call is decided by
policy, approved by a person when a rule says so, run, witnessed and sealed as a deed. The same
package checks deeds offline. It has no runtime dependencies and runs on Node 22.12 and newer, Bun,
Deno, Cloudflare Workers and Vercel Edge. The quickstart is `sdk/typescript/README.md`; this page is
the reference. Versions on this page were checked on 2026-10-04 (rows C-081 to C-085 in
`CLAIMS_REGISTER.md`).

## The client

```ts
new Deedglass({
  apiKey,            // default DEEDGLASS_API_KEY; dg_live_ or dg_test_, an 8-character prefix, a 32-character secret
  baseUrl,           // default DEEDGLASS_SERVER, else http://127.0.0.1:8080
  timeoutMs,         // per request, default 30,000
  maxRetries,        // default 2
  fetch,             // injectable (tests, proxies, edge runtimes)
  intentKey,         // { privateKeyPem, org?, agent?, kid? }: sign every act() and wrapTool call
  appInfo,           // { name, version? } appended to the User-Agent
  dangerouslyAllowBrowser,
  logger,            // { warn, debug }, default console
})
```

| Method | Route | Notes |
|---|---|---|
| `act(tool, args, opts)` | `POST /v1/actions` (gateway) | `opts`: `idempotencyKey`, `onBehalfOf`, `session`, `waitMs` (≤ 25,000, default 10,000), `approval: { wait, timeoutMs }`, `signal`. A tool that ran and failed returns `status: 'failed'` |
| `actions.get(id, { waitMs, expand })` | `GET /v1/actions/{id}?wait=&expand[]=` | a long poll up to 25 s |
| `actions.waitUntilSettled(id, { timeoutMs })` | repeated `?wait=25` | returns once the status isn't `processing` or `pending_approval` |
| `actions.cancel(id)` | `POST /v1/actions/{id}/cancel` | only while pending |
| `actions.reportResult(id, result)` | `POST /v1/actions/{id}/result` | proxy mode; Idempotency-Key `sdkr_<action id>` |
| `tools.list({ include, includeHidden })` | `GET /v1/tools` | every page; `include` filters by glob |
| `deeds.get(id)` | `GET /v1/deeds/{id}` | the envelope as sealed |
| `deeds.verify(deed, keys?)` | none (offline) | fetches `/.well-known/deedglass/keys.json` once when no keys are given |
| `keys.fetch(url?)` | `GET /.well-known/deedglass/keys.json` | https, or http on localhost |
| `whoami()` | `GET /v1/whoami` | cached 5 minutes |

Every request sends `Authorization: Bearer …`, `Deedglass-Version: 2026-10-01`, `Accept`, a
`User-Agent` of `deedglass-sdk-typescript/0.1.0 (<runtime>)` where the runtime allows one, an
`Idempotency-Key` on every POST, and `Deedglass-On-Behalf-Of`, `Deedglass-Session` and
`Deedglass-Intent` when set. The body's `context` is `{ client: { name: "@deedglass/sdk", version },
transport: "sdk", call_id? }`: since S32 (DL-047, PROPOSED as C1 of ADR-063) the API accepts an optional
`context.call_id`, and `ActOptions.callId` (and every adapter: the AI SDK's `toolCallId`, LangChain's
`toolCall.id`, the OpenAI Agents SDK's `callId`, Claude's `tool_use_id`) sends it; an id outside the
API's rule (1 to 128 printable ASCII characters, no spaces) is left out, never an error.

**The key** is checked at construction (no request is made with a bad one), held in a private
field, and never printed: `String(client)`, `JSON.stringify(client)` and `util.inspect(client)` show
`dg_test_1a2b3c4d_…`.

**The browser guard:** with `window` and `document` present, the constructor throws
`browser_not_supported` unless `dangerouslyAllowBrowser: true`. `@deedglass/sdk/verify` has no guard.

**CORS stays closed** on the API (contract §8): the SDK is meant for servers, where an agent key
can be kept secret. Only the public seal keys answer cross-origin requests.

## Retries and timeouts

Retried: network errors, 408, 429 `rate_limited`, 500, 502, 503, 504. Never retried: 429
`budget_exceeded` (a policy limit with its own reset) and everything else. Backoff is
500 ms × 2^n ± 25%, or `Retry-After` up to 10 s, never past the caller's `signal` or `timeoutMs`
(`AbortSignal.any`). Every retry of a POST reuses its Idempotency-Key, and the Gate never runs a
tool twice for one key.

## Errors

`DeedglassError` carries `type`, `code`, `message` (the server's, verbatim), `param`, `status`,
`requestId`, `docUrl` and, for refusals on `POST /v1/actions`, the sealed `action`. Classes are chosen
by `type` first, then `code`; an unknown code is still a `DeedglassError`. `isDeedglassError(e)`
works across copies of the package (a `Symbol.for('deedglass.error')` brand).

| Class | type / code |
|---|---|
| `AuthenticationError` | `authentication_error` |
| `PermissionDeniedError` | `permission_error` (`agent_stopped`, `org_stopped`, `insufficient_scope`, `intent_*`) |
| `PolicyDeniedError` | `policy_error` (`policy_denied`), and a recorded `tool_unknown` |
| `BudgetExceededError` | `rate_limit_error` + `budget_exceeded` (`retryAfterMs`) |
| `RateLimitError` | `rate_limit_error` otherwise (`retryAfterMs`) |
| `InvalidRequestError` | `invalid_request_error`, and the SDK's own argument checks (`client_error`) |
| `APIError` | `api_error`, a 2xx the SDK couldn't read |
| `ConnectionError` | the network |
| `TimeoutError` | no answer in time (`actionId` when known): the action may still be running |
| `ApprovalPendingError` | `wrapTool` with `onPending: 'throw'` (`approvalId`, `actionId`, `expiresAt`) |
| `ToolExecutionError` | `wrapTool`: your function threw after it was authorized (`actionId`, `cause`) |

## `wrapTool`

```ts
wrapTool(fn, { client, tool, failureMeans = 'unknown', onPending = 'throw', approvalTimeoutMs = 600_000, toResult, onSealed, connectorType = 'sdk' })
  → (args, { signal, onBehalfOf, session }) => Promise<R>
```

1. The call is a proxy-mode action with `connector_type: "sdk"` and `wait: 0`.
2. Denied → `PolicyDeniedError`; your function never runs.
3. Pending → `ApprovalPendingError` (or, with `'wait'`, a long poll until a person decides). The SDK
   remembers the pending action by `(tool, args digest, on-behalf-of)` until it settles: calling
   again with the same arguments re-reads that action instead of asking again, and runs your function
   once it is authorized. Two identical calls racing after the approval share one run.
4. Authorized → your function runs with `{ signal, actionId }`; the signal ends at the edge deadline.
5. The result is reported: plain JSON up to 256 KiB, larger results as `result_digest` (SHA-256 of JCS)
   only, anything else through `toResult` or as `{ "text": String(value) }` (64 KiB). If your function
   throws, the report is `unknown` with `error_code: "tool_error"` (or `failed` with
   `failureMeans: 'not_applied'`) and a message scrubbed of secret shapes (the Go `vault.Scrub`
   patterns), at most 1 KiB.
6. Reporting retries up to 5 times inside the deadline; a 409 counts as reported. If every attempt
   fails the SDK logs a warning, and the server seals the action UNVERIFIABLE (`edge_lost`).

## Signed intents

`signIntent({ key, org, agent, tool, args, onBehalfOf, session, ttlMs = 60_000, now, nonce })` is a
byte-for-byte port of S09's signer (`internal/intent/agent.go`): the intent is the JCS of
`{v: "deedglass.intent/1", org, agent, kid, tool, args_digest, nonce, iat, exp, on_behalf_of?, session?}`
(at most 2,048 bytes; TTL at most 300,000 ms; a 16-byte hex nonce), the signature covers
`"deedglass/intent/v1\n" + intent_hash`, and the header is `b64url(intent).b64url(signature)`. The
tool must match S09's `^[a-z][a-z0-9-]{1,31}\.[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`. Arguments follow
the args profile: finite numbers, depth at most 32, at most 256 KiB, and no integer above
2^53 − 1 written without an exponent (send it as a string). `argsDigest`, `intentKid` (hex of the
first 16 bytes of SHA-256 of the public key) and `generateIntentKey` (a PKCS#8 PEM, the
`ed25519:<b64url>` public key, the kid) are exported. Every vector in `testdata/intents/valid`
reproduces byte for byte.

## `@deedglass/sdk/verify`

`parseKeySet`, `verifyDeed`, `verifyDeeds`, `Verifier`, `splitLines`, `canonicalize`, `fetchKeys`
and the message catalog. It is S05's browser verifier (`web/verify/src`), copied with a drift check
(`make sdk-sync-verify`), so the CLI, the verify page and the SDK give the same code, seq and field on
every vector in `testdata/deeds` and on all 500 parity cases. `fetchKeys(url, { fetch, timeoutMs })`
accepts https, or http on `localhost`/`127.0.0.1`; it sends no credentials or referrer, refuses
redirects, reads at most 1 MiB and returns `{ keys, pinned: true, source }`.

## Numbers (`bench/results/2026-10-04-darwin-arm64-sdk-ts.json`)

| Measure | Budget | Result |
|---|---|---|
| core `dist` gzip, without verify | ≤ 16 KiB | 12.2 KiB |
| `/verify` gzip | ≤ 19 KiB (ADR-062) | 18.07 KiB |
| each adapter gzip | ≤ 3 KiB | 0.45 to 1.65 KiB |
| `act()` client overhead, mock fetch, p50 | < 0.3 ms | 0.014 ms |
| `verifyDeeds`, 1,000 deeds | < 1.5 s | 88 ms |
| cold `import('@deedglass/sdk')` | < 30 ms | 9.2 ms |

## What it doesn't do

- `wrapTool` decides whether your code runs; it doesn't sandbox what it does.
- Proxy-mode results are L0 (edge-reported). Independent L1 and L2 checks need a Deedglass connector.
- The Claude hooks see only tools the Agent SDK routes through hooks.
- Offline verification checks seals and the chain, not whether the action was wise.
