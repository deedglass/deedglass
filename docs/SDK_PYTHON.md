# The Python SDK (`deedglass`)

`deedglass` puts Deedglass in front of your Python agent's tools: a policy decides each call, a
person approves the risky ones, and every call is sealed as a deed you can check offline. It
mirrors the TypeScript SDK (`@deedglass/sdk`, S31) name for name, in snake_case.

## Install and extras

`deedglass` isn't on PyPI yet (the publishing workflow is ready and switched off until the project
exists there). Until then, install it from the repository, or from the wheel attached to each GitHub
release:

```sh
pip install "deedglass @ git+https://github.com/deedglass/deedglass#subdirectory=sdk/python"
pip install "deedglass[crypto] @ git+https://github.com/deedglass/deedglass#subdirectory=sdk/python"            # offline deed checks and signed intents (cryptography)
pip install "deedglass[langchain] @ git+https://github.com/deedglass/deedglass#subdirectory=sdk/python"         # deedglass.langchain
pip install "deedglass[claude-agent-sdk] @ git+https://github.com/deedglass/deedglass#subdirectory=sdk/python"  # deedglass.claude_agent_sdk
pip install "deedglass @ https://github.com/deedglass/deedglass/releases/download/v0.6.0/deedglass-0.6.0-py3-none-any.whl"   # a release's wheel (use the latest release's version)
```

The client has one dependency: httpx (>=0.28.1,<0.29). Once it is on PyPI, the same extras work as
`pip install "deedglass[crypto]"` and so on.

Python 3.10 to 3.14 (tested on 3.11, 3.13 and 3.14; 3.10 in Docker). Python 3.10 reaches its end of
life in October 2026; a release after that drops it. `import deedglass` imports no agent framework
(a test checks it); each adapter imports its framework when you import the adapter.
`deedglass.openai_agents` and `deedglass.crewai` aren't built yet (0.2.1): importing them
raises `ImportError` saying so (the TypeScript SDK has an OpenAI Agents adapter), and their extras are declared so the layout doesn't change.

## Configure

```python
from deedglass import Deedglass, AsyncDeedglass

dg = Deedglass()                    # DEEDGLASS_API_KEY, then DEEDGLASS_SERVER, else http://127.0.0.1:8080
dg = Deedglass("dg_test_…", base_url="https://api.deedglass.com", timeout=30.0, max_retries=2)
```

A missing key raises `AuthenticationError: No API key. Pass api_key=… or set DEEDGLASS_API_KEY.
Create one with: deedglass agent keys create <agent>.` A key that isn't `dg_live_` or `dg_test_`
shaped fails before any request. The key never appears in `repr()`, an exception or a log line:
`Deedglass(base_url='http://127.0.0.1:8080', api_key='dg_test_ab12cd34_••••')`.

Every request sends `Authorization`, `Deedglass-Version: 2026-10-01`, `User-Agent:
deedglass-python/0.2.1 (python 3.13.x; httpx 0.28.1)` and, on every POST, an `Idempotency-Key`
made once per call and reused by its retries. `on_behalf_of=` and `session=` on the client (or per
call) send `Deedglass-On-Behalf-Of` and `Deedglass-Session`.

## act

```python
action = dg.act("postgres.execute", {"sql": "UPDATE public.orders SET status = 'shipped' WHERE id = 42"},
                wait_for_approval=120)
print(action)          # act_01k6… · postgres.execute · executed · L2 · VERIFIED · deed_01k6…
action.verified        # True
action.result          # the connector's result
```

`act` returns the `Action` for `executed` and `failed`. It raises `PolicyDeniedError` (the policy's
own sentence, `.rule_id`, `.action` with its deed), `BudgetExceededError` (`.retry_after`, never
retried) and `PermissionDeniedError` (a stopped agent). A pending approval is returned as an action
with `status == "pending_approval"`; pass `wait_for_approval=<seconds>` to long-poll until a person
decides. `call_id=` sends your framework's tool call id as `context.call_id` (an id that doesn't fit
the API's rule is left out, never an error).

### MISMATCH is not an exception

When the Witness finds that the system of record changed something other than what the tool
reported, the write happened: `act` returns the action with `action.witness.verdict ==
"MISMATCH"` and `action.verified is False`. Your code decides what to do next, often
`deedglass undo preview`.

## Tools and wrap_tool

```python
@dg.tool("postgres.execute", on_deny="return")      # gateway: the body never runs, the connector does
def execute(sql: str) -> dict: ...

@dg.tool("billing.refund", execute="local")         # edge: the body runs only once authorized
def refund(customer: str, amount: int) -> dict:
    return stripe.Refund.create(customer=customer, amount=amount)
```

`on_deny="return"` returns the Gate's sentence (`Blocked by policy "…": …. Nothing was run.`) so a
model can read it; the default raises. In local mode the arguments become the action's args (only
JSON values; anything else raises `TypeError` naming the argument), the body runs at most once per
action, and its outcome is reported: a raised exception is reported as `unknown` (the deed says it
may have run) unless you pass `failure_means="not_applied"`, then re-raised as `ToolExecutionError`.
A model that retries a pending call gets the same approval, not a second one.
`wrap_tool(fn, client=…, tool=…)` and `wrap_tool_async` are the same without a decorator.

## Errors

The classes come from the API's error catalog (`sdk/errors.json`, written from
`internal/httpx/errors.go`; a TypeScript test and a Python test both check every code lands on the
same class).

