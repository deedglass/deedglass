# @deedglass/sdk

Put Deedglass in front of your agent's tools. Every call is decided by policy, approved by a person when a rule says so, and sealed as a deed you can check offline.

```ts
import { Deedglass, wrapTool } from '@deedglass/sdk';

const dg = new Deedglass();            // reads DEEDGLASS_API_KEY and DEEDGLASS_SERVER
const refund = wrapTool(refundCustomer, { client: dg, tool: 'billing.refund_customer' });
await refund({ customer: 'cus_123', amount: 2000 });   // runs only if policy allows it
```

Works with the Vercel AI SDK, the OpenAI Agents SDK, LangChain.js and the Claude Agent SDK. No runtime dependencies. Node 22.12+, Bun, Deno and edge runtimes.

**What it doesn't do:** it doesn't sandbox your function (it decides whether it runs), and results your code reports are recorded as reported (L0), not independently checked. Use a Deedglass connector when you need L1 or L2.

## Install

```sh
npm install @deedglass/sdk
```

Set `DEEDGLASS_API_KEY` (an agent key, `dg_test_…` or `dg_live_…`, from Console › Agents) and `DEEDGLASS_SERVER` (default `http://127.0.0.1:8080`). The package is ESM; on Node 22.12 and newer, `require('@deedglass/sdk')` works too.

## Three ways to use it

**1. Your own functions: `wrapTool` (proxy mode).** Deedglass decides; your function runs only after the action is authorized, at most once; its outcome is reported and sealed.

```ts
const refund = wrapTool(refundCustomer, {
  client: dg,
  tool: 'billing.refund_customer',
  onPending: 'throw',          // or 'wait' for a person to decide
  failureMeans: 'unknown',     // a thrown error means "it may have run" (the default)
});
```

- A denial throws `PolicyDeniedError` with the policy's sentence. Your function never ran.
- A pending approval throws `ApprovalPendingError` (with `approvalId` and `expiresAt`). Calling again with the same arguments after a person approves runs your function once; it doesn't ask again.
- If your function throws, the deed says UNVERIFIABLE ("it may have run") and you get `ToolExecutionError` with the original as `cause`. Pass `failureMeans: 'not_applied'` only when the function fails before any side effect.

**2. Connector tools: `act` (gateway mode).** Deedglass runs the tool with the connector's credentials, which never leave it.

```ts
const a = await dg.act('stripe.create_refund', { charge: 'ch_3QxZ8f2L', amount: 2000 });
a.status;          // 'executed' · 'pending_approval' · 'failed'
a.witness?.verdict; // 'VERIFIED' at L1: Stripe's own record was read back
a.deed;            // 'deed_01j9…'
```

A tool that ran and failed returns `status: 'failed'` with its deed: an action happened, so it isn't an exception.

**3. Check deeds offline: `@deedglass/sdk/verify`.** No key needed, same answers as `deedglass verify`.

```ts
import { fetchKeys, verifyDeeds, splitLines } from '@deedglass/sdk/verify';
const { keys } = await fetchKeys('https://deedglass.example/.well-known/deedglass/keys.json');
const report = await verifyDeeds(splitLines(bytes), keys, { keysPinned: true });
report.result;     // 'valid' · 'invalid' · 'cannot_determine'
```

## Frameworks

| Import | Govern your tools (proxy) | Connector tools (gateway) |
|---|---|---|
| `@deedglass/sdk/ai-sdk` | `governTools(tools, { client })` | `deedglassTools(client)` |
| `@deedglass/sdk/openai-agents` | `governTool(tool, { client })` | `deedglassTools(client)` |
| `@deedglass/sdk/langchain` | `governTool(tool, { client })` | `deedglassTools(client)` |
| `@deedglass/sdk/claude-agent-sdk` | `deedglassHooks({ client })`, `governSdkTool(def, { client })` | `deedglassMcpServer(client)` |

Adapters return the denial or pending sentence to the model by default (`onDenied: 'message'`, `onPending: 'message'`), because frameworks feed tool output back to the model and a throw ends some runs.

## Errors

Every error is a `DeedglassError` with `type`, `code`, `message` (the server's, verbatim), `param`, `status`, `requestId` and `docUrl`; denials carry the sealed `action`. Use `isDeedglassError(e)`; it works across copies of the package.

| Class | When |
|---|---|
| `AuthenticationError` | `invalid_api_key`, `api_key_revoked`, `api_key_expired` |
| `PermissionDeniedError` | `agent_stopped`, `org_stopped`, `insufficient_scope`, `intent_*` |
| `PolicyDeniedError` | `policy_denied` |
| `BudgetExceededError` | 429 `budget_exceeded` (never retried) |
| `RateLimitError` | 429 `rate_limited`, after retries |
| `InvalidRequestError` | `validation_failed`, `not_found`, `conflict`, `idempotency_conflict` |
| `APIError` | 5xx |
| `ConnectionError`, `TimeoutError` | the network; a timeout means the action may still be running |
| `ApprovalPendingError`, `ToolExecutionError` | `wrapTool` |

## Idempotency and retries

Every POST carries an `Idempotency-Key`, the same one on each retry, so the server never runs a tool twice. Network errors, 408, 429 `rate_limited`, 500, 502, 503 and 504 are retried twice by default with exponential backoff, honouring `Retry-After` up to 10 s and never past your `signal` or `timeoutMs` (30 s per request).

## Signed intents

Give the client an Ed25519 key (`deedglass agent intent keygen` writes one) and every call is signed with `deedglass.intent/1`:

```ts
const dg = new Deedglass({ intentKey: { privateKeyPem: process.env.DEEDGLASS_INTENT_KEY! } });
```

`signIntent`, `argsDigest`, `intentKid` and `generateIntentKey` are exported for your own use. Integers above 2^53 − 1 in arguments are refused: send them as strings.

## Runtimes

Node 22.12 and newer, Bun, Deno, Cloudflare Workers and Vercel Edge (Ed25519 in WebCrypto). The client refuses to start in a browser page, because an agent key is a secret: call Deedglass from your server. Pass `dangerouslyAllowBrowser: true` only if you accept that risk. `@deedglass/sdk/verify` runs anywhere.

## What it doesn't do

- `wrapTool` decides whether your code runs; it doesn't sandbox what it does.
- Proxy-mode results are L0 (edge-reported). Independent L1/L2 checks need a Deedglass connector.
- The Claude hooks see only tools the Agent SDK routes through hooks.
- Offline verification checks seals and the chain, not whether the action was wise.

Apache-2.0.