| Class | When |
|---|---|
| `AuthenticationError` | `authentication_error`: a bad, revoked or expired key |
| `PermissionDeniedError` | `permission_error`: a stopped or suspended agent, a stopped org, a missing scope, intent refusals |
| `PolicyDeniedError` | `policy_error` (`policy_denied`), and a recorded `tool_unknown` |
| `RateLimitError` | 429 `rate_limited`, after the retries |
| `BudgetExceededError` | 429 `budget_exceeded`, never retried (`.retry_after`) |
| `InvalidRequestError` | `invalid_request_error`: validation, not found, conflicts, `approval_expired` |
| `APIError` | 5xx, or an answer the SDK couldn't read (or larger than 8 MiB) |
| `APIConnectionError`, `APITimeoutError` | no answer: `Can't reach Deedglass at http://127.0.0.1:8080. Is deedglass serve running? Try: deedglass doctor.` |
| `ApprovalPendingError` | a local tool's action waits for a person |
| `ToolExecutionError` | your function raised after Deedglass authorized it |

Every one is a `DeedglassError` with `.type`, `.code`, `.message`, `.param`, `.status`,
`.request_id`, `.doc_url` and, for refusals, `.action`. `str(err)` is the sentence and the request
id. Python's builtin `PermissionError`, `ConnectionError` and `TimeoutError` are never shadowed.

## Retries and idempotency

Retried: connection errors (GET), 408, 409 `idempotency_key_in_use`, 429 `rate_limited`, 502, 503
and 504, with a backoff of 0.5 s × 2ⁿ ± 25% and `Retry-After` honoured up to 10 s, `max_retries`
times. A POST is retried only with its first `Idempotency-Key`, so the server never runs a tool
twice. Policy results are never retried.

## Async

`AsyncDeedglass` has the same calls, awaited, over `httpx.AsyncClient`. Both clients drive one
request core, so they send byte-identical requests (a test compares 30 calls).

## Offline verification

```python
from deedglass.verify import KeySet, verify_deed, verify_chain, split_lines

keys = KeySet.fetch("https://deedglass.example/.well-known/deedglass/keys.json")   # https, 5 s, 1 MiB
report = verify_chain(split_lines(open("deeds.jsonl", "rb").read()), keys)
report.result, report.intact, report.chain_status                                   # ('valid', 1204, 'intact')
```

The verifier is a port of Go's: the strict JSON profile (no duplicate keys, integers only within
±2⁵³−1, no lone surrogates), JCS, the closed schema by kind, `body_hash`, the seal over
`deedglass/deed/v1\n` + `body_hash`, key validity and revocation, small-order key refusal, and the
chain (seq, `prev_hash`, the genesis rule, org and time order). It gives the same code, seq and
field as Go on every published vector and on all 500 cases of the parity corpus.

## Signed intents

```python
from deedglass import IntentKey

dg = Deedglass(intent_key=IntentKey(open(".deedglass/intent.pem").read()))
dg.act("stripe.refund", {"charge": "ch_…", "amount": 1200})   # sends Deedglass-Intent
```

`sign_intent` reproduces S09's vectors byte for byte (the cross-check signature ends in `…nqjJCA`).
`generate_intent_key()` returns the PEM, the public key to register and its kid.

## Adapters

### LangChain and LangGraph

```python
from langchain.agents import create_agent
from deedglass import Deedglass
from deedglass.langchain import DeedglassMiddleware, deedglass_tool

dg = Deedglass()
agent = create_agent(model, tools=[ship_order], middleware=[DeedglassMiddleware(dg, connector="orders")])
sql = deedglass_tool(dg, "postgres.execute", SQLArgs)      # a connector tool Deedglass runs
```

The middleware asks Deedglass before each tool call (proxy mode, tool `orders.<name>`) and reports
the result after it. A denial, a pending approval or an outage becomes a `ToolMessage(status="error")`
the model reads; the tool never runs. Gateway tools made with `deedglass_tool` pass through it.

### Claude Agent SDK

```python
from claude_agent_sdk import ClaudeAgentOptions
from deedglass import AsyncDeedglass
from deedglass.claude_agent_sdk import deedglass_hooks, deedglass_mcp_server

dg = AsyncDeedglass()
options = ClaudeAgentOptions(hooks=deedglass_hooks(dg),
                             mcp_servers={"deedglass": deedglass_mcp_server(dg, await dg.tools.list())})
```

PreToolUse asks Deedglass (tool `claude.<ToolName>`): a refusal is `permissionDecision: "deny"`
with the Gate's sentence as the reason Claude reads; an authorized call returns no decision, so your
own permission prompts still apply. PostToolUse and PostToolUseFailure report the outcome.

### Every adapter

- Fails closed: if Deedglass can't be reached the tool isn't run, and the model reads `Deedglass
  isn't reachable, so this tool was not run. Check that deedglass serve is running.`
  (`fail_open=True` exists for local experiments only and logs a warning every call).
- A pending approval is waited for up to `wait_for_approval` (120 s); then the model reads `Waiting
  for approval in Console › Approvals (apr_…). Try again after someone decides.`
- Sends `context.client = deedglass-python/<framework>`, the framework's call id as
  `context.call_id`, and the Claude session id as `Deedglass-Session`.

## Versioning

`0.2.1`, API `2026-10-01`. Objects keep the server's JSON in `.raw`, so a newer server never breaks
an older SDK.

## What it doesn't do

- It doesn't evaluate policies locally or cache decisions: Deedglass decides every call.
- The adapters govern tool calls, not model prompts or replies.
- It doesn't publish itself: the PyPI release is a later step.
